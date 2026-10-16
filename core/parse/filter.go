package parse

import (
	"bytes"
	"compress/flate"
	"compress/zlib"
	"errors"
	"fmt"
	"io"
	"math"
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
	data, _, err := DecodeStreamLimit(s, math.MaxInt64)
	return data, err
}

// ErrDecodeLimit is returned (wrapped) by DecodeStreamLimit when the filters
// would produce more than the caller's limit, and by PageContent and
// PageContentLimit when a page's content exceeds its limit.
var ErrDecodeLimit = errors.New("parse: decoded stream exceeds the caller's limit")

// DecodeStreamLimit is DecodeStream for a caller that meters decode work, such
// as the renderer's per-page budget, which decodes the same image or form on
// every Do. It stops as soon as the filters have produced more than limit
// bytes in total and returns an error wrapping ErrDecodeLimit, so one call
// never inflates past what the caller has left. When everything the filters
// output fits within the limit — counting every stage and every inflate
// attempt, a failed zlib attempt before the raw-DEFLATE fallback included —
// its data and error are exactly DecodeStream's.
//
// produced counts every byte the filters output, the intermediate stages of a
// chain and an inflate attempt that failed part-way included — the work done —
// whether or not err is nil. The allowance is shared by all stages and
// attempts, so produced is at most limit+1 in total.
func DecodeStreamLimit(s *Stream, limit int64) (data []byte, produced int64, err error) {
	filters := nameList(s.Dict["Filter"])
	data = s.Raw
	lim := &decodeLimit{left: limit}

	for i, f := range filters {
		switch f {
		case "FlateDecode", "Fl":
			dec, err := flateDecode(data, lim)
			if err != nil {
				if errors.Is(err, ErrDecodeLimit) {
					return nil, lim.produced, err
				}
				return nil, lim.produced, fmt.Errorf("%w: FlateDecode: %v", errLex, err)
			}
			dec, err = applyPredictor(dec, parmsAt(s.Dict["DecodeParms"], i))
			if err != nil {
				return nil, lim.produced, err
			}
			data = dec
		default:
			return nil, lim.produced, fmt.Errorf("%w: unsupported filter /%s", errLex, f)
		}
	}
	return data, lim.produced, nil
}

// decodeLimit tracks DecodeStreamLimit's output allowance across filter stages
// and inflate attempts.
type decodeLimit struct {
	left     int64 // bytes still allowed; < 0 once exceeded
	produced int64 // bytes output so far
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
// maxDecoded bytes and at lim. Exceeding lim ends the decode: it does not fall
// back to raw DEFLATE.
func flateDecode(data []byte, lim *decodeLimit) ([]byte, error) {
	out, err := inflate(zlibReader(data), lim)
	if err == nil || errors.Is(err, ErrDecodeLimit) {
		return out, err
	}
	// Some producers omit the zlib header; fall back to raw DEFLATE.
	return inflate(flate.NewReader(bytes.NewReader(data)), lim)
}

func zlibReader(data []byte) io.ReadCloser {
	zr, err := zlib.NewReader(bytes.NewReader(data))
	if err != nil {
		return io.NopCloser(failingReader{err})
	}
	return zr
}

// inflate reads rc to the end, up to maxDecoded bytes and lim's allowance,
// charging what it read to lim (also when the read fails part-way).
func inflate(rc io.ReadCloser, lim *decodeLimit) ([]byte, error) {
	defer rc.Close()
	n := int64(maxDecoded + 1)
	capped := lim.left < maxDecoded
	if capped {
		n = max(lim.left, 0) + 1
	}
	out, err := io.ReadAll(io.LimitReader(rc, n))
	lim.produced += int64(len(out))
	lim.left -= int64(len(out))
	if capped && int64(len(out)) == n {
		return nil, fmt.Errorf("%w: more than %d bytes", ErrDecodeLimit, n-1)
	}
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
	// Bound each parameter before multiplying (SECURITY.md §2): unbounded, the
	// row-length product wraps (e.g. Colors 3 × BitsPerComponent 2^62 × Columns
	// 3), and a hostile row length drives the allocations below. With every
	// factor ≤ maxDecoded (2^28) and bpc ≤ 16 the product is ≤ 2^60, so it fits
	// an int64, and a row longer than maxDecoded cannot decode anyway.
	if colors < 1 || bpc < 1 || columns < 1 || colors > maxDecoded || bpc > 16 || columns > maxDecoded {
		return nil, fmt.Errorf("%w: invalid predictor parameters", errLex)
	}

	bytesPerPixel := int((int64(colors)*int64(bpc) + 7) / 8)
	rowLen64 := (int64(colors)*int64(bpc)*int64(columns) + 7) / 8
	if rowLen64 > maxDecoded { // ≥ 1: every factor was checked ≥ 1 above
		return nil, fmt.Errorf("%w: invalid predictor row length", errLex)
	}
	rowLen := int(rowLen64)

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
	if rows == 0 {
		return []byte{}, nil // empty data: do not allocate a row from parameters alone
	}
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

// parmInt reads an integer /DecodeParms value with a default. A value beyond
// the int32 range is clamped to it in int64 before the conversion to int, so
// the result does not depend on int's width (on a 32-bit int, 2^32+1 would wrap
// to 1); every caller's bounds reject the clamped extremes.
func parmInt(d Dict, key Name, def int) int {
	if v, ok := d.GetInt(key); ok {
		switch {
		case v > math.MaxInt32:
			return math.MaxInt32
		case v < math.MinInt32:
			return math.MinInt32
		}
		return int(v)
	}
	return def
}
