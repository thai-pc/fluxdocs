//go:build js && wasm

// Command wasm is the FluxDocs client-side entry point: it compiles the pure-Go
// core to WebAssembly (GOOS=js GOARCH=wasm) and exposes it to JavaScript as
// globalThis.fluxdocs. Everything runs in the browser — not a single byte of a
// document is sent to a server (the privacy-first promise, CLAUDE.md §1.3). No
// network or filesystem access is used here.
//
// Every exported function returns a plain result object with an "error" string
// field and a "code" string field: both "" on success (plus the result fields);
// on failure "error" is a human-readable "<export>: <message>" and "code" is a
// non-empty value from the closed set in wasm/internal/errcode
// ("budget_exceeded", "timeout", "invalid_handle", …), for programmatic
// handling. This envelope is predictable and easy to consume from JS without
// try/catch around Go panics: every export is wrapped by export(), which turns a
// panic into the same error envelope with code "internal".
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"syscall/js"

	"github.com/thai-pc/fluxdocs/core"
	"github.com/thai-pc/fluxdocs/wasm/internal/errcode"
	"github.com/thai-pc/fluxdocs/wasm/internal/jsopt"
)

// docs holds the open documents by integer handle. WASM is single-threaded, so
// no locking is needed around this registry.
var (
	docs       = map[int]*core.Document{}
	nextHandle = 1
)

func main() {
	api := js.Global().Get("Object").New()
	api.Set("version", export("version", version))
	api.Set("open", export("open", open))
	api.Set("close", export("close", closeDoc))
	api.Set("pageCount", export("pageCount", pageCount))
	api.Set("pageSize", export("pageSize", pageSize))
	api.Set("extractText", export("extractText", extractText))
	api.Set("extractTextByPage", export("extractTextByPage", extractTextByPage))
	api.Set("renderPage", export("renderPage", renderPage))
	api.Set("save", export("save", save))
	api.Set("addAnnotation", export("addAnnotation", addAnnotation))
	api.Set("getAnnotations", export("getAnnotations", getAnnotations))
	api.Set("exportAnnotations", export("exportAnnotations", exportAnnotations))
	registerTestExports(api) // none unless built with -tags smoketest
	js.Global().Set("fluxdocs", api)

	// Keep the Go runtime alive so the exported functions stay callable.
	select {}
}

// --- result envelope helpers ---

func newObject() js.Value { return js.Global().Get("Object").New() }

// errResult builds a failure envelope. code must be a non-empty errcode value:
// taking it as a parameter means no failure can leave without one.
func errResult(code, msg string) js.Value {
	o := newObject()
	o.Set("error", msg)
	o.Set("code", code)
	return o
}

// errFrom builds the failure envelope for a core error: "<prefix>: <err>" with
// the code errcode.Of maps err to.
func errFrom(prefix string, err error) js.Value {
	return errResult(errcode.Of(err), prefix+": "+err.Error())
}

func okResult() js.Value {
	o := newObject()
	o.Set("error", "")
	o.Set("code", "")
	return o
}

// export wraps fn as a JS function that never lets a Go panic escape. An
// uncaught panic in a js.FuncOf callback kills the Go runtime, leaving every
// fluxdocs.* call dead for the page's lifetime; instead the panic becomes the
// usual error envelope ("<name>: internal error: ...", code "internal") and the
// runtime stays up. The message shows a Go runtime error's own text but only the
// type of any other panic value, which could hold document bytes (the
// core.InternalError policy). The parser/renderer must not panic on untrusted
// input (SECURITY.md §2) — this is the last line of defense, not a substitute
// for bounding the input.
func export(name string, fn func(js.Value, []js.Value) any) js.Func {
	return js.FuncOf(func(this js.Value, args []js.Value) (result any) {
		defer func() {
			if r := recover(); r != nil {
				result = errResult(errcode.Internal, fmt.Sprintf("%s: internal error: %s", name, errcode.PanicDetail(r)))
			}
		}()
		return fn(this, args)
	})
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

// docArg resolves the handle in args[0] to an open document. On failure (no
// handle, a non-number, or one that was never opened or is already closed) it
// returns an "invalid_handle" envelope and ok=false.
func docArg(args []js.Value) (*core.Document, js.Value, bool) {
	if len(args) < 1 {
		return nil, errResult(errcode.InvalidHandle, "missing document handle"), false
	}
	if args[0].Type() != js.TypeNumber {
		return nil, errResult(errcode.InvalidHandle, "unknown document handle"), false
	}
	doc, exists := docs[args[0].Int()]
	if !exists {
		return nil, errResult(errcode.InvalidHandle, "unknown document handle"), false
	}
	return doc, js.Value{}, true
}

// indexArg reads the page index in args[i] through a checked conversion
// (jsopt.Int; js.Value.Int is a raw float→int cast). On failure (missing, not a
// number, or NaN, infinite or out of the int range) it returns an
// "invalid_argument" envelope and ok=false.
func indexArg(name string, args []js.Value, i int) (int, js.Value, bool) {
	if len(args) > i && args[i].Type() == js.TypeNumber {
		if n, ok := jsopt.Int(args[i].Float()); ok {
			return n, js.Value{}, true
		}
	}
	return 0, errResult(errcode.InvalidArgument, name+": expected a page index"), false
}

// intOpt reads the optional integer option name of o through a checked
// conversion (jsopt.Int). present is false when it is absent or not a number;
// a number that is NaN, infinite or out of the int range yields an
// "invalid_argument" envelope and ok=false.
func intOpt(export string, o js.Value, name string) (n int, present bool, errObj js.Value, ok bool) {
	v := o.Get(name)
	if v.Type() != js.TypeNumber {
		return 0, false, js.Value{}, true
	}
	n, ok = jsopt.Int(v.Float())
	if !ok {
		return 0, false, errResult(errcode.InvalidArgument, export+": "+name+" must be a finite integer"), false
	}
	return n, true, js.Value{}, true
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
		return errResult(errcode.InvalidArgument, "open: expected a Uint8Array of PDF bytes")
	}
	doc, err := core.OpenBytes(bytesArg(args[0]))
	if err != nil {
		return errFrom("open", err)
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
	idx, errObj, ok := indexArg("pageSize", args, 1)
	if !ok {
		return errObj
	}
	p, err := doc.GetPage(idx)
	if err != nil {
		return errFrom("pageSize", err)
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
		return errFrom("extractText", err)
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
	idx, errObj, ok := indexArg("extractTextByPage", args, 1)
	if !ok {
		return errObj
	}
	text, err := doc.ExtractTextByPage(idx, core.ExtractOptions{})
	if err != nil {
		return errFrom("extractTextByPage", err)
	}
	o := okResult()
	o.Set("text", text)
	return o
}

// renderPage(handle, index, opts?) -> {error, code, image, format}
// opts: { dpi?, format?: "png"|"jpeg", quality?, skipAnnotations?: bool,
// maxPaintPixels?, maxOperators?, timeoutMs? }
//
// The page index, dpi and quality go through a checked conversion: a NaN,
// infinite or out-of-range number is "invalid_argument" (a fraction is
// truncated toward zero, as before).
//
// maxPaintPixels / maxOperators map to the per-page render budget (0 or absent
// = default, < 0 = unlimited, > 0 = absolute); a NaN, infinite, or out-of-range
// value is ignored. timeoutMs, when finite and > 0, bounds the render's wall
// clock: the render checks the clock itself, since a synchronous export never
// yields to Go's timer goroutine. Over budget the code is "budget_exceeded"
// (or "canvas_too_large"); past the deadline it is "timeout".
func renderPage(_ js.Value, args []js.Value) any {
	doc, errObj, ok := docArg(args)
	if !ok {
		return errObj
	}
	idx, errObj, ok := indexArg("renderPage", args, 1)
	if !ok {
		return errObj
	}
	opts := core.RenderOptions{Format: "png"}
	ctx := context.Background()
	if len(args) >= 3 {
		switch o := args[2]; o.Type() {
		case js.TypeUndefined, js.TypeNull:
		case js.TypeObject:
			n, present, errObj, ok := intOpt("renderPage", o, "dpi")
			if !ok {
				return errObj
			}
			if present {
				opts.DPI = n
			}
			if v := o.Get("format"); v.Type() == js.TypeString {
				opts.Format = v.String()
			}
			if n, present, errObj, ok = intOpt("renderPage", o, "quality"); !ok {
				return errObj
			}
			if present {
				opts.Quality = n
			}
			if v := o.Get("skipAnnotations"); v.Type() == js.TypeBoolean {
				opts.SkipAnnotations = v.Bool()
			}
			if n, ok := int64Opt(o.Get("maxPaintPixels")); ok {
				opts.MaxPaintPixels = n
			}
			if n, ok := int64Opt(o.Get("maxOperators")); ok {
				opts.MaxOperators = n
			}
			if v := o.Get("timeoutMs"); v.Type() == js.TypeNumber {
				if d, ok := jsopt.TimeoutMs(v.Float()); ok {
					var cancel context.CancelFunc
					ctx, cancel = context.WithTimeout(ctx, d)
					defer cancel()
				}
			}
		default:
			return errResult(errcode.InvalidArgument, "renderPage: options must be an object")
		}
	}
	img, err := doc.RenderPageContext(ctx, idx, opts)
	if err != nil {
		return errFrom("renderPage", err)
	}
	res := okResult()
	res.Set("image", toUint8Array(img))
	res.Set("format", opts.Format)
	return res
}

// int64Opt reads an optional integer option through a checked conversion
// (jsopt.Int64). ok is false when v is not a number or is NaN, infinite, or out
// of the int64 range.
func int64Opt(v js.Value) (int64, bool) {
	if v.Type() != js.TypeNumber {
		return 0, false
	}
	return jsopt.Int64(v.Float())
}

// save(handle) -> {error, bytes}
func save(_ js.Value, args []js.Value) any {
	doc, errObj, ok := docArg(args)
	if !ok {
		return errObj
	}
	var buf bytes.Buffer
	if err := doc.SaveTo(&buf); err != nil {
		return errFrom("save", err)
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
		return errResult(errcode.InvalidArgument, "addAnnotation: expected an annotation object")
	}
	spec := args[1]
	// Checked conversion, like indexArg: js.Value.Int panics on a non-number
	// and casts NaN or out-of-range values unchecked.
	var idx int
	ok = false
	if pi := spec.Get("pageIndex"); pi.Type() == js.TypeNumber {
		idx, ok = jsopt.Int(pi.Float())
	}
	if !ok {
		return errResult(errcode.InvalidArgument, "addAnnotation: expected a numeric pageIndex")
	}
	page, err := doc.GetPage(idx)
	if err != nil {
		return errFrom("addAnnotation", err)
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
		return errFrom("addAnnotation", err)
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
	idx, errObj, ok := indexArg("getAnnotations", args, 1)
	if !ok {
		return errObj
	}
	page, err := doc.GetPage(idx)
	if err != nil {
		return errFrom("getAnnotations", err)
	}
	anns, err := doc.GetAnnotations(page.ID)
	if err != nil {
		return errFrom("getAnnotations", err)
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
		return errFrom("exportAnnotations", err)
	}
	data, err := json.Marshal(layer)
	if err != nil {
		return errFrom("exportAnnotations", err)
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
