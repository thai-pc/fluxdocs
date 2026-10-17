package redact

import (
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// needles.go is the text matching of automatic plan extension and of the
// post-redaction verification (docs/design/redaction-hardening.md §5.6,
// §6.11): a needle is a piece of removed text; a haystack is any text the
// output (or the source, for the pre-pass) still holds. Both are compared on
// their skeleton.

// MinNeedle is the shortest needle searched for, in runes of its skeleton
// (or bytes, for raw code needles): shorter ones match too much by chance
// (§5.6). MinMatchNeedle and MinAreaNeedle are the propagation thresholds of
// decision P9: a matched string propagates from 4 skeleton characters, text
// removed under a RedactArea region from 8.
const (
	MinNeedle      = 4
	MinMatchNeedle = 4
	MinAreaNeedle  = 8
)

// Skeleton returns the form of s that needles are compared in (§6.11): NFKC
// (ligatures such as U+FB01 fold to "fi", compatibility forms to their
// plain letters), then every white-space and default-ignorable code point
// (soft hyphen, zero-width space and joiners, the byte-order mark, variation
// selectors) removed. Case is kept: matching is case-sensitive.
func Skeleton(s string) string {
	ascii := true
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			ascii = false
			break
		}
	}
	if ascii {
		// NFKC leaves ASCII as it is, and no ASCII character is ignorable.
		return strings.Map(func(r rune) rune {
			if unicode.IsSpace(r) {
				return -1
			}
			return r
		}, s)
	}
	s = norm.NFKC.String(s)
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if unicode.IsSpace(r) || ignorable(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// SkeletonLen is the length of a skeleton in runes.
func SkeletonLen(s string) int { return utf8.RuneCountInString(s) }

// ignorable reports whether r is a default-ignorable code point (Unicode
// DerivedCoreProperties Default_Ignorable_Code_Point, approximated by its
// definition: format characters, variation selectors and the other
// default-ignorables; the prepended concatenation marks, which are visible,
// excepted).
func ignorable(r rune) bool {
	switch {
	case r < 0x80:
		return false
	case unicode.Is(unicode.Prepended_Concatenation_Mark, r):
		return false
	case unicode.Is(unicode.Cf, r), unicode.Is(unicode.Variation_Selector, r),
		unicode.Is(unicode.Other_Default_Ignorable_Code_Point, r):
		return true
	}
	return false
}

// Needles is a fixed set of needles, each with a tag bit the caller assigns
// (e.g. which plan source it comes from). Text needles are skeletons and are
// searched in skeletons; raw needles are byte strings searched in raw bytes.
// Every needle has at least MinNeedle bytes, so a needle is found by its
// first four bytes. The zero value is an empty set.
type Needles struct {
	list  []string
	tags  []uint8
	index map[uint32][]int32
	seen  map[string]int32
}

// Add adds a needle with tag bits; one shorter than MinNeedle bytes is
// ignored. Adding a needle again adds its tag bits.
func (n *Needles) Add(needle string, tag uint8) {
	if len(needle) < MinNeedle {
		return
	}
	if n.seen == nil {
		n.seen = map[string]int32{}
		n.index = map[uint32][]int32{}
	}
	if i, ok := n.seen[needle]; ok {
		n.tags[i] |= tag
		return
	}
	i := int32(len(n.list))
	n.seen[needle] = i
	n.list = append(n.list, needle)
	n.tags = append(n.tags, tag)
	k := key4(needle)
	n.index[k] = append(n.index[k], i)
}

// Len is the number of needles.
func (n *Needles) Len() int {
	if n == nil {
		return 0
	}
	return len(n.list)
}

// key4 packs the first four bytes of s.
func key4(s string) uint32 {
	return uint32(s[0])<<24 | uint32(s[1])<<16 | uint32(s[2])<<8 | uint32(s[3])
}

// Find returns the union of the tags of the needles that occur in hay (0
// when none does).
func (n *Needles) Find(hay string) uint8 {
	if n.Len() == 0 || len(hay) < MinNeedle {
		return 0
	}
	var tags uint8
	k := uint32(hay[0])<<16 | uint32(hay[1])<<8 | uint32(hay[2])
	for i := 3; i < len(hay); i++ {
		k = k<<8 | uint32(hay[i])
		cands, ok := n.index[k]
		if !ok {
			continue
		}
		at := hay[i-3:]
		for _, c := range cands {
			if strings.HasPrefix(at, n.list[c]) {
				tags |= n.tags[c]
			}
		}
	}
	return tags
}

// FindText returns the tags of the needles that occur in the skeleton of t.
func (n *Needles) FindText(t string) uint8 {
	if n.Len() == 0 || len(t) < MinNeedle {
		return 0
	}
	if tags, ascii := n.findASCII(t); ascii {
		return tags
	}
	return n.Find(Skeleton(t))
}

// findASCII is FindText for t of ASCII only (the common case), without
// building the skeleton as a string: it is t without ASCII white-space.
// It reports false, having searched nothing, when t is not all ASCII.
func (n *Needles) findASCII(t string) (uint8, bool) {
	var arr [512]byte
	buf := arr[:0]
	for i := 0; i < len(t); i++ {
		c := t[i]
		if c >= 0x80 {
			return 0, false
		}
		switch c {
		case ' ', '\t', '\n', '\v', '\f', '\r':
			continue
		}
		buf = append(buf, c)
	}
	return n.findIn(buf), true
}

// findIn is Find over bytes.
func (n *Needles) findIn(hay []byte) uint8 {
	if len(hay) < MinNeedle {
		return 0
	}
	var tags uint8
	k := uint32(hay[0])<<16 | uint32(hay[1])<<8 | uint32(hay[2])
	for i := 3; i < len(hay); i++ {
		k = k<<8 | uint32(hay[i])
		cands, ok := n.index[k]
		if !ok {
			continue
		}
		at := hay[i-3:]
		for _, c := range cands {
			if nd := n.list[c]; len(at) >= len(nd) && string(at[:len(nd)]) == nd {
				tags |= n.tags[c]
			}
		}
	}
	return tags
}

// FindTextBytes is FindText for UTF-8 text in a byte slice (not kept).
func (n *Needles) FindTextBytes(b []byte) uint8 {
	if n.Len() == 0 || len(b) < MinNeedle {
		return 0
	}
	ascii := true
	for _, c := range b {
		if c >= 0x80 {
			ascii = false
			break
		}
	}
	if !ascii {
		return n.Find(Skeleton(string(b)))
	}
	var arr [512]byte
	buf := arr[:0]
	for _, c := range b {
		switch c {
		case ' ', '\t', '\n', '\v', '\f', '\r':
			continue
		}
		buf = append(buf, c)
	}
	return n.findIn(buf)
}

// FindLatin1 returns the tags of the needles that occur in the skeleton of
// b read as Latin-1.
func (n *Needles) FindLatin1(b []byte) uint8 {
	if n.Len() == 0 || len(b) < MinNeedle {
		return 0
	}
	for _, c := range b {
		if c >= 0x80 {
			return n.Find(Skeleton(latin1(b)))
		}
	}
	tags, _ := n.findASCII(string(b))
	return tags
}

// FindBytes returns the tags of the needles that occur in b read as text
// every way a reader may decode it: as a PDF text string (§7.9.2.2), as
// Latin-1, as UTF-8 when it is valid, and as UTF-16BE at both byte
// alignments (two-byte font codes equal to code points, a missing
// byte-order mark). Each reading is compared on its skeleton; raw needles
// are also searched in b's bytes.
func (n *Needles) FindBytes(b []byte, raw *Needles) uint8 {
	var tags uint8
	if raw.Len() > 0 {
		tags |= raw.Find(string(b))
	}
	if n.Len() == 0 || len(b) < MinNeedle {
		return tags
	}
	tags |= n.FindText(textString(b))
	tags |= n.FindText(latin1(b))
	if utf8.Valid(b) {
		tags |= n.FindText(string(b))
	}
	for off := 0; off < 2 && off+1 < len(b); off++ {
		tags |= n.FindText(utf16BE(b[off:]))
	}
	return tags
}

// latin1 decodes b as ISO 8859-1.
func latin1(b []byte) string {
	var s strings.Builder
	s.Grow(len(b))
	for _, c := range b {
		s.WriteRune(rune(c))
	}
	return s.String()
}

// utf16BE decodes b as UTF-16BE (an odd last byte is ignored).
func utf16BE(b []byte) string {
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		u = append(u, uint16(b[i])<<8|uint16(b[i+1]))
	}
	return string(utf16.Decode(u))
}
