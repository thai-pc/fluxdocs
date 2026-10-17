package redaction

import (
	"bytes"
	"compress/zlib"
	"encoding/hex"
	"strings"
)

// This file holds the leak-corpus rows from the batch D review (rvD probes):
// removed text kept in comments and property lists of rewritten pages,
// content streams in filters the search did not decode, streams whose
// dictionary keys made the verifier skip them, annotations removed by
// automatic extension that still kept a removed image, and pages whose
// lenient reading hides text Ghostscript shows.

// twoPagesRes is a two-page fixture: page 3 (index 0) shows content0 with
// /F1; page 6 (index 1) has resources res1, content 8 content1 and more
// page-dictionary entries extra1; extra objects from 9.
func twoPagesRes(content0, res1, content1, extra1 string, extra map[int]string) func() []byte {
	return func() []byte {
		objs := map[int]string{
			1: "<< /Type /Catalog /Pages 2 0 R >>",
			2: "<< /Type /Pages /Kids [3 0 R 6 0 R] /Count 2 /MediaBox [0 0 400 800] >>",
			3: "<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
			4: stm("", content0),
			5: helvetica,
			6: "<< /Type /Page /Parent 2 0 R /Resources " + res1 + " /Contents 8 0 R" + extra1 + " >>",
			8: stm("", content1),
		}
		for k, v := range extra {
			objs[k] = v
		}
		return leakPDF(objs, "")
	}
}

// otherPageStream is twoPagesRes with page 1's content stream object 8
// replaced by stream (any filter).
func otherPageStream(stream string) func() []byte {
	return func() []byte {
		return leakPDF(map[int]string{
			1: "<< /Type /Catalog /Pages 2 0 R >>",
			2: "<< /Type /Pages /Kids [3 0 R 6 0 R] /Count 2 /MediaBox [0 0 400 800] >>",
			3: "<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
			4: stm("", show(S)),
			5: helvetica,
			6: "<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 5 0 R >> >> /Contents 8 0 R >>",
			8: stream,
		}, "")
	}
}

// type3Font is a Type 3 font (object 11) with one glyph procedure, object 12.
const type3Font = "<< /Type /Font /Subtype /Type3 /FontBBox [0 0 1000 1000] /FontMatrix [0.001 0 0 0.001 0 0] /CharProcs << /a 12 0 R >> " +
	"/Encoding << /Type /Encoding /Differences [97 /a] >> /FirstChar 97 /LastChar 97 /Widths [1000] /Resources << /Font << /F1 5 0 R >> >> >>"

// ahxStm is a stream of data in ASCIIHexDecode.
func ahxStm(data string) string {
	return stm("/Filter /ASCIIHexDecode", hex.EncodeToString([]byte(data))+">")
}

// badAdlerStm is a Flate stream of data whose Adler-32 checksum is wrong:
// DecodeStream fails on it, Ghostscript reads it.
func badAdlerStm(data string) string {
	var b bytes.Buffer
	w := zlib.NewWriter(&b)
	w.Write([]byte(data))
	w.Close()
	z := b.Bytes()
	z[len(z)-1] ^= 0xFF
	return stm("/Filter /FlateDecode", string(z))
}

// rvdCases are the rows.
func rvdCases() []leakCase {
	both := []leakOp{opText, opArea}
	line0 := partial(60, 690, 300, 40)
	keepLine := "\nBT /F1 24 Tf 72 300 Td (public) Tj ET"
	otherPage := func(c1 string) func() []byte {
		return twoPagesRes(show(S), "<< /Font << /F1 5 0 R >> >>", c1, "", nil)
	}
	hexS := "<" + strings.ToUpper(hex.EncodeToString([]byte(secret))) + ">"
	unusedForm := "<< /Font << /F1 5 0 R >> /XObject << /Fm9 13 0 R >> >>"
	twoImagePages := func(annots string) func() []byte {
		return func() []byte {
			objs := map[int]string{
				1:  "<< /Type /Catalog /Pages 2 0 R >>",
				2:  "<< /Type /Pages /Kids [3 0 R 6 0 R] /Count 2 /MediaBox [0 0 400 800] >>",
				3:  "<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 5 0 R >> /XObject << /Im1 10 0 R >> >> /Contents 4 0 R >>",
				4:  stm("", show(S)+" q 100 0 0 100 50 50 cm /Im1 Do Q"),
				5:  helvetica,
				6:  "<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 5 0 R >> /XObject << /Im1 10 0 R >> >> /Contents 8 0 R" + annots + " >>",
				8:  stm("", show("(public)")),
				10: secretImage(),
			}
			if annots != "" {
				objs[9] = "<< /Type /Annot /Subtype /Text /Rect [0 0 10 10] /Contents " + S + " /AP << /N 12 0 R >> >>"
				objs[12] = stm("/Type /XObject /Subtype /Form /BBox [0 0 10 10] /Resources << /XObject << /Im1 10 0 R >> >>", "q 10 0 0 10 0 0 cm /Im1 Do Q")
			}
			return leakPDF(objs, "")
		}
	}
	return []leakCase{
		// B1: comments and property-list strings of a rewritten page.
		{name: "rvd_comment_between_operands", pdf: page(show(S) + "\nBT /F1 24 Tf 72 300 Td (public) %" + secret + "\n Tj ET"),
			ops: both, area: line0, want: clean},
		{name: "rvd_comment_in_kept_cm", pdf: page(show(S) + "\nq 1 0 0 %" + secret + "\n1 0 0 cm Q"),
			ops: both, area: line0, want: clean},
		{name: "rvd_comment_in_inline_image_dict", pdf: page(show(S) + "\nq 11 0 0 1 72 300 cm BI /W 11 /H 1 /BPC 8 %" + secret + "\n/CS /G ID abcdefghijk EI Q"),
			ops: both, area: line0, want: clean},
		{name: "rvd_custom_bdc_key", pdf: page("/Span <</X " + S + " /MCID 0>> BDC " + show(S) + " EMC\nBT /F1 24 Tf 72 300 Td (public) Tj ET"),
			ops: both, area: line0, want: clean},
		// S1: objects the verifier must read by their route, not their keys.
		{name: "rvd_unused_type3_hex_charproc", pdf: twoPagesRes(show(S), "<< /Font << /F1 5 0 R /F3 11 0 R >> >>", show("(public)"), "",
			map[int]string{11: type3Font, 12: flateStm("", "1000 0 d0 BT /F1 1 Tf "+hexS+" Tj ET")}), ops: both, want: clean},
		{name: "rvd_unused_type3_literal_charproc", pdf: twoPagesRes(show(S), "<< /Font << /F1 5 0 R /F3 11 0 R >> >>", show("(public)"), "",
			map[int]string{11: type3Font, 12: flateStm("", "1000 0 d0 BT /F1 1 Tf "+S+" Tj ET")}), ops: both, want: clean},
		{name: "rvd_used_type3_hex_charproc", pdf: twoPagesRes(show(S), "<< /Font << /F1 5 0 R /F3 11 0 R >> >>", show("(public)")+" BT /F3 24 Tf 72 300 Td (a) Tj ET", "",
			map[int]string{11: type3Font, 12: flateStm("", "1000 0 d0 BT /F1 1 Tf "+hexS+" Tj ET")}), ops: both, want: refuse, reason: "type3_text"},
		{name: "rvd_unused_form_with_N", pdf: twoPagesRes(show(S), unusedForm, show("(public)"), "", map[int]string{13: textForm("/N 1")}), ops: both, want: clean},
		{name: "rvd_unused_form_control", pdf: twoPagesRes(show(S), unusedForm, show("(public)"), "", map[int]string{13: textForm("")}), ops: both, want: clean},
		{name: "rvd_unused_form_with_Length1", pdf: twoPagesRes(show(S), unusedForm, show("(public)"), "", map[int]string{13: textForm("/Length1 5")}), ops: both, want: clean},
		{name: "rvd_pieceinfo_registry_ordering", pdf: twoPagesRes(show(S), "<< /Font << /F1 5 0 R >> >>", show("(public)"),
			" /PieceInfo << /X << /Registry (a) /Ordering (b) /Private "+S+" >> >>", nil), ops: both, want: clean},
		{name: "rvd_pieceinfo_control", pdf: twoPagesRes(show(S), "<< /Font << /F1 5 0 R >> >>", show("(public)"),
			" /PieceInfo << /X << /Private "+S+" >> >>", nil), ops: both, want: clean},
		// B3: an image removed under a region must not survive through the
		// appearance of an annotation automatic extension removed.
		{name: "rvd_removed_annot_ap_keeps_removed_image", pdf: twoImagePages(" /Annots [9 0 R]"), ops: []leakOp{opArea}, want: clean},
		{name: "rvd_removed_image_control", pdf: twoImagePages(""), ops: []leakOp{opArea}, want: clean},
		// S2: page hygiene (the needle only in /PieceInfo) also drops the
		// page's link and strips its /ActualText (reported).
		{name: "rvd_hygiene_drops_link", pdf: twoPagesRes(show(S), "<< /Font << /F1 5 0 R >> >>", "/Span <</ActualText (public words)>> BDC "+show("(public)")+" EMC",
			" /PieceInfo << /X << /Private "+S+" >> >> /Annots [9 0 R]",
			map[int]string{9: "<< /Type /Annot /Subtype /Link /Rect [0 0 10 10] /A << /S /URI /URI (https://example.com) >> >>"}),
			ops: []leakOp{opText}, want: clean},
		// B2: a page the extension must search but cannot read for certain.
		// Ghostscript shows the secret behind an inline image whose end
		// readers disagree on, and reads a Flate stream whose checksum is
		// wrong.
		{name: "rvd_lenient_hidden_other_page", pdf: twoPagesRes(show(S), "<< /Font << /F1 5 0 R >> >>",
			"BT /F1 24 Tf 72 700 Td ET\nBI /W 1 /H 1 /BPC 8 /CS /G ID AEI\n"+show(S)+"\nEI\n", "", nil),
			ops: both, want: refuse, reason: "inline_image"},
		{name: "rvd_corrupt_flate_other_page", pdf: otherPageStream(badAdlerStm(show(S) + "\n")),
			ops: both, want: refuse, reason: "content_ambiguous"},
		{name: "rvd_ahx_content_other_page", pdf: otherPageStream(ahxStm(show(S))), ops: both, want: clean},
		{name: "rvd_ahx_pieceinfo_other_page", pdf: func() []byte {
			return leakPDF(map[int]string{
				1: "<< /Type /Catalog /Pages 2 0 R >>",
				2: "<< /Type /Pages /Kids [3 0 R 6 0 R] /Count 2 /MediaBox [0 0 400 800] >>",
				3: "<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
				4: stm("", show(S)),
				5: helvetica,
				6: "<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 5 0 R >> >> /Contents 8 0 R /PieceInfo << /X << /Private 9 0 R >> >> >>",
				8: stm("", show("(public)")),
				9: ahxStm("private data " + secret),
			}, "")
		}, ops: both, want: clean},
		// Annotation arrays with a missing reference, a null and an integer;
		// a JavaScript action holding the secret.
		{name: "rvd_link_js_action", pdf: twoPagesRes(show(S), "<< /Font << /F1 5 0 R >> >>", show("(public)"), " /Annots [9 0 R]",
			map[int]string{9: "<< /Type /Annot /Subtype /Link /Rect [0 0 10 10] /A << /S /JavaScript /JS " + S + " >> >>"}), ops: both, want: clean},
		{name: "rvd_link_js_after_missing_ref", pdf: twoPagesRes(show(S), "<< /Font << /F1 5 0 R >> >>", show("(public)"), " /Annots [99 0 R 9 0 R 14 0 R]",
			map[int]string{9: "<< /Type /Annot /Subtype /Link /Rect [0 0 10 10] /A << /S /JavaScript /JS " + S + " >> >>",
				14: "<< /Type /Annot /Subtype /Link /Rect [0 0 10 10] /A << /S /URI /URI (https://example.com) >> >>"}), ops: both, want: clean},
		{name: "rvd_annots_with_null_and_int", pdf: twoPagesRes(show(S), "<< /Font << /F1 5 0 R >> >>", show("(public)"), " /Annots [null 5 9 0 R 14 0 R]",
			map[int]string{9: "<< /Type /Annot /Subtype /Link /Rect [0 0 10 10] /A << /S /JavaScript /JS " + S + " >> >>",
				14: "<< /Type /Annot /Subtype /Link /Rect [0 0 10 10] /A << /S /URI /URI (https://example.com) >> >>"}), ops: both, want: clean},
		{name: "rvd_settle", pdf: page("BT /F1 24 Tf 72 700 Td " + S + " Tj (public) Tj ET"), ops: []leakOp{opArea}, area: partial(60, 690, 100, 40), want: clean},
		// Review round 2 (r2). A /Lang can hold any text (a valid BCP 47
		// shape): a rewritten page keeps no property-list string (S1).
		{name: "r2_lang_inline_wrapping", pdf: page("/Span <</Lang (" + secret + ") /MCID 0>> BDC " + show(S) + " EMC" + keepLine), ops: both, area: line0, want: clean},
		{name: "r2_lang_inline_kept_text", pdf: page(show(S) + "\n/Span <</Lang (" + secret + ")>> BDC BT /F1 24 Tf 72 300 Td (public) Tj ET EMC"), ops: both, area: line0, want: clean},
		{name: "r2_lang_named_props", pdf: func() []byte {
			return leakPDF(map[int]string{
				1: "<< /Type /Catalog /Pages 2 0 R >>",
				2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 400 800] >>",
				3: "<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 5 0 R >> /Properties << /P0 << /Lang (" + secret + ") >> >> >> /Contents 4 0 R >>",
				4: stm("", show(S)+"\n/Span /P0 BDC BT /F1 24 Tf 72 300 Td (public) Tj ET EMC"),
				5: helvetica,
			}, "")
		}, ops: both, area: line0, want: clean},
		{name: "r2_lang_DP", pdf: page(show(S) + "\n/Foo <</Lang (" + secret + ")>> DP" + keepLine), ops: both, area: line0, want: clean},
		// Hidden copies on a page that is not redacted (BL2): the pre-pass
		// reads what the page holds but does not show, and page hygiene
		// rewrites it (comments blanked, property lists scrubbed).
		{name: "r2_other_lang", pdf: otherPage("/Span <</Lang (" + secret + ")>> BDC " + show("(public)") + " EMC"), ops: both, want: clean},
		{name: "r2_other_lang_with_space", pdf: otherPage("/Span <</Lang (x " + secret + ")>> BDC " + show("(public)") + " EMC"), ops: both, want: clean},
		{name: "r2_other_custom_key", pdf: otherPage("/Span <</X (" + secret + ")>> BDC " + show("(public)") + " EMC"), ops: both, want: clean},
		{name: "r2_other_comment", pdf: otherPage(show("(public)") + "\n%" + secret + "\n"), ops: both, want: clean},
		{name: "r2_other_bi_comment", pdf: otherPage(show("(public)") + "\nq 1 0 0 1 72 300 cm BI /W 1 /H 1 /BPC 8 %" + secret + "\n/CS /G ID A EI Q"), ops: both, want: clean},
		// The same in a form the page paints: Phase 1 does not rewrite forms,
		// so the comment cannot be removed and the extension refuses.
		{name: "r2_other_form_comment", pdf: twoPagesRes(show(S), "<< /Font << /F1 5 0 R >> /XObject << /Fm1 9 0 R >> >>", show("(public)")+" /Fm1 Do", "",
			map[int]string{9: stm("/Type /XObject /Subtype /Form /BBox [0 0 10 10]", "%"+secret+"\n0 0 1 1 re f")}), ops: both, want: refuse, reason: "form_text"},
		{name: "r2_other_actualtext_control", pdf: otherPage("/Span <</ActualText (" + secret + ")>> BDC " + show("(public)") + " EMC"), ops: both, want: clean},
		{name: "r2_other_named_X_control", pdf: twoPagesRes(show(S), "<< /Font << /F1 5 0 R >> /Properties << /P0 << /X ("+secret+") >> >> >>", "/Span /P0 BDC "+show("(public)")+" EMC", "", nil), ops: both, want: clean},
		{name: "rvd_unknown_subtype_name", pdf: twoPagesRes(show(S), "<< /Font << /F1 5 0 R >> >>", show("(public)"), " /Annots [9 0 R]",
			map[int]string{9: "<< /Type /Annot /Subtype /" + secret + " /Rect [0 0 10 10] /Contents " + S + " >>"}), ops: []leakOp{opText}, want: clean},
	}
}
