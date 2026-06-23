package parse

import (
	"bytes"
	"testing"
)

// encodePNG applies a single PNG row filter (forward) to every row, producing
// the byte layout the decoder reverses: each row prefixed by its filter byte.
func encodePNG(rows [][]byte, filter byte, bpp int) []byte {
	var out []byte
	prev := make([]byte, len(rows[0]))
	for _, row := range rows {
		out = append(out, filter)
		enc := make([]byte, len(row))
		for i := range row {
			var a, b, c int // left, up, upper-left
			if i >= bpp {
				a = int(row[i-bpp])
				c = int(prev[i-bpp])
			}
			b = int(prev[i])
			switch filter {
			case 0:
				enc[i] = row[i]
			case 1:
				enc[i] = byte(int(row[i]) - a)
			case 2:
				enc[i] = byte(int(row[i]) - b)
			case 3:
				enc[i] = byte(int(row[i]) - (a+b)/2)
			case 4:
				enc[i] = byte(int(row[i]) - paeth(a, b, c))
			}
		}
		out = append(out, enc...)
		prev = row
	}
	return out
}

// TestPNGPredictorRoundTrip exercises every PNG filter type (None/Sub/Up/
// Average/Paeth) through the real FlateDecode + predictor path. These reverse
// the byte transforms applied to real-world xref streams; a reconstruction bug
// would silently corrupt the cross-reference table.
func TestPNGPredictorRoundTrip(t *testing.T) {
	rows := [][]byte{
		{10, 20, 30, 40},
		{15, 25, 35, 45},
		{12, 0, 250, 7},
	}
	flat := bytes.Join(rows, nil)

	for filter := byte(0); filter <= 4; filter++ {
		st := &Stream{
			Dict: Dict{
				"Filter":      Name("FlateDecode"),
				"DecodeParms": Dict{"Predictor": Integer(15), "Columns": Integer(4)},
			},
			Raw: zlibBytes(encodePNG(rows, filter, 1)),
		}
		got, err := DecodeStream(st)
		if err != nil {
			t.Fatalf("filter %d: %v", filter, err)
		}
		if !bytes.Equal(got, flat) {
			t.Errorf("filter %d: decoded = %v, want %v", filter, got, flat)
		}
	}
}

// TestPNGPredictorMultiByteBpp covers the i >= bpp boundary with Colors=3
// (bpp=3), where Sub/Paeth reference three bytes back.
func TestPNGPredictorMultiByteBpp(t *testing.T) {
	rows := [][]byte{
		{255, 0, 128, 64, 32, 16}, // 2 pixels x 3 colors
		{100, 50, 25, 200, 150, 75},
	}
	flat := bytes.Join(rows, nil)

	st := &Stream{
		Dict: Dict{
			"Filter": Name("FlateDecode"),
			"DecodeParms": Dict{
				"Predictor":        Integer(15),
				"Colors":           Integer(3),
				"BitsPerComponent": Integer(8),
				"Columns":          Integer(2),
			},
		},
		Raw: zlibBytes(encodePNG(rows, 4, 3)), // Paeth, bpp=3
	}
	got, err := DecodeStream(st)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, flat) {
		t.Errorf("decoded = %v, want %v", got, flat)
	}
}

// TestTIFFPredictorRoundTrip covers TIFF predictor 2 (horizontal differencing).
func TestTIFFPredictorRoundTrip(t *testing.T) {
	rows := [][]byte{
		{10, 20, 30, 40},
		{5, 5, 5, 5},
	}
	flat := bytes.Join(rows, nil)

	// Forward TIFF: out[i] = raw[i] - raw[i-1] within each row (bpp=1).
	var enc []byte
	for _, row := range rows {
		prev := 0
		for _, v := range row {
			enc = append(enc, byte(int(v)-prev))
			prev = int(v)
		}
	}

	st := &Stream{
		Dict: Dict{
			"Filter":      Name("FlateDecode"),
			"DecodeParms": Dict{"Predictor": Integer(2), "Columns": Integer(4)},
		},
		Raw: zlibBytes(enc),
	}
	got, err := DecodeStream(st)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, flat) {
		t.Errorf("TIFF decoded = %v, want %v", got, flat)
	}
}

// TestDecodeStream_NoFilter returns raw bytes unchanged.
func TestDecodeStream_NoFilter(t *testing.T) {
	st := &Stream{Dict: Dict{}, Raw: []byte("plain")}
	got, err := DecodeStream(st)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "plain" {
		t.Errorf("got %q, want plain", got)
	}
}

// TestDecodeStream_PredictorBadRowLength rejects data that is not a whole number
// of predictor rows.
func TestDecodeStream_PredictorBadRowLength(t *testing.T) {
	// 5 bytes can't split into rows of (Columns 4 + 1 filter byte) = 5; that is
	// exactly one row, so use 4 columns with 6 bytes (not a multiple of 5).
	st := &Stream{
		Dict: Dict{
			"Filter":      Name("FlateDecode"),
			"DecodeParms": Dict{"Predictor": Integer(12), "Columns": Integer(4)},
		},
		Raw: zlibBytes([]byte{2, 0, 0, 0, 0, 9}), // 6 bytes, stride 5 -> remainder
	}
	if _, err := DecodeStream(st); err == nil {
		t.Error("DecodeStream with ragged predictor data = nil error, want an error")
	}
}
