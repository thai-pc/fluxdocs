package parse

import (
	"bytes"
	"compress/zlib"
	"errors"
	"io"
)

// This file delimits inline images for strict content scanning
// (ScanContentStrict). ISO 32000-1 §8.9.7 says the ID operator is followed by
// one white-space byte, the image data, then EI, but not how a reader finds the
// end of the data. Readers compute it from the sample layout or a filter's
// end-of-data marker, or search for EI; and a reader that rejects the image
// (a dictionary value it does not accept, data it cannot decode) or reads fewer
// bytes than the data holds parses the rest of the data as content. A stream
// crafted for any of these shows text to one viewer that another treats as
// image data. Strict scanning therefore accepts only images every reader
// delimits the same way (docs/design/redaction-hardening.md §6.1; maintainer
// decisions of 2026-10-16):
//
//   - the dictionary holds only allow-listed keys with valid values;
//   - the data is unfiltered, ASCIIHex, zlib Flate, or ASCIIHex then Flate, and
//     decodes to exactly the bytes its samples need, so no reader stops early;
//   - the raw data holds no "EI" (decision D1-A) and nothing that would show
//     text if a reader parsed it as content;
//   - only white-space, then EI, then white-space or the end follow the data.
//
// Anything else is a loss. The lenient scanner (skipInlineImage) is unchanged.

// maxInlineInflate bounds the bytes one strict scan inflates to check its
// Flate-encoded inline images (SECURITY.md §2). The samples are discarded, but
// a decompression bomb still costs the time to inflate it. §8.9.7 recommends
// inline images of at most 4 KB.
const maxInlineInflate = maxDecoded

// inlineImageKeys maps each allow-listed inline-image dictionary key,
// abbreviated or not, to its full name (ISO 32000-1 §8.9.7, Table 93; Intent
// from Table 89). L/Length is the length entry ISO 32000-2 adds.
var inlineImageKeys = map[Name]Name{
	"BPC": "BitsPerComponent", "BitsPerComponent": "BitsPerComponent",
	"CS": "ColorSpace", "ColorSpace": "ColorSpace",
	"D": "Decode", "Decode": "Decode",
	"DP": "DecodeParms", "DecodeParms": "DecodeParms",
	"F": "Filter", "Filter": "Filter",
	"H": "Height", "Height": "Height",
	"IM": "ImageMask", "ImageMask": "ImageMask",
	"I": "Interpolate", "Interpolate": "Interpolate",
	"W": "Width", "Width": "Width",
	"L": "Length", "Length": "Length",
	"Intent": "Intent",
}

// inlineFilters maps the filter names strict scanning accepts, abbreviated or
// not (§8.9.7, Table 94), to their abbreviation. Every other filter (A85, LZW,
// RL, CCF, DCT, JBIG2, JPX, Crypt) is a loss: its data cannot be checked to
// decode to exactly the image's samples without a decoder this package lacks,
// or its format (JPEG segments) can carry bytes a reader skips or parses.
var inlineFilters = map[Name]string{
	"AHx": "AHx", "ASCIIHexDecode": "AHx",
	"Fl": "Fl", "FlateDecode": "Fl",
}

// inlineDeviceComponents is the component count of each device colour space
// name and abbreviation (§8.9.7, Table 93; §8.6.4).
var inlineDeviceComponents = map[Name]int{
	"G": 1, "DeviceGray": 1, "RGB": 3, "DeviceRGB": 3, "CMYK": 4, "DeviceCMYK": 4,
}

// inlineImage is an accepted inline image's decoding: its filters, outermost
// first, and the exact number of decoded bytes its samples need.
type inlineImage struct {
	filters []string
	need    int64
}

// strictInlineImage reads the inline image whose BI operator ends at i in
// strict mode (see the file comment). Each plausible data start — after the
// one white-space byte, and after CR LF — must give the same EI.
//
// Returns the offset just past EI and an empty string; or, when some reader
// could delimit or reject the image differently, the offset to report and a
// description of the loss. inflated is the scan's running Flate allowance.
func strictInlineImage(data []byte, i int, opts *StrictOptions, inflated *int64) (int, string) {
	dict, idEnd, what := inlineImageDict(data, i)
	if what != "" {
		return idEnd, what
	}
	img, what := checkInlineDict(dict, opts)
	if what != "" {
		return idEnd, what
	}
	// §8.9.7: exactly one white-space byte separates ID from the data. With
	// CR LF, a reader may take the LF as that byte or as the first data byte.
	if idEnd >= len(data) || !isWhitespace(data[idEnd]) {
		return idEnd, "inline image data does not start with white-space"
	}
	starts := []int{idEnd + 1}
	if data[idEnd] == '\r' && idEnd+1 < len(data) && data[idEnd+1] == '\n' {
		starts = append(starts, idEnd+2)
	}

	eiEnd := -1
	for _, start := range starts {
		end, what := inlineDataEnd(data, start, img, inflated)
		if what != "" {
			return start, what
		}
		if l, ok := dict["Length"]; ok {
			if n, isInt := l.(Integer); !isInt || int64(n) != int64(end-start) {
				return start, "inline image length entry disagrees with its data"
			}
		}
		if textShowCandidate(data[start:end]) {
			return start, "inline image data could show text if parsed as content"
		}
		next, what := inlineTerminator(data, start, end)
		if what != "" {
			return end, what
		}
		if eiEnd >= 0 && next != eiEnd {
			return start, "inline image data has two plausible ends"
		}
		eiEnd = next
	}
	return eiEnd, ""
}

// inlineImageDict parses the inline-image dictionary that starts at i (just
// after BI) up to the ID operator, keyed by full names (inlineImageKeys; other
// keys are kept as written for checkInlineDict to refuse). Values are parsed
// strictly: one nested past the parser's depth bound is an error, not a Null.
//
// Returns the dictionary and the offset just past ID; or, on a token error, a
// key that is not a name, a key without a value, an unreadable value, a
// number written as a sign not followed by a digit or '.' (loneSign), a
// repeated key (abbreviated or not) or a missing ID, the offset of the problem
// and a description.
func inlineImageDict(data []byte, i int) (Dict, int, string) {
	dict := Dict{}
	p := &iparser{data: data, pos: i}
	for {
		t, ni, err := nextToken(data, p.pos)
		if err != nil {
			return nil, p.pos, "inline image dictionary has a token error"
		}
		switch {
		case t.kind == tokKeyword && t.kw == "ID":
			return dict, ni, ""
		case t.kind == tokEOF:
			return nil, p.pos, "inline image has no ID"
		case t.kind != tokName:
			return nil, p.pos, "inline image dictionary key is not a name"
		}
		p.pos = ni
		if v, _, err := nextToken(data, p.pos); err == nil && v.kind == tokKeyword && v.kw == "ID" {
			return nil, p.pos, "inline image dictionary key has no value"
		}
		valStart := p.pos
		val, err := p.parseValue()
		if err != nil {
			return nil, p.pos, "inline image dictionary value is unreadable"
		}
		if loneSign(data[valStart:p.pos]) {
			return nil, valStart, "inline image dictionary has a malformed number"
		}
		key := Name(t.name)
		if full, ok := inlineImageKeys[key]; ok {
			key = full
		}
		if _, dup := dict[key]; dup {
			return nil, p.pos, "inline image dictionary repeats a key"
		}
		dict[key] = val
	}
}

// loneSign reports whether the value bytes hold a number token that is a sign
// not followed by a digit or '.', such as "--1" or "- 1": the lexer reads "-"
// as 0 and then -1, while other readers read one number (-1) or none, so a
// /Decode array's length depends on the reader.
func loneSign(value []byte) bool {
	for i := 0; i < len(value); {
		start := skipBlank(value, i)
		t, ni, err := nextToken(value, start)
		if err != nil || t.kind == tokEOF || ni <= start {
			return false // the value parsed, so this is not reached
		}
		if (t.kind == tokInt || t.kind == tokReal) && (value[start] == '+' || value[start] == '-') {
			if start+1 >= ni || !(isDigit(value[start+1]) || value[start+1] == '.') {
				return true
			}
		}
		i = ni
	}
	return false
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// checkInlineDict accepts an inline-image dictionary only if every key is
// allow-listed and every value is one no reader rejects or reads differently:
//   - Width and Height positive integers;
//   - ImageMask and Interpolate booleans; Intent a name;
//   - an image mask with no ColorSpace and BitsPerComponent absent or 1;
//   - otherwise BitsPerComponent 1, 2, 4 or 8 and a ColorSpace
//     inlineComponents accepts for that depth;
//   - Decode, when present, exactly two numbers per component;
//   - Filter absent, AHx, Fl, or AHx then Fl (every element checked: at most
//     one Fl, AHx only first); DecodeParms absent, null, or a dictionary (or an
//     array aligned with Filter of null or such dictionaries) holding at most
//     /Predictor 1 — no predictor;
//   - Length, when present, an integer (compared with the data later).
//
// Returns the image's filters and decoded size; or a description of the loss.
func checkInlineDict(dict Dict, opts *StrictOptions) (inlineImage, string) {
	for k := range dict {
		if _, ok := inlineImageKeys[k]; !ok {
			return inlineImage{}, "inline image dictionary has a key outside the allow-list"
		}
	}
	w, okW := positiveInt(dict["Width"])
	h, okH := positiveInt(dict["Height"])
	if !okW || !okH {
		return inlineImage{}, "inline image width or height is not a positive integer"
	}
	mask, ok := optionalBool(dict, "ImageMask")
	if !ok {
		return inlineImage{}, "inline image mask flag is not a boolean"
	}
	if _, ok := optionalBool(dict, "Interpolate"); !ok {
		return inlineImage{}, "inline image interpolate flag is not a boolean"
	}
	if v, ok := dict["Intent"]; ok && !isName(v) {
		return inlineImage{}, "inline image intent is not a name"
	}
	if v, ok := dict["Length"]; ok {
		if _, isInt := v.(Integer); !isInt {
			return inlineImage{}, "inline image length entry disagrees with its data"
		}
	}

	bpc, n := int64(1), 1
	if mask {
		if v, ok := dict["BitsPerComponent"]; ok {
			if b, isInt := v.(Integer); !isInt || b != 1 {
				return inlineImage{}, "inline image mask has bits per component other than 1"
			}
		}
		if _, ok := dict["ColorSpace"]; ok {
			return inlineImage{}, "inline image mask has a colour space"
		}
	} else {
		b, isInt := dict["BitsPerComponent"].(Integer)
		if !isInt || (b != 1 && b != 2 && b != 4 && b != 8) {
			return inlineImage{}, "inline image bits per component is invalid"
		}
		bpc = int64(b)
		c, ok := inlineComponents(dict["ColorSpace"], bpc, opts)
		if !ok {
			return inlineImage{}, "inline image colour space is missing, unsupported or cannot be resolved"
		}
		n = c
	}
	if v, ok := dict["Decode"]; ok {
		arr, isArr := v.(Array)
		if !isArr || len(arr) != 2*n || !allNumbers(arr) {
			return inlineImage{}, "inline image decode array does not have two numbers per component"
		}
	}
	filters, what := inlineFilterChain(dict["Filter"])
	if what != "" {
		return inlineImage{}, what
	}
	if !noPredictor(dict["DecodeParms"], len(filters)) {
		return inlineImage{}, "inline image decode parameters are not supported"
	}

	// Every factor is bounded before it is multiplied: no overflow, and no
	// allocation sized from untrusted Width × Height.
	if w > maxInlineInflate*8 || h > maxInlineInflate {
		return inlineImage{}, "inline image is too large"
	}
	rowBytes := (w*bpc*int64(n) + 7) / 8 // w ≤ 2^31, bpc ≤ 8, n ≤ 4: no overflow
	if rowBytes > maxInlineInflate/h {
		return inlineImage{}, "inline image is too large"
	}
	return inlineImage{filters: filters, need: rowBytes * h}, ""
}

// positiveInt returns o as an int64 if it is an Integer greater than zero.
func positiveInt(o Object) (int64, bool) {
	v, ok := o.(Integer)
	return int64(v), ok && v > 0
}

// optionalBool returns dict[key] as a bool: false when absent; ok is false if
// the value is not a Boolean.
func optionalBool(dict Dict, key Name) (value, ok bool) {
	v, present := dict[key]
	if !present {
		return false, true
	}
	b, isBool := v.(Boolean)
	return bool(b), isBool
}

// inlineComponents returns the number of colour components of an inline
// image's colour space at bpc bits per component (Step 1a of §6.1):
//   - a device colour space name, or its abbreviation G, RGB or CMYK;
//   - an Indexed array [/I base hival lookup] (or /Indexed) whose base is a
//     device colour space, with 0 ≤ hival ≤ min(255, 2^bpc − 1) and a lookup
//     string of exactly (hival+1) × base components bytes (§8.6.6.3); it has
//     one component;
//   - another name, which opts.InlineColorSpace resolves to a full device
//     colour space name or to an Indexed array over one, written with full
//     names (abbreviations are valid only inline, §8.9.7).
//
// Only the full device names are protected from resource lookup (§8.6.3), so
// an abbreviation — G, RGB, CMYK or I, as the colour space or an Indexed base —
// that the resources also define is refused: a reader that looks the name up
// first counts a different number of components and under-reads the data.
//
// Returns ok false for anything else, including CalGray, CalRGB, Lab,
// Separation, DeviceN, ICCBased and Pattern, a family name on its own, an
// incomplete array, and a name the callback is nil for or cannot resolve.
func inlineComponents(cs Object, bpc int64, opts *StrictOptions) (int, bool) {
	fromResource := false
	if name, ok := cs.(Name); ok {
		if _, device := inlineDeviceComponents[name]; !device {
			if opts == nil || opts.InlineColorSpace == nil {
				return 0, false
			}
			resolved, ok := opts.InlineColorSpace(name)
			if !ok {
				return 0, false
			}
			cs, fromResource = resolved, true
		}
	}
	switch v := cs.(type) {
	case Name:
		n, ok := inlineDeviceComponents[v]
		if !ok || (fromResource && isAbbreviation(v)) || redefined(v, opts) {
			return 0, false
		}
		return n, true
	case Array:
		if len(v) != 4 {
			return 0, false
		}
		family, _ := v[0].(Name)
		if family != "Indexed" && (fromResource || family != "I") {
			return 0, false
		}
		base, _ := v[1].(Name)
		nb, isDevice := inlineDeviceComponents[base]
		if !isDevice || (fromResource && isAbbreviation(base)) || redefined(base, opts) {
			return 0, false
		}
		hival, isInt := v[2].(Integer)
		lookup, isStr := v[3].(String)
		if !isInt || hival < 0 || int64(hival) > min(int64(255), int64(1)<<bpc-1) ||
			!isStr || len(lookup.Value) != (int(hival)+1)*nb {
			return 0, false
		}
		// A literal string turns any end-of-line into one LF (§7.3.4.2); the
		// lexer keeps CR and CR LF as written, so with a CR in the lookup our
		// length and a conforming reader's differ. (An escaped \r is refused
		// too: the decoded bytes cannot tell it apart.)
		if !lookup.Hex && bytes.IndexByte(lookup.Value, '\r') >= 0 {
			return 0, false
		}
		return 1, true
	}
	return 0, false
}

// isAbbreviation reports whether a device colour space name is the inline
// abbreviation G, RGB or CMYK rather than the full name.
func isAbbreviation(n Name) bool { return n == "G" || n == "RGB" || n == "CMYK" }

// redefined reports whether an abbreviation the image uses is also a colour
// space resource in opts' scope (InlineColorSpace resolves it).
func redefined(n Name, opts *StrictOptions) bool {
	if !isAbbreviation(n) || opts == nil || opts.InlineColorSpace == nil {
		return false
	}
	_, ok := opts.InlineColorSpace(n)
	return ok
}

// noPredictor reports whether an inline image's /DecodeParms value (for a
// chain of nFilters filters) selects no predictor: absent, null, a dictionary
// whose only key is /Predictor 1, or an array of nFilters such values (null or
// dictionary). Any predictor, or any other parameter, is refused (Step 1a).
func noPredictor(dp Object, nFilters int) bool {
	plain := func(o Object) bool {
		switch v := o.(type) {
		case nil, Null:
			return true
		case Dict:
			for k, val := range v {
				if p, isInt := val.(Integer); k != "Predictor" || !isInt || p != 1 {
					return false
				}
			}
			return true
		}
		return false
	}
	if arr, ok := dp.(Array); ok {
		if len(arr) != nFilters {
			return false
		}
		for _, el := range arr {
			if !plain(el) {
				return false
			}
		}
		return true
	}
	return plain(dp)
}

// inlineFilterChain returns an inline image's /Filter value (absent, a name,
// or an array of names) as abbreviations, outermost first.
//
// Returns a description of the loss unless the chain is empty, AHx, Fl, or AHx
// then Fl — the chains whose decoded length strict scanning can check.
func inlineFilterChain(v Object) ([]string, string) {
	var names []Object
	switch f := v.(type) {
	case nil:
		return nil, ""
	case Name:
		names = []Object{f}
	case Array:
		names = f
	default:
		return nil, "inline image filter is not a name"
	}
	chain := make([]string, 0, len(names))
	for _, el := range names {
		name, ok := el.(Name)
		if !ok {
			return nil, "inline image filter is not a name"
		}
		abbr, ok := inlineFilters[name]
		if !ok {
			return nil, "inline image filter /" + string(name) + " is not supported"
		}
		chain = append(chain, abbr)
	}
	switch len(chain) {
	case 0, 1:
		return chain, ""
	case 2:
		if chain[0] == "AHx" && chain[1] == "Fl" {
			return chain, ""
		}
	}
	return nil, "inline image filter chain is not supported"
}

// inlineDataEnd computes where the data of img, starting at start, ends: after
// exactly img.need bytes when unfiltered, at the first '>' for ASCIIHex, and
// at the end of the zlib stream (its Adler-32 trailer included) for Flate. The
// data must decode to exactly img.need bytes: a reader that has its samples
// stops reading and parses whatever is left as content.
//
// Returns the end offset; or a description of the loss if the data is
// malformed, truncated, or decodes to a different length.
func inlineDataEnd(data []byte, start int, img inlineImage, inflated *int64) (int, string) {
	if len(img.filters) == 0 {
		if img.need > int64(len(data)-start) {
			return 0, "inline image data runs past the end of the content"
		}
		return start + int(img.need), ""
	}
	if img.filters[0] == "Fl" {
		n, what := zlibExact(data[start:], img.need, inflated)
		if what != "" {
			return 0, what
		}
		return start + n, ""
	}
	// ASCIIHex (§7.4.2): hex digits and white-space up to the first '>'; an
	// odd final digit is followed by an implicit 0.
	end := -1
	digits := 0
	for k := start; k < len(data) && end < 0; k++ {
		switch c := data[k]; {
		case c == '>':
			end = k + 1
		case isHex(c):
			digits++
		case !isWhitespace(c):
			return 0, "inline image ASCIIHex data has an invalid byte"
		}
	}
	if end < 0 {
		return 0, "inline image ASCIIHex data has no end marker"
	}
	decoded := int64(digits+1) / 2
	if len(img.filters) == 1 {
		if decoded != img.need {
			return 0, "inline image data does not decode to its samples' length"
		}
		return end, ""
	}
	hex := make([]byte, 0, decoded) // ≤ half the raw data
	var hi byte
	odd := false
	for _, c := range data[start : end-1] {
		if !isHex(c) {
			continue
		}
		if odd {
			hex = append(hex, hi<<4|hexVal(c))
		} else {
			hi = hexVal(c)
		}
		odd = !odd
	}
	if odd {
		hex = append(hex, hi<<4)
	}
	n, what := zlibExact(hex, img.need, inflated)
	if what != "" {
		return 0, what
	}
	if n != len(hex) {
		return 0, "inline image Flate data does not end with its ASCIIHex data"
	}
	return end, ""
}

// zlibExact inflates the zlib stream at the start of src through a
// bytes.Reader, which implements io.ByteReader so the decompressor reads no
// byte past the stream's end, and requires it to decode to exactly need bytes
// and end with a matching Adler-32 checksum. Raw DEFLATE without the zlib
// header is not accepted. The output is discarded and charged to inflated.
//
// Returns the number of bytes the stream occupies in src; or a description of
// the loss.
func zlibExact(src []byte, need int64, inflated *int64) (int, string) {
	if need > maxInlineInflate-*inflated {
		return 0, "inline image Flate data inflates past the scan's limit"
	}
	br := bytes.NewReader(src)
	zr, err := zlib.NewReader(br)
	if errors.Is(err, zlib.ErrHeader) {
		return 0, "inline image Flate data has no zlib header"
	}
	if err != nil {
		return 0, "inline image Flate data is unreadable"
	}
	defer zr.Close()
	n, err := io.Copy(io.Discard, io.LimitReader(zr, need+1))
	*inflated += n
	if err != nil {
		return 0, "inline image Flate data is corrupt or truncated"
	}
	if n != need {
		return 0, "inline image data does not decode to its samples' length"
	}
	// io.Copy returned at io.EOF, which zlib reports only after the final
	// block and a matching checksum.
	return len(src) - br.Len(), ""
}

// textShowCandidate reports whether raw inline-image data holds bytes that
// would show text if a reader parsed them as content: a "Tj" or "TJ" byte
// pair, or ')' or '>' followed by optional white-space or comments (a comment
// is white-space, ISO 32000-1 §7.2.3) and then ' or " (decision D16). A reader that rejects or under-reads the image
// parses the raw bytes of the content stream, never the decoded samples, so
// the raw bytes are what is checked.
func textShowCandidate(raw []byte) bool {
	if bytes.Contains(raw, []byte("Tj")) || bytes.Contains(raw, []byte("TJ")) {
		return true
	}
	for k, c := range raw {
		if c != ')' && c != '>' {
			continue
		}
		j := skipBlank(raw, k+1)
		if j < len(raw) && (raw[j] == '\'' || raw[j] == '"') {
			return true
		}
	}
	return false
}

// inlineTerminator checks what follows inline-image data [start, end): only
// white-space, then EI, then white-space or the end of the content. The byte
// before EI must be white-space (the last data byte or a separator), or a
// reader that searches for a white-space-preceded EI runs past it; and no "EI"
// may occur inside the data at all (D1-A), or a reader that searches for EI
// stops early.
//
// Returns the offset just past EI, or a description of the loss.
func inlineTerminator(data []byte, start, end int) (int, string) {
	if bytes.Contains(data[start:end], []byte("EI")) {
		return 0, "inline image data contains EI"
	}
	k := end
	for k < len(data) && isWhitespace(data[k]) {
		k++
	}
	if k+1 >= len(data) || data[k] != 'E' || data[k+1] != 'I' {
		return 0, "inline image data is not followed by EI"
	}
	if !isWhitespace(data[k-1]) {
		return 0, "inline image EI is not preceded by white-space"
	}
	if k+2 < len(data) && !isWhitespace(data[k+2]) {
		return 0, "inline image EI is not followed by white-space"
	}
	return k + 2, ""
}
