package parse

// writer.go serializes the parse object model back into a conforming PDF byte
// stream: the exact inverse of the read layer (lexer.go, indirect.go, xref.go).
// It emits a plain PDF-1.7 file with a classic cross-reference table (ISO
// 32000-1:2008 §7.5), never an xref stream or object stream. See
// docs/design/pdf-writer-merge-split.md.
//
// The serializers here deliberately do NOT reuse the Object.String() debug
// methods in object.go: those are for logging and are not valid PDF syntax
// (e.g. Real prints via %g, which can emit scientific notation).

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
)

// realDigits is the fixed fractional-digit budget for Real numbers the writer
// emits itself (MediaBox, materialized geometry). Six digits round-trips page
// geometry comfortably; content-stream numbers are copied verbatim inside Raw
// and never pass through here (design O1).
const realDigits = 6

// Writer assigns fresh, contiguous object numbers (starting at 1, all
// generation 0) and serializes them into a complete PDF file. A Writer never
// reuses a number, so every emitted object is generation 0.
//
// Typical use: Add / Reserve+Fill objects, SetRoot (and optionally SetInfo),
// then WriteTo or Bytes. A Writer is not safe for concurrent use.
type Writer struct {
	objects  []Object   // objects[i] is indirect object number i+1
	root     *Reference // /Root for the trailer (required before emit)
	info     *Reference // /Info for the trailer (optional)
	id       []byte     // trailer /ID value (both entries), optional
	outBytes int64      // running tally of stream bytes queued, for the DoS cap
}

// SetID sets the trailer /ID (both array entries to the same value). PDF/A
// requires a file identifier. id should be 16 bytes.
func (w *Writer) SetID(id []byte) { w.id = append([]byte(nil), id...) }

// NewWriter returns an empty Writer whose next object number is 1.
func NewWriter() *Writer { return &Writer{} }

// Add appends obj as a new indirect object and returns its Reference
// (generation 0). Add does not rewrite references inside obj; the caller must
// hand in an object whose internal references already point at writer numbers.
func (w *Writer) Add(obj Object) Reference {
	w.objects = append(w.objects, obj)
	return Reference{Number: len(w.objects), Generation: 0}
}

// Reserve claims the next object number without a body yet, so a caller can
// splice its Reference into other objects (e.g. a page's /Parent, or a cycle)
// before the body is known. Fill the slot later with Fill.
func (w *Writer) Reserve() Reference {
	w.objects = append(w.objects, Null{})
	return Reference{Number: len(w.objects), Generation: 0}
}

// Fill stores obj into a slot previously claimed by Reserve. A ref outside the
// allocated range is ignored (defensive; never panics).
func (w *Writer) Fill(ref Reference, obj Object) {
	if ref.Number >= 1 && ref.Number <= len(w.objects) {
		w.objects[ref.Number-1] = obj
	}
}

// SetRoot records which object is the document catalog (/Root). Required before
// WriteTo/Bytes.
func (w *Writer) SetRoot(ref Reference) { r := ref; w.root = &r }

// SetInfo records the optional document information dictionary (/Info).
func (w *Writer) SetInfo(ref Reference) { r := ref; w.info = &r }

// count returns the number of objects allocated so far (used by the copier to
// enforce the object cap).
func (w *Writer) count() int { return len(w.objects) }

// WriteTo serializes the whole file and writes it to dst, satisfying
// io.WriterTo. Returns the number of bytes written and a wrapped errLex if
// /Root was never set, or the writer error from dst. Never panics.
func (w *Writer) WriteTo(dst io.Writer) (int64, error) {
	buf, err := w.Bytes()
	if err != nil {
		return 0, err
	}
	n, err := dst.Write(buf)
	return int64(n), err
}

// Bytes serializes the whole file and returns it. Returns a wrapped errLex if
// /Root was never set.
func (w *Writer) Bytes() ([]byte, error) {
	if w.root == nil {
		return nil, fmt.Errorf("%w: writer has no /Root set", errLex)
	}

	var buf bytes.Buffer

	// Header (§7.5.2): version line + a binary-marker comment (four bytes >= 128)
	// so tools treat the file as binary. LF throughout.
	buf.WriteString("%PDF-1.7\n")
	buf.Write([]byte{'%', 0xE2, 0xE3, 0xCF, 0xD3, '\n'})

	// Body (§7.5.3): record each object's byte offset, then emit it.
	offsets := make([]int, len(w.objects))
	for i, obj := range w.objects {
		offsets[i] = buf.Len()
		buf.WriteString(strconv.Itoa(i + 1))
		buf.WriteString(" 0 obj\n")
		serializeObject(&buf, obj)
		buf.WriteString("\nendobj\n")
	}

	// Classic cross-reference table (§7.5.4): a single subsection 0..N, with
	// object 0 as the free-list head. Each entry line is exactly 20 bytes.
	xrefStart := buf.Len()
	n := len(w.objects)
	buf.WriteString("xref\n")
	buf.WriteString("0 ")
	buf.WriteString(strconv.Itoa(n + 1))
	buf.WriteByte('\n')
	buf.WriteString("0000000000 65535 f \n")
	for _, off := range offsets {
		writeXrefEntry(&buf, off)
	}

	// Trailer + startxref + EOF (§7.5.5). No /Encrypt, no /Prev.
	buf.WriteString("trailer\n")
	tr := Dict{
		"Size": Integer(int64(n + 1)),
		"Root": *w.root,
	}
	if w.info != nil {
		tr["Info"] = *w.info
	}
	if w.id != nil {
		idStr := String{Value: w.id, Hex: true}
		tr["ID"] = Array{idStr, idStr}
	}
	serializeObject(&buf, tr)
	buf.WriteString("\nstartxref\n")
	buf.WriteString(strconv.Itoa(xrefStart))
	buf.WriteString("\n%%EOF\n")

	return buf.Bytes(), nil
}

// writeXrefEntry writes one in-use xref entry, exactly 20 bytes:
// "<10-digit offset> 00000 n \n" (§7.5.4 fixed-width rule).
func writeXrefEntry(buf *bytes.Buffer, off int) {
	var line [20]byte
	// 10-digit zero-padded offset.
	for i := 9; i >= 0; i-- {
		line[i] = byte('0' + off%10)
		off /= 10
	}
	line[10] = ' '
	line[11], line[12], line[13], line[14], line[15] = '0', '0', '0', '0', '0'
	line[16] = ' '
	line[17] = 'n'
	line[18] = ' '
	line[19] = '\n'
	buf.Write(line[:])
}

// SerializeObject returns obj as conforming PDF syntax (ISO §7.3), with any
// indirect references emitted verbatim (n g R) rather than renumbered. It is
// used by the incremental-update signer to re-emit an existing object (e.g. an
// updated catalog) without disturbing the rest of the file.
func SerializeObject(obj Object) []byte {
	var buf bytes.Buffer
	serializeObject(&buf, obj)
	return buf.Bytes()
}

// serializeObject writes obj as conforming PDF syntax (ISO §7.3). It handles
// every Object variant plus a nil interface (defensively emitted as null).
func serializeObject(buf *bytes.Buffer, obj Object) {
	switch v := obj.(type) {
	case nil:
		buf.WriteString("null")
	case Null:
		buf.WriteString("null")
	case Boolean:
		if v {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case Integer:
		buf.WriteString(strconv.FormatInt(int64(v), 10))
	case Real:
		buf.WriteString(formatReal(float64(v)))
	case String:
		serializeString(buf, v)
	case Name:
		serializeName(buf, v)
	case Array:
		buf.WriteByte('[')
		for i, e := range v {
			if i > 0 {
				buf.WriteByte(' ')
			}
			serializeObject(buf, e)
		}
		buf.WriteByte(']')
	case Dict:
		serializeDict(buf, v)
	case Reference:
		buf.WriteString(strconv.Itoa(v.Number))
		buf.WriteByte(' ')
		buf.WriteString(strconv.Itoa(v.Generation))
		buf.WriteString(" R")
	case *Stream:
		serializeStream(buf, v)
	default:
		buf.WriteString("null") // unreachable for the closed Object set
	}
}

// formatReal renders a PDF real number: fixed-point (never scientific), with a
// bounded fractional-digit budget and trailing zeros/point trimmed. Non-finite
// values degrade to "0" (never panic; NaN/Inf must not reach a PDF number).
func formatReal(f float64) string {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return "0"
	}
	s := strconv.FormatFloat(f, 'f', realDigits, 64)
	if strings.IndexByte(s, '.') >= 0 {
		s = strings.TrimRight(s, "0")
		s = strings.TrimRight(s, ".")
	}
	if s == "" || s == "-0" {
		return "0"
	}
	return s
}

// serializeName writes a PDF name (§7.3.5): '/' then the stored value, with
// every non-regular byte (whitespace, delimiter, '#', or byte < 0x21 / > 0x7e)
// re-escaped as #xx (two uppercase hex digits). The stored Name has #xx already
// decoded, so a '#' in the value re-escapes to #23.
func serializeName(buf *bytes.Buffer, n Name) {
	const hexd = "0123456789ABCDEF"
	buf.WriteByte('/')
	for i := 0; i < len(n); i++ {
		c := n[i]
		if c < 0x21 || c > 0x7e || c == '#' || isDelimiter(c) {
			buf.WriteByte('#')
			buf.WriteByte(hexd[c>>4])
			buf.WriteByte(hexd[c&0x0f])
		} else {
			buf.WriteByte(c)
		}
	}
}

// serializeString writes a PDF string (§7.3.4), honoring String.Hex so a
// round-trip stays faithful. Literal form escapes '(', ')', and '\\'; hex form
// emits each byte as two uppercase hex digits.
func serializeString(buf *bytes.Buffer, s String) {
	const hexd = "0123456789ABCDEF"
	if s.Hex {
		buf.WriteByte('<')
		for _, b := range s.Value {
			buf.WriteByte(hexd[b>>4])
			buf.WriteByte(hexd[b&0x0f])
		}
		buf.WriteByte('>')
		return
	}
	buf.WriteByte('(')
	for _, b := range s.Value {
		switch b {
		case '(', ')', '\\':
			buf.WriteByte('\\')
			buf.WriteByte(b)
		default:
			buf.WriteByte(b)
		}
	}
	buf.WriteByte(')')
}

// serializeDict writes a dictionary (§7.3.7) with keys in sorted order so output
// is deterministic (Go map iteration is random; golden files depend on this).
func serializeDict(buf *bytes.Buffer, d Dict) {
	keys := make([]string, 0, len(d))
	for k := range d {
		keys = append(keys, string(k))
	}
	sort.Strings(keys)

	buf.WriteString("<<")
	for _, k := range keys {
		buf.WriteByte(' ')
		serializeName(buf, Name(k))
		buf.WriteByte(' ')
		serializeObject(buf, d[Name(k)])
	}
	buf.WriteString(" >>")
}

// serializeStream writes a stream object (§7.3.8): its dictionary with /Length
// authoritatively rewritten to the true Raw length (a direct Integer, never an
// indirect reference), then the raw bytes verbatim between stream/endstream.
// Filters and /DecodeParms are copied through untouched — the writer never
// decodes or re-encodes stream data, so it is binary-safe.
func serializeStream(buf *bytes.Buffer, s *Stream) {
	d := make(Dict, len(s.Dict)+1)
	for k, v := range s.Dict {
		if k == "Length" {
			continue // rewritten below
		}
		d[k] = v
	}
	d["Length"] = Integer(int64(len(s.Raw)))

	serializeDict(buf, d)
	buf.WriteString("\nstream\n")
	buf.Write(s.Raw)
	buf.WriteString("\nendstream")
}
