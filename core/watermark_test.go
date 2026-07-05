package core

import (
	"fmt"
	"strings"
	"testing"
)

// watermarkDoc builds an n-page document with a little text on each page.
func watermarkDoc(t *testing.T, n int) *Document {
	t.Helper()
	objs := map[int]string{1: "<< /Type /Catalog /Pages 2 0 R >>"}
	fontObj := 3 + 2*n
	objs[fontObj] = "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"
	kids := ""
	obj := 3
	for i := 0; i < n; i++ {
		p, c := obj, obj+1
		obj += 2
		kids += fmt.Sprintf("%d 0 R ", p)
		objs[p] = fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 400] /Resources << /Font << /F1 %d 0 R >> >> /Contents %d 0 R >>", fontObj, c)
		objs[c] = coreStreamObj(fmt.Sprintf("BT /F1 12 Tf 20 200 Td (Body %d) Tj ET", i))
	}
	objs[2] = fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", kids, n)
	pdf := buildCorePDF(objs, fmt.Sprintf("<< /Size %d /Root 1 0 R >>", fontObj+1))
	doc, err := OpenBytes(pdf)
	if err != nil {
		t.Fatalf("OpenBytes: %v", err)
	}
	return doc
}

func TestWatermark_BakesTextOnAllPages(t *testing.T) {
	doc := watermarkDoc(t, 2)
	out, err := doc.Watermark(WatermarkOptions{Text: "CONFIDENTIAL"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < out.PageCount; i++ {
		txt, _ := out.ExtractTextByPage(i, ExtractOptions{})
		if !strings.Contains(txt, "CONFIDENTIAL") {
			t.Errorf("page %d missing watermark; got %q", i, txt)
		}
		if !strings.Contains(txt, fmt.Sprintf("Body %d", i)) {
			t.Errorf("page %d lost its body text; got %q", i, txt)
		}
	}
}

func TestWatermark_SpecificPagesOnly(t *testing.T) {
	doc := watermarkDoc(t, 3)
	out, err := doc.Watermark(WatermarkOptions{Text: "DRAFT", Pages: []int{1}})
	if err != nil {
		t.Fatal(err)
	}
	if txt, _ := out.ExtractTextByPage(1, ExtractOptions{}); !strings.Contains(txt, "DRAFT") {
		t.Errorf("page 1 should carry the watermark; got %q", txt)
	}
	for _, p := range []int{0, 2} {
		if txt, _ := out.ExtractTextByPage(p, ExtractOptions{}); strings.Contains(txt, "DRAFT") {
			t.Errorf("page %d should NOT carry the watermark; got %q", p, txt)
		}
	}
}

func TestWatermark_InjectsFontAndExtGState(t *testing.T) {
	doc := watermarkDoc(t, 1)
	out, err := doc.Watermark(WatermarkOptions{Text: "X", Opacity: 0.4})
	if err != nil {
		t.Fatal(err)
	}
	res := out.pageInfos[0].Resources
	fonts, ok, _ := out.resolver.ResolveDict(res["Font"])
	if !ok || fonts[watermarkFontName] == nil {
		t.Errorf("watermark font %q not injected into /Font: %v", watermarkFontName, fonts)
	}
	eg, ok, _ := out.resolver.ResolveDict(res["ExtGState"])
	if !ok || eg[watermarkGSName] == nil {
		t.Errorf("watermark ExtGState %q not injected: %v", watermarkGSName, eg)
	}
}

func TestWatermark_SourceUnchanged(t *testing.T) {
	doc := watermarkDoc(t, 1)
	if _, err := doc.Watermark(WatermarkOptions{Text: "WM"}); err != nil {
		t.Fatal(err)
	}
	if txt, _ := doc.ExtractText(ExtractOptions{}); strings.Contains(txt, "WM") {
		t.Errorf("source document was mutated by Watermark")
	}
}

func TestWatermark_EscapesSpecialChars(t *testing.T) {
	doc := watermarkDoc(t, 1)
	out, err := doc.Watermark(WatermarkOptions{Text: "(c) A\\B"})
	if err != nil {
		t.Fatal(err)
	}
	if txt, _ := out.ExtractText(ExtractOptions{}); !strings.Contains(txt, "(c) A\\B") {
		t.Errorf("escaped watermark not round-tripped; got %q", txt)
	}
}

func TestWatermark_Validation(t *testing.T) {
	doc := watermarkDoc(t, 2)
	if _, err := doc.Watermark(WatermarkOptions{Text: "  "}); err != ErrInvalidArgument {
		t.Errorf("empty text err = %v, want ErrInvalidArgument", err)
	}
	if _, err := doc.Watermark(WatermarkOptions{Text: "x", Pages: []int{9}}); err != ErrPageNotFound {
		t.Errorf("bad page err = %v, want ErrPageNotFound", err)
	}
	if _, err := (&Document{}).Watermark(WatermarkOptions{Text: "x"}); err != ErrInvalidPDF {
		t.Errorf("resolver-less err = %v, want ErrInvalidPDF", err)
	}
}
