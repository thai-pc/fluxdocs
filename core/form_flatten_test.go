package core

import (
	"strings"
	"testing"
)

// widgetFormPDF builds a form whose fields are merged widget annotations placed
// on the page (the common AcroForm layout): a text field and a checkbox.
func widgetFormPDF() []byte {
	return buildCorePDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R /AcroForm 5 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 300 400] >>",
		3: "<< /Type /Page /Parent 2 0 R /Contents 4 0 R /Annots [6 0 R 7 0 R] >>",
		4: coreStreamObj(""),
		5: "<< /Fields [6 0 R 7 0 R] >>",
		6: "<< /Type /Annot /Subtype /Widget /FT /Tx /T (name) /V (Alice) /Rect [50 300 250 320] >>",
		7: "<< /Type /Annot /Subtype /Widget /FT /Btn /T (agree) /V /Yes /Rect [50 260 80 280] >>",
	}, "<< /Size 8 /Root 1 0 R >>")
}

func openWidgetForm(t *testing.T) *Document {
	t.Helper()
	doc, err := OpenBytes(widgetFormPDF())
	if err != nil {
		t.Fatalf("OpenBytes: %v", err)
	}
	return doc
}

func TestFlattenForm_DrawsValuesAndRemovesForm(t *testing.T) {
	doc := openWidgetForm(t)

	out, err := doc.FlattenForm()
	if err != nil {
		t.Fatal(err)
	}

	// The filled values are baked into the page content.
	text, _ := out.ExtractText(ExtractOptions{})
	if !strings.Contains(text, "Alice") {
		t.Errorf("flattened text missing the text field value; got %q", text)
	}
	if !strings.Contains(text, "X") {
		t.Errorf("flattened text missing the checkbox mark; got %q", text)
	}

	// The interactive form is gone.
	fields, err := out.GetFormFields()
	if err != nil {
		t.Fatal(err)
	}
	if len(fields) != 0 {
		t.Errorf("flattened doc still has %d form fields, want 0", len(fields))
	}

	// The source document is unchanged.
	srcFields, _ := doc.GetFormFields()
	if len(srcFields) != 2 {
		t.Errorf("source form has %d fields after flatten, want 2 (unchanged)", len(srcFields))
	}
}

func TestFlattenForm_UsesOverrideValue(t *testing.T) {
	doc := openWidgetForm(t)
	if err := doc.SetFormFieldValue("name", "Bob"); err != nil {
		t.Fatal(err)
	}
	out, err := doc.FlattenForm()
	if err != nil {
		t.Fatal(err)
	}
	text, _ := out.ExtractText(ExtractOptions{})
	if !strings.Contains(text, "Bob") {
		t.Errorf("flattened text missing the override value; got %q", text)
	}
	if strings.Contains(text, "Alice") {
		t.Errorf("flattened text still shows the original value: %q", text)
	}
}

func TestFlattenForm_ResolverLessErrors(t *testing.T) {
	if _, err := (&Document{}).FlattenForm(); err != ErrInvalidPDF {
		t.Errorf("err = %v, want ErrInvalidPDF", err)
	}
}

func TestFlattenForm_NoFormIsFaithfulCopy(t *testing.T) {
	doc := renderTestDoc(t, 2)
	out, err := doc.FlattenForm()
	if err != nil {
		t.Fatal(err)
	}
	if out.PageCount != 2 {
		t.Errorf("PageCount = %d, want 2", out.PageCount)
	}
}
