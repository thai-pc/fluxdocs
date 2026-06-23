package core

import (
	"bytes"
	"fmt"
	"os"
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
// ErrEncryptedDocument if the file is encrypted (decryption is not yet
// supported).
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
// Returns the opened *Document, ErrInvalidPDF on a structural parse failure, or
// ErrEncryptedDocument if the file is encrypted.
func OpenBytes(data []byte) (*Document, error) {
	if headerOffset(data) < 0 {
		return nil, ErrInvalidPDF
	}

	resolver, err := parse.NewResolver(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidPDF, err)
	}

	// Encryption is signalled by /Encrypt in the trailer. We cannot decrypt yet,
	// so report it rather than returning garbled content.
	trailer := resolver.Trailer()
	if _, ok := trailer["Encrypt"]; ok {
		return nil, ErrEncryptedDocument
	}

	pageInfos, err := resolver.Pages()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidPDF, err)
	}

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
	return &Document{
		Title:     title,
		PageCount: len(pages),
		Pages:     pages,
		Metadata:  md,
	}, nil
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
