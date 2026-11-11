package psfont

import (
	"bytes"
	"encoding/binary"
	"strconv"

	"github.com/thai-pc/fluxdocs/core/internal/std14"
)

// Type 1 encryption constants (Type 1 Font Format Ch. 7): the cipher's two
// multiplier constants and the initial keys of eexec and charstring
// encryption.
const (
	cryptC1         = 52845
	cryptC2         = 22719
	eexecKey        = 55665
	charstringKey   = 4330
	eexecDiscard    = 4  // random plaintext bytes at the start of the eexec part
	defaultLenIV    = 4  // charstring bytes to discard when /lenIV is absent
	zeroRunTrailer  = 64 // ASCII zeros that mark the fixed-content trailer
	pfbHeaderLength = 6  // 0x80, type, 4-byte little-endian length
)

// decrypt runs the Type 1 cipher over src with initial key r (Ch. 7):
// plain = cipher ^ (r >> 8); r = (cipher + r) · c1 + c2 mod 2^16. It writes
// len(src) bytes to dst, which must be at least that long.
func decrypt(dst, src []byte, r uint16) {
	for i, c := range src {
		dst[i] = c ^ byte(r>>8)
		r = (uint16(c)+r)*cryptC1 + cryptC2
	}
}

// ParseType1 parses a Type 1 font program (an embedded /FontFile, §9.9),
// leniently: PFB segment headers are stripped, the cleartext ends at the
// eexec token whatever /Length1 says, and the encrypted part may be binary or
// hex and runs to the end or to the trailer of zeros. /Length1–3 are not
// needed.
//
// Returns errLimit for a program past maxProgramBytes or a refused charge,
// errMalformed when no eexec part or no /CharStrings can be found or /lenIV
// is out of range.
func ParseType1(data []byte, opts Options) (*Font, error) {
	if len(data) > maxProgramBytes {
		return nil, errLimit
	}
	if len(data) >= 2 && data[0] == 0x80 && data[1] == 0x01 {
		var err error
		if data, err = stripPFB(data, opts); err != nil {
			return nil, err
		}
	}
	clear, enc, ok := splitEexec(data)
	if !ok {
		return nil, errMalformed
	}
	if isHexStart(enc) {
		if !opts.charge(len(enc) / 2) {
			return nil, errLimit
		}
		enc = decodeHex(enc)
	}
	if len(enc) < eexecDiscard {
		return nil, errMalformed
	}
	if !opts.charge(len(enc)) {
		return nil, errLimit
	}
	private := make([]byte, len(enc))
	decrypt(private, enc, eexecKey)
	private = private[eexecDiscard:]

	f := &Font{matrix: defaultMatrix}
	f.parseCleartext(clear)
	if err := f.parsePrivate(private, opts); err != nil {
		return nil, err
	}
	return f, nil
}

// stripPFB concatenates the ASCII and binary segments of a PFB file (TN
// #5040): each is 0x80, a type byte (1 ASCII, 2 binary, 3 end of file) and a
// 4-byte little-endian length. A length past the data is clamped to what
// remains; anything that is not a segment header ends the file.
func stripPFB(data []byte, opts Options) ([]byte, error) {
	if !opts.charge(len(data)) {
		return nil, errLimit
	}
	out := make([]byte, 0, len(data))
	for i := 0; i+pfbHeaderLength <= len(data); {
		if data[i] != 0x80 || (data[i+1] != 1 && data[i+1] != 2) {
			break
		}
		n := int64(binary.LittleEndian.Uint32(data[i+2 : i+6]))
		start := int64(i + pfbHeaderLength)
		end := min(start+n, int64(len(data)))
		out = append(out, data[start:end]...)
		i = int(end)
	}
	return out, nil
}

// splitEexec splits a program at its eexec token: the cleartext before it and
// the encrypted part after it and the white space that follows, up to a run of
// zeroRunTrailer ASCII zeros (the fixed-content part) or the end.
func splitEexec(data []byte) (clear, enc []byte, ok bool) {
	from := 0
	for {
		i := bytes.Index(data[from:], []byte("eexec"))
		if i < 0 {
			return nil, nil, false
		}
		i += from
		end := i + len("eexec")
		if (i == 0 || isSpace(data[i-1])) && (end == len(data) || isSpace(data[end])) {
			clear = data[:i]
			j := end
			for j < len(data) && isSpace(data[j]) {
				j++
			}
			enc = data[j:]
			if k := bytes.Index(enc, bytes.Repeat([]byte{'0'}, zeroRunTrailer)); k >= 0 {
				enc = enc[:k]
			}
			return clear, enc, true
		}
		from = end
	}
}

// isHexStart reports whether the encrypted part is hex: its first four bytes
// are hex digits (Type 1 Font Format Ch. 7).
func isHexStart(b []byte) bool {
	if len(b) < 4 {
		return false
	}
	for _, c := range b[:4] {
		if hexValue(c) < 0 {
			return false
		}
	}
	return true
}

// decodeHex decodes hex digits, skipping white space and stopping at any other
// byte; a trailing odd nibble is dropped.
func decodeHex(b []byte) []byte {
	out := make([]byte, 0, len(b)/2)
	hi := -1
	for _, c := range b {
		if isSpace(c) {
			continue
		}
		v := hexValue(c)
		if v < 0 {
			break
		}
		if hi < 0 {
			hi = v
			continue
		}
		out = append(out, byte(hi<<4|v))
		hi = -1
	}
	return out
}

func hexValue(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}

// isSpace reports PostScript white space (PLRM §3.2.2, as in PDF §7.2.2).
func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == '\f' || c == 0
}

// isDelim reports a PostScript delimiter.
func isDelim(c byte) bool {
	switch c {
	case '(', ')', '<', '>', '[', ']', '{', '}', '/', '%':
		return true
	}
	return false
}

// scanner reads PostScript tokens from a buffer. It is a token scan, not an
// interpreter: names, numbers and keywords come back as their text, with a
// literal name keeping its slash; strings and comments are skipped whole. A
// token is a subslice of the buffer, not a copy, so a long run of
// non-delimiters costs nothing beyond the scan; only the names a font keeps
// (glyph and encoding names) are copied to strings.
type scanner struct {
	b   []byte
	pos int
}

// next returns the next token, or nil at the end.
func (s *scanner) next() []byte {
	for s.pos < len(s.b) {
		c := s.b[s.pos]
		switch {
		case isSpace(c):
			s.pos++
		case c == '%':
			for s.pos < len(s.b) && s.b[s.pos] != '\n' && s.b[s.pos] != '\r' {
				s.pos++
			}
		case c == '(':
			s.skipString()
		case c == '[' || c == ']' || c == '{' || c == '}':
			s.pos++
			return s.b[s.pos-1 : s.pos]
		case c == '<' || c == '>':
			s.pos++ // a hex string or dictionary bracket: skipped as punctuation
		default:
			start := s.pos
			s.pos++ // a leading '/' or ')' belongs to this token
			for s.pos < len(s.b) && !isSpace(s.b[s.pos]) && !isDelim(s.b[s.pos]) {
				s.pos++
			}
			return s.b[start:s.pos]
		}
	}
	return nil
}

// skipString skips a parenthesized string, with nesting and backslash escapes.
func (s *scanner) skipString() {
	depth := 0
	for ; s.pos < len(s.b); s.pos++ {
		switch s.b[s.pos] {
		case '\\':
			s.pos++
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				s.pos++
				return
			}
		}
	}
}

// maxNumberToken bounds the length of a token parsed as a number: longer
// tokens are not numbers the parser can use, and are not copied.
const maxNumberToken = 64

// integer parses a token as a decimal integer.
func integer(tok []byte) (int, bool) {
	if len(tok) > maxNumberToken {
		return 0, false
	}
	v, err := strconv.Atoi(string(tok))
	return v, err == nil
}

// parseCleartext reads /FontMatrix and /Encoding from the cleartext part.
func (f *Font) parseCleartext(b []byte) {
	s := &scanner{b: b}
	for tok := s.next(); tok != nil; tok = s.next() {
		switch string(tok) {
		case "/FontMatrix":
			f.readMatrix(s)
		case "/Encoding":
			f.readEncoding(s)
		}
	}
}

// readMatrix reads "[a b c d e f]" (or the same in braces). A matrix that is
// not six numbers, or fails validMatrix, leaves the default.
func (f *Font) readMatrix(s *scanner) {
	open := s.next()
	if string(open) != "[" && string(open) != "{" {
		return
	}
	var m [6]float64
	for i := 0; ; i++ {
		tok := s.next()
		if string(tok) == "]" || string(tok) == "}" {
			if i == 6 && validMatrix(m) {
				f.matrix = m
			}
			return
		}
		if len(tok) > maxNumberToken || i >= 6 {
			return
		}
		v, err := strconv.ParseFloat(string(tok), 64)
		if err != nil {
			return
		}
		m[i] = v
	}
}

// readEncoding reads /Encoding: StandardEncoding, or an array filled by
// "dup <code> /<name> put" entries up to the "def" that ends the definition
// (codes outside 0–255 are ignored; at most maxEncodingPuts entries). An
// array is the program's built-in encoding even when no entry is put (every
// code .notdef).
func (f *Font) readEncoding(s *scanner) {
	tok := s.next()
	if string(tok) == "StandardEncoding" {
		f.encoding = std14.StandardEncoding()
		f.hasEncoding = true
		return
	}
	puts, depth := 0, 0
	var window [3][]byte // the three tokens before the current one
	for ; tok != nil; tok = s.next() {
		switch string(tok) {
		case "{":
			depth++
		case "}":
			depth--
		case "array":
			if depth <= 0 {
				f.hasEncoding = true
			}
		case "def", "readonly":
			if depth <= 0 {
				return
			}
		case "put":
			if string(window[0]) == "dup" && len(window[2]) > 1 && window[2][0] == '/' {
				if code, ok := integer(window[1]); ok && code >= 0 && code <= 255 {
					f.encoding[code] = string(window[2][1:])
					f.hasEncoding = true
				}
				if puts++; puts >= maxEncodingPuts {
					return
				}
			}
		}
		window = [3][]byte{window[1], window[2], tok}
	}
}

// parsePrivate reads /lenIV, /Subrs and the first non-empty /CharStrings from
// the decrypted private part and decrypts every charstring.
func (f *Font) parsePrivate(b []byte, opts Options) error {
	lenIV := defaultLenIV
	s := &scanner{b: b}
	var subrs [][]byte
	haveSubrs := false
	var names []string
	var css [][]byte
	byName := map[string]GID{}
	total := 0
	for tok := s.next(); tok != nil; tok = s.next() {
		switch string(tok) {
		case "/lenIV":
			v, ok := integer(s.next())
			if !ok || v < -1 || v > maxLenIV {
				return errMalformed
			}
			lenIV = v
		case "/Subrs":
			if len(css) > 0 || haveSubrs {
				// A /Subrs after /CharStrings (e.g. in a nested dict), or after
				// one that parsed, is ignored: rereading repeated arrays would
				// cost each one's allocation again.
				break
			}
			subrs, haveSubrs = readSubrs(s)
		case "/CharStrings":
			if len(css) > 0 {
				break // only the first non-empty /CharStrings counts: byName indexes it
			}
			names, css = readCharStrings(s, byName)
		}
	}
	if len(css) == 0 {
		return errMalformed
	}
	for _, c := range subrs {
		total += len(c)
	}
	for _, c := range css {
		total += len(c)
	}
	if !opts.charge(total) {
		return errLimit
	}
	buf := make([]byte, 0, total)
	dec := func(c []byte) []byte {
		if c == nil {
			return nil
		}
		start := len(buf)
		buf = buf[:start+len(c)]
		out := buf[start:]
		if lenIV < 0 {
			copy(out, c)
			return out
		}
		decrypt(out, c, charstringKey)
		if len(out) < lenIV {
			return out[:0] // shorter than lenIV: an empty glyph
		}
		return out[lenIV:]
	}
	f.subrs = make([][]byte, len(subrs))
	for i, c := range subrs {
		f.subrs[i] = dec(c)
	}
	f.charstrings = make([][]byte, len(css))
	for i, c := range css {
		f.charstrings[i] = dec(c)
	}
	f.names = names
	f.byName = byName
	return nil
}

// binaryEntry reads "<len> <RD> <len bytes>" after the tokens before it: the
// length, the RD token (any spelling: RD, -|, or a font's own name), exactly
// one space, then the bytes. Returns false when the entry does not fit.
func binaryEntry(s *scanner) ([]byte, bool) {
	n, ok := integer(s.next())
	if !ok || n < 0 {
		return nil, false
	}
	if rd := s.next(); rd == nil {
		return nil, false
	}
	start := s.pos + 1 // the single space after RD
	// n is compared with the bytes left, never added to start first: a
	// length near MaxInt would overflow the sum.
	if start > len(s.b) || n > len(s.b)-start {
		return nil, false
	}
	end := start + n
	s.pos = end
	if n > maxCharstringLen {
		return []byte{}, true // too long: kept as an empty, undrawable entry
	}
	return s.b[start:end], true
}

// skipNoise reports the tokens that end a /Subrs or /CharStrings entry
// (NP, ND and their spellings) or separate them.
func skipNoise(tok []byte) bool {
	switch string(tok) {
	case "NP", "|", "ND", "|-", "noaccess", "put", "def", "readonly", "executeonly":
		return true
	}
	return false
}

// readSubrs reads "<n> array" and then "dup <i> <len> RD <bytes> NP" entries.
// Indices at or past n (the declared size) or maxSubrs are dropped, so the
// table never grows past the array the program declared; the scan stops at
// the first token that is not part of an entry. ok is false when there is no
// "<n> array" header.
func readSubrs(s *scanner) (subrs [][]byte, ok bool) {
	n, ok := integer(s.next())
	if !ok || n < 0 {
		return nil, false
	}
	if string(s.next()) != "array" {
		return nil, false
	}
	slots := min(n, maxSubrs)
	for {
		save := s.pos
		tok := s.next()
		if skipNoise(tok) {
			continue
		}
		if string(tok) != "dup" {
			s.pos = save
			return subrs, true
		}
		i, ok := integer(s.next())
		if !ok {
			s.pos = save
			return subrs, true
		}
		data, ok := binaryEntry(s)
		if !ok {
			return subrs, true
		}
		if i < 0 || i >= slots {
			continue
		}
		if i >= len(subrs) {
			subrs = append(subrs, make([][]byte, i+1-len(subrs))...)
		}
		subrs[i] = data
	}
}

// readCharStrings reads "<n> dict dup begin" and then "/<name> <len> RD
// <bytes> ND" entries up to "end", at most maxCharStrings. A duplicate name
// keeps its first entry.
func readCharStrings(s *scanner, byName map[string]GID) ([]string, [][]byte) {
	var names []string
	var css [][]byte
	for i := 0; i < 4; i++ { // "<n> dict dup begin"
		save := s.pos
		tok := s.next()
		if len(tok) > 0 && tok[0] == '/' {
			s.pos = save
			break
		}
		if string(tok) == "begin" {
			break
		}
	}
	for {
		tok := s.next()
		switch {
		case tok == nil || string(tok) == "end":
			return names, css
		case skipNoise(tok):
			continue
		case len(tok) > 1 && tok[0] == '/':
			data, ok := binaryEntry(s)
			if !ok {
				return names, css
			}
			if _, dup := byName[string(tok[1:])]; dup {
				continue
			}
			if len(css) >= maxCharStrings {
				return names, css
			}
			name := string(tok[1:])
			byName[name] = GID(len(css))
			names = append(names, name)
			css = append(css, data)
		default:
			return names, css
		}
	}
}
