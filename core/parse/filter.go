package parse

import (
	"bytes"
	"compress/flate"
	"compress/zlib"
	"fmt"
	"io"
)

// maxDecoded bounds the output of any single stream decode, guarding against
// decompression bombs (SECURITY.md §2). 256 MiB is far above any legitimate
// xref/object stream while still bounding a hostile input.
const maxDecoded = 256 << 20

// DecodeStream applies a stream's /Filter chain to its raw bytes and returns the
// decoded content. Currently supports FlateDecode (with PNG/TIFF predictors via
// /DecodeParms). An unsupported filter yields a wrapped errLex.
//
// Returns the decoded bytes, or an error if a filter is unsupported or decoding
// fails (including exceeding maxDecoded).
func DecodeStream(s *Stream) ([]byte, error) {
	filters := nameList(s.Dict["Filter"])
	data := s.Raw

	for i, f := range filters {
		switch f {
		case "FlateDecode", "Fl":
			dec, err := flateDecode(data)
			if err != nil {
				return nil, fmt.Errorf("%w: FlateDecode: %v", errLex, err)
			}
			dec, err = applyPredictor(dec, parmsAt(s.Dict["DecodeParms"], i))
			if err != nil {
				return nil, err
			}
			data = dec
		default:
			return nil, fmt.Errorf("%w: unsupported filter /%s", errLex, f)
		}
	}
	return data, nil
}

// nameList normalizes a /Filter (or similar) entry that may be a single Name or
// an Array of Names into a slice.
func nameList(o Object) []Name {
	switch v := o.(type) {
	case Name:
		return []Name{v}
	case Array:
		var out []Name
		for _, e := range v {
			if n, ok := e.(Name); ok {
				out = append(out, n)
			}
		}
		return out
	default:
		return nil
	}
}

// parmsAt returns the /DecodeParms dictionary for filter index i. The entry may
// be a single Dict (single-filter case) or an Array aligned with /Filter.
func parmsAt(o Object, i int) Dict {
	switch v := o.(type) {
	case Dict:
		if i == 0 {
			return v
		}
	case Array:
		if i < len(v) {
			if d, ok := v[i].(Dict); ok {
				return d
			}
		}
	}
	return nil
}

// flateDecode inflates zlib- or raw-deflate-compressed data, capped at
// maxDecoded bytes.
func flateDecode(data []byte) ([]byte, error) {
	if out, err := inflate(zlibReader(data)); err == nil {
		return out, nil
	}
	// Some producers omit the zlib header; fall back to raw DEFLATE.
	return inflate(flate.NewReader(bytes.NewReader(data)))
}

func zlibReader(data []byte) io.ReadCloser {
	zr, err := zlib.NewReader(bytes.NewReader(data))
	if err != nil {
		return io.NopCloser(failingReader{err})
	}
	return zr
}

func inflate(rc io.ReadCloser) ([]byte, error) {
	defer rc.Close()
	out, err := io.ReadAll(io.LimitReader(rc, maxDecoded+1))
	if err != nil {
		return nil, err
	}
	if len(out) > maxDecoded {
		return nil, fmt.Errorf("%w: decoded stream exceeds %d bytes (suspected bomb)", errLex, maxDecoded)
	}
	return out, nil
}

type failingReader struct{ err error }

func (f failingReader) Read([]byte) (int, error) { return 0, f.err }

// applyPredictor reverses a PNG or TIFF predictor as configured by /DecodeParms.
// With no parms or Predictor <= 1 the data is returned unchanged.
func applyPredictor(data []byte, parms Dict) ([]byte, error) {
	if parms == nil {
		return data, nil
	}
	predictor := parmInt(parms, "Predictor", 1)
	if predictor <= 1 {
		return data, nil
	}

	colors := parmInt(parms, "Colors", 1)
	bpc := parmInt(parms, "BitsPerComponent", 8)
	columns := parmInt(parms, "Columns", 1)
	if colors < 1 || bpc < 1 || columns < 1 {
		return nil, fmt.Errorf("%w: invalid predictor parameters", errLex)
	}

	bytesPerPixel := (colors*bpc + 7) / 8
	rowLen := (colors*bpc*columns + 7) / 8
	if rowLen <= 0 {
		return nil, fmt.Errorf("%w: invalid predictor row length", errLex)
	}

	if predictor == 2 {
		return tiffPredictor(data, rowLen, bytesPerPixel), nil
	}
	return pngPredictor(data, rowLen, bytesPerPixel)
}

// pngPredictor reverses PNG predictors (10–15). Each decoded row is prefixed by
// a 1-byte filter type, followed by rowLen data bytes.
func pngPredictor(data []byte, rowLen, bpp int) ([]byte, error) {
	stride := rowLen + 1
	if stride <= 1 || len(data)%stride != 0 {
		return nil, fmt.Errorf("%w: PNG predictor data not a multiple of row length", errLex)
	}
	rows := len(data) / stride
	out := make([]byte, 0, rows*rowLen)
	prev := make([]byte, rowLen)

	for r := 0; r < rows; r++ {
		ft := data[r*stride]
		row := append([]byte(nil), data[r*stride+1:r*stride+1+rowLen]...)
		for i := 0; i < rowLen; i++ {
			var a, b, c int // left, up, upper-left
			if i >= bpp {
				a = int(row[i-bpp])
				c = int(prev[i-bpp])
			}
			b = int(prev[i])
			switch ft {
			case 0: // None
			case 1: // Sub
				row[i] = byte(int(row[i]) + a)
			case 2: // Up
				row[i] = byte(int(row[i]) + b)
			case 3: // Average
				row[i] = byte(int(row[i]) + (a+b)/2)
			case 4: // Paeth
				row[i] = byte(int(row[i]) + paeth(a, b, c))
			default:
				return nil, fmt.Errorf("%w: unknown PNG filter type %d", errLex, ft)
			}
		}
		out = append(out, row...)
		prev = row
	}
	return out, nil
}

// tiffPredictor reverses TIFF predictor 2 (horizontal differencing) for 8-bit
// components.
func tiffPredictor(data []byte, rowLen, bpp int) []byte {
	out := append([]byte(nil), data...)
	rows := len(out) / rowLen
	for r := 0; r < rows; r++ {
		base := r * rowLen
		for i := bpp; i < rowLen; i++ {
			out[base+i] = byte(int(out[base+i]) + int(out[base+i-bpp]))
		}
	}
	return out
}

func paeth(a, b, c int) int {
	p := a + b - c
	pa, pb, pc := abs(p-a), abs(p-b), abs(p-c)
	switch {
	case pa <= pb && pa <= pc:
		return a
	case pb <= pc:
		return b
	default:
		return c
	}
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// parmInt reads an integer /DecodeParms value with a default.
func parmInt(d Dict, key Name, def int) int {
	if v, ok := d.GetInt(key); ok {
		return int(v)
	}
	return def
}
