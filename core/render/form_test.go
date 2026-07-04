package render

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/thai-pc/fluxdocs/core/parse"
	"golang.org/x/image/font/gofont/goregular"
)

// formStreamObj builds a Form XObject stream object body: the /Type /Subtype
// plus the caller-supplied dict entries (e.g. /BBox, /Matrix, /Resources) and a
// computed /Length, followed by the content stream. The content is a page-marking
// operator string, exactly like a page /Contents stream.
func formStreamObj(dictBody, content string) string {
	return fmt.Sprintf("<< /Type /XObject /Subtype /Form %s /Length %d >>\nstream\n%s\nendstream", dictBody, len(content), content)
}

// renderFormPDF assembles a 100x100 one-page PDF whose page carries pageResources
// (the raw inside of the page's /Resources dict) and runs pageContent. Extra
// indirect objects — forms, fonts, images — are supplied in objs (object numbers
// must start at 5 and stay contiguous, since the builder computes a classic xref
// over 1..N). Returns the decoded PNG.
func renderFormPDF(t *testing.T, pageResources, pageContent string, objs map[int]string) image.Image {
	t.Helper()
	objects := map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 100 100] >>",
		3: fmt.Sprintf("<< /Type /Page /Parent 2 0 R /Resources %s /Contents 4 0 R >>", pageResources),
		4: streamObj(pageContent),
	}
	for k, v := range objs {
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

var (
	red   = color.RGBA{255, 0, 0, 255}
	blue  = color.RGBA{0, 0, 255, 255}
	white = color.RGBA{255, 255, 255, 255}
)

// --- basic form drawing ---

func TestForm_BasicFill(t *testing.T) {
	// A form whose content fills a red rect in PDF space [30 30 40 40]. Do'ing it
	// with an identity CTM must land the mark at the centre of the page and leave
	// the corners white — proof the form's content stream is actually interpreted.
	objs := map[int]string{
		5: formStreamObj("/BBox [0 0 100 100]", "1 0 0 rg 30 30 40 40 re f"),
	}
	img := renderFormPDF(t, "<< /XObject << /Fm 5 0 R >> >>", "/Fm Do", objs)

	if got := img.At(50, 50); !nearlyEqual(got, red, 6) {
		t.Errorf("centre = %v, want red (form content must be drawn)", got)
	}
	if got := img.At(5, 5); !nearlyEqual(got, white, 6) {
		t.Errorf("corner = %v, want white (outside the filled rect)", got)
	}
}

// --- /Matrix + CTM concat + per-Do isolation ---

func TestForm_MatrixPlacementAndIsolation(t *testing.T) {
	// The SAME form is Do'd twice through two different page cm transforms. The
	// form's own /Matrix scales its content x2, and its content fills [0 0 10 10]:
	//   - Do #1 at cm identity     -> device rect cols[0,20) rows[80,100)
	//   - Do #2 at cm translate(50,50) -> device rect cols[50,70) rows[30,50)
	// This pins formMatrix.mul(ctm) (the x2 scale, and the translate composition)
	// and the implicit q/Q around Do: the second placement is not shifted by the
	// first (each Do restores the CTM).
	objs := map[int]string{
		5: formStreamObj("/BBox [0 0 100 100] /Matrix [2 0 0 2 0 0]", "1 0 0 rg 0 0 10 10 re f"),
	}
	content := "q 1 0 0 1 0 0 cm /Fm Do Q q 1 0 0 1 50 50 cm /Fm Do Q"
	img := renderFormPDF(t, "<< /XObject << /Fm 5 0 R >> >>", content, objs)

	// Placement #1 centre.
	if got := img.At(10, 90); !nearlyEqual(got, red, 6) {
		t.Errorf("placement #1 centre = %v, want red", got)
	}
	// A pixel that is only inside the x2-scaled rect (device (18,82) sits in
	// cols[0,20)×rows[80,100)); at scale x1 the rect would be [0,10)×[90,100) and
	// this pixel would be white. So red here pins the /Matrix scale.
	if got := img.At(18, 82); !nearlyEqual(got, red, 6) {
		t.Errorf("scaled-rect pixel (18,82) = %v, want red (pins /Matrix x2 scale)", got)
	}
	// Placement #2 centre — proves the translate composed and did not accumulate
	// with placement #1's transform.
	if got := img.At(60, 40); !nearlyEqual(got, red, 6) {
		t.Errorf("placement #2 centre = %v, want red", got)
	}
	// A spot neither placement covers stays white.
	if got := img.At(90, 90); !nearlyEqual(got, white, 6) {
		t.Errorf("uncovered pixel = %v, want white", got)
	}
}

func TestForm_DoIsImplicitQQ(t *testing.T) {
	// The form's content applies an internal cm translate(40,40) then fills
	// [0 0 20 20] -> device cols[40,60) rows[40,60). The page Do's it TWICE with no
	// page cm in between. Because Do is an implicit q/Q, the internal cm is undone
	// after each Do, so both placements land at the SAME spot (page centre). If the
	// internal cm leaked, the second placement would shift to device cols[80,100).
	objs := map[int]string{
		5: formStreamObj("/BBox [0 0 100 100]", "1 0 0 1 40 40 cm 1 0 0 rg 0 0 20 20 re f"),
	}
	img := renderFormPDF(t, "<< /XObject << /Fm 5 0 R >> >>", "/Fm Do /Fm Do", objs)

	if got := img.At(50, 50); !nearlyEqual(got, red, 6) {
		t.Errorf("centre = %v, want red (both placements draw here)", got)
	}
	// If the internal cm had leaked into the caller, the second Do would paint here.
	if got := img.At(90, 10); !nearlyEqual(got, white, 6) {
		t.Errorf("pixel (90,10) = %v, want white (internal cm must not leak across Do)", got)
	}
}

// --- /BBox clipping ---

func TestForm_BBoxClipsContent(t *testing.T) {
	// The form's /BBox is [0 0 40 40] (device cols[0,40) rows[60,100)) but its
	// content fills the WHOLE page. The BBox clip must crop the fill to its device
	// rectangle: inside the box is painted, outside stays white. Pins the clip
	// enforced by raster.blend.
	objs := map[int]string{
		5: formStreamObj("/BBox [0 0 40 40]", "1 0 0 rg 0 0 100 100 re f"),
	}
	img := renderFormPDF(t, "<< /XObject << /Fm 5 0 R >> >>", "/Fm Do", objs)

	if got := img.At(20, 80); !nearlyEqual(got, red, 6) {
		t.Errorf("inside BBox (20,80) = %v, want red", got)
	}
	if got := img.At(80, 80); !nearlyEqual(got, white, 6) {
		t.Errorf("outside BBox in x (80,80) = %v, want white (clipped)", got)
	}
	if got := img.At(20, 20); !nearlyEqual(got, white, 6) {
		t.Errorf("outside BBox in y (20,20) = %v, want white (clipped)", got)
	}
}

// --- form owns its /Resources ---

func TestForm_OwnFontDrawsText(t *testing.T) {
	// The form carries its OWN /Font (embedded Go Regular via /FontFile2); the page
	// does not declare any font. If the form's resource scope is honoured, its text
	// renders ink. Pins the resource/font swap on form entry.
	ttf := string(goregular.TTF)
	objs := map[int]string{
		5: formStreamObj("/BBox [0 0 100 100] /Resources << /Font << /F1 6 0 R >> >>", "BT /F1 50 Tf 10 30 Td (Hi) Tj ET"),
		6: "<< /Type /Font /Subtype /TrueType /BaseFont /GoRegular /FontDescriptor 7 0 R >>",
		7: "<< /Type /FontDescriptor /FontName /GoRegular /Flags 32 /FontFile2 8 0 R >>",
		8: streamObj(ttf),
	}
	img := renderFormPDF(t, "<< /XObject << /Fm 5 0 R >> >>", "/Fm Do", objs)

	if got := darkPixels(img); got < 30 {
		t.Errorf("dark pixels = %d, want >= 30 (form's own font must draw text)", got)
	}
}

func TestForm_NestedImageDo(t *testing.T) {
	// A form that Do's an image XObject from its OWN /Resources: form -> image
	// nested Do. The 2x2 all-red image scaled to fill the BBox must paint red.
	redData := []byte{255, 0, 0, 255, 0, 0, 255, 0, 0, 255, 0, 0}
	imgDict := "/Subtype /Image /Width 2 /Height 2 /ColorSpace /DeviceRGB /BitsPerComponent 8"
	objs := map[int]string{
		5: formStreamObj("/BBox [0 0 100 100] /Resources << /XObject << /Im 6 0 R >> >>", "100 0 0 100 0 0 cm /Im Do"),
		6: imageStreamObj(imgDict, redData),
	}
	img := renderFormPDF(t, "<< /XObject << /Fm 5 0 R >> >>", "/Fm Do", objs)

	if got := img.At(50, 50); !nearlyEqual(got, red, 6) {
		t.Errorf("centre = %v, want red (nested image Do from form resources)", got)
	}
}

// --- q/Q isolation: the fixed bug ---

func TestForm_StrayQDoesNotLeakColor(t *testing.T) {
	// THE regression test. The page fills red, saves (q), Do's a form, then AFTER
	// the form fills the page WITHOUT resetting the colour, and finally restores
	// (Q). The form is unbalanced: it pops twice (Q Q) — the second Q would reach
	// below the form's own saved frame into the caller's — then sets blue and
	// pushes (q). The stackFloor guard blocks that second Q, so the caller's saved
	// red frame is never overwritten and the page's final fill stays red.
	//
	// Without the floor guard, the second Q pops the caller's red frame, the
	// following q overwrites it with blue, and after the form returns the page's
	// `Q` restores blue — the fill would come out blue. Red here proves the guard
	// is a real pin (see the test report for the frame-by-frame trace).
	objs := map[int]string{
		5: formStreamObj("/BBox [0 0 100 100]", "Q Q 0 0 1 rg q"),
	}
	content := "1 0 0 rg q /Fm Do Q 0 0 100 100 re f"
	img := renderFormPDF(t, "<< /XObject << /Fm 5 0 R >> >>", content, objs)

	if got := img.At(50, 50); !nearlyEqual(got, red, 6) {
		t.Errorf("centre = %v, want red (form's stray Q/colour must not leak into caller)", got)
	}
	if nearlyEqual(img.At(50, 50), blue, 6) {
		t.Error("centre is blue: the form's colour leaked into the caller (stackFloor guard broken)")
	}
}

func TestForm_ExtraQFramesDoNotCorrupt(t *testing.T) {
	// The form leaves unbalanced extra `q` frames (three pushes, no pops) and sets
	// blue. On return, truncating the stack to the form's entry length must discard
	// those leftover frames so the page's subsequent drawing uses the page colour.
	objs := map[int]string{
		5: formStreamObj("/BBox [0 0 100 100]", "0 0 1 rg q q q"),
	}
	content := "1 0 0 rg q /Fm Do Q 0 0 100 100 re f"
	img := renderFormPDF(t, "<< /XObject << /Fm 5 0 R >> >>", content, objs)

	if got := img.At(50, 50); !nearlyEqual(got, red, 6) {
		t.Errorf("centre = %v, want red (leftover form q-frames must not corrupt the caller)", got)
	}
}

// --- security / robustness: must render without panic, page effectively
// unaffected ---

func TestForm_SelfReferenceTerminates(t *testing.T) {
	// A form whose own /Resources point /Fm back at itself and whose content Do's
	// /Fm. The cycle guard (formPath keyed by the XObject reference) rejects the
	// re-entry, so the form paints exactly once and the render terminates.
	objs := map[int]string{
		5: formStreamObj("/BBox [0 0 100 100] /Resources << /XObject << /Fm 5 0 R >> >>", "1 0 0 rg 30 30 40 40 re f /Fm Do"),
	}
	img := renderFormPDF(t, "<< /XObject << /Fm 5 0 R >> >>", "/Fm Do", objs)

	if got := img.At(50, 50); !nearlyEqual(got, red, 6) {
		t.Errorf("centre = %v, want red (self-referential form should paint once then stop)", got)
	}
}

func TestForm_MutualRecursionTerminates(t *testing.T) {
	// A -> B -> A. Two distinct forms reference each other; the depth guard alone
	// would let this burn maxFormDepth frames, but the reference-keyed cycle guard
	// stops the A re-entry immediately. B paints once; the render terminates.
	objs := map[int]string{
		5: formStreamObj("/BBox [0 0 100 100] /Resources << /XObject << /Fm 6 0 R >> >>", "/Fm Do"),
		6: formStreamObj("/BBox [0 0 100 100] /Resources << /XObject << /Fm 5 0 R >> >>", "1 0 0 rg 30 30 40 40 re f /Fm Do"),
	}
	img := renderFormPDF(t, "<< /XObject << /Fm 5 0 R >> >>", "/Fm Do", objs)

	if got := img.At(50, 50); !nearlyEqual(got, red, 6) {
		t.Errorf("centre = %v, want red (mutual recursion should paint once then stop)", got)
	}
}

func TestForm_DeepNestingTerminates(t *testing.T) {
	// A chain of 20 distinct forms, each Do'ing the next; the deepest (which would
	// paint) sits far below maxFormDepth (12). The depth guard must cut the chain
	// off — no stack blow-up, no panic — so the paint is never reached and the page
	// stays blank. Every reference is unique, so this exercises the depth bound
	// specifically (not the cycle guard).
	objs := map[int]string{}
	const last = 24 // objects 5..24 => 20 forms
	for i := 5; i < last; i++ {
		objs[i] = formStreamObj(fmt.Sprintf("/BBox [0 0 100 100] /Resources << /XObject << /Fm %d 0 R >> >>", i+1), "/Fm Do")
	}
	objs[last] = formStreamObj("/BBox [0 0 100 100]", "1 0 0 rg 30 30 40 40 re f")

	img := renderFormPDF(t, "<< /XObject << /Fm 5 0 R >> >>", "/Fm Do", objs)

	if got := nonWhitePixels(img); got != 0 {
		t.Errorf("non-white pixels = %d, want 0 (depth guard should cut the chain before the paint)", got)
	}
}

func TestForm_MissingBBoxRendersUnclipped(t *testing.T) {
	// /BBox is spec-required but a hostile/sloppy file may omit it. Per design O2
	// the form renders UNCLIPPED (marks preserved) rather than being dropped, and
	// must not crash.
	objs := map[int]string{
		5: formStreamObj("", "1 0 0 rg 30 30 40 40 re f"),
	}
	img := renderFormPDF(t, "<< /XObject << /Fm 5 0 R >> >>", "/Fm Do", objs)

	if got := img.At(50, 50); !nearlyEqual(got, red, 6) {
		t.Errorf("centre = %v, want red (missing /BBox renders unclipped, no crash)", got)
	}
}

func TestForm_TruncatedStreamSkipped(t *testing.T) {
	// A form declaring /Filter /FlateDecode over bytes that are not valid Flate:
	// DecodeStream fails, so execForm skips the form (consuming no state). The page
	// stays blank and does not panic.
	garbage := []byte("this is not valid flate data at all")
	obj := fmt.Sprintf("<< /Type /XObject /Subtype /Form /BBox [0 0 100 100] /Filter /FlateDecode /Length %d >>\nstream\n%s\nendstream", len(garbage), garbage)
	objs := map[int]string{5: obj}
	img := renderFormPDF(t, "<< /XObject << /Fm 5 0 R >> >>", "/Fm Do", objs)

	if got := nonWhitePixels(img); got != 0 {
		t.Errorf("non-white pixels = %d, want 0 (undecodable form stream must be skipped)", got)
	}
}
