package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// stubAI records the last call and returns canned data, so the core wiring can
// be tested without any network.
type stubAI struct {
	gotText     string
	gotSchema   Schema
	gotQuestion string
	extract     map[string]any
	answer      string
}

func (s *stubAI) Extract(_ context.Context, text string, schema Schema) (map[string]any, error) {
	s.gotText, s.gotSchema = text, schema
	return s.extract, nil
}

func (s *stubAI) Ask(_ context.Context, text, question string) (string, error) {
	s.gotText, s.gotQuestion = text, question
	return s.answer, nil
}

// aiPDF builds a one-page PDF containing the given text.
func aiPDF(t *testing.T, text string) *Document {
	t.Helper()
	content := "BT /F1 12 Tf 72 700 Td (" + text + ") Tj ET"
	pdf := buildCorePDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 612 792] >>",
		3: "<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
		4: fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
		5: "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}, "<< /Size 6 /Root 1 0 R >>")
	doc, err := OpenBytes(pdf)
	if err != nil {
		t.Fatalf("OpenBytes: %v", err)
	}
	return doc
}

func TestExtractStructuredRequiresCloud(t *testing.T) {
	doc := aiPDF(t, "Invoice from Acme")
	if _, err := doc.ExtractStructured(Schema{Fields: []SchemaField{{Name: "vendor"}}}); !errors.Is(err, ErrFeatureRequiresCloud) {
		t.Fatalf("ExtractStructured without client err = %v, want ErrFeatureRequiresCloud", err)
	}
	if _, err := doc.AskDocument("who?"); !errors.Is(err, ErrFeatureRequiresCloud) {
		t.Fatalf("AskDocument without client err = %v, want ErrFeatureRequiresCloud", err)
	}
}

func TestExtractStructuredBadArgument(t *testing.T) {
	doc := aiPDF(t, "x")
	doc.SetAIClient(&stubAI{})
	// Unsupported type and empty schema are both ErrInvalidArgument, checked
	// before any client call.
	if _, err := doc.ExtractStructured(42); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("ExtractStructured(42) err = %v, want ErrInvalidArgument", err)
	}
	if _, err := doc.ExtractStructured(Schema{}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("ExtractStructured(empty) err = %v, want ErrInvalidArgument", err)
	}
	if _, err := doc.AskDocument("  "); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("AskDocument(blank) err = %v, want ErrInvalidArgument", err)
	}
}

func TestExtractStructuredDelegates(t *testing.T) {
	doc := aiPDF(t, "Total 99 USD")
	stub := &stubAI{extract: map[string]any{"total": "99"}}
	doc.SetAIClient(stub)

	// map[string]string is accepted and normalized into a Schema.
	got, err := doc.ExtractStructured(map[string]string{"total": "the amount due"})
	if err != nil {
		t.Fatalf("ExtractStructured: %v", err)
	}
	if got["total"] != "99" {
		t.Errorf("result = %v, want total=99", got)
	}
	if len(stub.gotSchema.Fields) != 1 || stub.gotSchema.Fields[0].Name != "total" {
		t.Errorf("schema not forwarded: %+v", stub.gotSchema)
	}
	// The extracted document text must reach the client (grounding).
	if !strings.Contains(stub.gotText, "Total 99 USD") {
		t.Errorf("document text not forwarded: %q", stub.gotText)
	}
}

func TestAskDocumentDelegates(t *testing.T) {
	doc := aiPDF(t, "The sky is blue")
	stub := &stubAI{answer: "blue"}
	doc.SetAIClient(stub)
	ans, err := doc.AskDocument("What color is the sky?")
	if err != nil {
		t.Fatalf("AskDocument: %v", err)
	}
	if ans != "blue" {
		t.Errorf("answer = %q, want blue", ans)
	}
	if stub.gotQuestion != "What color is the sky?" {
		t.Errorf("question not forwarded: %q", stub.gotQuestion)
	}
}
