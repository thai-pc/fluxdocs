package render

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math"
	"runtime"
	"testing"

	"github.com/thai-pc/fluxdocs/core/parse"
)

// imageStreamObj builds an image XObject stream object body: the given dict
// entries (the inside of the "<< >>", no filter keys unless supplied) plus a
// computed /Length, followed by the raw sample bytes. Binary data is safe: the
// parser reads exactly /Length bytes.
func imageStreamObj(dictBody string, data []byte) string {
	return fmt.Sprintf("<< %s /Length %d >>\nstream\n%s\nendstream", dictBody, len(data), data)
}

// renderImagePDF assembles a 100x100 one-page PDF whose page exposes an XObject
// /Im (object 5), runs content, and returns the decoded PNG. Callers provide the
// image object(s) in imgObjs (must include object 5; object 6 for an /SMask).
func renderImagePDF(t *testing.T, content string, imgObjs map[int]string) image.Image {
	t.Helper()
	objects := map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 100 100] >>",
		3: "<< /Type /Page /Parent 2 0 R /Resources << /XObject << /Im 5 0 R >> >> /Contents 4 0 R >>",
		4: streamObj(content),
	}
	for k, v := range imgObjs {
		objects[k] = v
	}
	n := len(objects)
	pdf := buildRenderPDF(objects, fmt.Sprintf("<< /Size %d /Root 1 0 R >>", n+1))

	r, err := parse.NewResolver(pdf)
	if err != nil {
		t.Fatal(err)
	}
	pages, err := r.Pages()
	if err != nil {
		t.Fatal(err)
	}
	data, err := Page(r, pages[0], Options{DPI: 72, Format: "png"})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode PNG: %v", err)
	}
	return img
}

// fillWholePage scales the image's unit square to cover the whole 100x100 page.
const fillWholePage = "100 0 0 100 0 0 cm /Im Do"

// nonWhitePixels counts pixels that are not (near) white, used to assert that a
// skipped/degenerate image leaves the page unchanged.
func nonWhitePixels(img image.Image) int {
	b := img.Bounds()
	n := 0
	white := color.RGBA{255, 255, 255, 255}
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if !nearlyEqual(img.At(x, y), white, 4) {
				n++
			}
		}
	}
	return n
}

// --- decode + draw: colour, orientation ---

func TestImage_RawDeviceRGB(t *testing.T) {
	// 2x2 image, one distinct colour per corner. Row 0 (top of the image data)
	// is red|green; row 1 is blue|white. After the y-flip, row 0 must land at the
	// TOP of the page.
	data := []byte{
		255, 0, 0, 0, 255, 0, // row 0: red,   green
		0, 0, 255, 255, 255, 255, // row 1: blue,  white
	}
	dict := "/Subtype /Image /Width 2 /Height 2 /ColorSpace /DeviceRGB /BitsPerComponent 8"
	img := renderImagePDF(t, fillWholePage, map[int]string{5: imageStreamObj(dict, data)})

	cases := []struct {
		x, y int
		want color.RGBA
		name string
	}{
		{25, 25, color.RGBA{255, 0, 0, 255}, "top-left red (row 0)"},
		{75, 25, color.RGBA{0, 255, 0, 255}, "top-right green (row 0)"},
		{25, 75, color.RGBA{0, 0, 255, 255}, "bottom-left blue (row 1)"},
		{75, 75, color.RGBA{255, 255, 255, 255}, "bottom-right white (row 1)"},
	}
	for _, c := range cases {
		if got := img.At(c.x, c.y); !nearlyEqual(got, c.want, 6) {
			t.Errorf("%s: At(%d,%d) = %v, want %v", c.name, c.x, c.y, got, c.want)
		}
	}
}

func TestImage_RawDeviceGray(t *testing.T) {
	// Top row dark, bottom row light: pins both the gray value and the y-flip.
	data := []byte{0x30, 0x30, 0xC0, 0xC0}
	dict := "/Subtype /Image /Width 2 /Height 2 /ColorSpace /DeviceGray /BitsPerComponent 8"
	img := renderImagePDF(t, fillWholePage, map[int]string{5: imageStreamObj(dict, data)})

	if got := img.At(50, 25); !nearlyEqual(got, color.RGBA{0x30, 0x30, 0x30, 255}, 6) {
		t.Errorf("top gray = %v, want ~0x30 (row 0 at top)", got)
	}
	if got := img.At(50, 75); !nearlyEqual(got, color.RGBA{0xC0, 0xC0, 0xC0, 255}, 6) {
		t.Errorf("bottom gray = %v, want ~0xC0 (row 1 at bottom)", got)
	}
}

func TestImage_DCTDecode(t *testing.T) {
	// A solid-red 8x8 JPEG. Solid colour keeps DCT block artefacts small; assert
	// the colour is approximately present (JPEG is lossy).
	src := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for i := 0; i < len(src.Pix); i += 4 {
		src.Pix[i], src.Pix[i+1], src.Pix[i+2], src.Pix[i+3] = 220, 20, 20, 255
	}
	var jbuf bytes.Buffer
	if err := jpeg.Encode(&jbuf, src, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	dict := "/Subtype /Image /Width 8 /Height 8 /ColorSpace /DeviceRGB /BitsPerComponent 8 /Filter /DCTDecode"
	img := renderImagePDF(t, fillWholePage, map[int]string{5: imageStreamObj(dict, jbuf.Bytes())})

	if got := img.At(50, 50); !nearlyEqual(got, color.RGBA{220, 20, 20, 255}, 40) {
		t.Errorf("center = %v, want ~red (JPEG tolerance)", got)
	}
}

// --- image masks ---

func TestImage_ImageMask(t *testing.T) {
	// 1-bpc stencil, default /Decode [0 1]: bit 0 paints in the fill colour, bit
	// 1 is transparent. Row 0 = 0x00 (both columns paint), row 1 = 0xC0 (both
	// transparent). Fill is red via `rg`.
	data := []byte{0x00, 0xC0}
	dict := "/Subtype /Image /Width 2 /Height 2 /ImageMask true"
	content := "1 0 0 rg " + fillWholePage
	img := renderImagePDF(t, content, map[int]string{5: imageStreamObj(dict, data)})

	if got := img.At(50, 25); !nearlyEqual(got, color.RGBA{255, 0, 0, 255}, 6) {
		t.Errorf("painted region = %v, want red", got)
	}
	if got := img.At(50, 75); !nearlyEqual(got, color.RGBA{255, 255, 255, 255}, 6) {
		t.Errorf("masked-off region = %v, want white (transparent)", got)
	}
}

func TestImage_ImageMaskDecodeInverted(t *testing.T) {
	// Same bits, but /Decode [1 0] inverts: now bit 1 paints. Row 0 (bits 0)
	// becomes transparent, row 1 (bits 1) paints red.
	data := []byte{0x00, 0xC0}
	dict := "/Subtype /Image /Width 2 /Height 2 /ImageMask true /Decode [1 0]"
	content := "1 0 0 rg " + fillWholePage
	img := renderImagePDF(t, content, map[int]string{5: imageStreamObj(dict, data)})

	if got := img.At(50, 25); !nearlyEqual(got, color.RGBA{255, 255, 255, 255}, 6) {
		t.Errorf("top region = %v, want white (inverted -> transparent)", got)
	}
	if got := img.At(50, 75); !nearlyEqual(got, color.RGBA{255, 0, 0, 255}, 6) {
		t.Errorf("bottom region = %v, want red (inverted -> painted)", got)
	}
}

// --- soft mask ---

func TestImage_SMaskAlpha(t *testing.T) {
	// An all-red base with a soft mask that is opaque on the top row and fully
	// transparent on the bottom row. The transparent half must reveal the white
	// page background.
	base := []byte{
		255, 0, 0, 255, 0, 0,
		255, 0, 0, 255, 0, 0,
	}
	smask := []byte{0xFF, 0xFF, 0x00, 0x00} // row 0 opaque, row 1 transparent
	baseDict := "/Subtype /Image /Width 2 /Height 2 /ColorSpace /DeviceRGB /BitsPerComponent 8 /SMask 6 0 R"
	smDict := "/Subtype /Image /Width 2 /Height 2 /ColorSpace /DeviceGray /BitsPerComponent 8"
	img := renderImagePDF(t, fillWholePage, map[int]string{
		5: imageStreamObj(baseDict, base),
		6: imageStreamObj(smDict, smask),
	})

	if got := img.At(50, 25); !nearlyEqual(got, color.RGBA{255, 0, 0, 255}, 6) {
		t.Errorf("opaque region = %v, want red", got)
	}
	if got := img.At(50, 75); !nearlyEqual(got, color.RGBA{255, 255, 255, 255}, 6) {
		t.Errorf("transparent region = %v, want white background", got)
	}
}

// --- robustness / security skips: each must render without panic and leave the
// page blank (the image is silently skipped). ---

func TestImage_RobustnessSkips(t *testing.T) {
	redRGB := []byte{255, 0, 0, 0, 0, 255, 0, 255, 0, 255, 255, 0} // 2x2, non-white

	cases := []struct {
		name    string
		content string
		objs    map[int]string
	}{
		{
			name:    "width <= 0",
			content: fillWholePage,
			objs:    map[int]string{5: imageStreamObj("/Subtype /Image /Width 0 /Height 2 /ColorSpace /DeviceRGB /BitsPerComponent 8", redRGB)},
		},
		{
			name:    "oversized W*H exceeds pixel cap",
			content: fillWholePage,
			objs:    map[int]string{5: imageStreamObj("/Subtype /Image /Width 100000 /Height 100000 /ColorSpace /DeviceRGB /BitsPerComponent 8", redRGB)},
		},
		{
			name:    "truncated sample buffer",
			content: fillWholePage,
			// 2x2 DeviceRGB needs 12 bytes; supply 3.
			objs: map[int]string{5: imageStreamObj("/Subtype /Image /Width 2 /Height 2 /ColorSpace /DeviceRGB /BitsPerComponent 8", []byte{255, 0, 0})},
		},
		{
			name:    "zero-area cm (det ~ 0)",
			content: "0 0 0 0 0 0 cm /Im Do",
			objs:    map[int]string{5: imageStreamObj("/Subtype /Image /Width 2 /Height 2 /ColorSpace /DeviceRGB /BitsPerComponent 8", redRGB)},
		},
		{
			name:    "unsupported filter (CCITTFaxDecode)",
			content: fillWholePage,
			objs:    map[int]string{5: imageStreamObj("/Subtype /Image /Width 2 /Height 2 /ColorSpace /DeviceGray /BitsPerComponent 1 /Filter /CCITTFaxDecode", redRGB)},
		},
		{
			name:    "corrupt DCTDecode stream",
			content: fillWholePage,
			objs:    map[int]string{5: imageStreamObj("/Subtype /Image /Width 8 /Height 8 /ColorSpace /DeviceRGB /BitsPerComponent 8 /Filter /DCTDecode", []byte("not a real jpeg stream at all"))},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			img := renderImagePDF(t, c.content, c.objs)
			if got := nonWhitePixels(img); got != 0 {
				t.Errorf("non-white pixels = %d, want 0 (image should be skipped, page blank)", got)
			}
		})
	}
}

// --- unit: matrix.invert ---

func TestMatrixInvert_RoundTrip(t *testing.T) {
	mats := []matrix{
		{2, 0, 0, 3, 5, 7},                    // scale + translate
		{1, 0, 0, -1, 0, 100},                 // a y-flip like the device matrix
		{0.5, 0.2, -0.3, 0.9, 10, -4},         // rotation + shear + translate
		{100.0 / 2, 0, 0, -100.0 / 2, 0, 100}, // an image sample->device transform
	}
	for i, m := range mats {
		inv, ok := m.invert()
		if !ok {
			t.Fatalf("case %d: invert reported singular for a non-degenerate matrix", i)
		}
		prod := m.mul(inv)
		id := identity()
		for k := range prod {
			if math.Abs(prod[k]-id[k]) > 1e-9 {
				t.Errorf("case %d: m*inv[%d] = %g, want %g (not identity)", i, k, prod[k], id[k])
			}
		}
	}
}

func TestMatrixInvert_SingularGuard(t *testing.T) {
	singular := []matrix{
		{1, 2, 2, 4, 0, 0}, // det = 1*4 - 2*2 = 0
		{0, 0, 0, 0, 5, 5}, // collapses to a point (zero-area placement)
		{3, 6, 1, 2, 0, 0}, // rows parallel: det = 0
	}
	for i, m := range singular {
		if _, ok := m.invert(); ok {
			t.Errorf("case %d: invert ok=true for a singular matrix, want false", i)
		}
	}
}

// --- unit: imageFilters normalization ---

func TestImageFilters_Normalization(t *testing.T) {
	tests := []struct {
		name string
		dict parse.Dict
		want []parse.Name
	}{
		{"single name", parse.Dict{"Filter": parse.Name("FlateDecode")}, []parse.Name{"FlateDecode"}},
		{"array", parse.Dict{"Filter": parse.Array{parse.Name("ASCII85Decode"), parse.Name("DCTDecode")}}, []parse.Name{"ASCII85Decode", "DCTDecode"}},
		{"absent", parse.Dict{}, nil},
		{"wrong type", parse.Dict{"Filter": parse.Integer(3)}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := imageFilters(&parse.Stream{Dict: tt.dict})
			if len(got) != len(tt.want) {
				t.Fatalf("imageFilters = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("imageFilters[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// TestImage_ExtremePlacement draws a 1x1 black image through CTMs with huge and
// NaN extents and asserts the exact ink count, identically on native and js/wasm
// targets (see TestFill_ExtremeCoordinates): the part of the placement that
// overlaps the page is painted, a NaN placement paints nothing, and an absurd
// /Width is rejected rather than overflowing the size checks.
func TestImage_ExtremePlacement(t *testing.T) {
	big := spelled("1", 30)   // 1e30
	big2 := spelled("2", 30)  // 2e30
	huge := spelled("1", 308) // 1e308
	pixel := imageStreamObj("/Subtype /Image /Width 1 /Height 1 /ColorSpace /DeviceGray /BitsPerComponent 8", []byte{0})
	cases := []struct {
		name    string
		content string
		image   string
		want    int
	}{
		{"1e30 placement covering the page", big + " 0 0 " + big + " 0 0 cm /Im Do", pixel, 100 * 100},
		{"+-1e30 placement covering the page", big2 + " 0 0 " + big2 + " -" + big + " -" + big + " cm /Im Do", pixel, 100 * 100},
		{"1e30 placement over the right half", big + " 0 0 " + big + " 50 0 cm /Im Do", pixel, 50 * 100},
		{"1e30 placement off the page", big + " 0 0 " + big + " " + big + " " + big + " cm /Im Do", pixel, 0},
		{"NaN placement", huge + " 0 0 " + huge + " 0 0 cm " + huge + " 0 0 " + huge + " 0 0 cm /Im Do", pixel, 0},
		// A real /Width of 1e30 saturated to MaxInt64 under js/wasm, overflowing
		// the int64 area check and panicking in makeslice.
		{"1e30 real width", fillWholePage,
			imageStreamObj("/Subtype /Image /Width "+big+" /Height 2 /ColorSpace /DeviceGray /BitsPerComponent 8", []byte{0, 0}), 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			img := renderImagePDF(t, tc.content, map[int]string{5: tc.image})
			if got := darkPixels(img); got != tc.want {
				t.Errorf("dark pixels = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestDrawImage_NonFiniteCTM calls drawImage directly with transforms whose
// unit-square corners mix NaN and ±Inf (math.Max(NaN, +Inf) is +Inf, so the
// bounding box alone would span the canvas). The placement is undefined and must
// paint nothing on every target.
func TestDrawImage_NonFiniteCTM(t *testing.T) {
	nan, inf := math.NaN(), math.Inf(1)
	img := &decodedImage{w: 1, h: 1, pix: []color.RGBA{{0, 0, 0, 0xff}}}
	for _, ctm := range []matrix{
		{inf, 0, 0, inf, 0, 0},
		{inf, 0, 0, -inf, 0, 100},
		{-inf, 0, 0, -inf, 100, 100},
		{nan, 0, 0, 1, 0, 0},
		{1, 0, 0, 1, nan, 0},
	} {
		rs := newRaster(10, 10)
		drawImage(rs, ctm, img)
		for i := 0; i < len(rs.img.Pix); i += 4 {
			if rs.img.Pix[i] != 0xff {
				t.Errorf("ctm %v: pixel %d painted, want none", ctm, i/4)
				break
			}
		}
	}
}

// TestImage_HostileDimensions feeds /Width and /Height values that overflow the
// size arithmetic (SECURITY.md §2) to every image decode path. 2^32 × 2^32 used
// to wrap int64(w)*int64(h) to 0, slip past the pixel cap and panic indexing an
// empty sample buffer, natively and under js/wasm. Each invalid image must be
// skipped silently (no ink, no panic); an invalid /SMask only drops the mask, so
// its base image is still drawn opaque.
func TestImage_HostileDimensions(t *testing.T) {
	var jbuf bytes.Buffer
	if err := jpeg.Encode(&jbuf, image.NewGray(image.Rect(0, 0, 8, 8)), nil); err != nil {
		t.Fatal(err)
	}
	dims := []struct{ name, w, h string }{
		{"2^32 x 2^32", "4294967296", "4294967296"},
		{"2^62 x 4", "4611686018427387904", "4"},
		{"MaxInt64 x 2", "9223372036854775807", "2"},
		{"2^31 x 2", "2147483648", "2"},
		{"side over the pixel cap", "67108865", "1"},
		{"negative width", "-1", "4"},
		{"zero width", "0", "4"},
		{"zero height", "4", "0"},
	}
	samples := []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	kinds := []struct {
		name    string
		content string
		objs    func(w, h string) map[int]string
		want    int
	}{
		{"raw DeviceGray", fillWholePage, func(w, h string) map[int]string {
			return map[int]string{5: imageStreamObj("/Subtype /Image /Width "+w+" /Height "+h+" /ColorSpace /DeviceGray /BitsPerComponent 8", samples)}
		}, 0},
		{"raw DeviceRGB", fillWholePage, func(w, h string) map[int]string {
			return map[int]string{5: imageStreamObj("/Subtype /Image /Width "+w+" /Height "+h+" /ColorSpace /DeviceRGB /BitsPerComponent 8", samples)}
		}, 0},
		{"ImageMask", fillWholePage, func(w, h string) map[int]string {
			return map[int]string{5: imageStreamObj("/Subtype /Image /Width "+w+" /Height "+h+" /ImageMask true", samples)}
		}, 0},
		{"DCTDecode", fillWholePage, func(w, h string) map[int]string {
			return map[int]string{5: imageStreamObj("/Subtype /Image /Width "+w+" /Height "+h+" /ColorSpace /DeviceGray /BitsPerComponent 8 /Filter /DCTDecode", jbuf.Bytes())}
		}, 0},
		{"SMask", fillWholePage, func(w, h string) map[int]string {
			return map[int]string{
				5: imageStreamObj("/Subtype /Image /Width 1 /Height 1 /ColorSpace /DeviceGray /BitsPerComponent 8 /SMask 6 0 R", []byte{0}),
				6: imageStreamObj("/Subtype /Image /Width "+w+" /Height "+h+" /ColorSpace /DeviceGray /BitsPerComponent 8", samples),
			}
		}, 100 * 100},
		// Inline images are skipped whole by the content scanner; pin that the
		// hostile size never reaches a decoder. /Im stays defined but unused.
		{"inline BI/ID/EI", "", func(w, h string) map[int]string {
			return map[int]string{5: imageStreamObj("/Subtype /Image /Width 1 /Height 1 /ColorSpace /DeviceGray /BitsPerComponent 8", []byte{0})}
		}, 0},
	}
	for _, k := range kinds {
		for _, d := range dims {
			t.Run(k.name+"/"+d.name, func(t *testing.T) {
				content := k.content
				if content == "" {
					content = "q 100 0 0 100 0 0 cm BI /W " + d.w + " /H " + d.h + " /CS /G /BPC 8 ID \x00\x00\x00\x00 EI Q"
				}
				img := renderImagePDF(t, content, k.objs(d.w, d.h))
				if got := darkPixels(img); got != k.want {
					t.Errorf("dark pixels = %d, want %d", got, k.want)
				}
			})
		}
	}
}

// TestDecodeJPEG_HeaderSizeCheckedBeforeDecode pins that a JPEG whose SOF header
// claims a frame over the pixel cap is rejected from the header alone. The
// standard decoder allocates the whole frame before reading scan data, so
// without the pre-check a few header bytes cost hundreds of megabytes (and
// 65535×65535 exceeds js/wasm's 4 GiB memory).
func TestDecodeJPEG_HeaderSizeCheckedBeforeDecode(t *testing.T) {
	var jbuf bytes.Buffer
	if err := jpeg.Encode(&jbuf, image.NewGray(image.Rect(0, 0, 8, 8)), nil); err != nil {
		t.Fatal(err)
	}
	raw := jbuf.Bytes()
	sof := bytes.Index(raw, []byte{0xFF, 0xC0})
	if sof < 0 {
		t.Fatal("no SOF0 marker in encoded JPEG")
	}
	// SOF0: marker(2) length(2) precision(1) height(2) width(2).
	raw[sof+5], raw[sof+6] = 0x40, 0x00 // height 16384
	raw[sof+7], raw[sof+8] = 0x40, 0x00 // width 16384: 2^28 px > maxCanvasPixels

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	_, ok := decodeJPEG(&parse.Stream{Raw: raw})
	runtime.ReadMemStats(&after)
	if ok {
		t.Fatal("decodeJPEG accepted a 16384x16384 frame over the pixel cap")
	}
	if got := after.TotalAlloc - before.TotalAlloc; got > 16<<20 {
		t.Errorf("decodeJPEG allocated %d bytes for a rejected header, want < 16 MiB", got)
	}
}
