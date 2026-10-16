package redaction

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"sort"
	"strings"

	"github.com/thai-pc/fluxdocs/core"
)

// This file holds the leak corpus fixtures and table (see leakcorpus_test.go).
// Every fixture is generated here from synthetic content; the only secret is
// the constant `secret` (SECURITY.md §7).

// leakPDF assembles objs into a classic-xref PDF. Object numbers may have gaps
// (free xref entries). info, when not empty, is the trailer's /Info value.
func leakPDF(objs map[int]string, info string) []byte {
	keys := make([]int, 0, len(objs))
	for k := range objs {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	size := keys[len(keys)-1] + 1
	var b bytes.Buffer
	b.WriteString("%PDF-1.7\n")
	offs := make([]int, size)
	for _, k := range keys {
		offs[k] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", k, objs[k])
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", size)
	for i := 1; i < size; i++ {
		if offs[i] == 0 {
			b.WriteString("0000000000 65535 f \n")
			continue
		}
		fmt.Fprintf(&b, "%010d 00000 n \n", offs[i])
	}
	if info != "" {
		info = " /Info " + info
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R%s >>\nstartxref\n%d\n%%%%EOF\n", size, info, xref)
	return b.Bytes()
}

// stm is a stream object with dict entries and data.
func stm(dict, data string) string {
	return fmt.Sprintf("<< %s /Length %d >>\nstream\n%s\nendstream", dict, len(data), data)
}

// flateStm is stm with data Flate-encoded, so the raw bytes do not show it.
func flateStm(dict, data string) string {
	var b bytes.Buffer
	w := zlib.NewWriter(&b)
	w.Write([]byte(data))
	w.Close()
	return stm(dict+" /Filter /FlateDecode", b.String())
}

// onePage describes a one-page 400x800 fixture: catalog 1, pages 2, page 3,
// content 4 (more content streams from 20), Helvetica /F1 5, extra objects
// from 6.
type onePage struct {
	content   []string       // content streams; more than one makes /Contents an array
	res       string         // page /Resources; "" is << /Font << /F1 5 0 R >> >>, "NONE" none
	pagesRes  string         // /Resources on the /Pages node
	pageExtra string         // more page-dict entries
	extra     map[int]string // more objects
	info      string         // trailer /Info value
}

func (f onePage) pdf() []byte {
	res := f.res
	switch res {
	case "":
		res = " /Resources << /Font << /F1 5 0 R >> >>"
	case "NONE":
		res = ""
	default:
		res = " /Resources " + res
	}
	pagesRes := ""
	if f.pagesRes != "" {
		pagesRes = " /Resources " + f.pagesRes
	}
	objs := map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 400 800]" + pagesRes + " >>",
		4: stm("", f.content[0]),
		5: helvetica,
	}
	contents := "4 0 R"
	if len(f.content) > 1 {
		refs := []string{"4 0 R"}
		for i, c := range f.content[1:] {
			objs[20+i] = stm("", c)
			refs = append(refs, fmt.Sprintf("%d 0 R", 20+i))
		}
		contents = "[" + strings.Join(refs, " ") + "]"
	}
	objs[3] = "<< /Type /Page /Parent 2 0 R" + res + " /Contents " + contents + f.pageExtra + " >>"
	for k, v := range f.extra {
		objs[k] = v
	}
	return leakPDF(objs, f.info)
}

const helvetica = "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"

// page is a one-page fixture with a single content stream and default
// resources.
func page(content string) func() []byte { return onePage{content: []string{content}}.pdf }

// twoPages is a two-page fixture: page 3 (index 0) with content 4 and
// pageExtra0, page 6 (index 1) with content 8 and pageExtra1, Helvetica 5, and
// extra objects from 9.
func twoPages(content0, pageExtra0, content1, pageExtra1 string, extra map[int]string, info string) func() []byte {
	return func() []byte {
		objs := map[int]string{
			1: "<< /Type /Catalog /Pages 2 0 R >>",
			2: "<< /Type /Pages /Kids [3 0 R 6 0 R] /Count 2 /MediaBox [0 0 400 800] >>",
			3: "<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R" + pageExtra0 + " >>",
			4: stm("", content0),
			5: helvetica,
			6: "<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 5 0 R >> >> /Contents 8 0 R" + pageExtra1 + " >>",
			8: stm("", content1),
		}
		for k, v := range extra {
			objs[k] = v
		}
		return leakPDF(objs, info)
	}
}

// show is a text object showing s with Helvetica 24 at (72, 700).
func show(s string) string { return "BT /F1 24 Tf 72 700 Td " + s + " Tj ET" }

// S is the secret as a literal string operand.
const S = "(" + secret + ")"

// textForm is a form XObject showing the secret, with extra dict entries.
func textForm(extraDict string) string {
	return stm("/Type /XObject /Subtype /Form /BBox [0 0 400 800] /Resources << /Font << /F1 5 0 R >> >> "+extraDict,
		"BT /F1 24 Tf 72 500 Td "+S+" Tj ET")
}

// identityHFont returns objects 6–9: a Type 0 font /F2 with Identity-H
// encoding (as Word, Chrome and LaTeX write them) whose CIDs are the Unicode
// code points of the text, and its /ToUnicode CMap.
func identityHFont() map[int]string {
	cmap := "/CIDInit /ProcSet findresource begin\n12 dict begin\nbegincmap\n" +
		"/CIDSystemInfo << /Registry (Adobe) /Ordering (UCS) /Supplement 0 >> def\n" +
		"/CMapName /Adobe-Identity-UCS def\n/CMapType 2 def\n" +
		"1 begincodespacerange\n<0000> <FFFF>\nendcodespacerange\n" +
		"1 beginbfrange\n<0020> <007E> <0020>\nendbfrange\n" +
		"endcmap\nCMapName currentdict /CMap defineresource pop\nend\nend"
	return map[int]string{
		6: "<< /Type /Font /Subtype /Type0 /BaseFont /Helvetica /Encoding /Identity-H /DescendantFonts [7 0 R] /ToUnicode 8 0 R >>",
		7: "<< /Type /Font /Subtype /CIDFontType2 /BaseFont /Helvetica /CIDSystemInfo << /Registry (Adobe) /Ordering (Identity) /Supplement 0 >> " +
			"/FontDescriptor 9 0 R /DW 600 /CIDToGIDMap /Identity >>",
		8: stm("", cmap),
		9: "<< /Type /FontDescriptor /FontName /Helvetica /Flags 32 /FontBBox [0 -200 1000 900] /ItalicAngle 0 " +
			"/Ascent 900 /Descent -200 /CapHeight 700 /StemV 80 >>",
	}
}

// utf16Hex is s as a hex string of 2-byte codes equal to its code points.
func utf16Hex(s string) string {
	var b strings.Builder
	b.WriteByte('<')
	for _, r := range s {
		fmt.Fprintf(&b, "%04X", r)
	}
	b.WriteByte('>')
	return b.String()
}

// partial returns a pointer to an area rect (for rows that redact part of a
// page).
func partial(x, y, w, h float64) *core.Rect { return &core.Rect{X: x, Y: y, Width: w, Height: h} }

// leakCases is the leak corpus (docs/design/redaction-hardening.md §12.1):
// every repro from the review rounds (reviewer harnesses rv5 and rv7), the
// N1–N6 repros, D4 extension cases, and the malformed-content and
// deep-nesting tables. Each fixture runs once per operation in ops.
//
// want is the expectation at this point of Phase 1. A knownLeak row names the
// step that closes it and the outcome it will then have; it is skipped while
// the leak reproduces and fails once it no longer does, so the table cannot
// silently drift.
func leakCases() []leakCase {
	both := []leakOp{opText, opArea}
	ii := "BI /W 1 /H 1 /BPC 8 /CS /G ID "
	cases := []leakCase{
		// --- rv7 ---
		{name: "control", pdf: page(show(S)), ops: both, want: clean},

		// Inline image boundary (§6.1, item 1). A: the exact-length end is
		// followed by EI with no white-space before it, so an EI-searching
		// reader (our lenient scan) runs on to a later EI and hides the text an
		// exact-length reader (Ghostscript) shows. B: the data holds a
		// white-space-delimited EI, so the lenient scan stops early and a
		// string opened in the rest of the data swallows the real text.
		// Reverse of A: text sits between the exact end and the EI the
		// lenient scan finds.
		{name: "ii_tight_EI", pdf: page("BT /F1 24 Tf 72 700 Td ET\n" + ii + "AEI\n" + show(S) + "\nEI\n"),
			ops: both, want: refuse, reason: "inline_image"},
		{name: "ii_tight_EI_hex", pdf: page("BI /W 1 /H 1 /BPC 8 /CS /G /F /AHx ID 41>EI\n" + show(S) + "\nEI\n"),
			ops: both, want: refuse, reason: "inline_image"},
		{name: "ii_early_swallow",
			pdf:  page("BI /W 8 /H 1 /BPC 8 /CS /G ID \nEI\n(abc\nEI\nBT /F1 24 Tf 72 100 Td " + S + " Tj ET\nBT /F1 24 Tf 72 700 Td (x)) Tj ET"),
			area: partial(60, 90, 300, 40), ops: both, want: refuse, reason: "inline_image"},
		{name: "ii_reverse_text_before_EI", pdf: page(ii + "A\n" + show(S) + "\nEI\n"),
			ops: both, want: refuse, reason: "inline_image"},

		// Form XObjects (item 2).
		{name: "form_indirect_subtype", pdf: onePage{content: []string{"q /Fm1 Do Q"},
			res:   "<< /Font << /F1 5 0 R >> /XObject << /Fm1 6 0 R >> >>",
			extra: map[int]string{6: textForm("/Width 1 /Height 1 /Subtype 7 0 R"), 7: "/Form"}}.pdf,
			ops: both, want: knownLeak, closesIn: "1.4 (walker resolves an indirect /Subtype) -> refuse form_text"},
		{name: "form_indirect_subtype_noWH", pdf: onePage{content: []string{"q /Fm1 Do Q"},
			res:   "<< /Font << /F1 5 0 R >> /XObject << /Fm1 6 0 R >> >>",
			extra: map[int]string{6: textForm("/Subtype 7 0 R"), 7: "/Form"}}.pdf,
			ops: both, want: refuse, reason: "form_text"},
		{name: "form_res_fallback", pdf: onePage{content: []string{"q /Fm1 Do Q"},
			res: "<< /Font << /F1 5 0 R >> /XObject << /Fm1 6 0 R /Fm2 7 0 R >> >>",
			extra: map[int]string{
				6: stm("/Type /XObject /Subtype /Form /BBox [0 0 400 800] /Resources << /ProcSet [/PDF] >>", "/Fm2 Do"),
				7: textForm("")}}.pdf,
			ops: both, want: knownLeak, closesIn: "1.4 (candidate scopes fall back to page resources) -> refuse form_text"},
		{name: "form_pages_inherited", pdf: onePage{content: []string{"q /Fm1 Do Q"}, res: "NONE",
			pagesRes: "<< /Font << /F1 5 0 R >> /XObject << /Fm1 6 0 R >> >>", extra: map[int]string{6: textForm("")}}.pdf,
			ops: both, want: refuse, reason: "form_text"},
		{name: "form_indirect_res", pdf: onePage{content: []string{"q /Fm1 Do Q"}, res: "8 0 R",
			extra: map[int]string{6: textForm(""), 8: "<< /Font << /F1 5 0 R >> /XObject 9 0 R >>", 9: "<< /Fm1 6 0 R >>"}}.pdf,
			ops: both, want: refuse, reason: "form_text"},
		{name: "form_name_escape", pdf: onePage{content: []string{"q /Fm#31 Do Q"},
			res: "<< /Font << /F1 5 0 R >> /XObject << /Fm1 6 0 R >> >>", extra: map[int]string{6: textForm("")}}.pdf,
			ops: both, want: refuse, reason: "form_text"},

		// Routes not walked (item 3).
		{name: "pattern_text", pdf: onePage{content: []string{"/Pattern cs /P1 scn 0 0 400 800 re f"},
			res: "<< /Font << /F1 5 0 R >> /Pattern << /P1 6 0 R >> >>",
			extra: map[int]string{6: stm("/Type /Pattern /PatternType 1 /PaintType 1 /TilingType 1 /BBox [0 0 400 800] "+
				"/XStep 400 /YStep 800 /Resources << /Font << /F1 5 0 R >> >>", "BT /F1 24 Tf 72 500 Td "+S+" Tj ET")}}.pdf,
			ops: both, want: knownLeak, closesIn: "1.4 (tiling pattern route) -> refuse pattern_text"},
		{name: "smask_text", pdf: onePage{content: []string{"/GS1 gs 0 0 0 rg 0 0 400 800 re f"},
			res: "<< /Font << /F1 5 0 R >> /ExtGState << /GS1 << /SMask << /S /Luminosity /G 6 0 R >> >> >> >>",
			extra: map[int]string{6: stm("/Type /XObject /Subtype /Form /BBox [0 0 400 800] /Group << /S /Transparency /CS /DeviceGray >> "+
				"/Resources << /Font << /F1 5 0 R >> >>", "1 g BT /F1 24 Tf 72 500 Td "+S+" Tj ET")}}.pdf,
			ops: both, want: knownLeak, closesIn: "1.4 (soft-mask group route) -> refuse softmask_text"},
		{name: "type3_text", pdf: onePage{content: []string{"BT /T3 24 Tf 72 700 Td (a) Tj ET"},
			res: "<< /Font << /F1 5 0 R /T3 6 0 R >> >>",
			extra: map[int]string{
				6: "<< /Type /Font /Subtype /Type3 /FontBBox [0 0 1000 1000] /FontMatrix [0.001 0 0 0.001 0 0] /CharProcs << /a 7 0 R >> " +
					"/Encoding << /Differences [97 /a] >> /FirstChar 97 /LastChar 97 /Widths [1000] /Resources << /Font << /F1 5 0 R >> >> >>",
				7: stm("", "1000 0 0 0 1000 1000 d1 BT /F1 200 Tf 0 0 Td "+S+" Tj ET")}}.pdf,
			ops: both, want: knownLeak, closesIn: "1.4 (Type 3 /CharProcs route) -> refuse type3_text"},

		// Marked-content property text (item 7). RedactText matches only shown
		// glyphs, so it leaves the page unmarked and D7 (strip on marked pages)
		// does not apply; property-list matching (D14) closes the text rows.
		{name: "actualtext", pdf: page("/Span << /ActualText " + S + " >> BDC " + show("(XXXXXXXXXXX)") + " EMC"),
			ops: []leakOp{opText}, want: knownLeak, closesIn: "1.6 (property-list matching, D14) -> clean"},
		{name: "actualtext", pdf: page("/Span << /ActualText " + S + " >> BDC " + show("(XXXXXXXXXXX)") + " EMC"),
			ops: []leakOp{opArea}, want: knownLeak, closesIn: "1.10 (D7 strips /ActualText on marked pages) -> clean"},
		{name: "actualtext_props", pdf: onePage{content: []string{"/Span /MC0 BDC " + show("(XXXXXXXXXXX)") + " EMC"},
			res: "<< /Font << /F1 5 0 R >> /Properties << /MC0 << /ActualText " + S + " >> >> >>"}.pdf,
			ops: []leakOp{opText}, want: knownLeak, closesIn: "1.6 (property-list matching, D14) -> clean"},
		{name: "actualtext_props", pdf: onePage{content: []string{"/Span /MC0 BDC " + show("(XXXXXXXXXXX)") + " EMC"},
			res: "<< /Font << /F1 5 0 R >> /Properties << /MC0 << /ActualText " + S + " >> >> >>"}.pdf,
			ops: []leakOp{opArea}, want: knownLeak, closesIn: "1.10 (D7 strips named /Properties entries) -> clean"},
		{name: "alt", pdf: page("/Figure << /Alt " + S + " >> BDC 0 0 10 10 re f EMC " + show("(public)")),
			ops: []leakOp{opArea}, want: knownLeak, closesIn: "1.10 (D7 strips /Alt) -> clean"},
		{name: "expansion_E", pdf: page("/Span << /E " + S + " >> BDC " + show("(SR)") + " EMC"),
			ops: []leakOp{opArea}, want: knownLeak, closesIn: "1.10 (D7 strips /E) -> clean"},

		// TJ, compatibility sections, quote operators, comments.
		{name: "tj_nested", pdf: page("BT /F1 24 Tf 72 700 Td [ (A) [ " + S + " ] ] TJ ET"),
			ops: []leakOp{opText}, want: knownLeak, closesIn: "1.5 (text model must read strings nested in TJ elements) -> clean"},
		{name: "tj_nested", pdf: page("BT /F1 24 Tf 72 700 Td [ (A) [ " + S + " ] ] TJ ET"),
			ops: []leakOp{opArea}, want: clean, gsBlind: true},
		{name: "bx_known", pdf: page("BX " + show(S) + " EX"), ops: both, want: clean},
		{name: "quote_dq", pdf: page("BT /F1 24 Tf 30 TL 72 700 Td 0 0 " + S + " \" ET"), ops: both, want: clean},
		{name: "quote_sq", pdf: page("BT /F1 24 Tf 30 TL 72 700 Td " + S + " ' ET"), ops: both, want: clean},
		{name: "comment_split", pdf: page("BT /F1 24 Tf 72 700 Td " + S + "%c)\nTj ET"), ops: both, want: clean},

		// Text boxes (items 5, 6).
		{name: "tc_spread_60Tc", pdf: page("BT /F1 24 Tf 60 Tc 10 700 Td " + S + " Tj ET"),
			ops: []leakOp{opText}, want: clean},
		{name: "tc_spread_60Tc", pdf: page("BT /F1 24 Tf 60 Tc 10 700 Td " + S + " Tj ET"), area: partial(300, 680, 100, 60),
			ops: []leakOp{opArea}, want: knownLeak, closesIn: "1.7 (Tc/Tw in the glyph box, interval pen) -> clean"},
		{name: "wide_glyphs", pdf: page("BT /F1 24 Tf 10 700 Td (WWWWWWWWWWWWWWW" + secret + ") Tj ET"),
			area: partial(330, 680, 70, 60), ops: both, want: clean},
		{name: "zero_tf", pdf: page("BT /F1 0 Tf 72 700 Td " + S + " Tj ET"),
			ops: []leakOp{opText}, want: knownLeak, closesIn: "1.5 (span marks remove matched runs whatever their box) -> clean"},
		{name: "zero_tf", pdf: page("BT /F1 0 Tf 72 700 Td " + S + " Tj ET"), ops: []leakOp{opArea}, want: clean, gsBlind: true},
		{name: "zero_tz", pdf: page("BT /F1 24 Tf 0 Tz 72 700 Td " + S + " Tj ET"),
			ops: []leakOp{opText}, want: knownLeak, closesIn: "1.5 (span marks) -> clean"},
		{name: "zero_tz", pdf: page("BT /F1 24 Tf 0 Tz 72 700 Td " + S + " Tj ET"), ops: []leakOp{opArea}, want: clean, gsBlind: true},
		{name: "degenerate_tm", pdf: page("BT /F1 24 Tf 0 0 0 0 72 700 Tm " + S + " Tj ET"),
			ops: []leakOp{opText}, want: knownLeak, closesIn: "1.5 (span marks) -> clean"},
		{name: "degenerate_tm", pdf: page("BT /F1 24 Tf 0 0 0 0 72 700 Tm " + S + " Tj ET"), ops: []leakOp{opArea}, want: clean, gsBlind: true},

		// N5: a token across two /Contents streams (step 1.2).
		{name: "split_string", pdf: onePage{content: []string{"BT /F1 24 Tf 72 700 Td (SECRET-12", "34) Tj ET"}}.pdf,
			ops: both, want: refuse, reason: "content_ambiguous"},
		{name: "split_keyword", pdf: onePage{content: []string{"BT /F1 24 Tf 72 700 Td " + S + " T", "j ET"}}.pdf,
			ops: both, want: refuse, reason: "content_ambiguous"},
		{name: "split_comment", pdf: onePage{content: []string{"BT /F1 24 Tf 72 700 Td %c", S + " Tj ET"}}.pdf,
			ops: both, want: refuse, reason: "content_ambiguous"},
		{name: "split_name", pdf: onePage{content: []string{"BT /F", "1 24 Tf 72 700 Td " + S + " Tj ET"}}.pdf,
			ops: both, want: refuse, reason: "content_ambiguous"},
		{name: "split_between_tokens", pdf: onePage{content: []string{"BT /F1 24 Tf 72 700 Td " + S, "Tj ET"}}.pdf,
			ops: both, want: clean},

		// --- rv5 ---
		{name: "rv5_b1_deep_sibling", pdf: page("BT /F1 24 Tf 72 700 Td [ " + S + " " + openArrays(overDeep) + "1" + closeArrays(overDeep) + " ] TJ ET"),
			ops: both, want: clean},
		{name: "rv5_form_xobject", pdf: onePage{content: []string{"q /Fm1 Do Q"},
			res: "<< /Font << /F1 5 0 R >> /XObject << /Fm1 6 0 R >> >>", extra: map[int]string{6: textForm("")}}.pdf,
			ops: both, want: refuse, reason: "form_text"},

		// --- N1: RedactText matches raw bytes, not /ToUnicode text, per TJ element ---
		{name: "n1_identity_h", pdf: onePage{content: []string{"BT /F2 24 Tf 72 700 Td " + utf16Hex(secret) + " Tj ET"},
			res: "<< /Font << /F1 5 0 R /F2 6 0 R >> >>", extra: identityHFont()}.pdf,
			ops: []leakOp{opText}, want: knownLeak, closesIn: "1.5 (Unicode via the font model, D12) -> clean"},
		{name: "n1_identity_h", pdf: onePage{content: []string{"BT /F2 24 Tf 72 700 Td " + utf16Hex(secret) + " Tj ET"},
			res: "<< /Font << /F1 5 0 R /F2 6 0 R >> >>", extra: identityHFont()}.pdf,
			ops: []leakOp{opArea}, want: clean},
		{name: "n1_kerned_tj", pdf: page("BT /F1 24 Tf 72 700 Td [(SEC) -20 (RET-1234)] TJ ET"),
			ops: []leakOp{opText}, want: knownLeak, closesIn: "1.5 (one run per TJ, elements concatenated) -> clean"},
		{name: "n1_kerned_tj", pdf: page("BT /F1 24 Tf 72 700 Td [(SEC) -20 (RET-1234)] TJ ET"),
			ops: []leakOp{opArea}, want: clean},

		// --- N2: back-references from an unmarked page drag in the original marked page ---
		{name: "n2_link_dest", pdf: twoPages(show(S), " /Annots [7 0 R]", show("(public)"), " /Annots [9 0 R]", map[int]string{
			7: "<< /Type /Annot /Subtype /Text /Rect [10 10 30 30] /Contents " + S + " >>",
			9: "<< /Type /Annot /Subtype /Link /Rect [0 0 100 100] /Dest [3 0 R /XYZ 0 800 0] >>"}, ""),
			ops: both, want: knownLeak, closesIn: "1.9 (BuildOptions.PageRefs remaps page references) -> clean"},
		{name: "n2_widget_parent", pdf: twoPages(show(S), " /Annots [7 0 R]", show("(public)"), " /Annots [9 0 R]", map[int]string{
			7:  "<< /Type /Annot /Subtype /Widget /Rect [72 600 300 630] /Parent 10 0 R /P 3 0 R /AP << /N 11 0 R >> >>",
			9:  "<< /Type /Annot /Subtype /Widget /Rect [72 600 300 630] /Parent 10 0 R /P 6 0 R >>",
			10: "<< /FT /Tx /T (name) /V (public) /Kids [7 0 R 9 0 R] >>",
			11: stm("/Type /XObject /Subtype /Form /BBox [0 0 228 30] /Resources << /Font << /F1 5 0 R >> >>",
				"BT /F1 12 Tf 2 10 Td "+S+" Tj ET")}, ""),
			ops: both, want: knownLeak, closesIn: "1.9 (PageRefs + deny field /Kids widgets of marked pages) -> clean"},

		// --- N3: page-dict entries that hold a copy of the page ---
		{name: "n3_thumb", pdf: onePage{content: []string{show(S)}, pageExtra: " /Thumb 9 0 R",
			extra: map[int]string{9: flateStm("/Width 4 /Height 4 /ColorSpace /DeviceGray /BitsPerComponent 8", "thumbnail stand-in "+secret)}}.pdf,
			ops: both, want: knownLeak, closesIn: "1.9 (Redacted page-dict allow-list) -> clean"},

		// --- N4: an unused page resource holds the text; it is copied verbatim.
		// A reader that parses inline-image data as content (T5) could also
		// invoke it with a Do, gs or scn hidden there, which T6 does not refuse.
		{name: "n4_unused_form", pdf: onePage{content: []string{show("(public)")},
			res: "<< /Font << /F1 5 0 R >> /XObject << /Fm1 6 0 R >> >>", extra: map[int]string{6: textForm("")}}.pdf,
			ops: []leakOp{opArea}, want: knownLeak, closesIn: "1.9 (resource GC drops entries the walk never invoked) -> clean"},

		// --- N6: gs /Font sets the font and size, which the scanner ignores ---
		{name: "n6_gs_font", pdf: onePage{content: []string{"BT /GS1 gs 72 700 Td " + S + " Tj ET"},
			res: "<< /Font << /F1 5 0 R >> /ExtGState << /GS1 << /Font [5 0 R 24] >> >> >>"}.pdf,
			ops: []leakOp{opText}, want: knownLeak, closesIn: "1.5 (span marks remove the matched zero-size run) -> clean"},
		{name: "n6_gs_font", pdf: onePage{content: []string{"BT /GS1 gs 72 700 Td " + S + " Tj ET"},
			res: "<< /Font << /F1 5 0 R >> /ExtGState << /GS1 << /Font [5 0 R 24] >> >> >>"}.pdf,
			area: partial(100, 690, 200, 40), ops: []leakOp{opArea},
			want: knownLeak, closesIn: "1.4 + 1.7 (walker reads gs /Font; boxes from its size) -> clean"},

		// --- D4: the secret elsewhere in the document (automatic extension) ---
		{name: "d4_other_page", pdf: twoPages(show(S), "", show(S), "", nil, ""),
			ops: both, want: knownLeak, closesIn: "1.13 (match/area needle extends to page 1) -> clean"},
		{name: "d4_text_annotation", pdf: twoPages(show(S), "", show("(public)"), " /Annots [9 0 R]", map[int]string{
			9: "<< /Type /Annot /Subtype /Text /Rect [10 10 30 30] /Contents " + S + " >>"}, ""),
			ops: []leakOp{opText}, want: knownLeak, closesIn: "1.13 (annotation holding a needle is removed) -> clean"},
		{name: "d4_widget_value", pdf: twoPages(show(S), "", show("(public)"), " /Annots [9 0 R]", map[int]string{
			9:  "<< /Type /Annot /Subtype /Widget /Rect [72 600 300 630] /Parent 10 0 R >>",
			10: "<< /FT /Tx /T (id) /V " + S + " /Kids [9 0 R] >>"}, ""),
			ops: []leakOp{opText}, want: knownLeak, closesIn: "1.13 (widget whose field value holds a needle is removed) -> clean"},
		{name: "d4_attachment", pdf: twoPages(show(S), "", show("(public)"), " /Annots [9 0 R]", map[int]string{
			9:  "<< /Type /Annot /Subtype /FileAttachment /Rect [10 10 30 30] /FS << /Type /Filespec /F (a.txt) /EF << /F 10 0 R >> >> >>",
			10: flateStm("/Type /EmbeddedFile", "attached "+secret)}, ""),
			ops: []leakOp{opText}, want: knownLeak, closesIn: "1.13 (attachments dropped whenever the plan is non-empty) -> clean"},
		{name: "d4_info_title", pdf: onePage{content: []string{show(S)}, extra: map[int]string{9: "<< /Title " + S + " >>"}, info: "9 0 R"}.pdf,
			ops: both, want: knownLeak, closesIn: "1.13 (/Info field holding a needle is removed) -> clean"},
	}

	cases = append(cases, inlineImageCases()...)

	// The malformed-content table: unreadable content fails closed.
	for _, tc := range unreadableContentCases {
		cases = append(cases, leakCase{name: "malformed/" + tc.name, pdf: page(tc.content), ops: both,
			want: refuse, reason: "content_ambiguous|inline_image"})
	}
	// The deep-nesting table: the secret next to an over-deep operand is
	// removed.
	// Ghostscript drops the whole TJ for some of them (gsBlind).
	gsDropsTJ := map[string]bool{
		"TJ, deep array before the string":     true,
		"TJ, deep dictionary after the string": true,
		"TJ, deep elements on both sides":      true,
	}
	for _, tc := range overDeepSiblingCases {
		cases = append(cases, leakCase{name: "deep/" + tc.name, pdf: page(tc.content), ops: both, want: clean,
			gsBlind: gsDropsTJ[tc.name]})
	}
	return cases
}
