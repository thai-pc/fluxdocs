package redaction

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"hash/adler32"
	"image"
	"image/jpeg"
	"strings"
)

// This file holds the leak corpus rows for inline images
// (docs/design/redaction-hardening.md §6.1). Ghostscript parses the raw bytes
// of an inline image as content when it rejects the image (a dictionary value
// it does not accept, data it cannot decode) or when the data holds more than
// the samples need (it stops once it has them): go-reviewer's batch A repros
// B1–B4. Each must be refused; a valid unfiltered, ASCIIHex or Flate image
// followed by the secret must be redacted cleanly.

// imgText is the secret as a text object, as the repros place it in image data.
const imgText = "\nBT /F1 24 Tf 72 700 Td " + S + " Tj ET\n"

// imgPre scales the image to a 10x10 corner, away from the secret.
const imgPre = "q 10 0 0 10 0 0 cm "

// stored is a stored DEFLATE block holding data (RFC 1951 §3.2.4).
func stored(final bool, data []byte) []byte {
	b := []byte{0}
	if final {
		b[0] = 1
	}
	b = binary.LittleEndian.AppendUint16(b, uint16(len(data)))
	b = binary.LittleEndian.AppendUint16(b, ^uint16(len(data)))
	return append(b, data...)
}

// zwrap wraps DEFLATE blocks decoding to plain in a zlib header and trailer.
func zwrap(blocks, plain []byte) []byte {
	return binary.BigEndian.AppendUint32(append([]byte{0x78, 0x01}, blocks...), adler32.Checksum(plain))
}

// zlibOf is plain zlib-compressed.
func zlibOf(plain []byte) []byte {
	var b bytes.Buffer
	w := zlib.NewWriter(&b)
	w.Write(plain)
	w.Close()
	return b.Bytes()
}

// jpegOf is a w×h gray baseline JPEG.
func jpegOf(w, h int) []byte {
	var b bytes.Buffer
	jpeg.Encode(&b, image.NewGray(image.Rect(0, 0, w, h)), &jpeg.Options{Quality: 50})
	return b.Bytes()
}

// jpegSeg is a JPEG marker segment with payload.
func jpegSeg(marker byte, payload []byte) []byte {
	b := binary.BigEndian.AppendUint16([]byte{0xFF, marker}, uint16(len(payload)+2))
	return append(b, payload...)
}

// inlineImageCases returns the inline-image rows.
func inlineImageCases() []leakCase {
	both := []leakOp{opText, opArea}
	var cases []leakCase
	refused := func(name, content string) {
		cases = append(cases, leakCase{name: "inline/" + name, pdf: page(content), ops: both, want: refuse, reason: "inline_image"})
	}
	img := func(dict, data string) string { return imgPre + "BI " + dict + " ID " + data + " EI Q" }
	gray := "/W 1 /H 1 /BPC 8 /CS /G"

	// Valid images, then the secret: removed, and the image does not hide it.
	for _, c := range []struct{ name, image string }{
		{"unfiltered", img(gray, "A")},
		{"ahx", img(gray+" /F /AHx", "41>")},
		{"flate", img(gray+" /F /Fl", string(zlibOf([]byte("A"))))},
		{"ahx_flate", img(gray+" /F [/AHx /Fl]", fmt.Sprintf("%x>", zlibOf([]byte("A"))))},
		{"image_mask", img("/W 8 /H 1 /IM true /D [1 0]", "A")},
	} {
		cases = append(cases, leakCase{name: "inline/clean_" + c.name, pdf: page(c.image + "\n" + show(S)), ops: both, want: clean})
	}

	// B2: Flate carrying more than the samples; later blocks hold the text.
	plain := append([]byte("A"), imgText...)
	refused("fl_stored_tail_small", img(gray+" /F /Fl", string(zwrap(append(stored(false, []byte("A")), stored(true, []byte(imgText))...), plain))))
	pad := bytes.Repeat([]byte(" "), 60000)
	plain = append(append([]byte("A"), pad...), imgText...)
	refused("fl_stored_tail_padded", img(gray+" /F /Fl",
		string(zwrap(append(append(stored(false, []byte("A")), stored(false, pad)...), stored(true, []byte(imgText))...), plain))))
	big := bytes.Repeat([]byte{0x80}, 10000)
	plain = append(append(append([]byte{}, big...), pad...), imgText...)
	refused("fl_bigimg_padded", img("/W 100 /H 100 /BPC 8 /CS /G /F /Fl",
		string(zwrap(append(append(stored(false, big), stored(false, pad)...), stored(true, []byte(imgText))...), plain))))
	refused("fl_zlib_text_inside", img(gray+" /F /Fl", string(zlibOf(append([]byte("A"), imgText...)))))

	// B3: raw DEFLATE (no zlib header) whose stored block is plain text.
	refused("fl_raw_stored_text", img(gray+" /F /Fl", string(stored(true, []byte(imgText)))))

	// A85 (refused in Phase 1): over-long data holding the text.
	a85 := strings.Repeat("z", 100000) + imgText + "~>"
	refused("a85_z_pad_text", img("/W 4 /H 1 /BPC 8 /CS /G /F /A85", a85))
	refused("a85_small_text", img(gray+" /F /A85", "z"+imgText+"~>"))
	refused("a85fl_z_pad_text", img("/W 4 /H 1 /BPC 8 /CS /G /F [/A85 /Fl]", a85))
	refused("a85fl_small_text", img(gray+" /F [/A85 /Fl]", imgText+"~>"))
	refused("a85_small_text_bad_dict", img(gray+" /I 5 /F /A85", imgText+"~>"))

	// B4: DCT (refused in Phase 1): text in segments a reader skips or parses.
	bad := append([]byte{0xFF, 0xD8}, jpegSeg(0xE1, []byte(imgText))...)
	bad = append(append(bad, jpegSeg(0xDA, []byte{1, 1, 0})...), 0x00, 0xFF, 0xD9)
	refused("dct_app_text_bad", img(gray+" /F /DCT", string(bad)))
	v := jpegOf(8, 8)
	withCom := append(append(append([]byte{}, v[:len(v)-2]...), jpegSeg(0xFE, []byte(imgText))...), 0xFF, 0xD9)
	refused("dct_com_after_scan", img("/W 8 /H 8 /BPC 8 /CS /G /F /DCT", string(withCom)))
	padded := append([]byte{}, v[:len(v)-2]...)
	for i := 0; i < 4; i++ {
		padded = append(padded, jpegSeg(0xFE, pad)...)
	}
	padded = append(append(padded, jpegSeg(0xFE, []byte(imgText))...), 0xFF, 0xD9)
	refused("dct_com_pad_after_scan", img("/W 8 /H 8 /BPC 8 /CS /G /F /DCT", string(padded)))
	appFirst := append(append([]byte{0xFF, 0xD8}, jpegSeg(0xE5, []byte(imgText))...), v[2:]...)
	refused("dct_app_before_valid", img("/W 8 /H 8 /BPC 8 /CS /G /F /DCT", string(appFirst)))
	refused("dct_wh_mismatch_com", img(gray+" /F /DCT", string(withCom)))

	// B1: unfiltered data that is the text, under a dictionary a reader may
	// reject (and T6: the data shows text if parsed).
	d := "BT /F1 24 Tf 72 700 Td " + S + " Tj ET"
	w := fmt.Sprint(len(d))
	for _, c := range []struct{ name, dict, data string }{
		{"raw_devn_null_tint", "/W " + w + " /H 1 /BPC 8 /CS [/DeviceN [/A] /DeviceGray null]", d},
		{"raw_indexed_short_lookup", "/W " + w + " /H 1 /BPC 8 /CS [/I /RGB 255 <00>]", d},
		{"raw_decode_wrong_len", "/W " + w + " /H 1 /BPC 8 /CS /G /D [0]", d},
		{"raw_decode_strings", "/W " + w + " /H 1 /BPC 8 /CS /G /D [(a) (b)]", d},
		{"raw_interp_int", "/W " + w + " /H 1 /BPC 8 /CS /G /I 5", d},
		{"raw_unknown_key", "/W " + w + " /H 1 /BPC 8 /CS /G /Foo /Bar", d},
		{"raw_indexed_bpc16", "/W " + w + " /H 1 /BPC 16 /CS [/I /G 1 <00ff>]", d + d},
		{"raw_calgray_noparams", "/W " + w + " /H 1 /BPC 8 /CS [/CalGray]", d},
		{"raw_lab_noparams", "/W 14 /H 1 /BPC 8 /CS [/Lab]", d},
		{"raw_sep_short", "/W " + w + " /H 1 /BPC 8 /CS [/Separation /A]", d},
		{"raw_dp_on_unfiltered", "/W " + w + " /H 1 /BPC 8 /CS /G /DP << /Predictor 15 >>", d},
		{"raw_ok_control", "/W " + w + " /H 1 /BPC 8 /CS /G", d},
		{"raw_im_decode_bad", "/W " + fmt.Sprint(len(d)*8) + " /H 1 /IM true /D [0]", d},
	} {
		refused(c.name, img(c.dict, c.data))
	}
	refused("raw_inside_text_object", "BT /F1 24 Tf 72 700 Td BI /W "+w+" /H 1 /BPC 8 /CS /G ID "+d+" EI ET")

	// B1 with Flate: a stored block of the text, exact length, under a
	// dictionary or chain a reader may reject.
	st := string(zwrap(stored(true, []byte(imgText)), []byte(imgText)))
	wt := fmt.Sprint(len(imgText))
	for _, c := range []struct{ name, dict string }{
		{"fl_stored_text_exact", "/W " + wt + " /H 1 /BPC 8 /CS /G /F /Fl"},
		{"fl_stored_text_bad_dp", "/W " + wt + " /H 1 /BPC 8 /CS /G /F /Fl /DP << /Predictor 15 /Colors 99 /Columns 1 >>"},
		{"fl_stored_text_bad_dict", "/W " + wt + " /H 1 /BPC 8 /CS /G /I 5 /F /Fl"},
		{"chain_fl_unknown", "/W " + wt + " /H 1 /BPC 8 /CS /G /F [/Fl /Foo]"},
		{"chain_fl_jpx", "/W " + wt + " /H 1 /BPC 8 /CS /G /F [/Fl /JPXDecode]"},
		{"chain_fl_dp_mismatch", "/W " + wt + " /H 1 /BPC 8 /CS /G /F [/Fl] /DP [null null 5]"},
	} {
		refused(c.name, img(c.dict, st))
	}
	refused("fl_stored_text_ws_after_ID", imgPre+"BI /W "+wt+" /H 1 /BPC 8 /CS /G /F /Fl ID  "+st+" EI Q")
	refused("probe_text_before_EI", imgPre+"BI "+gray+" ID A"+imgText+"EI Q")

	// Round-2 review (S1–S6). Ghostscript 10.07 shows none of these secrets,
	// at HEAD or now: each guards against a reader that differs from it.
	quoted := "BT /F1 24 Tf 30 TL " + S + "%x\n' ET" // T6 must see past the comment (S1)
	refused("t6_comment_before_quote", img("/W "+fmt.Sprint(len(quoted))+" /H 1 /BPC 8 /CS /G", quoted))
	cases = append(cases, leakCase{name: "inline/rgb_redefined_by_resources", // S2
		pdf: onePage{content: []string{img("/W 40 /H 1 /BPC 8 /CS /RGB", strings.Repeat("\x80", 40)+" Q q /Fm1 Do Q q "+strings.Repeat("\x80", 63)) + "\n" + show(S)},
			res: "<< /Font << /F1 5 0 R >> /ColorSpace << /RGB /DeviceGray >> >>"}.pdf,
		ops: both, want: refuse, reason: "inline_image"})
	refused("indexed_lookup_crlf", img("/W 1 /H 1 /BPC 8 /CS [/I /RGB 1 (ab\r\ncd)]", "a")+"\n"+show(S)) // S4
	refused("decode_doubled_sign", img(gray+" /D [--1]", "A")+"\n"+show(S))                              // S5
	refused("inside_text_object", "BT /F1 24 Tf 72 700 Td BI "+gray+" ID A EI "+S+" Tj ET")              // S6
	return cases
}
