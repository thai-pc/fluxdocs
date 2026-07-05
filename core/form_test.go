package core

import "testing"

// formPDF builds a document with an AcroForm: a text field, a checkbox, a
// read-only text field, a combo (dropdown) choice, and a required text field.
func formPDF() []byte {
	return buildCorePDF(map[int]string{
		1:  "<< /Type /Catalog /Pages 2 0 R /AcroForm 5 0 R >>",
		2:  "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 300 400] >>",
		3:  "<< /Type /Page /Parent 2 0 R /Contents 4 0 R >>",
		4:  coreStreamObj(""),
		5:  "<< /Fields [6 0 R 7 0 R 8 0 R 9 0 R 10 0 R] >>",
		6:  "<< /FT /Tx /T (fullName) /V (Alice) >>",
		7:  "<< /FT /Btn /T (subscribe) /V /Off >>",
		8:  "<< /FT /Tx /T (locked) /V (fixed) /Ff 1 >>",
		9:  "<< /FT /Ch /T (country) /V (US) /Opt [(US) (UK) (VN)] /Ff 131072 >>",
		10: "<< /FT /Tx /T (email) /Ff 2 >>",
	}, "<< /Size 11 /Root 1 0 R >>")
}

func openForm(t *testing.T) *Document {
	t.Helper()
	doc, err := OpenBytes(formPDF())
	if err != nil {
		t.Fatalf("OpenBytes: %v", err)
	}
	return doc
}

func fieldByName(fields []FormField, name string) (FormField, bool) {
	for _, f := range fields {
		if f.Name == name {
			return f, true
		}
	}
	return FormField{}, false
}

func TestGetFormFields(t *testing.T) {
	doc := openForm(t)
	fields, err := doc.GetFormFields()
	if err != nil {
		t.Fatal(err)
	}
	if len(fields) != 5 {
		t.Fatalf("got %d fields, want 5: %+v", len(fields), fields)
	}

	tests := []struct {
		name     string
		typ      FormFieldType
		value    string
		readOnly bool
		required bool
	}{
		{"fullName", FieldText, "Alice", false, false},
		{"subscribe", FieldCheckbox, "Off", false, false},
		{"locked", FieldText, "fixed", true, false},
		{"country", FieldDropdown, "US", false, false},
		{"email", FieldText, "", false, true},
	}
	for _, tt := range tests {
		f, ok := fieldByName(fields, tt.name)
		if !ok {
			t.Errorf("field %q missing", tt.name)
			continue
		}
		if f.Type != tt.typ || f.Value != tt.value || f.ReadOnly != tt.readOnly || f.Required != tt.required {
			t.Errorf("field %q = %+v, want type=%s value=%q ro=%v req=%v",
				tt.name, f, tt.typ, tt.value, tt.readOnly, tt.required)
		}
	}

	// The choice field carries its options.
	country, _ := fieldByName(fields, "country")
	if len(country.Options) != 3 || country.Options[0] != "US" || country.Options[2] != "VN" {
		t.Errorf("country options = %v, want [US UK VN]", country.Options)
	}
}

func TestSetFormFieldValue(t *testing.T) {
	doc := openForm(t)
	if err := doc.SetFormFieldValue("fullName", "Bob"); err != nil {
		t.Fatal(err)
	}
	fields, _ := doc.GetFormFields()
	if f, _ := fieldByName(fields, "fullName"); f.Value != "Bob" {
		t.Errorf("fullName = %q after set, want Bob", f.Value)
	}
}

func TestSetFormFieldValue_Errors(t *testing.T) {
	doc := openForm(t)
	if err := doc.SetFormFieldValue("nope", "x"); err != ErrFormFieldNotFound {
		t.Errorf("missing field err = %v, want ErrFormFieldNotFound", err)
	}
	if err := doc.SetFormFieldValue("locked", "x"); err != ErrReadOnly {
		t.Errorf("read-only field err = %v, want ErrReadOnly", err)
	}
	if err := (&Document{}).SetFormFieldValue("a", "b"); err != ErrInvalidPDF {
		t.Errorf("resolver-less err = %v, want ErrInvalidPDF", err)
	}
}

func TestGetFormFields_NoForm(t *testing.T) {
	doc := renderTestDoc(t, 1) // a plain doc with no AcroForm
	fields, err := doc.GetFormFields()
	if err != nil {
		t.Fatal(err)
	}
	if len(fields) != 0 {
		t.Errorf("got %d fields, want 0 for a document with no form", len(fields))
	}
}

func TestGetFormFields_NestedNames(t *testing.T) {
	// A parent field "address" with child fields "street" and "city" → the
	// qualified names are "address.street" and "address.city".
	pdf := buildCorePDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R /AcroForm 5 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 300 400] >>",
		3: "<< /Type /Page /Parent 2 0 R /Contents 4 0 R >>",
		4: coreStreamObj(""),
		5: "<< /Fields [6 0 R] >>",
		6: "<< /T (address) /Kids [7 0 R 8 0 R] >>",
		7: "<< /FT /Tx /T (street) /V (Main St) /Parent 6 0 R >>",
		8: "<< /FT /Tx /T (city) /V (Hanoi) /Parent 6 0 R >>",
	}, "<< /Size 9 /Root 1 0 R >>")
	doc, err := OpenBytes(pdf)
	if err != nil {
		t.Fatal(err)
	}
	fields, err := doc.GetFormFields()
	if err != nil {
		t.Fatal(err)
	}
	if len(fields) != 2 {
		t.Fatalf("got %d fields, want 2: %+v", len(fields), fields)
	}
	if f, ok := fieldByName(fields, "address.street"); !ok || f.Value != "Main St" {
		t.Errorf("address.street = %+v, ok=%v", f, ok)
	}
	if _, ok := fieldByName(fields, "address.city"); !ok {
		t.Errorf("address.city missing")
	}
}
