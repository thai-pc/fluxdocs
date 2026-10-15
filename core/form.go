package core

import (
	"strings"

	"github.com/thai-pc/fluxdocs/core/form"
	"github.com/thai-pc/fluxdocs/core/parse"
)

// flattenFontName is the injected Helvetica used to draw filled field values.
const flattenFontName = "FdFF"

// form.go implements §7.5 form reading and filling. GetFormFields reads the
// AcroForm field tree; SetFormFieldValue records a fill value that subsequent
// GetFormFields calls reflect. Baking the fills into the page content (and
// dropping the interactive widgets) is FlattenForm, a follow-up increment.

// GetFormFields lists the AcroForm fields in the document, with any values set
// via SetFormFieldValue applied.
//
// Returns ErrInvalidPDF if the document was not opened from bytes. A document
// with no interactive form returns an empty slice.
func (d *Document) GetFormFields() (_ []FormField, err error) {
	defer guard("GetFormFields", -1, &err)
	if d.resolver == nil {
		return nil, ErrInvalidPDF
	}
	fields, err := form.ReadFields(d.resolver)
	if err != nil {
		return nil, err
	}

	d.mu.Lock()
	overrides := make(map[string]string, len(d.formValues))
	for k, v := range d.formValues {
		overrides[k] = v
	}
	d.mu.Unlock()

	out := make([]FormField, len(fields))
	for i, f := range fields {
		val := f.Value
		if v, ok := overrides[f.Name]; ok {
			val = v
		}
		out[i] = FormField{
			Name:     f.Name,
			Type:     FormFieldType(f.Type),
			Value:    val,
			Options:  f.Options,
			ReadOnly: f.ReadOnly,
			Required: f.Required,
		}
	}
	return out, nil
}

// SetFormFieldValue records a value for the named field; a later GetFormFields
// reflects it. The change is held in memory until the (upcoming) FlattenForm
// bakes it in.
//
// Returns ErrInvalidPDF if the document was not opened from bytes,
// ErrFormFieldNotFound if no field has that name, or ErrReadOnly if the field is
// read-only or a signature field (which cannot be filled with a value).
func (d *Document) SetFormFieldValue(name string, value string) (err error) {
	defer guard("SetFormFieldValue", -1, &err)
	if d.resolver == nil {
		return ErrInvalidPDF
	}
	fields, err := form.ReadFields(d.resolver)
	if err != nil {
		return err
	}
	var target *form.Field
	for i := range fields {
		if fields[i].Name == name {
			target = &fields[i]
			break
		}
	}
	if target == nil {
		return ErrFormFieldNotFound
	}
	if target.ReadOnly || target.Type == form.Signature {
		return ErrReadOnly
	}

	d.mu.Lock()
	if d.formValues == nil {
		d.formValues = map[string]string{}
	}
	d.formValues[name] = value
	d.mu.Unlock()
	return nil
}

// FlattenForm bakes the current form values into the page content and removes
// the interactive widgets, returning a new static document that can no longer be
// edited. Each filled field's value is drawn as text inside its widget rectangle
// (checkboxes/radios draw an "X"); pages that carry form widgets have their
// annotations dropped so the form is no longer interactive, and the /AcroForm is
// not carried over. The source document is unchanged.
//
// The drawn values use the standard-14 Helvetica font, so they show in standard
// viewers and in ExtractText (FluxDocs's own renderer does not yet draw
// non-embedded fonts — the same limitation as watermarks).
//
// Returns ErrInvalidPDF if the document was not opened from bytes, or a
// build/re-open error.
func (d *Document) FlattenForm() (_ *Document, err error) {
	defer guard("FlattenForm", -1, &err)
	if d.resolver == nil || len(d.pageInfos) != len(d.Pages) {
		return nil, ErrInvalidPDF
	}

	d.mu.Lock()
	overrides := make(map[string]string, len(d.formValues))
	for k, v := range d.formValues {
		overrides[k] = v
	}
	d.mu.Unlock()

	widgets, withWidgets := form.WidgetsToFlatten(d.resolver, d.pageInfos, overrides)
	byPage := map[int][]form.FillWidget{}
	for _, w := range widgets {
		byPage[w.PageIndex] = append(byPage[w.PageIndex], w)
	}

	helv := map[parse.Name]parse.Dict{
		flattenFontName: {
			"Type":     parse.Name("Font"),
			"Subtype":  parse.Name("Type1"),
			"BaseFont": parse.Name("Helvetica"),
		},
	}

	sources := make([]parse.PageSource, len(d.pageInfos))
	for i := range d.pageInfos {
		src := parse.PageSource{Resolver: d.resolver, Page: d.pageInfos[i]}
		if withWidgets[i] {
			src.DropAnnots = true // remove the interactive widgets
		}
		if ws := byPage[i]; len(ws) > 0 {
			src.AppendContent = flattenFormContent(ws)
			src.Fonts = helv
		}
		sources[i] = src
	}

	built, err := parse.BuildDocument(sources, buildInfoDict(d))
	if err != nil {
		return nil, err
	}
	return OpenBytes(built)
}

// flattenFormContent generates the content-stream operators that draw each
// widget's value inside its rectangle (in PDF user space).
func flattenFormContent(widgets []form.FillWidget) []byte {
	var b strings.Builder
	for _, w := range widgets {
		rw := w.Rect.URX - w.Rect.LLX
		rh := w.Rect.URY - w.Rect.LLY
		if rw <= 0 || rh <= 0 {
			continue
		}
		text := w.Value
		if w.Type == form.Checkbox || w.Type == form.Radio {
			text = "X"
		}
		fontSize := rh * 0.7
		if fontSize > 12 {
			fontSize = 12
		}
		if fontSize < 6 {
			fontSize = 6
		}
		x := w.Rect.LLX + 2
		y := w.Rect.LLY + (rh-fontSize)/2 + fontSize*0.2

		b.WriteString("q 0 0 0 rg BT /")
		b.WriteString(flattenFontName)
		b.WriteByte(' ')
		b.WriteString(formatNum(fontSize))
		b.WriteString(" Tf ")
		b.WriteString(formatNum(x))
		b.WriteByte(' ')
		b.WriteString(formatNum(y))
		b.WriteString(" Td (")
		b.WriteString(escapePDFLiteral(text))
		b.WriteString(") Tj ET Q\n")
	}
	return []byte(b.String())
}
