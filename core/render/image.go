package render

import (
	"bytes"
	"image/color"
	"image/jpeg"
	"math"

	"github.com/thai-pc/fluxdocs/core/parse"
)

// maxImageBytes bounds the decoded raw-sample buffer of a single image, a
// backstop to the maxCanvasPixels area check (SECURITY.md §2–3). parse.DecodeStream
// independently caps Flate output; this pre-check avoids the work up front.
const maxImageBytes = 256 << 20

// decodedImage is an image XObject after decoding: a row-major RGBA grid with
// alpha already resolved (an /SMask or /ImageMask stencil is baked in). Row 0 is
// the top of the image (§8.9.5.2). It is an internal representation for drawing,
// independent of the sample count in the PDF (a JPEG may decode to different
// dimensions than /Width×/Height).
type decodedImage struct {
	w, h int
	pix  []color.RGBA // len == w*h
}

// at returns the sample at column col, row row. Callers guarantee bounds.
func (d *decodedImage) at(col, row int) color.RGBA {
	return d.pix[row*d.w+col]
}

// doXObject handles the Do operator: it resolves /Resources /XObject /<name> to
// a stream and dispatches on its /Subtype. An /Image is decoded and drawn; a
// /Form is interpreted recursively into the same surface (see form.go). Anything
// the renderer cannot handle is a graceful no-op, so the rest of the page still
// renders (§8.8).
func (in *interp) doXObject(name parse.Name) {
	if in.res == nil || in.resources == nil {
		return
	}
	xobjDict, ok, err := in.res.ResolveDict(in.resources["XObject"])
	if err != nil || !ok {
		return
	}
	// Keep the raw entry: when it is an indirect reference it serves as the
	// form's font-cache and cycle-detection key (form.go).
	raw := xobjDict[name]
	st, ok := resolveStream(in.res, raw)
	if !ok {
		return
	}
	ref, hasRef := raw.(parse.Reference)

	subtype, hasSubtype := st.Dict.GetName("Subtype")
	switch {
	case hasSubtype && subtype == "Form":
		in.execForm(st, ref, hasRef)
		return
	case hasSubtype && subtype == "Image":
		// fall through to the image path below
	case hasSubtype:
		return // PS, PostScript, or any other subtype: unsupported
	default:
		// Untyped stream: disambiguate leniently. /Width+/Height ⇒ image (the
		// historical behaviour); otherwise a /BBox ⇒ form.
		_, hasW := st.Dict["Width"]
		_, hasH := st.Dict["Height"]
		if hasW && hasH {
			break
		}
		if _, hasBBox := st.Dict["BBox"]; hasBBox {
			in.execForm(st, ref, hasRef)
		}
		return
	}

	img, ok := decodeImage(in.res, st, in.gs.fill)
	if !ok {
		return
	}
	drawImage(in.rs, in.gs.ctm, img)
}

// decodeImage decodes an image XObject stream to a decodedImage, baking in any
// /ImageMask stencil (painted in fill) or /SMask alpha. It returns ok=false for
// any unsupported or malformed case — an oversized/degenerate size, an
// unsupported filter/color space/bit depth, or a truncated sample buffer — so
// the caller skips the image rather than crashing.
//
// Supported in Wave 1: DCTDecode (baseline JPEG), and FlateDecode/no-filter raw
// samples in DeviceRGB (3×8) and DeviceGray (1×8); plus /ImageMask (1 bpc) and
// an optional DeviceGray /SMask. Everything else is skipped.
func decodeImage(res *parse.Resolver, st *parse.Stream, fill color.RGBA) (*decodedImage, bool) {
	w, wok := imageInt(res, st.Dict, "Width", "W")
	h, hok := imageInt(res, st.Dict, "Height", "H")
	if !wok || !hok || w <= 0 || h <= 0 {
		return nil, false
	}
	if int64(w)*int64(h) > maxCanvasPixels {
		return nil, false // decompression-bomb / oversize guard
	}

	if imageBool(res, st.Dict, "ImageMask", "IM") {
		return decodeImageMask(res, st, w, h, fill)
	}

	filters := imageFilters(st)
	switch lastFilter(filters) {
	case "DCTDecode", "DCT":
		if len(filters) > 1 {
			return nil, false // filter chain before JPEG: unsupported, skip
		}
		img, ok := decodeJPEG(st)
		if !ok {
			return nil, false
		}
		applySMask(res, st, img)
		return img, true
	case "FlateDecode", "Fl", "":
		csName, ok := colorSpaceName(res, st.Dict)
		if !ok {
			return nil, false // array color spaces (Indexed/ICCBased/…) deferred
		}
		img, ok := decodeRawImage(res, st, csName, w, h)
		if !ok {
			return nil, false
		}
		applySMask(res, st, img)
		return img, true
	default:
		// CCITTFaxDecode, JBIG2Decode, JPXDecode, LZWDecode, RunLengthDecode: skip.
		return nil, false
	}
}

// decodeJPEG decodes a baseline DCTDecode image from the stream's raw bytes via
// the standard library. The JPEG carries its own dimensions, so the returned
// grid may differ from /Width×/Height; the pixel-count bound is re-checked
// against the decoded size. CMYK JPEGs are converted through image/color, which
// matches interpret.go's cmyk() formula; Adobe-marker inversion is accepted as a
// Wave-1 limitation.
func decodeJPEG(st *parse.Stream) (*decodedImage, bool) {
	img, err := jpeg.Decode(bytes.NewReader(st.Raw))
	if err != nil {
		return nil, false
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 || int64(w)*int64(h) > maxCanvasPixels {
		return nil, false
	}
	pix := make([]color.RGBA, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, bl, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
			pix[y*w+x] = color.RGBA{uint8(r >> 8), uint8(g >> 8), uint8(bl >> 8), 0xff}
		}
	}
	return &decodedImage{w: w, h: h, pix: pix}, true
}

// decodeRawImage slices Flate-inflated (or unfiltered) 8-bit samples into an
// RGBA grid. Rows are byte-aligned: stride = ceil(W * 8 * nComp / 8). Only
// DeviceRGB (3 components) and DeviceGray (1 component) are supported; other
// color spaces or bit depths return ok=false.
func decodeRawImage(res *parse.Resolver, st *parse.Stream, csName parse.Name, w, h int) (*decodedImage, bool) {
	var nComp int
	switch csName {
	case "DeviceRGB", "RGB":
		nComp = 3
	case "DeviceGray", "G":
		nComp = 1
	default:
		return nil, false // DeviceCMYK/Indexed/ICCBased/… deferred
	}
	if bpc, _ := imageInt(res, st.Dict, "BitsPerComponent", "BPC"); bpc != 8 {
		return nil, false
	}

	// Compute stride in int64: w is bounded only by maxCanvasPixels (up to 64M
	// when h==1), so w*8*nComp can overflow a 32-bit int (the WASM target)
	// before the size check below runs. Do the multiply wide, then narrow.
	stride64 := (int64(w)*8*int64(nComp) + 7) / 8
	if stride64*int64(h) > maxImageBytes {
		return nil, false
	}
	stride := int(stride64)
	data, err := parse.DecodeStream(st)
	if err != nil {
		return nil, false // unsupported filter in the chain, or decode error
	}
	if int64(len(data)) < int64(stride)*int64(h) {
		return nil, false // truncated/hostile: skip rather than index out of range
	}

	pix := make([]color.RGBA, w*h)
	for row := 0; row < h; row++ {
		base := row * stride
		for col := 0; col < w; col++ {
			o := base + col*nComp
			if nComp == 3 {
				pix[row*w+col] = color.RGBA{data[o], data[o+1], data[o+2], 0xff}
			} else {
				g := data[o]
				pix[row*w+col] = color.RGBA{g, g, g, 0xff}
			}
		}
	}
	return &decodedImage{w: w, h: h, pix: pix}, true
}

// decodeImageMask decodes a 1-bpc /ImageMask stencil into an RGBA grid painted
// in fill. Per the default /Decode [0 1], sample bit 0 paints and bit 1 is
// transparent; /Decode [1 0] inverts. Transparent samples get A=0 so blend
// skips them. Only Flate/no-filter mask data is supported.
func decodeImageMask(res *parse.Resolver, st *parse.Stream, w, h int, fill color.RGBA) (*decodedImage, bool) {
	// An image mask is 1 bpc by definition (§8.9.6.2). If /BitsPerComponent is
	// present and not 1, reject rather than mis-slice the 1-bit-packed rows.
	if bpc, present := imageInt(res, st.Dict, "BitsPerComponent", "BPC"); present && bpc != 1 {
		return nil, false
	}

	filters := imageFilters(st)
	for _, f := range filters {
		switch f {
		case "FlateDecode", "Fl":
		default:
			return nil, false // CCITT masks etc. deferred
		}
	}

	stride := (w + 7) / 8
	if int64(stride)*int64(h) > maxImageBytes {
		return nil, false
	}
	data, err := parse.DecodeStream(st)
	if err != nil {
		return nil, false
	}
	if int64(len(data)) < int64(stride)*int64(h) {
		return nil, false
	}

	// paintBit is the sample value that means "paint" (default 0; inverted to 1
	// by /Decode [1 0]).
	var paintBit byte
	if maskDecodeInverted(res, st.Dict) {
		paintBit = 1
	}
	painted := color.RGBA{fill.R, fill.G, fill.B, 0xff}
	transparent := color.RGBA{}

	pix := make([]color.RGBA, w*h)
	for row := 0; row < h; row++ {
		base := row * stride
		for col := 0; col < w; col++ {
			bit := (data[base+col/8] >> (7 - uint(col%8))) & 1
			if bit == paintBit {
				pix[row*w+col] = painted
			} else {
				pix[row*w+col] = transparent
			}
		}
	}
	return &decodedImage{w: w, h: h, pix: pix}, true
}

// maskDecodeInverted reports whether an image mask's /Decode array is [1 0]
// (i.e. the mask is inverted relative to the [0 1] default).
func maskDecodeInverted(res *parse.Resolver, d parse.Dict) bool {
	var arr parse.Array
	if v, ok := d["Decode"]; ok {
		arr, _ = v.(parse.Array)
	} else if v, ok := d["D"]; ok {
		arr, _ = v.(parse.Array)
	}
	if len(arr) < 2 {
		return false
	}
	return num(arr[0]) == 1 && num(arr[1]) == 0
}

// applySMask decodes an image's /SMask (DeviceGray, 8 bpc) and bakes it into img
// as per-pixel alpha, sampled at the shared unit-square coordinate with
// nearest-neighbour. A missing or unsupported SMask leaves img opaque.
func applySMask(res *parse.Resolver, st *parse.Stream, img *decodedImage) {
	smSt, ok := resolveStream(res, st.Dict["SMask"])
	if !ok {
		return
	}
	alpha, ws, hs, ok := decodeSMask(res, smSt)
	if !ok {
		return // treat the base image as opaque, do not skip it
	}
	for row := 0; row < img.h; row++ {
		v := (float64(row) + 0.5) / float64(img.h)
		sr := clampIndex(int(v*float64(hs)), hs)
		for col := 0; col < img.w; col++ {
			u := (float64(col) + 0.5) / float64(img.w)
			sc := clampIndex(int(u*float64(ws)), ws)
			img.pix[row*img.w+col].A = alpha[sr*ws+sc]
		}
	}
}

// decodeSMask decodes a soft-mask stream to an 8-bpc gray alpha grid. Only
// Flate/no-filter DeviceGray data is supported; anything else returns ok=false.
func decodeSMask(res *parse.Resolver, st *parse.Stream) (alpha []uint8, w, h int, ok bool) {
	w, wok := imageInt(res, st.Dict, "Width", "W")
	h, hok := imageInt(res, st.Dict, "Height", "H")
	if !wok || !hok || w <= 0 || h <= 0 || int64(w)*int64(h) > maxCanvasPixels {
		return nil, 0, 0, false
	}
	for _, f := range imageFilters(st) {
		switch f {
		case "FlateDecode", "Fl":
		default:
			return nil, 0, 0, false
		}
	}
	if bpc, _ := imageInt(res, st.Dict, "BitsPerComponent", "BPC"); bpc != 8 {
		return nil, 0, 0, false
	}
	stride := w // 8 bpc, 1 component
	if int64(stride)*int64(h) > maxImageBytes {
		return nil, 0, 0, false
	}
	data, err := parse.DecodeStream(st)
	if err != nil || int64(len(data)) < int64(stride)*int64(h) {
		return nil, 0, 0, false
	}
	alpha = make([]uint8, w*h)
	for row := 0; row < h; row++ {
		copy(alpha[row*w:(row+1)*w], data[row*stride:row*stride+w])
	}
	return alpha, w, h, true
}

// drawImage composites img onto rs by mapping the image's sample grid through
// ctm (§8.9.5.2, row 0 is the top). It uses inverse mapping over the image's
// clamped device bounding box with nearest-neighbour sampling and source-over
// compositing, so gaps never appear on upscaling and total work is bounded by
// the canvas size. A degenerate (zero-area) placement is skipped.
func drawImage(rs *raster, ctm matrix, img *decodedImage) {
	// S maps sample (col,row) to the unit square (u,v) with the y-flip that puts
	// row 0 at the top (v=1). Composed with ctm it maps samples to device pixels.
	s := matrix{1.0 / float64(img.w), 0, 0, -1.0 / float64(img.h), 0, 1}
	imgToDevice := s.mul(ctm)
	inv, ok := imgToDevice.invert()
	if !ok {
		return // zero-area placement: nothing to draw
	}

	// Device bounding box = the unit square's four corners through ctm.
	minX, minY := math.Inf(1), math.Inf(1)
	maxX, maxY := math.Inf(-1), math.Inf(-1)
	for _, c := range [4][2]float64{{0, 0}, {1, 0}, {1, 1}, {0, 1}} {
		dx, dy := ctm.apply(c[0], c[1])
		minX, maxX = math.Min(minX, dx), math.Max(maxX, dx)
		minY, maxY = math.Min(minY, dy), math.Max(maxY, dy)
	}
	x0 := clampCoord(int(math.Floor(minX)), rs.w)
	y0 := clampCoord(int(math.Floor(minY)), rs.h)
	x1 := clampCoord(int(math.Ceil(maxX)), rs.w)
	y1 := clampCoord(int(math.Ceil(maxY)), rs.h)
	// Restrict to the active clip so a form's /BBox crops its images (blend also
	// enforces this per pixel; clamping the box just avoids the wasted scan).
	x0 = maxInt(x0, rs.clip.x0)
	y0 = maxInt(y0, rs.clip.y0)
	x1 = minInt(x1, rs.clip.x1)
	y1 = minInt(y1, rs.clip.y1)
	if x0 >= x1 || y0 >= y1 {
		return // empty box
	}

	for dy := y0; dy < y1; dy++ {
		for dx := x0; dx < x1; dx++ {
			fcol, frow := inv.apply(float64(dx)+0.5, float64(dy)+0.5)
			col := int(math.Floor(fcol))
			row := int(math.Floor(frow))
			if col < 0 || col >= img.w || row < 0 || row >= img.h {
				continue
			}
			rs.blend(dx, dy, img.at(col, row))
		}
	}
}

// --- image-dictionary helpers ---

// imageFilters normalizes an image stream's /Filter (a single Name or an Array
// of Names) into a slice. parse.nameList is unexported, and the image decode
// must branch on the filter itself. Only /Filter is read; the abbreviated /F key
// denotes a file specification on a stream dictionary, not a filter, so it is
// deliberately not consulted.
func imageFilters(st *parse.Stream) []parse.Name {
	switch v := st.Dict["Filter"].(type) {
	case parse.Name:
		return []parse.Name{v}
	case parse.Array:
		var out []parse.Name
		for _, e := range v {
			if n, ok := e.(parse.Name); ok {
				out = append(out, n)
			}
		}
		return out
	default:
		return nil
	}
}

// lastFilter returns the last (outermost) filter name, or "" when there is none.
func lastFilter(filters []parse.Name) parse.Name {
	if len(filters) == 0 {
		return ""
	}
	return filters[len(filters)-1]
}

// imageInt reads an integer image-dictionary value under its full or abbreviated
// key, resolving an indirect reference. ok is false when neither key holds an
// integer.
func imageInt(res *parse.Resolver, d parse.Dict, primary, abbrev parse.Name) (int, bool) {
	for _, k := range [2]parse.Name{primary, abbrev} {
		v, present := d[k]
		if !present {
			continue
		}
		r, err := res.Resolve(v)
		if err != nil {
			continue
		}
		switch n := r.(type) {
		case parse.Integer:
			return int(n), true
		case parse.Real:
			return int(n), true
		}
	}
	return 0, false
}

// imageBool reads a boolean image-dictionary value under its full or abbreviated
// key. A missing or non-boolean value reads as false.
func imageBool(res *parse.Resolver, d parse.Dict, primary, abbrev parse.Name) bool {
	for _, k := range [2]parse.Name{primary, abbrev} {
		v, present := d[k]
		if !present {
			continue
		}
		if r, err := res.Resolve(v); err == nil {
			if b, ok := r.(parse.Boolean); ok {
				return bool(b)
			}
		}
	}
	return false
}

// colorSpaceName resolves the image's /ColorSpace (or /CS) to a device color
// space Name. An array color space (Indexed, ICCBased, …) yields ok=false, so
// those images are skipped in Wave 1.
func colorSpaceName(res *parse.Resolver, d parse.Dict) (parse.Name, bool) {
	for _, k := range [2]parse.Name{"ColorSpace", "CS"} {
		v, present := d[k]
		if !present {
			continue
		}
		r, err := res.Resolve(v)
		if err != nil {
			return "", false
		}
		if n, ok := r.(parse.Name); ok {
			return n, true
		}
		return "", false // array/other: unsupported
	}
	return "", false
}

// clampIndex clamps i into [0, n).
func clampIndex(i, n int) int {
	if i < 0 {
		return 0
	}
	if i >= n {
		return n - 1
	}
	return i
}

// clampCoord clamps a device-coordinate bound into [0, n].
func clampCoord(v, n int) int {
	if v < 0 {
		return 0
	}
	if v > n {
		return n
	}
	return v
}
