package fontmodel

import "sync"

// ByteTables are the readings of a font that reads one byte per code, per
// byte value: what Text, AltText and ByteText return for a one-byte string.
// A string's readings are the concatenation of its bytes' entries, so
// redaction's text model builds them without decoding each string anew.
type ByteTables struct {
	Text, Byte [256]string
	// Alt is the AltText entry of each byte, and AltDiffers marks the bytes
	// whose entry differs from Text's: AltText(raw) is a reading (ok) exactly
	// when one of raw's bytes differs. Alt is nil when the font has no
	// AltText reading at all.
	Alt        *[256]string
	AltDiffers [256]bool
	// ByteIsText is set when Byte equals Text for every byte: ByteText is
	// then the same reading as Text.
	ByteIsText bool
}

// latin1Tables are the tables of the nil font and of a simple font with
// neither /ToUnicode nor /Differences.
var latin1Tables = func() *ByteTables {
	t := &ByteTables{}
	for c := 0; c < 256; c++ {
		t.Text[c] = string(rune(c))
		t.Byte[c] = t.Text[c]
	}
	t.ByteIsText = true
	return t
}()

// fontTables holds a Font's ByteTables, computed once.
type fontTables struct {
	once sync.Once
	t    *ByteTables
}

// Tables returns the font's per-byte readings, or nil when Text does not
// read one byte per code (a composite font, a font read as two-byte, or a
// /ToUnicode CMap whose codes are wider than a byte). The nil font has them.
func (f *Font) Tables() *ByteTables {
	if f == nil {
		return latin1Tables
	}
	f.tables.once.Do(func() { f.tables.t = f.buildTables() })
	return f.tables.t
}

func (f *Font) buildTables() *ByteTables {
	if f.kind == Composite || f.twoByte || (f.toUni != nil && f.toUni.codeLen > 1) {
		return nil
	}
	if f.toUni == nil && len(f.diffs) == 0 {
		return latin1Tables
	}
	t := &ByteTables{}
	var alt [256]string
	hasAlt := false
	for c := 0; c < 256; c++ {
		b := []byte{byte(c)}
		t.Text[c] = f.Text(b)
		if s, ok := f.ByteText(b); ok {
			t.Byte[c] = s
		} else {
			t.Byte[c] = t.Text[c]
		}
		alt[c] = t.Text[c]
		if s, ok := f.AltText(b); ok {
			alt[c] = s
			t.AltDiffers[c] = true
			hasAlt = true
		}
	}
	if hasAlt {
		t.Alt = &alt
	}
	t.ByteIsText = t.Byte == t.Text
	return t
}
