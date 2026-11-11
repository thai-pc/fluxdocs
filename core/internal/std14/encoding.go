// Package std14 holds the font data PDF defines by name rather than by
// program: the simple-font base encodings of ISO 32000-1:2008 Annex D
// (StandardEncoding, WinAnsiEncoding, MacRomanEncoding). Later phases of
// docs/design/type1-std14-fonts.md add the standard 14 fonts' names, metrics
// and glyph-name tables here.
//
// Clean-room: transcribed from ISO 32000-1:2008 Annex D (Table D.2 and its
// notes) only. Pure Go; Core (MIT).
package std14

// Encoding is a simple-font encoding: the glyph name of each one-byte code,
// "" for a code the encoding leaves unassigned. The functions below return
// copies, so a caller cannot modify the package's tables.
type Encoding = [256]string

// latinRow is one row of Annex D Table D.2: a glyph name and its codes in
// StandardEncoding, MacRomanEncoding and WinAnsiEncoding (0 = not encoded;
// no row uses code 0).
type latinRow struct {
	name          string
	std, mac, win byte
}

// latinSet is Table D.2 minus the single letters and digits, which the
// tables below add from their ASCII codes (identical in all three encodings).
// Codes are octal, as in the table.
var latinSet = []latinRow{
	{"AE", 0341, 0256, 0306}, {"Aacute", 0, 0347, 0301}, {"Acircumflex", 0, 0345, 0302},
	{"Adieresis", 0, 0200, 0304}, {"Agrave", 0, 0313, 0300}, {"Aring", 0, 0201, 0305},
	{"Atilde", 0, 0314, 0303}, {"Ccedilla", 0, 0202, 0307}, {"Eacute", 0, 0203, 0311},
	{"Ecircumflex", 0, 0346, 0312}, {"Edieresis", 0, 0350, 0313}, {"Egrave", 0, 0351, 0310},
	{"Eth", 0, 0, 0320}, {"Euro", 0, 0, 0200}, {"Iacute", 0, 0352, 0315},
	{"Icircumflex", 0, 0353, 0316}, {"Idieresis", 0, 0354, 0317}, {"Igrave", 0, 0355, 0314},
	{"Lslash", 0350, 0, 0}, {"Ntilde", 0, 0204, 0321}, {"OE", 0352, 0316, 0214},
	{"Oacute", 0, 0356, 0323}, {"Ocircumflex", 0, 0357, 0324}, {"Odieresis", 0, 0205, 0326},
	{"Ograve", 0, 0361, 0322}, {"Oslash", 0351, 0257, 0330}, {"Otilde", 0, 0315, 0325},
	{"Scaron", 0, 0, 0212}, {"Thorn", 0, 0, 0336}, {"Uacute", 0, 0362, 0332},
	{"Ucircumflex", 0, 0363, 0333}, {"Udieresis", 0, 0206, 0334}, {"Ugrave", 0, 0364, 0331},
	{"Yacute", 0, 0, 0335}, {"Ydieresis", 0, 0331, 0237}, {"Zcaron", 0, 0, 0216},
	{"aacute", 0, 0207, 0341}, {"acircumflex", 0, 0211, 0342}, {"acute", 0302, 0253, 0264},
	{"adieresis", 0, 0212, 0344}, {"ae", 0361, 0276, 0346}, {"agrave", 0, 0210, 0340},
	{"ampersand", 046, 046, 046}, {"aring", 0, 0214, 0345}, {"asciicircum", 0136, 0136, 0136},
	{"asciitilde", 0176, 0176, 0176}, {"asterisk", 052, 052, 052}, {"at", 0100, 0100, 0100},
	{"atilde", 0, 0213, 0343}, {"backslash", 0134, 0134, 0134}, {"bar", 0174, 0174, 0174},
	{"braceleft", 0173, 0173, 0173}, {"braceright", 0175, 0175, 0175},
	{"bracketleft", 0133, 0133, 0133}, {"bracketright", 0135, 0135, 0135},
	{"breve", 0306, 0371, 0}, {"brokenbar", 0, 0, 0246}, {"bullet", 0267, 0245, 0225},
	{"caron", 0317, 0377, 0}, {"ccedilla", 0, 0215, 0347}, {"cedilla", 0313, 0374, 0270},
	{"cent", 0242, 0242, 0242}, {"circumflex", 0303, 0366, 0210}, {"colon", 072, 072, 072},
	{"comma", 054, 054, 054}, {"copyright", 0, 0251, 0251}, {"currency", 0250, 0333, 0244},
	{"dagger", 0262, 0240, 0206}, {"daggerdbl", 0263, 0340, 0207}, {"degree", 0, 0241, 0260},
	{"dieresis", 0310, 0254, 0250}, {"divide", 0, 0326, 0367}, {"dollar", 044, 044, 044},
	{"dotaccent", 0307, 0372, 0}, {"dotlessi", 0365, 0365, 0}, {"eacute", 0, 0216, 0351},
	{"ecircumflex", 0, 0220, 0352}, {"edieresis", 0, 0221, 0353}, {"egrave", 0, 0217, 0350},
	{"eight", 070, 070, 070}, {"ellipsis", 0274, 0311, 0205}, {"emdash", 0320, 0321, 0227},
	{"endash", 0261, 0320, 0226}, {"equal", 075, 075, 075}, {"eth", 0, 0, 0360},
	{"exclam", 041, 041, 041}, {"exclamdown", 0241, 0301, 0241}, {"fi", 0256, 0336, 0},
	{"five", 065, 065, 065}, {"fl", 0257, 0337, 0}, {"florin", 0246, 0304, 0203},
	{"four", 064, 064, 064}, {"fraction", 0244, 0332, 0}, {"germandbls", 0373, 0247, 0337},
	{"grave", 0301, 0140, 0140}, {"greater", 076, 076, 076},
	{"guillemotleft", 0253, 0307, 0253}, {"guillemotright", 0273, 0310, 0273},
	{"guilsinglleft", 0254, 0334, 0213}, {"guilsinglright", 0255, 0335, 0233},
	{"hungarumlaut", 0315, 0375, 0}, {"hyphen", 055, 055, 055}, {"iacute", 0, 0222, 0355},
	{"icircumflex", 0, 0224, 0356}, {"idieresis", 0, 0225, 0357}, {"igrave", 0, 0223, 0354},
	{"less", 074, 074, 074}, {"logicalnot", 0, 0302, 0254}, {"lslash", 0370, 0, 0},
	{"macron", 0305, 0370, 0257}, {"mu", 0, 0265, 0265}, {"multiply", 0, 0, 0327},
	{"nine", 071, 071, 071}, {"ntilde", 0, 0226, 0361}, {"numbersign", 043, 043, 043},
	{"oacute", 0, 0227, 0363}, {"ocircumflex", 0, 0231, 0364}, {"odieresis", 0, 0232, 0366},
	{"oe", 0372, 0317, 0234}, {"ogonek", 0316, 0376, 0}, {"ograve", 0, 0230, 0362},
	{"one", 061, 061, 061}, {"onehalf", 0, 0, 0275}, {"onequarter", 0, 0, 0274},
	{"onesuperior", 0, 0, 0271}, {"ordfeminine", 0343, 0273, 0252},
	{"ordmasculine", 0353, 0274, 0272}, {"oslash", 0371, 0277, 0370}, {"otilde", 0, 0233, 0365},
	{"paragraph", 0266, 0246, 0266}, {"parenleft", 050, 050, 050}, {"parenright", 051, 051, 051},
	{"percent", 045, 045, 045}, {"period", 056, 056, 056}, {"periodcentered", 0264, 0341, 0267},
	{"perthousand", 0275, 0344, 0211}, {"plus", 053, 053, 053}, {"plusminus", 0, 0261, 0261},
	{"question", 077, 077, 077}, {"questiondown", 0277, 0300, 0277}, {"quotedbl", 042, 042, 042},
	{"quotedblbase", 0271, 0343, 0204}, {"quotedblleft", 0252, 0322, 0223},
	{"quotedblright", 0272, 0323, 0224}, {"quoteleft", 0140, 0324, 0221},
	{"quoteright", 047, 0325, 0222}, {"quotesinglbase", 0270, 0342, 0202},
	{"quotesingle", 0251, 047, 047}, {"registered", 0, 0250, 0256}, {"ring", 0312, 0373, 0},
	{"scaron", 0, 0, 0232}, {"section", 0247, 0244, 0247}, {"semicolon", 073, 073, 073},
	{"seven", 067, 067, 067}, {"six", 066, 066, 066}, {"slash", 057, 057, 057},
	{"space", 040, 040, 040}, {"sterling", 0243, 0243, 0243}, {"thorn", 0, 0, 0376},
	{"three", 063, 063, 063}, {"threequarters", 0, 0, 0276}, {"threesuperior", 0, 0, 0263},
	{"tilde", 0304, 0367, 0230}, {"trademark", 0, 0252, 0231}, {"two", 062, 062, 062},
	{"twosuperior", 0, 0, 0262}, {"uacute", 0, 0234, 0372}, {"ucircumflex", 0, 0236, 0373},
	{"udieresis", 0, 0237, 0374}, {"ugrave", 0, 0235, 0371}, {"underscore", 0137, 0137, 0137},
	{"yacute", 0, 0, 0375}, {"ydieresis", 0, 0330, 0377}, {"yen", 0245, 0264, 0245},
	{"zcaron", 0, 0, 0236}, {"zero", 060, 060, 060},
}

// The three Latin-text encodings of Annex D, built from latinSet.
var (
	standard = buildLatin(func(r latinRow) byte { return r.std })
	macRoman = buildLatin(func(r latinRow) byte { return r.mac })
	winAnsi  = buildLatin(func(r latinRow) byte { return r.win })
)

func init() {
	// Annex D, notes to Table D.2: space is also encoded as 312 (octal) in
	// MacRomanEncoding and as 240 in WinAnsiEncoding (nonbreaking space,
	// typographically a space); hyphen also as 255 in WinAnsiEncoding (soft
	// hyphen, typographically a hyphen); and every unused WinAnsiEncoding code
	// greater than 40 maps to bullet.
	macRoman[0312] = "space"
	winAnsi[0240] = "space"
	winAnsi[0255] = "hyphen"
	for c := 041; c < 256; c++ {
		if winAnsi[c] == "" {
			winAnsi[c] = "bullet"
		}
	}
}

// buildLatin builds an encoding from the letters A–Z and a–z (named and coded
// as themselves in all three encodings) and latinSet's codes picked by code.
func buildLatin(code func(latinRow) byte) *Encoding {
	e := new(Encoding)
	for c := byte('A'); c <= 'Z'; c++ {
		e[c] = string(rune(c))
		e[c+'a'-'A'] = string(rune(c + 'a' - 'A'))
	}
	for _, r := range latinSet {
		if c := code(r); c != 0 {
			e[c] = r.name
		}
	}
	return e
}

// StandardEncoding returns a copy of Adobe StandardEncoding (Annex D.2), the
// built-in encoding of most Latin Type 1 fonts.
func StandardEncoding() Encoding { return *standard }

// StandardName returns the glyph name StandardEncoding gives code, "" when it
// leaves the code unassigned: the lookup seac components are named through,
// without copying the table.
func StandardName(code byte) string { return standard[code] }

// BaseEncoding returns the predefined encoding a simple font's /Encoding or
// /BaseEncoding names (§9.6.6.1): WinAnsiEncoding or MacRomanEncoding, plus
// StandardEncoding, which a font program can name as its built-in encoding.
// MacExpertEncoding is not transcribed yet: it and any other name return
// false, and the caller falls back to the font's built-in encoding. The
// result is a copy.
func BaseEncoding(name string) (Encoding, bool) {
	switch name {
	case "WinAnsiEncoding":
		return *winAnsi, true
	case "MacRomanEncoding":
		return *macRoman, true
	case "StandardEncoding":
		return *standard, true
	}
	return Encoding{}, false
}
