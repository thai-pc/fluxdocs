// Package pdfa builds the structural pieces of a PDF/A-1b conformant file: the
// XMP metadata identifying the conformance level and an embedded sRGB ICC output
// profile. It is pure Go and dependency-free.
//
// SCOPE (honest): this adds the PDF/A-1b *structural* markers — XMP metadata
// (pdfaid:part/conformance), an OutputIntent with an embedded ICC profile, and a
// file identifier. It does NOT embed fonts, flatten transparency, or perform the
// full colour conversion a certified PDF/A converter does, and its output has
// not been checked against an external validator (e.g. veraPDF). Treat it as
// PDF/A-1b scaffolding, not a certified conversion. Full conformance is a
// follow-up.
package pdfa

import (
	"crypto/rand"
	"fmt"
	"html"
	"strings"
	"time"
)

// Meta carries the document metadata written into the XMP packet.
type Meta struct {
	Title    string
	Author   string
	Producer string
	Created  time.Time
}

// XMPPacket builds an XMP metadata packet declaring PDF/A-1b conformance and the
// given document metadata (Dublin Core + PDF + XMP Basic schemas). The packet is
// plain UTF-8 XML, as PDF/A requires the metadata stream to be uncompressed and
// readable.
func XMPPacket(m Meta) []byte {
	created := m.Created
	if created.IsZero() {
		created = time.Now()
	}
	ts := created.UTC().Format("2006-01-02T15:04:05Z")

	var b strings.Builder
	b.WriteString("<?xpacket begin=\"" + string(rune(0xFEFF)) + "\" id=\"W5M0MpCehiHzreSzNTczkc9d\"?>\n")
	b.WriteString(`<x:xmpmeta xmlns:x="adobe:ns:meta/">` + "\n")
	b.WriteString(` <rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` + "\n")

	b.WriteString(`  <rdf:Description rdf:about="" xmlns:pdfaid="http://www.aiim.org/pdfa/ns/id/">` + "\n")
	b.WriteString(`   <pdfaid:part>1</pdfaid:part>` + "\n")
	b.WriteString(`   <pdfaid:conformance>B</pdfaid:conformance>` + "\n")
	b.WriteString(`  </rdf:Description>` + "\n")

	b.WriteString(`  <rdf:Description rdf:about="" xmlns:dc="http://purl.org/dc/elements/1.1/">` + "\n")
	if m.Title != "" {
		b.WriteString(`   <dc:title><rdf:Alt><rdf:li xml:lang="x-default">` +
			html.EscapeString(m.Title) + `</rdf:li></rdf:Alt></dc:title>` + "\n")
	}
	if m.Author != "" {
		b.WriteString(`   <dc:creator><rdf:Seq><rdf:li>` +
			html.EscapeString(m.Author) + `</rdf:li></rdf:Seq></dc:creator>` + "\n")
	}
	b.WriteString(`  </rdf:Description>` + "\n")

	producer := m.Producer
	if producer == "" {
		producer = "FluxDocs"
	}
	b.WriteString(`  <rdf:Description rdf:about="" xmlns:pdf="http://ns.adobe.com/pdf/1.3/">` + "\n")
	b.WriteString(`   <pdf:Producer>` + html.EscapeString(producer) + `</pdf:Producer>` + "\n")
	b.WriteString(`  </rdf:Description>` + "\n")

	b.WriteString(`  <rdf:Description rdf:about="" xmlns:xmp="http://ns.adobe.com/xap/1.0/">` + "\n")
	b.WriteString(`   <xmp:CreateDate>` + ts + `</xmp:CreateDate>` + "\n")
	b.WriteString(`   <xmp:CreatorTool>FluxDocs</xmp:CreatorTool>` + "\n")
	b.WriteString(`  </rdf:Description>` + "\n")

	b.WriteString(` </rdf:RDF>` + "\n")
	b.WriteString(`</x:xmpmeta>` + "\n")
	// Padding whitespace (recommended) then the closing packet marker.
	b.WriteString(strings.Repeat(" ", 200) + "\n")
	b.WriteString(`<?xpacket end="w"?>`)
	return []byte(b.String())
}

// NewID returns a random 16-byte file identifier for the trailer /ID.
func NewID() []byte {
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		// Fall back to a time-derived id; uniqueness is best-effort.
		for i := range id {
			id[i] = byte(time.Now().UnixNano() >> (i % 8 * 8))
		}
	}
	return id
}

// Report is the result of validating a document's PDF/A-1b structural markers.
type Report struct {
	Conformant bool
	Checks     []Check
}

// Check is one validation rule and whether it passed.
type Check struct {
	Rule string
	Pass bool
	Note string
}

func (r *Report) add(rule string, pass bool, note string) {
	r.Checks = append(r.Checks, Check{Rule: rule, Pass: pass, Note: note})
}

// Summarize sets Conformant to true iff every check passed and returns a
// human-readable string.
func (r *Report) Summarize() string {
	r.Conformant = true
	var b strings.Builder
	for _, c := range r.Checks {
		mark := "PASS"
		if !c.Pass {
			mark = "FAIL"
			r.Conformant = false
		}
		fmt.Fprintf(&b, "[%s] %s", mark, c.Rule)
		if c.Note != "" {
			fmt.Fprintf(&b, " — %s", c.Note)
		}
		b.WriteByte('\n')
	}
	return b.String()
}
