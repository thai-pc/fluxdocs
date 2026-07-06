package pdfa

import (
	"encoding/binary"
	"strings"
	"testing"
)

func TestXMPPacket(t *testing.T) {
	x := string(XMPPacket(Meta{Title: "My & Doc", Author: "Alice", Producer: "FluxDocs"}))
	for _, want := range []string{
		"pdfaid:part>1<", "pdfaid:conformance>B<",
		"<dc:title>", "My &amp; Doc", "<dc:creator>", "Alice",
		"<pdf:Producer>FluxDocs", "<?xpacket end=\"w\"?>",
	} {
		if !strings.Contains(x, want) {
			t.Errorf("XMP missing %q", want)
		}
	}
	// The packet must start with the xpacket header (after the BOM).
	if !strings.Contains(x, "<?xpacket begin=") {
		t.Errorf("XMP lacks the xpacket begin header")
	}
}

func TestGenerateSRGBICC_Structure(t *testing.T) {
	icc := GenerateSRGBICC()

	// Profile size field must equal the actual length.
	if got := binary.BigEndian.Uint32(icc[0:]); int(got) != len(icc) {
		t.Errorf("ICC size field = %d, actual len = %d", got, len(icc))
	}
	// Required header signatures.
	if string(icc[36:40]) != "acsp" {
		t.Errorf("ICC missing 'acsp' signature, got %q", icc[36:40])
	}
	if string(icc[12:16]) != "mntr" {
		t.Errorf("device class = %q, want mntr", icc[12:16])
	}
	if string(icc[16:20]) != "RGB " {
		t.Errorf("colour space = %q, want 'RGB '", icc[16:20])
	}
	if string(icc[20:24]) != "XYZ " {
		t.Errorf("PCS = %q, want 'XYZ '", icc[20:24])
	}

	// Tag table: every offset+size must lie within the profile and each 4-char
	// signature must be present.
	tagCount := int(binary.BigEndian.Uint32(icc[128:]))
	if tagCount != 9 {
		t.Fatalf("tag count = %d, want 9", tagCount)
	}
	sigs := map[string]bool{}
	for i := 0; i < tagCount; i++ {
		e := 132 + i*12
		sig := string(icc[e : e+4])
		off := int(binary.BigEndian.Uint32(icc[e+4:]))
		size := int(binary.BigEndian.Uint32(icc[e+8:]))
		if off < 128 || off+size > len(icc) {
			t.Errorf("tag %s offset/size out of range: off=%d size=%d len=%d", sig, off, size, len(icc))
		}
		sigs[sig] = true
	}
	for _, req := range []string{"desc", "wtpt", "rXYZ", "gXYZ", "bXYZ", "rTRC", "gTRC", "bTRC", "cprt"} {
		if !sigs[req] {
			t.Errorf("ICC missing required tag %q", req)
		}
	}
}

func TestNewID(t *testing.T) {
	a, b := NewID(), NewID()
	if len(a) != 16 {
		t.Errorf("ID length = %d, want 16", len(a))
	}
	if string(a) == string(b) {
		t.Errorf("two IDs collided (not random)")
	}
}

func TestReport_Summarize(t *testing.T) {
	r := &Report{}
	r.add("a", true, "")
	r.add("b", false, "missing")
	s := r.Summarize()
	if r.Conformant {
		t.Errorf("report with a failing check should not be conformant")
	}
	if !strings.Contains(s, "[PASS] a") || !strings.Contains(s, "[FAIL] b — missing") {
		t.Errorf("summary = %q", s)
	}
}
