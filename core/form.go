package core

import "github.com/thai-pc/fluxdocs/core/form"

// form.go implements §7.5 form reading and filling. GetFormFields reads the
// AcroForm field tree; SetFormFieldValue records a fill value that subsequent
// GetFormFields calls reflect. Baking the fills into the page content (and
// dropping the interactive widgets) is FlattenForm, a follow-up increment.

// GetFormFields lists the AcroForm fields in the document, with any values set
// via SetFormFieldValue applied.
//
// Returns ErrInvalidPDF if the document was not opened from bytes. A document
// with no interactive form returns an empty slice.
func (d *Document) GetFormFields() ([]FormField, error) {
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
func (d *Document) SetFormFieldValue(name string, value string) error {
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
