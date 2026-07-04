//go:build js && wasm

// Command wasm is the FluxDocs client-side entry point: it compiles the pure-Go
// core to WebAssembly (GOOS=js GOARCH=wasm) and exposes it to JavaScript as
// globalThis.fluxdocs. Everything runs in the browser — not a single byte of a
// document is sent to a server (the privacy-first promise, CLAUDE.md §1.3). No
// network or filesystem access is used here.
//
// Every exported function returns a plain result object with an "error" string
// field: "" on success (plus the result fields), or a message on failure. This
// envelope is predictable and easy to consume from JS without try/catch around
// Go panics.
package main

import (
	"bytes"
	"encoding/json"
	"syscall/js"

	"github.com/thai-pc/fluxdocs/core"
)

// docs holds the open documents by integer handle. WASM is single-threaded, so
// no locking is needed around this registry.
var (
	docs       = map[int]*core.Document{}
	nextHandle = 1
)

func main() {
	api := js.Global().Get("Object").New()
	api.Set("version", js.FuncOf(version))
	api.Set("open", js.FuncOf(open))
	api.Set("close", js.FuncOf(closeDoc))
	api.Set("pageCount", js.FuncOf(pageCount))
	api.Set("pageSize", js.FuncOf(pageSize))
	api.Set("extractText", js.FuncOf(extractText))
	api.Set("extractTextByPage", js.FuncOf(extractTextByPage))
	api.Set("renderPage", js.FuncOf(renderPage))
	api.Set("save", js.FuncOf(save))
	api.Set("addAnnotation", js.FuncOf(addAnnotation))
	api.Set("getAnnotations", js.FuncOf(getAnnotations))
	api.Set("exportAnnotations", js.FuncOf(exportAnnotations))
	js.Global().Set("fluxdocs", api)

	// Keep the Go runtime alive so the exported functions stay callable.
	select {}
}

// --- result envelope helpers ---

func newObject() js.Value { return js.Global().Get("Object").New() }

func errResult(msg string) js.Value {
	o := newObject()
	o.Set("error", msg)
	return o
}

func okResult() js.Value {
	o := newObject()
	o.Set("error", "")
	return o
}

// toUint8Array copies b into a fresh JS Uint8Array.
func toUint8Array(b []byte) js.Value {
	u8 := js.Global().Get("Uint8Array").New(len(b))
	js.CopyBytesToJS(u8, b)
	return u8
}

// bytesArg reads a JS Uint8Array (or ArrayBuffer view) argument into Go bytes.
func bytesArg(v js.Value) []byte {
	n := v.Get("length").Int()
	b := make([]byte, n)
	js.CopyBytesToGo(b, v)
	return b
}

// docArg resolves the handle in args[0] to an open document. On failure it
// returns an error envelope and ok=false.
func docArg(args []js.Value) (*core.Document, js.Value, bool) {
	if len(args) < 1 {
		return nil, errResult("missing document handle"), false
	}
	h := args[0].Int()
	doc, exists := docs[h]
	if !exists {
		return nil, errResult("unknown document handle"), false
	}
	return doc, js.Value{}, true
}

// --- exported functions ---

func version(js.Value, []js.Value) any {
	o := okResult()
	o.Set("version", "0.1.0")
	return o
}

// open(bytes: Uint8Array) -> {error, handle}
func open(_ js.Value, args []js.Value) any {
	if len(args) < 1 {
		return errResult("open: expected a Uint8Array of PDF bytes")
	}
	doc, err := core.OpenBytes(bytesArg(args[0]))
	if err != nil {
		return errResult("open: " + err.Error())
	}
	h := nextHandle
	nextHandle++
	docs[h] = doc

	o := okResult()
	o.Set("handle", h)
	o.Set("pageCount", doc.PageCount)
	return o
}

// close(handle) -> {error}
func closeDoc(_ js.Value, args []js.Value) any {
	doc, errObj, ok := docArg(args)
	if !ok {
		return errObj
	}
	_ = doc.Close()
	delete(docs, args[0].Int())
	return okResult()
}

// pageCount(handle) -> {error, count}
func pageCount(_ js.Value, args []js.Value) any {
	doc, errObj, ok := docArg(args)
	if !ok {
		return errObj
	}
	o := okResult()
	o.Set("count", doc.GetPageCount())
	return o
}

// pageSize(handle, index) -> {error, width, height, rotation}
func pageSize(_ js.Value, args []js.Value) any {
	doc, errObj, ok := docArg(args)
	if !ok {
		return errObj
	}
	if len(args) < 2 {
		return errResult("pageSize: expected a page index")
	}
	p, err := doc.GetPage(args[1].Int())
	if err != nil {
		return errResult("pageSize: " + err.Error())
	}
	o := okResult()
	o.Set("width", p.Width)
	o.Set("height", p.Height)
	o.Set("rotation", p.Rotation)
	return o
}

// extractText(handle) -> {error, text}
func extractText(_ js.Value, args []js.Value) any {
	doc, errObj, ok := docArg(args)
	if !ok {
		return errObj
	}
	text, err := doc.ExtractText(core.ExtractOptions{})
	if err != nil {
		return errResult("extractText: " + err.Error())
	}
	o := okResult()
	o.Set("text", text)
	return o
}

// extractTextByPage(handle, index) -> {error, text}
func extractTextByPage(_ js.Value, args []js.Value) any {
	doc, errObj, ok := docArg(args)
	if !ok {
		return errObj
	}
	if len(args) < 2 {
		return errResult("extractTextByPage: expected a page index")
	}
	text, err := doc.ExtractTextByPage(args[1].Int(), core.ExtractOptions{})
	if err != nil {
		return errResult("extractTextByPage: " + err.Error())
	}
	o := okResult()
	o.Set("text", text)
	return o
}

// renderPage(handle, index, opts?) -> {error, image, format}
// opts: { dpi?, format?: "png"|"jpeg", quality?, skipAnnotations?: bool }
func renderPage(_ js.Value, args []js.Value) any {
	doc, errObj, ok := docArg(args)
	if !ok {
		return errObj
	}
	if len(args) < 2 {
		return errResult("renderPage: expected a page index")
	}
	opts := core.RenderOptions{Format: "png"}
	if len(args) >= 3 && args[2].Type() == js.TypeObject {
		o := args[2]
		if v := o.Get("dpi"); v.Type() == js.TypeNumber {
			opts.DPI = v.Int()
		}
		if v := o.Get("format"); v.Type() == js.TypeString {
			opts.Format = v.String()
		}
		if v := o.Get("quality"); v.Type() == js.TypeNumber {
			opts.Quality = v.Int()
		}
		if v := o.Get("skipAnnotations"); v.Type() == js.TypeBoolean {
			opts.SkipAnnotations = v.Bool()
		}
	}
	img, err := doc.RenderPage(args[1].Int(), opts)
	if err != nil {
		return errResult("renderPage: " + err.Error())
	}
	res := okResult()
	res.Set("image", toUint8Array(img))
	res.Set("format", opts.Format)
	return res
}

// save(handle) -> {error, bytes}
func save(_ js.Value, args []js.Value) any {
	doc, errObj, ok := docArg(args)
	if !ok {
		return errObj
	}
	var buf bytes.Buffer
	if err := doc.SaveTo(&buf); err != nil {
		return errResult("save: " + err.Error())
	}
	o := okResult()
	o.Set("bytes", toUint8Array(buf.Bytes()))
	return o
}

// addAnnotation(handle, ann) -> {error, annotation}
// ann: { pageIndex, type, rect:{x,y,width,height}, color?, opacity?, content?, points?:[{x,y}] }
func addAnnotation(_ js.Value, args []js.Value) any {
	doc, errObj, ok := docArg(args)
	if !ok {
		return errObj
	}
	if len(args) < 2 || args[1].Type() != js.TypeObject {
		return errResult("addAnnotation: expected an annotation object")
	}
	spec := args[1]
	idx := spec.Get("pageIndex").Int()
	page, err := doc.GetPage(idx)
	if err != nil {
		return errResult("addAnnotation: " + err.Error())
	}
	ann := core.Annotation{
		PageID:  page.ID,
		Type:    core.AnnotationType(spec.Get("type").String()),
		Rect:    rectFromJS(spec.Get("rect")),
		Color:   optString(spec.Get("color")),
		Opacity: optFloat(spec.Get("opacity")),
		Content: optString(spec.Get("content")),
		Points:  pointsFromJS(spec.Get("points")),
	}
	stored, err := doc.AddAnnotation(ann)
	if err != nil {
		return errResult("addAnnotation: " + err.Error())
	}
	o := okResult()
	o.Set("annotation", annotationToJS(stored))
	return o
}

// getAnnotations(handle, pageIndex) -> {error, annotations: []}
func getAnnotations(_ js.Value, args []js.Value) any {
	doc, errObj, ok := docArg(args)
	if !ok {
		return errObj
	}
	if len(args) < 2 {
		return errResult("getAnnotations: expected a page index")
	}
	page, err := doc.GetPage(args[1].Int())
	if err != nil {
		return errResult("getAnnotations: " + err.Error())
	}
	anns, err := doc.GetAnnotations(page.ID)
	if err != nil {
		return errResult("getAnnotations: " + err.Error())
	}
	arr := js.Global().Get("Array").New(len(anns))
	for i, a := range anns {
		arr.SetIndex(i, annotationToJS(a))
	}
	o := okResult()
	o.Set("annotations", arr)
	return o
}

// exportAnnotations(handle) -> {error, json}
func exportAnnotations(_ js.Value, args []js.Value) any {
	doc, errObj, ok := docArg(args)
	if !ok {
		return errObj
	}
	layer, err := doc.ExportAnnotationLayer()
	if err != nil {
		return errResult("exportAnnotations: " + err.Error())
	}
	data, err := json.Marshal(layer)
	if err != nil {
		return errResult("exportAnnotations: " + err.Error())
	}
	o := okResult()
	o.Set("json", string(data))
	return o
}

// --- JS <-> Go conversions ---

func rectFromJS(v js.Value) core.Rect {
	if v.Type() != js.TypeObject {
		return core.Rect{}
	}
	return core.Rect{
		X:      optFloat(v.Get("x")),
		Y:      optFloat(v.Get("y")),
		Width:  optFloat(v.Get("width")),
		Height: optFloat(v.Get("height")),
	}
}

func pointsFromJS(v js.Value) []core.Point {
	if v.Type() != js.TypeObject {
		return nil
	}
	n := v.Get("length").Int()
	if n <= 0 {
		return nil
	}
	pts := make([]core.Point, n)
	for i := 0; i < n; i++ {
		p := v.Index(i)
		pts[i] = core.Point{X: optFloat(p.Get("x")), Y: optFloat(p.Get("y"))}
	}
	return pts
}

func annotationToJS(a core.Annotation) js.Value {
	o := newObject()
	o.Set("id", string(a.ID))
	o.Set("pageId", string(a.PageID))
	o.Set("type", string(a.Type))
	rect := newObject()
	rect.Set("x", a.Rect.X)
	rect.Set("y", a.Rect.Y)
	rect.Set("width", a.Rect.Width)
	rect.Set("height", a.Rect.Height)
	o.Set("rect", rect)
	o.Set("color", a.Color)
	o.Set("opacity", a.Opacity)
	if a.Content != "" {
		o.Set("content", a.Content)
	}
	if len(a.Points) > 0 {
		pts := js.Global().Get("Array").New(len(a.Points))
		for i, p := range a.Points {
			po := newObject()
			po.Set("x", p.X)
			po.Set("y", p.Y)
			pts.SetIndex(i, po)
		}
		o.Set("points", pts)
	}
	return o
}

func optString(v js.Value) string {
	if v.Type() == js.TypeString {
		return v.String()
	}
	return ""
}

func optFloat(v js.Value) float64 {
	if v.Type() == js.TypeNumber {
		return v.Float()
	}
	return 0
}
