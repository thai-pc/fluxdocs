package core

import (
	"bytes"
	"time"

	"github.com/thai-pc/fluxdocs/core/docops/pdfa"
	"github.com/thai-pc/fluxdocs/core/parse"
)

// pdfa.go implements PDF/A-1b conformance tagging (ConvertToPDFA) and structural
// validation (ValidatePDFA). See core/docops/pdfa for the honest scope note: this
// adds the required XMP metadata, an sRGB OutputIntent, and a file identifier,
// but does not embed fonts or flatten transparency, and is not certified against
// an external validator.

// ConvertToPDFA returns a new document tagged for PDF/A-1b conformance: it adds
// XMP metadata (pdfaid:part 1 / conformance B plus the document's own metadata),
// an OutputIntent backed by an embedded sRGB ICC profile, and a trailer /ID. The
// source document is unchanged.
//
// This adds the structural PDF/A markers only; it does not embed fonts or
// flatten transparency (see the docops/pdfa scope note). Returns ErrInvalidPDF
// if the document was not opened from bytes.
func (d *Document) ConvertToPDFA() (_ *Document, err error) {
	defer guard("ConvertToPDFA", -1, &err)
	if d.resolver == nil || len(d.pageInfos) != len(d.Pages) {
		return nil, ErrInvalidPDF
	}

	xmp := pdfa.XMPPacket(pdfa.Meta{
		Title:    d.Title,
		Author:   d.Metadata.Author,
		Producer: d.Metadata.Producer,
		Created:  time.Now(),
	})

	sources := make([]parse.PageSource, len(d.pageInfos))
	for i := range d.pageInfos {
		sources[i] = parse.PageSource{Resolver: d.resolver, Page: d.pageInfos[i]}
	}

	built, err := parse.BuildDocumentPDFA(sources, buildInfoDict(d), parse.PDFAParams{
		XMP:           xmp,
		ICC:           pdfa.GenerateSRGBICC(),
		ICCComponents: 3,
		ID:            pdfa.NewID(),
		Condition:     "sRGB",
	})
	if err != nil {
		return nil, err
	}
	return OpenBytes(built)
}

// ValidatePDFA checks a document's PDF/A-1b structural markers: XMP metadata
// declaring the conformance level, an OutputIntent with an embedded ICC profile,
// a file identifier, and the absence of encryption. It is a structural check,
// not a certified validation.
//
// Returns ErrInvalidPDF if the document was not opened from bytes.
func (d *Document) ValidatePDFA() (_ *PDFAReport, err error) {
	defer guard("ValidatePDFA", -1, &err)
	if d.resolver == nil {
		return nil, ErrInvalidPDF
	}
	report := &PDFAReport{Conformant: true}
	add := func(rule string, pass bool, note string) {
		report.Checks = append(report.Checks, PDFACheck{Rule: rule, Pass: pass, Note: note})
		if !pass {
			report.Conformant = false
		}
	}

	cat, err := d.resolver.Catalog()
	if err != nil {
		add("catalog", false, err.Error())
		return report, nil
	}

	// XMP metadata declaring PDF/A conformance.
	xmpOK, xmpNote := checkXMP(d.resolver, cat)
	add("xmp-metadata", xmpOK, xmpNote)

	// OutputIntent with an embedded ICC profile.
	oiOK, oiNote := checkOutputIntent(d.resolver, cat)
	add("output-intent", oiOK, oiNote)

	// File identifier in the trailer.
	_, hasID := d.resolver.Trailer()["ID"]
	add("file-id", hasID, "")

	// PDF/A forbids encryption.
	_, hasEncrypt := d.resolver.Trailer()["Encrypt"]
	add("no-encryption", !hasEncrypt, "")

	return report, nil
}

func checkXMP(r *parse.Resolver, cat parse.Dict) (bool, string) {
	obj, err := r.Resolve(cat["Metadata"])
	if err != nil {
		return false, "metadata unresolved"
	}
	st, ok := obj.(*parse.Stream)
	if !ok {
		return false, "no /Metadata stream"
	}
	if !bytes.Contains(st.Raw, []byte("pdfaid:part")) {
		return false, "XMP lacks pdfaid:part"
	}
	return true, ""
}

func checkOutputIntent(r *parse.Resolver, cat parse.Dict) (bool, string) {
	arr, ok, err := resolveArrayObj(r, cat["OutputIntents"])
	if err != nil || !ok || len(arr) == 0 {
		return false, "no /OutputIntents"
	}
	oi, ok, err := r.ResolveDict(arr[0])
	if err != nil || !ok {
		return false, "output intent unresolved"
	}
	if s, _ := oi.GetName("S"); s != "GTS_PDFA1" {
		return false, "output intent is not GTS_PDFA1"
	}
	prof, err := r.Resolve(oi["DestOutputProfile"])
	if err != nil {
		return false, "profile unresolved"
	}
	if _, ok := prof.(*parse.Stream); !ok {
		return false, "no embedded ICC profile"
	}
	return true, ""
}

// resolveArrayObj resolves obj to an Array following a reference.
func resolveArrayObj(r *parse.Resolver, obj parse.Object) (parse.Array, bool, error) {
	resolved, err := r.Resolve(obj)
	if err != nil {
		return nil, false, err
	}
	a, ok := resolved.(parse.Array)
	return a, ok, nil
}
