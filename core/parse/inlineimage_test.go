package parse

import (
	"bytes"
	"compress/flate"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"hash/adler32"
	"math/rand"
	"reflect"
	"strings"
	"testing"
)

// zlibLevel returns payload zlib-compressed at level.
func zlibLevel(t *testing.T, payload []byte, level int) []byte {
	t.Helper()
	var b bytes.Buffer
	w, err := zlib.NewWriterLevel(&b, level)
	if err != nil {
		t.Fatal(err)
	}
	w.Write(payload)
	w.Close()
	return b.Bytes()
}

// deflateBytes returns payload as raw DEFLATE (no zlib header).
func deflateBytes(t *testing.T, payload []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	w, err := flate.NewWriter(&b, flate.BestCompression)
	if err != nil {
		t.Fatal(err)
	}
	w.Write(payload)
	w.Close()
	return b.Bytes()
}

// storedBlock is a stored DEFLATE block holding data (RFC 1951 §3.2.4).
func storedBlock(final bool, data []byte) []byte {
	b := []byte{0}
	if final {
		b[0] = 1
	}
	b = binary.LittleEndian.AppendUint16(b, uint16(len(data)))
	b = binary.LittleEndian.AppendUint16(b, ^uint16(len(data)))
	return append(b, data...)
}

// zlibWrap wraps DEFLATE blocks decoding to plain in a zlib header and
// Adler-32 trailer.
func zlibWrap(blocks, plain []byte) []byte {
	out := append([]byte{0x78, 0x01}, blocks...)
	return binary.BigEndian.AppendUint32(out, adler32.Checksum(plain))
}

// TestStrictInlineImage pins the strict inline-image rule (ISO 32000-1 §8.9.7;
// docs/design/redaction-hardening.md §6.1 Steps 1a–3, T1–T6): an allow-listed
// dictionary, unfiltered / AHx / zlib Flate data that decodes to exactly the
// samples' length, no "EI" and no text-show candidate in the raw data, and
// only white-space, EI and white-space after it.
func TestStrictInlineImage(t *testing.T) {
	payload := []byte("pixels pixels pixels") // 20 bytes: /W 20 /H 1 gray
	zl := zlibLevel(t, payload, zlib.BestCompression)
	if bytes.Contains(zl, []byte("EI")) || textShowCandidate(zl) {
		t.Fatal("test zlib data trips D1 or T6")
	}
	badSum := append([]byte(nil), zl...)
	badSum[len(badSum)-1] ^= 0xFF
	txt := []byte("\nBT /F1 24 Tf 72 700 Td (SECRET-1234) Tj ET\n")
	tail := zlibWrap(append(storedBlock(false, []byte("A")), storedBlock(true, txt)...), append([]byte("A"), txt...))
	hexZl := strings.ToUpper(bytesToHex(zl))
	cs := func(o Object, ok bool) *StrictOptions {
		return &StrictOptions{InlineColorSpace: func(Name) (Object, bool) { return o, ok }}
	}
	gray := "/W 1 /H 1 /BPC 8 /CS /G"
	g20 := "/W 20 /H 1 /BPC 8 /CS /G"
	fl20 := g20 + " /F /Fl"

	tests := []struct {
		name string
		in   string // starts with BI; an accepted image must end just before " Q"
		opts *StrictOptions
		ok   bool
	}{
		// Unfiltered: exact length from the sample layout (§8.9.5.2).
		{"gray 1x1", "BI " + gray + " ID A EI Q", nil, true},
		{"full key names", "BI /Width 2 /Height 1 /BitsPerComponent 8 /ColorSpace /DeviceRGB ID abcdef EI Q", nil, true},
		{"rows are byte-aligned", "BI /W 9 /H 2 /BPC 1 /CS /G ID abcd EI Q", nil, true},
		{"CMYK", "BI /W 1 /H 1 /BPC 8 /CS /CMYK ID abcd EI Q", nil, true},
		{"image mask", "BI /W 9 /H 1 /IM true ID ab EI Q", nil, true},
		{"image mask with BPC 1 and decode", "BI /W 9 /H 1 /IM true /BPC 1 /D [1 0] ID ab EI Q", nil, true},
		{"indexed, device base, exact lookup", "BI /W 2 /H 1 /BPC 8 /CS [/I /RGB 1 <000000ffffff>] ID ab EI Q", nil, true},
		{"indexed, hival at 2^BPC-1", "BI /W 8 /H 1 /BPC 1 /CS [/Indexed /DeviceGray 1 <00ff>] /D [0 1] ID a EI Q", nil, true},
		{"decode, interpolate, intent", "BI " + gray + " /D [1 0] /I true /Intent /Perceptual ID A EI Q", nil, true},
		{"decode parms without predictor", "BI " + gray + " /DP << /Predictor 1 >> ID A EI Q", nil, true},
		{"decode parms null", "BI " + gray + " /DP null ID A EI Q", nil, true},
		{"named device colour space", "BI /W 1 /H 1 /BPC 8 /CS /CS0 ID abc EI Q", cs(Name("DeviceRGB"), true), true},
		{"named indexed colour space", "BI /W 1 /H 1 /BPC 8 /CS /CS0 ID a EI Q",
			cs(Array{Name("Indexed"), Name("DeviceGray"), Integer(0), String{Value: []byte{0}}}, true), true},
		{"whitespace before EI is kept", "BI " + gray + " ID A\n\n EI Q", nil, true},
		{"data ending in white-space before EI", "BI " + gray + " ID \nEI Q", nil, true},
		{"EI at end of content", "BI " + gray + " ID A EI", nil, true},
		{"length entry matches", "BI " + gray + " /L 1 ID A EI Q", nil, true},
		{"filter array, empty", "BI " + gray + " /F [] ID A EI Q", nil, true},

		{"tight EI (repro ii_tight_EI)", "BI " + gray + " ID AEI\n(SECRET) Tj\nEI Q", nil, false},
		{"EI inside the data (repro ii_early_swallow)", "BI /W 8 /H 1 /BPC 8 /CS /G ID \nEI\n(abc\nEI\n", nil, false},
		{"EI pair inside binary data", "BI /W 4 /H 1 /BPC 8 /CS /G ID xEIx EI Q", nil, false},
		{"bytes between data and EI", "BI " + gray + " ID A (SECRET) Tj EI Q", nil, false},
		{"data runs past the end", "BI /W 100 /H 1 /BPC 8 /CS /G ID A EI Q", nil, false},
		{"EI followed by a delimiter", "BI " + gray + " ID A EI(x) Tj", nil, false},
		{"EI followed by a regular byte", "BI " + gray + " ID A EIQ", nil, false},
		{"no white-space after ID", "BI " + gray + " ID(A EI Q", nil, false},
		{"CR LF after ID, unfiltered", "BI " + gray + " ID\r\nA EI Q", nil, false},

		// T6: raw data that would show text if parsed as content.
		{"Tj in the data", "BI /W 2 /H 1 /BPC 8 /CS /G ID Tj EI Q", nil, false},
		{"TJ in the data", "BI /W 2 /H 1 /BPC 8 /CS /G ID TJ EI Q", nil, false},
		{"string then quote in the data", "BI /W 4 /H 1 /BPC 8 /CS /G ID ) \n' EI Q", nil, false},
		{"hex string then double quote in the data", "BI /W 2 /H 1 /BPC 8 /CS /G ID >\" EI Q", nil, false},
		{"string, comment, quote in the data (S1)", "BI /W 9 /H 1 /BPC 8 /CS /G ID (a)%c\n'xx EI Q", nil, false},
		{"text-object data (reviewer raw_ok_control)", "BI /W 41 /H 1 /BPC 8 /CS /G ID BT /F1 24 Tf 72 700 Td (SECRET-1234) Tj ET EI Q", nil, false},

		// Step 1a: the dictionary allow-list (B1).
		{"unknown key", "BI " + gray + " /Foo /Bar ID A EI Q", nil, false},
		{"missing BPC", "BI /W 1 /H 1 /CS /G ID A EI Q", nil, false},
		{"BPC 16", "BI /W 1 /H 1 /BPC 16 /CS /G ID ab EI Q", nil, false},
		{"BPC 3", "BI /W 1 /H 1 /BPC 3 /CS /G ID A EI Q", nil, false},
		{"real width", "BI /W 1.0 /H 1 /BPC 8 /CS /G ID A EI Q", nil, false},
		{"zero height", "BI /W 1 /H 0 /BPC 8 /CS /G ID A EI Q", nil, false},
		{"missing colour space", "BI /W 1 /H 1 /BPC 8 ID A EI Q", nil, false},
		{"bare Indexed name", "BI /W 1 /H 1 /BPC 8 /CS /I ID A EI Q", nil, false},
		{"indexed, short lookup", "BI /W 1 /H 1 /BPC 8 /CS [/I /RGB 255 <00>] ID A EI Q", nil, false},
		{"indexed, long lookup", "BI /W 1 /H 1 /BPC 8 /CS [/I /G 0 <0000>] ID A EI Q", nil, false},
		{"indexed, hival past 2^BPC-1", "BI /W 8 /H 1 /BPC 1 /CS [/I /G 2 <000080ff>] ID a EI Q", nil, false},
		{"indexed, BPC 16", "BI /W 1 /H 1 /BPC 16 /CS [/I /G 1 <00ff>] ID ab EI Q", nil, false},
		{"indexed, non-device base", "BI /W 1 /H 1 /BPC 8 /CS [/I /CS0 0 <00>] ID A EI Q", nil, false},
		{"DeviceN array", "BI /W 1 /H 1 /BPC 8 /CS [/DeviceN [/A /B] /DeviceRGB null] ID ab EI Q", nil, false},
		{"CalGray array", "BI " + "/W 1 /H 1 /BPC 8 /CS [/CalGray << /WhitePoint [1 1 1] >>] ID A EI Q", nil, false},
		{"Lab array", "BI /W 1 /H 1 /BPC 8 /CS [/Lab] ID abc EI Q", nil, false},
		{"Separation array", "BI /W 1 /H 1 /BPC 8 /CS [/Separation /A] ID A EI Q", nil, false},
		{"ICCBased array", "BI /W 1 /H 1 /BPC 8 /CS [/ICCBased 5 0 R] ID A EI Q", nil, false},
		{"named colour space, no resolver", "BI /W 1 /H 1 /BPC 8 /CS /CS0 ID abc EI Q", nil, false},
		{"named colour space, unresolved", "BI /W 1 /H 1 /BPC 8 /CS /CS0 ID abc EI Q", cs(nil, false), false},
		{"named colour space, resolves to Lab", "BI /W 1 /H 1 /BPC 8 /CS /CS0 ID abc EI Q",
			cs(Array{Name("Lab"), Dict{}}, true), false},
		{"redefined abbreviation as colour space (S2)", "BI /W 1 /H 1 /BPC 8 /CS /RGB ID abc EI Q", cs(Name("DeviceGray"), true), false},
		{"redefined abbreviation as Indexed base (S2)", "BI /W 1 /H 1 /BPC 8 /CS [/I /G 0 <00>] ID a EI Q", cs(Name("DeviceRGB"), true), false},
		{"abbreviation with an unrelated resolver", "BI /W 1 /H 1 /BPC 8 /CS /RGB ID abc EI Q", cs(nil, false), true},
		{"named colour space, resolves to /I array (S3)", "BI /W 1 /H 1 /BPC 8 /CS /CS0 ID a EI Q",
			cs(Array{Name("I"), Name("DeviceGray"), Integer(0), String{Value: []byte{0}}}, true), false},
		{"named colour space, resolves to an abbreviated base (S3)", "BI /W 1 /H 1 /BPC 8 /CS /CS0 ID a EI Q",
			cs(Array{Name("Indexed"), Name("G"), Integer(0), String{Value: []byte{0}}}, true), false},
		{"indexed, literal lookup with CR LF (S4)", "BI /W 1 /H 1 /BPC 8 /CS [/I /RGB 1 (ab\r\ncd)] ID a EI Q", nil, false},
		{"indexed, literal lookup with LF", "BI /W 1 /H 1 /BPC 8 /CS [/I /RGB 1 (ab\n\ncd)] ID a EI Q", nil, true},
		{"decode with a doubled sign (S5)", "BI " + gray + " /D [--1] ID A EI Q", nil, false},
		{"decode with a lone sign (S5)", "BI " + gray + " /D [- 1] ID A EI Q", nil, false},
		{"decode with signed numbers", "BI " + gray + " /D [-1 +.5] ID A EI Q", nil, true},
		{"named colour space, resolves to an abbreviation", "BI /W 1 /H 1 /BPC 8 /CS /CS0 ID A EI Q", cs(Name("G"), true), false},
		{"image mask with BPC 8", "BI /W 1 /H 1 /IM true /BPC 8 ID A EI Q", nil, false},
		{"image mask with a colour space", "BI /W 1 /H 1 /IM true /CS /G ID A EI Q", nil, false},
		{"image mask flag not boolean", "BI /W 1 /H 1 /IM 1 /BPC 8 /CS /G ID A EI Q", nil, false},
		{"interpolate not boolean (reviewer raw_interp_int)", "BI " + gray + " /I 5 ID A EI Q", nil, false},
		{"intent not a name", "BI " + gray + " /Intent (x) ID A EI Q", nil, false},
		{"decode of the wrong length", "BI " + gray + " /D [0] ID A EI Q", nil, false},
		{"decode of strings", "BI " + gray + " /D [(a) (b)] ID A EI Q", nil, false},
		{"image mask decode of the wrong length", "BI /W 8 /H 1 /IM true /D [0] ID A EI Q", nil, false},
		{"decode parms with a predictor", "BI " + gray + " /DP << /Predictor 15 >> ID A EI Q", nil, false},
		{"decode parms with other keys", "BI " + gray + " /F /Fl /DP << /Columns 1 >> ID A EI Q", nil, false},
		{"decode parms array not aligned", "BI " + fl20 + " /DP [null null 5] ID " + string(zl) + " EI Q", nil, false},
		{"huge width and height", "BI /W 9223372036854775807 /H 9223372036854775807 /BPC 8 /CS /CMYK ID A EI Q", nil, false},
		{"huge height", "BI /W 1 /H 4611686018427387904 /BPC 8 /CS /G ID A EI Q", nil, false},
		{"length entry disagrees", "BI " + gray + " /L 2 ID A EI Q", nil, false},
		{"length entry not an integer", "BI " + gray + " /Length (1) ID A EI Q", nil, false},
		{"repeated key", "BI " + gray + " /Width 1 ID A EI Q", nil, false},
		{"key without a value", "BI /W 1 /H 1 /BPC 8 /CS ID A EI Q", nil, false},
		{"non-name key", "BI 1 /W ID A EI Q", nil, false},
		{"dictionary token error", "BI /W <zz> ID A EI Q", nil, false},
		{"over-deep dictionary value", "BI " + gray + " /D " + strings.Repeat("[", 300) + strings.Repeat("]", 300) + " ID A EI Q", nil, false},
		{"no ID", "BI " + gray, nil, false},

		// ASCIIHex: the first '>', decoding to exactly the samples (§7.4.2).
		{"AHx", "BI " + gray + " /F /AHx ID 4 1> EI Q", nil, true},
		{"AHx odd final digit", "BI " + gray + " /F /AHx ID 4> EI Q", nil, true},
		{"AHx, CR LF after ID", "BI " + gray + " /F /AHx ID\r\n41> EI Q", nil, true},
		{"AHx then Fl", "BI " + g20 + " /F [/AHx /Fl] ID " + hexZl + "> EI Q", nil, true},
		{"AHx, too many digits", "BI " + gray + " /F /AHx ID 4142> EI Q", nil, false},
		{"AHx then Fl, wrong length", "BI /W 1 /H 1 /BPC 8 /CS /G /F [/AHx /Fl] ID " + hexZl + "> EI Q", nil, false},
		{"AHx tight EI (repro ii_tight_EI_hex)", "BI " + gray + " /F /AHx ID 41>EI\n(SECRET) Tj\nEI Q", nil, false},
		{"AHx invalid byte", "BI " + gray + " /F /AHx ID 4G> EI Q", nil, false},
		{"AHx without end marker", "BI " + gray + " /F /AHx ID 41 EI Q", nil, false},
		{"AHx, CR LF and a length entry", "BI " + gray + " /F /AHx /L 3 ID\r\n41> EI Q", nil, false},
		{"AHx twice", "BI " + gray + " /F [/AHx /AHx] ID 3431> EI Q", nil, false},
		{"Fl then AHx", "BI " + gray + " /F [/Fl /AHx] ID " + string(zl) + " EI Q", nil, false},

		// Flate: a zlib stream decoding to exactly the samples (B2, B3).
		{"Fl", "BI " + fl20 + " ID " + string(zl) + " EI Q", nil, true},
		{"Fl, full name in an array", "BI /W 20 /H 1 /BPC 8 /CS /G /F [/FlateDecode] ID " + string(zl) + " EI Q", nil, true},
		{"Fl decoding to more than the samples", "BI /W 1 /H 1 /BPC 8 /CS /G /F /Fl ID " + string(zl) + " EI Q", nil, false},
		{"Fl decoding to fewer than the samples", "BI /W 21 /H 1 /BPC 8 /CS /G /F /Fl ID " + string(zl) + " EI Q", nil, false},
		{"Fl with text in a later block (reviewer fl_stored_tail_small)", "BI " + gray + " /F /Fl ID " + string(tail) + " EI Q", nil, false},
		{"Fl raw DEFLATE (B3)", "BI " + fl20 + " ID " + string(deflateBytes(t, payload)) + " EI Q", nil, false},
		{"Fl with trailing bytes", "BI " + fl20 + " ID " + string(zl) + "xx EI Q", nil, false},
		{"Fl truncated", "BI " + fl20 + " ID " + string(zl[:len(zl)-6]) + " EI Q", nil, false},
		{"Fl bad checksum", "BI " + fl20 + " ID " + string(badSum) + " EI Q", nil, false},
		{"Fl stored block holding EI", "BI /W 6 /H 1 /BPC 8 /CS /G /F /Fl ID " + string(zlibLevel(t, []byte("xxEIxx"), zlib.NoCompression)) + " EI Q", nil, false},
		{"Fl stored block holding text (reviewer fl_stored_text_exact)", "BI /W 44 /H 1 /BPC 8 /CS /G /F /Fl ID " + string(zlibWrap(storedBlock(true, txt), txt)) + " EI Q", nil, false},
		{"Fl, CR LF after ID", "BI " + fl20 + " ID\r\n" + string(zl) + " EI Q", nil, false},
		{"Fl twice", "BI " + g20 + " /F [/Fl /Fl] ID " + string(zl) + " EI Q", nil, false},
		{"Fl then an unknown filter", "BI " + g20 + " /F [/Fl /Foo] ID " + string(zl) + " EI Q", nil, false},

		// Filters refused in Phase 1 (decision D15).
		{"A85", "BI " + gray + " /F /A85 ID 5l~> EI Q", nil, false},
		{"DCT", "BI " + gray + " /F /DCT ID \xff\xd8\xff\xd9 EI Q", nil, false},
		{"LZW", "BI " + gray + " /F /LZW ID \x80\x0b\x60\x50\x22\x0c\x0c\x85\x01 EI Q", nil, false},
		{"RunLength", "BI " + gray + " /F /RL ID \x00A\x80 EI Q", nil, false},
		{"CCITT", "BI " + gray + " /F /CCF ID \x00 EI Q", nil, false},
		{"JBIG2", "BI " + gray + " /F /JBIG2Decode ID \x00 EI Q", nil, false},
		{"JPX", "BI " + gray + " /F /JPXDecode ID \x00 EI Q", nil, false},
		{"unknown filter", "BI " + gray + " /F /Foo ID A EI Q", nil, false},
		{"filter not a name", "BI " + gray + " /F (Fl) ID A EI Q", nil, false},
		{"filter array with a non-name", "BI " + gray + " /F [/AHx 1] ID 41> EI Q", nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data := []byte(tc.in)
			var inflated int64
			_, end, what := strictInlineImage(data, len("BI"), tc.opts, &inflated)
			if tc.ok != (what == "") {
				t.Fatalf("strictInlineImage = (%d, %q), want ok=%v", end, what, tc.ok)
			}
			if tc.ok {
				if want := len(strings.TrimSuffix(tc.in, " Q")); end != want {
					t.Errorf("end = %d, want %d (just past EI)", end, want)
				}
				return
			}
			t.Logf("loss: %s", what)
			if end < 0 || end > len(data) {
				t.Errorf("loss offset %d outside the content [0, %d]", end, len(data))
			}
			// The scanner must report the same loss.
			opts := StrictOptions{}
			if tc.opts != nil {
				opts = *tc.opts
			}
			if _, err := ScanContentStrict(ContentSegments{Data: data}, opts); err == nil {
				t.Errorf("ScanContentStrict accepted the image")
			}
		})
	}
}

func bytesToHex(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, 2*len(b))
	for _, c := range b {
		out = append(out, digits[c>>4], digits[c&15])
	}
	return string(out)
}

// TestTextShowCandidate pins T6 (decision D16).
func TestTextShowCandidate(t *testing.T) {
	for in, want := range map[string]bool{
		"":               false,
		"abc":            false,
		"Tj":             true,
		"xTJx":           true,
		"T j":            false,
		"(a)'":           true,
		") \r\n\"":       true,
		"> '":            true,
		"<41> x '":       false,
		"'\")":           false,
		"(a)%c\n'":       true,
		"<41>%\r\"":      true,
		"(a) % x \r\n '": true,
		"(a)%c'":         false,
		"\x00\xff>":      false,
		"0123456789":     false,
	} {
		if got := textShowCandidate([]byte(in)); got != want {
			t.Errorf("textShowCandidate(%q) = %v, want %v", in, got, want)
		}
	}
}

// TestZlibExact: the Flate end is the exact length of the zlib stream,
// whatever its content and level, when it decodes to exactly the samples'
// length.
func TestZlibExact(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 300; i++ {
		payload := make([]byte, rng.Intn(3000))
		for j := range payload {
			if i%2 == 0 {
				payload[j] = byte(rng.Intn(256))
			} else {
				payload[j] = byte('a' + rng.Intn(3))
			}
		}
		for _, level := range []int{zlib.NoCompression, zlib.BestSpeed, zlib.BestCompression} {
			zl := zlibLevel(t, payload, level)
			n, what := zlibExact(append(zl, " EI Q trailing"...), int64(len(payload)), new(int64))
			if what != "" || n != len(zl) {
				t.Fatalf("payload %d level %d: zlibExact = (%d, %q), want (%d, \"\")", i, level, n, what, len(zl))
			}
		}
	}
}

// TestStrictInlineImage_InflateBudget: one scan inflates at most
// maxInlineInflate bytes to check its images; past that it is a loss.
func TestStrictInlineImage_InflateBudget(t *testing.T) {
	zl := zlibLevel(t, make([]byte, 1000), zlib.BestCompression)
	data := []byte("BI /W 1000 /H 1 /BPC 8 /CS /G /F /Fl ID " + string(zl) + " EI")
	inflated := int64(maxInlineInflate - 999)
	if _, _, what := strictInlineImage(data, 2, nil, &inflated); what == "" {
		t.Errorf("image accepted past the inflate allowance")
	}
	inflated = maxInlineInflate - 1000
	if _, _, what := strictInlineImage(data, 2, nil, &inflated); what != "" {
		t.Errorf("image within the allowance refused: %s", what)
	}
}

// TestScanContentStrict_InlineImage: the scanner applies the strict rule, and
// the lenient scanner keeps its own (unchanged) rule.
func TestScanContentStrict_InlineImage(t *testing.T) {
	// A reader using the exact length shows the Tj after the 1-byte image;
	// the lenient scan runs to the later EI and skips it.
	in := []byte("BI /W 1 /H 1 /BPC 8 /CS /G ID AEI\nBT (SECRET) Tj ET\nEI\nBT (public) Tj ET")
	if _, err := ScanContentStrict(ContentSegments{Data: in}, StrictOptions{}); err == nil {
		t.Errorf("strict scan accepted an inline image whose readers disagree")
	}
	lenient := ScanContentSpans(in)
	if len(lenient) != 3 || lenient[1].Op != "Tj" {
		t.Errorf("lenient spans = %+v, want BT (public) Tj ET", lenient)
	}

	// Two well-formed images, then text.
	in = []byte("q BI /W 1 /H 1 /BPC 8 /CS /G ID A EI Q BI /W 2 /H 1 /BPC 8 /CS /CS0 ID ab EI BT (x) Tj ET")
	opts := StrictOptions{InlineColorSpace: func(n Name) (Object, bool) { return Name("DeviceGray"), n == "CS0" }}
	spans, err := ScanContentStrict(ContentSegments{Data: in}, opts)
	if err != nil {
		t.Fatal(err)
	}
	var ops []string
	for _, s := range spans {
		ops = append(ops, s.Op)
	}
	if got := strings.Join(ops, " "); got != "q BI Q BI BT Tj ET" {
		t.Errorf("ops = %q, want \"q BI Q BI BT Tj ET\"", got)
	}
	// Each accepted image is one span through EI (from the white-space before
	// BI, like every span), with its
	// dictionary keyed by full names.
	if bi := spans[1]; string(in[bi.Start:bi.End]) != " BI /W 1 /H 1 /BPC 8 /CS /G ID A EI" {
		t.Errorf("first image span = %q", in[bi.Start:bi.End])
	} else if d, ok := bi.Operands[0].(Dict); !ok || d["Width"] != Integer(1) || d["ColorSpace"] != Name("G") {
		t.Errorf("first image operands = %v, want its dictionary with full keys", bi.Operands)
	}
	if bi := spans[3]; string(in[bi.Start:bi.End]) != " BI /W 2 /H 1 /BPC 8 /CS /CS0 ID ab EI" {
		t.Errorf("second image span = %q", in[bi.Start:bi.End])
	}

	// MaxOps: the scan stops before it materializes more operators.
	if _, err := ScanContentStrict(ContentSegments{Data: in}, StrictOptions{InlineColorSpace: opts.InlineColorSpace, MaxOps: 6}); !errors.Is(err, ErrOpLimit) {
		t.Errorf("MaxOps 6 (7 operators) err = %v, want ErrOpLimit", err)
	}
	if _, err := ScanContentStrict(ContentSegments{Data: in}, StrictOptions{InlineColorSpace: opts.InlineColorSpace, MaxOps: 7}); err != nil {
		t.Errorf("MaxOps 7 (7 operators) err = %v, want nil", err)
	}

	// No inline image inside a text object (ISO 32000-1 Figure 9; S6).
	for in, ok := range map[string]bool{
		"BT /F1 24 Tf BI /W 1 /H 1 /BPC 8 /CS /G ID A EI (x) Tj ET": false,
		"BT ET BI /W 1 /H 1 /BPC 8 /CS /G ID A EI BT (x) Tj ET":     true,
	} {
		if _, err := ScanContentStrict(ContentSegments{Data: []byte(in)}, StrictOptions{}); (err == nil) != ok {
			t.Errorf("ScanContentStrict(%q) err = %v, want ok=%v", in, err, ok)
		}
	}
}

// FuzzScanContentStrict fuzzes the strict scanner over content split into two
// streams at a fuzzed offset (docs/design/redaction-hardening.md §12.5): it
// must not panic, every inline image end it computes lies in the content, and
// whatever it accepts it reads exactly as the lenient scanner does.
//
// Run: go test ./core/parse -run=xxx -fuzz=FuzzScanContentStrict -fuzztime=60s
func FuzzScanContentStrict(f *testing.F) {
	f.Add([]byte("BI /W 1 /H 1 /BPC 8 /CS /G ID A EI Q"), 3)
	f.Add([]byte("BI /W 1 /H 1 /BPC 8 /CS /G /F /AHx ID 41> EI (x) Tj"), 40)
	f.Add([]byte("BI /W 8 /H 1 /BPC 8 /CS /G ID \nEI\n(abc\nEI\n(x) Tj"), 0)
	f.Add([]byte("BI /W 9 /H 1 /IM true /D [1 0] ID ab EI"), 12)
	f.Add([]byte("BI /W 2 /H 1 /BPC 8 /CS [/I /RGB 1 <000000ffffff>] /DP << /Predictor 1 >> ID ab EI"), 5)
	f.Add([]byte("BI /W 3 /H 1 /BPC 8 /CS /G /F /Fl ID x\x9cKLJ\x06\x00\x02M\x01'\n EI"), 30)
	f.Add([]byte("BT /F1 12 Tf (SECRET-1234) Tj ET %c"), 26)
	f.Fuzz(func(t *testing.T, data []byte, split int) {
		c := ContentSegments{Data: data}
		if split > 0 && split < len(data) {
			c.Data = append(append(append([]byte{}, data[:split]...), '\n'), data[split:]...)
			c.Joins = []int{split}
		}
		st := &strictScan{opts: &StrictOptions{}, images: map[int]int{}}
		ops, err := scanContent(c.Data, st, false)
		for bi, end := range st.images {
			if end < bi || end > len(c.Data) {
				t.Fatalf("inline image after BI at %d ends at %d, outside [%d, %d]", bi, end, bi, len(c.Data))
			}
		}
		if err == nil {
			if want := ScanContentSpans(c.Data); !reflect.DeepEqual(withoutInlineImages(ops), want) {
				t.Fatalf("strict spans %v differ from the lenient spans %v", ops, want)
			}
		}
		_, _ = ScanContentStrict(c, StrictOptions{InlineColorSpace: func(Name) (Object, bool) { return Name("DeviceRGB"), true }})
	})
}
