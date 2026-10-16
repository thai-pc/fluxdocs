package fontmodel

import "testing"

func TestParseCMap_BfChar(t *testing.T) {
	data := []byte(`
1 begincodespacerange
<0000> <FFFF>
endcodespacerange
2 beginbfchar
<0001> <0048>
<0002> <0069>
endbfchar
`)
	c := parseCMap(data)
	if c.codeLen != 2 {
		t.Errorf("codeLen = %d, want 2", c.codeLen)
	}
	if s, ok := c.lookup(0x0001); !ok || s != "H" {
		t.Errorf("lookup(1) = %q,%v, want H", s, ok)
	}
	if s, ok := c.lookup(0x0002); !ok || s != "i" {
		t.Errorf("lookup(2) = %q,%v, want i", s, ok)
	}
	if _, ok := c.lookup(0x0003); ok {
		t.Errorf("lookup(3) should be unmapped")
	}
}

func TestParseCMap_BfRangeIncrement(t *testing.T) {
	// Codes 0x0041..0x0043 map to consecutive Unicode starting at 'A'.
	data := []byte(`
1 beginbfrange
<0041> <0043> <0041>
endbfrange
`)
	c := parseCMap(data)
	for code, want := range map[uint32]string{0x41: "A", 0x42: "B", 0x43: "C"} {
		if s, ok := c.lookup(code); !ok || s != want {
			t.Errorf("lookup(%#x) = %q,%v, want %q", code, s, ok, want)
		}
	}
	if _, ok := c.lookup(0x44); ok {
		t.Errorf("lookup(0x44) should be out of range")
	}
}

func TestParseCMap_BfRangeArray(t *testing.T) {
	// An explicit destination per code (non-contiguous Unicode).
	data := []byte(`
1 beginbfrange
<0001> <0002> [<0058> <005A>]
endbfrange
`)
	c := parseCMap(data)
	if s, ok := c.lookup(0x0001); !ok || s != "X" {
		t.Errorf("lookup(1) = %q,%v, want X", s, ok)
	}
	if s, ok := c.lookup(0x0002); !ok || s != "Z" {
		t.Errorf("lookup(2) = %q,%v, want Z", s, ok)
	}
}

func TestParseCMap_MultiCharDestination(t *testing.T) {
	// A ligature code maps to two Unicode scalars (UTF-16BE, 4 bytes).
	data := []byte(`
1 beginbfchar
<0001> <00660069>
endbfchar
`)
	c := parseCMap(data)
	if s, ok := c.lookup(0x0001); !ok || s != "fi" {
		t.Errorf("lookup(1) = %q,%v, want fi", s, ok)
	}
}

func TestUTF16BEToString(t *testing.T) {
	// Surrogate pair for U+1F600.
	got := utf16BEToString([]byte{0xD8, 0x3D, 0xDE, 0x00})
	if got != "\U0001F600" {
		t.Errorf("utf16BEToString = %q, want emoji", got)
	}
}
