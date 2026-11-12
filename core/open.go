package core

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode/utf16"

	"github.com/thai-pc/fluxdocs/core/parse"
)

// headerScanLimit bounds how far we look for the "%PDF-" header. Some real files
// have junk bytes before it; the spec allows a small offset.
const headerScanLimit = 1024

// OpenDocument opens the PDF file at path, parses its structure, and resolves
// the page tree into a flat array (Document.Pages) so RenderPage need not
// re-walk the tree (see Appendix B).
//
// Returns the opened *Document, or: the underlying os error if the file cannot
// be read; ErrInvalidPDF if the structure cannot be parsed; or
// ErrEncryptedDocument if the file is encrypted and cannot be opened with the
// empty user password (see OpenBytes).
func OpenDocument(path string) (*Document, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	doc, err := OpenBytes(data)
	if err != nil {
		return nil, err
	}
	doc.ID = DocumentID(path)
	return doc, nil
}

// OpenBytes parses an in-memory PDF. It is the WASM/client-friendly entry point
// (no filesystem access), and OpenDocument delegates to it.
//
// A file whose cross-reference data cannot be used — no startxref, a table at
// the wrong offset, offsets that do not reach the catalog and page tree — is
// repaired (docs/design/xref-reconstruction.md §2): first by shifting every
// offset by the bytes before "%PDF-", then by rebuilding the object table
// from a bounded scan of the file for "N G obj" headers. Repair is tried only
// when the strict open fails, so a file that opens from its own
// cross-reference data opens exactly as it would without it. A repaired
// document reports how (RepairReport); it saves only as a full rewrite
// (SaveTo), and SignDocument refuses it.
//
// Returns the opened *Document, ErrInvalidPDF on a structural parse failure
// that repair could not fix either, ErrEncryptedDocument (wrapping the
// reason, which holds no document bytes) if the file is encrypted and the
// empty user password does not open it — an unsupported security handler or
// a document that needs a user password; a repaired file whose encryption
// dictionary survives, or shows signs of one the repair cannot read, is never
// opened as plaintext — or ErrInternal (an
// *InternalError) if parsing panicked — a FluxDocs bug, reported instead of
// crashing the process.
func OpenBytes(data []byte) (_ *Document, err error) {
	defer guard("OpenBytes", -1, &err)
	injectTestPanic("OpenBytes", -1)
	return openBytes(data, true)
}

// openStrict is OpenBytes without repair: a file whose cross-reference data
// cannot be used fails with ErrInvalidPDF. Every internal re-open of
// BuildDocument output uses it — the redaction verifier (xref-reconstruction.md
// §5 rule R.3), Merge, Split, the flatten, form, watermark and PDF/A writers:
// that output is always well-formed, so needing a repair there is a writer
// bug, never a reason to re-scan. A recovered panic names it as the
// operation, not OpenBytes.
func openStrict(data []byte) (_ *Document, err error) {
	defer guard("openStrict", -1, &err)
	return openBytes(data, false)
}

// openBytes is the repair ladder (xref-reconstruction.md §2.2): the strict
// open, then, when repair is set and the strict open failed structurally, the
// header-offset retry and the object scan, each accepted only if it decrypts
// and walks the page tree. ErrEncryptedDocument at any step ends the ladder.
func openBytes(data []byte, repair bool) (*Document, error) {
	h := headerOffset(data)
	if h < 0 {
		return nil, ErrInvalidPDF
	}

	resolver, pageInfos, err := openResolver(parse.NewResolver(data))
	if err == nil {
		return newDocument(resolver, pageInfos, nil), nil
	}
	// No repair after a resource bound (parse.IsLimitError: the xref caps
	// and /Prev chain length, a decode limit, the nesting depth, the page
	// tree's depth and page count, the ObjStm re-decode budget): a hostile or
	// oversized file fails the open (CONVENTIONS.md §4), whatever a shifted
	// table or a scan could make of it.
	var le limitError
	if !repair || errors.Is(err, ErrEncryptedDocument) || errors.As(err, &le) {
		return nil, err
	}
	strictErr := err

	if h > 0 {
		resolver, pageInfos, err = openResolver(parse.NewResolverShifted(data, h))
		if errors.Is(err, ErrEncryptedDocument) || errors.As(err, &le) {
			return nil, err
		}
		if err == nil {
			return newDocument(resolver, pageInfos, strictErr), nil
		}
	}
	resolver, pageInfos, err = openResolver(parse.NewRepairedResolver(data))
	if errors.Is(err, ErrEncryptedDocument) {
		return nil, err
	}
	if err == nil {
		return newDocument(resolver, pageInfos, strictErr), nil
	}
	return nil, fmt.Errorf("%w; repair failed: %s", strictErr, withoutInvalid(err))
}

// limitError marks a rung that failed on a parse resource bound
// (parse.IsLimitError). It wraps the ErrInvalidPDF error unchanged, so its
// message and errors.Is behaviour are those of the error it carries.
type limitError struct{ err error }

func (e limitError) Error() string { return e.err.Error() }
func (e limitError) Unwrap() error { return e.err }

// openResolver finishes one rung of the ladder on a resolver: decryption,
// when the trailer names /Encrypt, then the page-tree walk.
//
// Returns ErrInvalidPDF wrapping a parse failure (newErr included) — inside
// a limitError when the failure is a resource bound — or ErrEncryptedDocument
// wrapping a decryption failure or an object scan that lost the encryption
// dictionary (parse.ErrEncryptionLost: never opened as plaintext).
func openResolver(resolver *parse.Resolver, newErr error) (*parse.Resolver, []parse.PageInfo, error) {
	invalid := func(cause error) error {
		err := fmt.Errorf("%w: %v", ErrInvalidPDF, cause)
		if parse.IsLimitError(cause) {
			return limitError{err}
		}
		return err
	}
	if errors.Is(newErr, parse.ErrEncryptionLost) {
		return nil, nil, fmt.Errorf("%w: %v", ErrEncryptedDocument, newErr)
	}
	if newErr != nil {
		return nil, nil, invalid(newErr)
	}
	// Encryption is signalled by /Encrypt in the trailer. Set up the Standard
	// Security Handler (empty user password — the common openable case) before
	// resolving any content; an unsupported handler or wrong password reports
	// ErrEncryptedDocument rather than returning garbled content.
	if _, ok := resolver.Trailer()["Encrypt"]; ok {
		if err := resolver.EnableDecryption(nil); err != nil {
			// The parse error names the reason (unsupported handler, password
			// mismatch) and never quotes document bytes.
			return nil, nil, fmt.Errorf("%w: %v", ErrEncryptedDocument, err)
		}
	}
	pageInfos, err := resolver.Pages()
	if err != nil {
		return nil, nil, invalid(err)
	}
	return resolver, pageInfos, nil
}

// withoutInvalid returns the message of a repair-rung error without its
// ErrInvalidPDF prefix, so the combined error names ErrInvalidPDF once.
func withoutInvalid(err error) string {
	return strings.TrimPrefix(err.Error(), ErrInvalidPDF.Error()+": ")
}

// newDocument builds the Document for an opened resolver. strictErr, when
// non-nil, is why the strict open failed: the resolver was repaired, and the
// document carries its RepairReport.
func newDocument(resolver *parse.Resolver, pageInfos []parse.PageInfo, strictErr error) *Document {
	pages := make([]Page, len(pageInfos))
	for i, pi := range pageInfos {
		pages[i] = Page{
			ID:       PageID(fmt.Sprintf("page-%d", i)),
			Index:    i,
			Width:    pi.MediaBox.Width(),
			Height:   pi.MediaBox.Height(),
			Rotation: pi.Rotate,
		}
	}

	md, title := readInfo(resolver)
	_, encrypted := resolver.Trailer()["Encrypt"]
	doc := &Document{
		Title:     title,
		PageCount: len(pages),
		Pages:     pages,
		Metadata:  md,
		Encrypted: encrypted,
		resolver:  resolver,
		pageInfos: pageInfos,
	}
	if info, ok := resolver.Repair(); ok && strictErr != nil {
		rep := &RepairReport{
			Method:          RepairObjectScan,
			HeaderOffset:    info.HeaderOffset,
			Objects:         info.Objects,
			Duplicates:      info.Duplicates,
			CatalogFromScan: info.CatalogFromScan,
			Truncated:       info.Truncated,
			StrictError:     elideQuoted(withoutInvalid(strictErr)),
		}
		if !info.Scanned {
			rep.Method = RepairHeaderOffset
		}
		doc.repair = rep
	}
	return doc
}

// elideQuoted replaces the contents of every double-quoted run in msg, and of
// every single-quoted rune literal, with "…": parse errors quote (%q) the
// token or byte they stopped at, which can be document text (an unexpected
// keyword inside a content stream, an invalid character), and a
// RepairReport holds none. A backslash escape inside a run is skipped with
// it, so an escaped quote does not end the run; an unterminated run is
// elided to the end. A single-quoted run is elided only when it is what %q
// prints for a rune — one character, or one backslash escape — so the
// fixed keywords the messages quote ('startxref', 'obj') stay readable.
func elideQuoted(msg string) string {
	var b strings.Builder
	in, esc := false, false
	rs := []rune(msg)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case in && esc:
			esc = false
		case in && r == '\\':
			esc = true
		case r == '"':
			if in {
				b.WriteString("…")
			}
			in = !in
			b.WriteRune(r)
		case !in && r == '\'':
			if n := runeLiteral(rs[i+1:]); n > 0 {
				b.WriteString("'…'")
				i += n
				continue
			}
			b.WriteRune(r)
		case !in:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// runeLiteral returns the length of the rest of a %q rune literal at the
// start of rs (the opening quote already read), closing quote included: one
// character other than a quote or backslash, or a backslash escape (\x, \xhh,
// \uhhhh, \Uhhhhhhhh, \ooo), then a single quote. It returns 0 otherwise.
func runeLiteral(rs []rune) int {
	n := 1
	if len(rs) > 0 && rs[0] == '\\' {
		n = 2
		for n < len(rs) && n < 10 && rs[n] != '\'' {
			n++
		}
	} else if len(rs) == 0 || rs[0] == '\'' {
		return 0
	}
	if n >= len(rs) || rs[n] != '\'' {
		return 0
	}
	return n + 1
}

// headerOffset returns the index of the "%PDF-" header within the first
// headerScanLimit bytes, or -1 if absent.
func headerOffset(data []byte) int {
	end := len(data)
	if end > headerScanLimit {
		end = headerScanLimit
	}
	return bytes.Index(data[:end], []byte("%PDF-"))
}

// readInfo pulls the document information dictionary (trailer /Info) into
// DocumentMetadata and returns the document title separately (Title lives on
// Document, not DocumentMetadata). Missing fields are left empty; this never
// fails the open.
func readInfo(r *parse.Resolver) (DocumentMetadata, string) {
	md := DocumentMetadata{Custom: map[string]string{}}

	info, ok, err := r.ResolveDict(r.Trailer()["Info"])
	if err != nil || !ok {
		return md, ""
	}

	md.Author = infoText(info, "Author")
	md.Subject = infoText(info, "Subject")
	md.Producer = infoText(info, "Producer")
	if kw := infoText(info, "Keywords"); kw != "" {
		md.Keywords = splitKeywords(kw)
	}
	return md, infoText(info, "Title")
}

// infoText reads a string entry from an /Info dictionary, decoding PDF text
// (UTF-16BE with a BOM, otherwise treated as Latin-1/PDFDocEncoded bytes).
func infoText(d parse.Dict, key parse.Name) string {
	s, ok := d[key].(parse.String)
	if !ok {
		return ""
	}
	return decodePDFText(s.Value)
}

// decodePDFText decodes a PDF text string. A leading UTF-16BE byte-order mark
// (0xFE 0xFF) selects UTF-16BE; otherwise bytes are mapped 1:1 as runes
// (Latin-1), which is correct for the ASCII subset covering most metadata.
func decodePDFText(b []byte) string {
	if len(b) >= 2 && b[0] == 0xFE && b[1] == 0xFF {
		u16 := make([]uint16, 0, (len(b)-2)/2)
		for i := 2; i+1 < len(b); i += 2 {
			u16 = append(u16, uint16(b[i])<<8|uint16(b[i+1]))
		}
		return string(utf16.Decode(u16))
	}
	runes := make([]rune, len(b))
	for i, c := range b {
		runes[i] = rune(c)
	}
	return string(runes)
}

// splitKeywords splits a PDF /Keywords string on commas or semicolons, trimming
// surrounding whitespace and dropping empties.
func splitKeywords(s string) []string {
	var out []string
	start := 0
	flush := func(end int) {
		field := trimSpace(s[start:end])
		if field != "" {
			out = append(out, field)
		}
	}
	for i := 0; i < len(s); i++ {
		if s[i] == ',' || s[i] == ';' {
			flush(i)
			start = i + 1
		}
	}
	flush(len(s))
	return out
}

func trimSpace(s string) string {
	i, j := 0, len(s)
	for i < j && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
		i++
	}
	for j > i && (s[j-1] == ' ' || s[j-1] == '\t' || s[j-1] == '\n' || s[j-1] == '\r') {
		j--
	}
	return s[i:j]
}
