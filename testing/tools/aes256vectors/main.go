// Command aes256vectors writes the synthetic, unencrypted one-page source
// PDFs that generate.sh encrypts with the qpdf command-line tool
// (docs/design/aes256-decryption.md §11.1 option 1). Each page shows one
// marker line ("AES256-OK-<case>") in non-embedded Helvetica; with -meta the
// catalog also has an XMP /Metadata stream holding the marker, so a
// cleartext-metadata case can be checked. There is no PII (SECURITY.md §7).
// Offsets are computed, never hard-coded.
//
// It is a maintainer-run tool, not a build dependency; generate.sh runs it.
//
// Usage (from the repository root):
//
//	go run ./testing/tools/aes256vectors -marker AES256-OK-case [-meta] -o source.pdf
package main

import (
	"bytes"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
)

func main() {
	marker := flag.String("marker", "", "the text the page shows")
	meta := flag.Bool("meta", false, "add an XMP /Metadata stream holding the marker")
	out := flag.String("o", "", "output PDF")
	flag.Parse()
	if *marker == "" || *out == "" || strings.ContainsAny(*marker, "()\\") {
		log.Fatal("-marker (no parentheses or backslashes) and -o are required")
	}
	content := fmt.Sprintf("BT /F1 18 Tf 20 100 Td (%s) Tj ET", *marker)
	catalog := "<< /Type /Catalog /Pages 2 0 R >>"
	objs := []string{
		"", // catalog, below
		"<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 400 200] >>",
		"<< /Type /Page /Parent 2 0 R /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		fmt.Sprintf("<< /Title (Title %s) /Producer (FluxDocs aes256vectors) >>", *marker),
	}
	if *meta {
		catalog = "<< /Type /Catalog /Pages 2 0 R /Metadata 7 0 R >>"
		xmp := fmt.Sprintf(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?><x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"><rdf:Description rdf:about="" xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>%s-META</dc:title></rdf:Description></rdf:RDF></x:xmpmeta><?xpacket end="r"?>`, *marker)
		objs = append(objs, fmt.Sprintf("<< /Type /Metadata /Subtype /XML /Length %d >>\nstream\n%s\nendstream", len(xmp), xmp))
	}
	objs[0] = catalog

	var b bytes.Buffer
	b.WriteString("%PDF-1.7\n%\xE2\xE3\xCF\xD3\n")
	offs := make([]int, len(objs))
	for i, o := range objs {
		offs[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, off := range offs {
		fmt.Fprintf(&b, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R /Info 6 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xref)
	if err := os.WriteFile(*out, b.Bytes(), 0o644); err != nil {
		log.Fatal(err)
	}
}
