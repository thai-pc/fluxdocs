package render

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"math/rand/v2"
	"os"
	"strings"
	"testing"

	"github.com/thai-pc/fluxdocs/core/internal/std14/faces"
	"github.com/thai-pc/fluxdocs/core/parse"
	"golang.org/x/image/font/gofont/goregular"
)

// This file holds the text-clipping tests of docs/design/clipping-paths.md
// §10.1 item 15 (Phase B, §5): the clip of a text object shown in modes 4–7
// is the union of its glyphs, intersected with the current clip at ET. The
// central identity, by the §2.1 pixel rule: a full-page fill under a text
// clip paints exactly the pixels the same glyphs paint in mode 0. Like
// clip_test.go they run under both targets.

// samePixels fails unless a and b are pixel-identical.
func samePixels(t *testing.T, a, b image.Image) {
	t.Helper()
	if a.Bounds() != b.Bounds() {
		t.Fatalf("sizes %v and %v", a.Bounds(), b.Bounds())
	}
	bd := a.Bounds()
	for y := bd.Min.Y; y < bd.Max.Y; y++ {
		for x := bd.Min.X; x < bd.Max.X; x++ {
			if pa, pb := rgbaAt(a, x, y), rgbaAt(b, x, y); pa != pb {
				t.Fatalf("pixel (%d,%d) = %v, want %v", x, y, pa, pb)
			}
		}
	}
}

// fontFixtures are the fonts the text-clip identities run with: embedded
// TrueType (Go Regular) and Type 1 (the hand-built test font) and a standard
// 14 font drawn with its substitute face, each as a 200×200 pt page builder,
// a glyph-holding string and one with a space.
func fontFixtures() []struct {
	name     string
	mk       func(string) []byte
	ink, gap string
} {
	f := t1TestFont()
	l1, l2, l3 := f.Lengths()
	fixtures := []struct {
		name     string
		mk       func(string) []byte
		ink, gap string
	}{
		{"truetype", buildFontPDF, "HIx", "H x"},
		{"type1", func(c string) []byte { return t1PDF(f.Bytes(), l1, l2, l3, "", c) }, "ABA", "A B"},
		{"std14", func(c string) []byte {
			return buildRenderPDF(map[int]string{
				1: "<< /Type /Catalog /Pages 2 0 R >>",
				2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 200 200] >>",
				3: "<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>",
				4: "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
				5: streamObj(c),
			}, "<< /Size 6 /Root 1 0 R >>")
		}, "HIx", "H x"},
	}
	if !faces.Enabled {
		fixtures = fixtures[:2] // the standard-14 face is not compiled in (nostd14)
	}
	return fixtures
}

// TestTextClip_Modes: in each clip mode a full-page fill after ET paints
// exactly the glyph pixels of mode 0 (the union rule over every Tj, TJ, ' and
// " of the object, under the text state: Tc, Tw, Tz, Ts, TL, a rotated Tm and
// a skewed CTM), and modes 4–6 also paint as mode 0, mode 7 not at all.
func TestTextClip_Modes(t *testing.T) {
	shows := []struct{ name, pre, text, post string }{
		{"Tj", "", "/F1 60 Tf 10 40 Td (%[1]s) Tj", ""},
		{"multi", "", "/F1 50 Tf 10 20 Td (%[1]s) Tj [(%[1]s) -400 (%[1]s)] TJ 60 TL 0 100 Td (%[1]s) ' 3 0 (%[1]s) \"", ""},
		{"state", "", "/F1 40 Tf 7 Tc 20 Tw 160 Tz 9 Ts 0.866 0.5 -0.5 0.866 40 10 Tm (%[2]s%[1]s) Tj", ""},
		{"ctm", "q 1 0.3 -0.2 0.9 30 20 cm ", "/F1 40 Tf (%[1]s) Tj", " Q"},
	}
	for _, f := range fontFixtures() {
		for _, s := range shows {
			text := fmt.Sprintf(s.text, f.ink, f.gap)
			mode0 := renderPDF(t, f.mk(s.pre+"1 0 0 rg BT 0 Tr "+text+" ET"+s.post))
			if inkCount(mode0) == 0 {
				t.Fatalf("%s/%s: no ink in mode 0", f.name, s.name)
			}
			for _, m := range []string{"4", "5", "6", "7"} {
				t.Run(f.name+"/"+s.name+"/"+m, func(t *testing.T) {
					got := renderPDF(t, f.mk(s.pre+"BT "+m+" Tr "+text+" ET 1 0 0 rg -1000 -1000 3000 3000 re f"+s.post))
					samePixels(t, got, mode0)
					shown := renderPDF(t, f.mk(s.pre+"1 0 0 rg BT "+m+" Tr "+text+" ET"+s.post))
					if m == "7" {
						if n := inkCount(shown); n != 0 {
							t.Fatalf("mode 7 painted %d pixels", n)
						}
						return
					}
					samePixels(t, shown, mode0)
				})
			}
		}
	}
}

// TestTextClip_Intersection: ET intersects the union with the current clip,
// a rectangle or a general mask, in either order, and successive clip-mode
// text objects intersect each other.
func TestTextClip_Intersection(t *testing.T) {
	text := func(x int) string { return fmt.Sprintf("/F1 70 Tf %d 40 Td (HIH) Tj", x) }
	tri := "0 0 m 200 0 l 0 200 l h"
	for _, tt := range []struct{ name, clipped, want string }{
		{"rect then text", "q 0 0 60 200 re W n BT 7 Tr " + text(10) + " ET 0 g 0 0 200 200 re f Q", "q 0 0 60 200 re W n BT 0 Tr " + text(10) + " ET Q"},
		{"general then text", "q " + tri + " W n BT 7 Tr " + text(10) + " ET 0 g 0 0 200 200 re f Q", "q " + tri + " W n BT 0 Tr " + text(10) + " ET Q"},
		{"text then general", "BT 7 Tr " + text(10) + " ET " + tri + " W n 0 g 0 0 200 200 re f", "q " + tri + " W n BT 0 Tr " + text(10) + " ET Q"},
		{"text then text", "BT 7 Tr " + text(10) + " ET BT 7 Tr " + text(30) + " ET 0 g 0 0 200 200 re f", ""},
		{"inside W*", "q 10 10 180 180 re 50 50 60 60 re W* n BT 4 Tr " + text(10) + " ET 1 0 0 rg 0 0 200 200 re f Q", "q 10 10 180 180 re 50 50 60 60 re W* n 1 0 0 rg BT 0 Tr " + text(10) + " ET Q"},
		{"W inside BT", "BT 7 Tr " + text(10) + " 0 0 90 200 re W n ET 0 g 0 0 200 200 re f", "q 0 0 90 200 re W n BT 0 Tr " + text(10) + " ET Q"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := renderPDF(t, buildFontPDF(tt.clipped))
			if tt.want != "" {
				samePixels(t, got, renderPDF(t, buildFontPDF(tt.want)))
				return
			}
			a := renderPDF(t, buildFontPDF("BT 0 Tr "+text(10)+" ET"))
			b := renderPDF(t, buildFontPDF("BT 0 Tr "+text(30)+" ET"))
			both := 0
			for y := 0; y < 200; y++ {
				for x := 0; x < 200; x++ {
					want := white
					if rgbaAt(a, x, y) == black && rgbaAt(b, x, y) == black {
						want, both = black, both+1
					}
					if g := rgbaAt(got, x, y); g != want {
						t.Fatalf("pixel (%d,%d) = %v, want %v", x, y, g, want)
					}
				}
			}
			if both == 0 {
				t.Fatal("fixture proves nothing: the two texts share no pixel")
			}
		})
	}
}

// TestTextClip_Lenient pins the lenient decisions of textclip.go (§5, with
// the Ghostscript comparison in clip_gs_test.go): fail-visible discards
// narrow nothing, empty glyphs clip everything, and the clip follows the
// graphics state after ET.
func TestTextClip_Lenient(t *testing.T) {
	const fill = " 0 g 0 0 200 200 re f"
	const show = "/F1 70 Tf 10 40 Td (HI) Tj"
	full := 200 * 200
	glyphs := inkCount(renderPDF(t, buildFontPDF("BT "+show+" ET")))
	huge := "1" + strings.Repeat("0", 308) + ".0" // 1e308: the text rendering matrix overflows to ±Inf, and its products to NaN
	form := func(content string) []byte {
		return buildRenderPDF(map[int]string{
			1: "<< /Type /Catalog /Pages 2 0 R >>",
			2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 200 200] >>",
			3: "<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 4 0 R >> /XObject << /Fm 8 0 R /Op 9 0 R >> >> /Contents 5 0 R >>",
			4: "<< /Type /Font /Subtype /TrueType /BaseFont /GoRegular /FontDescriptor 6 0 R >>",
			5: streamObj(content),
			6: "<< /Type /FontDescriptor /FontName /GoRegular /Flags 32 /FontFile2 7 0 R >>",
			7: streamObj(string(goregular.TTF)),
			8: streamObjDict("/Type /XObject /Subtype /Form /BBox [0 0 200 200]", "BT 7 Tr "+show+" ET 0 0 1 rg 0 0 200 200 re f"),
			9: streamObjDict("/Type /XObject /Subtype /Form /BBox [0 0 200 200]", "BT 7 Tr "+show),
		}, "<< /Size 10 /Root 1 0 R >>")
	}
	// A Type 3 font is not drawn (glyphfont.go), so its outlines are
	// unavailable although its glyph procedures would paint.
	type3 := func(content string) []byte {
		return buildRenderPDF(map[int]string{
			1: "<< /Type /Catalog /Pages 2 0 R >>",
			2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 200 200] >>",
			3: "<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>",
			4: "<< /Type /Font /Subtype /Type3 /FontBBox [0 0 1000 1000] /FontMatrix [0.001 0 0 0.001 0 0] /CharProcs << /sq 6 0 R >> /Encoding << /Type /Encoding /Differences [72 /sq 73 /sq] >> /FirstChar 72 /LastChar 73 /Widths [1000 1000] /Resources << >> >>",
			5: streamObj(content),
			6: streamObj("1000 0 0 0 1000 1000 d1 0 0 1000 1000 re f"),
		}, "<< /Size 7 /Root 1 0 R >>")
	}
	for _, tt := range []struct {
		name, content string
		ink           int // -1: the glyphs' ink
		pdf           func(string) []byte
	}{
		{"clips", "BT 7 Tr " + show + " ET" + fill, -1, nil},
		{"Q4: only spaces clip everything", "BT 7 Tr /F1 70 Tf 10 40 Td (   ) Tj ET" + fill, 0, nil},
		{"a space adds nothing", "BT 7 Tr /F1 70 Tf 10 40 Td (H I) Tj ET" + fill, -2, nil},
		{"no clip-mode glyph", "BT 7 Tr ET" + fill, full, nil},
		{"empty string", "BT 7 Tr /F1 70 Tf 10 40 Td () Tj ET" + fill, full, nil},
		{"mode 3 does not clip", "BT 3 Tr " + show + " ET" + fill, full, nil},
		{"4.5 Tr fills and does not clip", "BT 4.5 Tr " + show + " ET 1 0 0 rg 0 0 200 200 re f", full, nil},
		{"7.0 Tr clips", "BT 7.0 Tr " + show + " ET" + fill, -1, nil},
		{"no ET", "BT 7 Tr " + show + fill, full, nil},
		{"BT inside", "BT 7 Tr " + show + " BT ET" + fill, full, nil},
		{"no font", "BT 7 Tr 10 40 Td (HI) Tj ET" + fill, full, nil},
		{"font that does not load", "BT 7 Tr /F9 70 Tf 10 40 Td (HI) Tj ET" + fill, full, nil},
		{"Type 3 font", "BT 7 Tr " + show + " ET" + fill, full, type3},
		{"0 Tf clips everything", "BT 7 Tr /F1 0 Tf 10 40 Td (HI) Tj ET" + fill, 0, nil},
		{"overflowing Tm clips everything", "BT 7 Tr /F1 70 Tf " + huge + " 0 0 " + huge + " 0 0 Tm (HI) Tj ET" + fill, 0, nil},
		{"code without a glyph", "BT 7 Tr /F1 70 Tf 10 40 Td (H\x01I) Tj ET" + fill, full, nil},
		{"Q inside discards", "q BT 7 Tr " + show + " Q ET" + fill, full, nil},
		{"Q inside keeps mode 4 paint", "q BT 4 Tr " + show + " Q ET 1 0 0 rg 0 0 200 200 re f", full, nil},
		{"q inside keeps", "BT q 7 Tr " + show + " ET" + fill, -1, nil},
		{"Q after ET restores", "q BT 7 Tr " + show + " ET Q" + fill, full, nil},
		{"mode switch keeps the clip-mode glyphs", "BT 7 Tr /F1 70 Tf 10 40 Td (H) Tj 0 Tr ET" + fill, -3, nil},
		{"form clip is undone at exit", "/Fm Do" + fill, full, form},
		{"form left open narrows nothing", "/Op Do" + fill, full, form},
		{"Do inside BT discards", "BT 7 Tr " + show + " /Fm Do ET" + fill, full, form},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mk := buildFontPDF
			if tt.pdf != nil {
				mk = tt.pdf
			}
			got := inkCount(renderPDF(t, mk(tt.content)))
			want := tt.ink
			switch want {
			case -1:
				want = glyphs
			case -2:
				want = inkCount(renderPDF(t, buildFontPDF("BT /F1 70 Tf 10 40 Td (H I) Tj ET")))
			case -3:
				want = inkCount(renderPDF(t, buildFontPDF("BT /F1 70 Tf 10 40 Td (H) Tj ET")))
			}
			if got != want {
				t.Errorf("ink = %d px, want %d", got, want)
			}
		})
	}
}

// TestTextClip_LiveBytes: the accumulation mask counts toward the live
// clip-mask bytes from the first clip-mode glyph; at ET it is released and
// the clip's mask, shrunk to the clip's tight box, is the only one live (or
// none is), and a trip names "clip masks".
func TestTextClip_LiveBytes(t *testing.T) {
	r, err := parse.NewResolver(buildFontPDF(""))
	if err != nil {
		t.Fatal(err)
	}
	pages, err := r.Pages()
	if err != nil {
		t.Fatal(err)
	}
	full := int64(200 * ((200+63)/64*8 + 8)) // a 200×200 mask: 4 words and 8 bytes of extents per row
	for _, tt := range []struct {
		name   string
		src    string
		after  []int64 // live bytes after each operator; -1: the clip mask's, sized to the clip's box
		noMask bool
	}{
		{"ET keeps it", "BT 7 Tr /F1 70 Tf 10 40 Td (H) Tj ET", []int64{0, 0, 0, 0, full, -1}, false},
		{"Q releases it", "q BT 7 Tr /F1 70 Tf 10 40 Td (H) Tj Q ET", []int64{0, 0, 0, 0, 0, full, 0, 0}, true},
		{"BT releases it", "BT 7 Tr /F1 70 Tf 10 40 Td (H) Tj BT", []int64{0, 0, 0, 0, full, 0}, true},
		{"spaces release it", "BT 7 Tr /F1 70 Tf 10 40 Td ( ) Tj ET", []int64{0, 0, 0, 0, full, 0}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rs := newRaster(200, 200)
			in := newInterp(rs, identity(), r, pages[0].Resources)
			ops := parse.ScanContent([]byte(tt.src))
			if len(ops) != len(tt.after) {
				t.Fatalf("%d ops, %d expectations", len(ops), len(tt.after))
			}
			for i, op := range ops {
				in.apply(op)
				want := tt.after[i]
				if want < 0 {
					if in.gs.clip.mask == nil {
						t.Fatalf("after op %d (%s): no clip mask", i, op.Op)
					}
					m, c := in.gs.clip.mask, in.gs.clip.rect
					stride, rows := int64((c.x1+63)>>6-c.x0>>6), int64(c.y1-c.y0)
					if want = stride*rows*8 + rows*8; m.bytes != want || m.y0 != c.y0 || m.y1 != c.y1 || want >= full {
						t.Fatalf("after op %d (%s): mask rows [%d,%d), %d bytes; want the clip's rows [%d,%d), %d bytes (< %d)",
							i, op.Op, m.y0, m.y1, m.bytes, c.y0, c.y1, want, full)
					}
				}
				if got := rs.b.clipMask; got != want {
					t.Fatalf("after op %d (%s): live bytes %d, want %d", i, op.Op, got, want)
				}
			}
			if tt.noMask && in.gs.clip.mask != nil {
				t.Errorf("clip mask kept: %+v", in.gs.clip.rect)
			}
		})
	}

	lim := withLimitT(func(l *budgetLimits) { l.clipMask = 1 << 10 })
	data, err := Page(r, pages[0], Options{DPI: 72, testLimits: lim})
	if err != nil || data == nil {
		t.Fatalf("no text: %v", err)
	}
	r2, err := parse.NewResolver(buildFontPDF("BT 7 Tr /F1 70 Tf 10 40 Td (H) Tj ET"))
	if err != nil {
		t.Fatal(err)
	}
	pages2, _ := r2.Pages()
	if _, err := Page(r2, pages2[0], Options{DPI: 72, testLimits: lim}); !errors.Is(err, ErrBudgetExceeded) || !strings.Contains(err.Error(), "clip masks") {
		t.Fatalf("err = %v, want a clip masks trip", err)
	}
}

// TestTextClip_NestedAtCanvasCap: 40 q-nested clip-mode words on a canvas
// of maxCanvasPixels, under the default clip-mask cap. Each level's clip
// keeps only a mask over its word's box, not the canvas-sized accumulation
// of the first level (≈ 8 MiB), so the levels hold ≈ 1 MiB together and the
// render never trips "clip masks". Mode 7 paints nothing, so the raster
// carries no pixel buffer.
func TestTextClip_NestedAtCanvasCap(t *testing.T) {
	const side, levels = 8192, 40 // side*side == maxCanvasPixels
	r, err := parse.NewResolver(buildFontPDF(""))
	if err != nil {
		t.Fatal(err)
	}
	pages, err := r.Pages()
	if err != nil {
		t.Fatal(err)
	}
	rs := &raster{w: side, h: side, clip: fullClip(side, side), b: newBudget(context.Background(), resolveLimits(-1, -1, side*side))}
	in := newInterp(rs, matrix{side / 200.0, 0, 0, -side / 200.0, 0, side}, r, pages[0].Resources)
	for _, op := range parse.ScanContent([]byte(strings.Repeat("q BT 7 Tr /F1 12 Tf 10 40 Td (HI) Tj ET ", levels))) {
		in.apply(op)
	}
	if rs.b.err != nil {
		t.Fatalf("render stopped: %v", rs.b.err)
	}
	m, c := in.gs.clip.mask, in.gs.clip.rect
	if m == nil || c.empty() {
		t.Fatalf("no clip mask: %+v", c)
	}
	stride, rows := int64((c.x1+63)>>6-c.x0>>6), int64(c.y1-c.y0)
	word := stride*rows*8 + rows*8
	if m.bytes != word {
		t.Errorf("innermost mask %d bytes, want %d (its box %+v)", m.bytes, word, c)
	}
	if live := rs.b.clipMask; live > levels*word {
		t.Errorf("live clip-mask bytes %d, want ≤ %d (%d levels of %d bytes)", live, levels*word, levels, word)
	}
}

// TestTextClip_Charges: a clip-mode text object pays its mask build (rows,
// the zeroing and extent pass, clipWords per span) and, at ET, the one-pass
// build of the mask shrunk to the clip's tight box on top of the glyph's
// outline and scan, and an ET under an enclosing mask pays the AND pass.
func TestTextClip_Charges(t *testing.T) {
	var clip *clipMask // the clip mask the last paint left
	paint := func(src string) int64 {
		r, err := parse.NewResolver(buildFontPDF(""))
		if err != nil {
			t.Fatal(err)
		}
		pages, _ := r.Pages()
		rs := newRaster(200, 200)
		in := newInterp(rs, identity(), r, pages[0].Resources)
		for _, op := range parse.ScanContent([]byte(src)) {
			in.apply(op)
		}
		clip = in.gs.clip.mask
		return rs.b.paint
	}
	invisible := paint("BT 3 Tr /F1 70 Tf 10 40 Td (H) Tj ET")
	visible := paint("BT 0 Tr /F1 70 Tf 10 40 Td (H) Tj ET")
	clip7 := paint("BT 7 Tr /F1 70 Tf 10 40 Td (H) Tj ET")
	if clip == nil {
		t.Fatal("no clip mask")
	}
	if clip.stride >= 4 || clip.y1-clip.y0 >= 200 {
		t.Fatalf("clip mask of %d words × rows [%d,%d), want one shrunk to the glyph", clip.stride, clip.y0, clip.y1)
	}
	shrink := int64(clip.y1-clip.y0) * (clipRowCost + clipWords(clip.stride))
	clip4 := paint("BT 4 Tr /F1 70 Tf 10 40 Td (H) Tj ET")
	build := int64(200)*(clipRowCost+textClipPasses*clipWords(4)) + shrink
	// Loading the font costs the same in every mode: count from mode 3,
	// which paints nothing. Mode 7 scans the glyph's rows without painting;
	// mode 4 paints them too. Both pay the build and one clipWords per span
	// (≤ 1 per row here) on top.
	clip7, clip4, visible = clip7-invisible, clip4-invisible, visible-invisible
	if clip7 < build || clip7 >= visible+build {
		t.Errorf("mode 7 charged %d, want in [%d, %d)", clip7, build, visible+build)
	}
	if clip4 <= visible+build || clip4 > visible+build+200 {
		t.Errorf("mode 4 charged %d, want in (%d, %d]", clip4, visible+build, visible+build+200)
	}
	tri := "0 0 m 200 0 l 0 200 l h W n "
	and := paint(tri+"BT 7 Tr /F1 70 Tf 10 40 Td (H) Tj ET") - paint(tri+"BT 7 Tr /F1 70 Tf 10 40 Td (H) Tj")
	if and <= 0 {
		t.Errorf("ET under a mask charged %d", and)
	}
}

// textClipParityFixture is the content of the text-clip fixture whose pixel
// hash is pinned here and in wasm/smoke_test.mjs (§7), on a 200×100 page
// with a standard 14 font (drawn with its substitute face, whose outlines
// are rounded explicitly) and an RGB image: a general W clip, then clip-mode
// text objects in modes 7, 4 and 6 with Tj and TJ, Tc, Tz, Ts and a rotated
// Tm, each followed by paint the text clip narrows, and a space-only object
// (Q4) inside q/Q.
const textClipParityFixture = "q 3 2 m 197 10 l 180 98 l 8 90 l h W n " +
	"BT 7 Tr /F1 48 Tf 4 Tc 120 Tz 10 30 Td (Flux) Tj [(D) -250 (o)] TJ ET " +
	"q 200 0 0 100 0 0 cm /Im Do Q " +
	"BT 4 Tr 0 0.5 0 rg /F1 30 Tf 6 Ts 0.94 0.34 -0.34 0.94 20 50 Tm (cs) Tj ET " +
	"1 0 0 rg 0 0 200 100 re f Q " +
	"q BT 6 Tr 0 0 1 rg /F1 20 Tf 120 70 Td (ok) Tj ET 0.9 0.6 0 rg 100 0 100 100 re f Q " +
	"q BT 7 Tr /F1 20 Tf 10 80 Td (  ) Tj ET 0 g 0 0 200 100 re f Q"

// textClipParityHash is the pixel hash of textClipParityFixture at 72 DPI
// (imgHash).
const textClipParityHash = "767c3b16600855f1268938040b739f023ac367b4ee59cb06c95828b4c3e71ad2"

// textClipParityPage builds the fixture's PDF.
func textClipParityPage() []byte {
	rgb := make([]byte, 4*4*3)
	for i := range rgb {
		rgb[i] = byte(i * 37)
	}
	return clipPage(200, 100, "<< /Font << /F1 5 0 R >> /XObject << /Im 6 0 R >> >>", textClipParityFixture,
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		fmt.Sprintf("<< /Type /XObject /Subtype /Image /Width 4 /Height 4 /ColorSpace /DeviceRGB /BitsPerComponent 8 /Length %d >>\nstream\n%s\nendstream", len(rgb), rgb))
}

// TestTextClip_ParityHash pins the text-clip fixture's pixels, so native
// amd64 and js/wasm renders are byte-identical (§7; arm64 is not checked,
// see clipParityFixture). The fixture draws Helvetica with the bundled
// substitute face, which -tags nostd14 leaves out.
func TestTextClip_ParityHash(t *testing.T) {
	if !faces.Enabled {
		t.Skip("faces not compiled in (nostd14)")
	}
	img := renderPDF(t, textClipParityPage())
	if got := imgHash(img); got != textClipParityHash {
		t.Errorf("text-clip parity fixture hash = %s, want %s", got, textClipParityHash)
	}
}

// textClipParityPDF is wasm/testdata/textclip.pdf, the fixture as a file for
// wasm/smoke_test.mjs.
const textClipParityPDF = "../../wasm/testdata/textclip.pdf"

// TestTextClip_ParityFixtureFile keeps wasm/testdata/textclip.pdf in step
// with textClipParityFixture (FLUXDOCS_WRITE_CLIP_FIXTURE=1 rewrites it).
func TestTextClip_ParityFixtureFile(t *testing.T) {
	want := textClipParityPage()
	if os.Getenv("FLUXDOCS_WRITE_CLIP_FIXTURE") == "1" {
		if err := os.WriteFile(textClipParityPDF, want, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(textClipParityPDF)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s is stale: rerun with FLUXDOCS_WRITE_CLIP_FIXTURE=1", textClipParityPDF)
	}
}

// TestTextClip_SmokeHashInStep keeps wasm/smoke_test.mjs's copy of
// textClipParityHash from drifting.
func TestTextClip_SmokeHashInStep(t *testing.T) {
	src, err := os.ReadFile("../../wasm/smoke_test.mjs")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(src, []byte(`const textClipHash = "`+textClipParityHash+`";`)) {
		t.Errorf("wasm/smoke_test.mjs does not hold textClipHash %q (textClipParityHash)", textClipParityHash)
	}
}

// TestTextClip_KeepColumns checks keepColumns against a bit-by-bit oracle on
// random rows, ranges inside, across and outside the row.
func TestTextClip_KeepColumns(t *testing.T) {
	rng := rand.New(rand.NewPCG(41, 42))
	for iter := 0; iter < 20000; iter++ {
		x0w, n := rng.IntN(4), 1+rng.IntN(5)
		row := make([]uint64, n)
		for i := range row {
			row[i] = rng.Uint64()
		}
		lo := rng.IntN((x0w+n+2)*64) - 32
		hi := lo + 1 + rng.IntN(400)
		want := make([]uint64, n)
		for i := range row {
			for b := 0; b < 64; b++ {
				if x := (x0w+i)*64 + b; x >= lo && x < hi {
					want[i] |= row[i] & (1 << uint(b))
				}
			}
		}
		got := append([]uint64(nil), row...)
		keepColumns(got, x0w, lo, hi)
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("x0w %d, %d words, [%d, %d): word %d = %#x, want %#x", x0w, n, lo, hi, i, got[i], want[i])
			}
		}
	}
}
