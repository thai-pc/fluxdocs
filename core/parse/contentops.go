package parse

// This file holds the content-stream operator table used by strict scanning
// (ScanContentSpansStrict). Interpreters disagree on what to do when an
// operator gets the wrong operands: a stack-based one (PostScript-style) leaves
// surplus operands for the next operator, others drop them or the operator. If
// a string pairs with a text-showing operator in some viewer but not in our
// scan, redaction cannot see that text. Strict scanning therefore accepts an
// operator only with exactly the operands ISO 32000-1 defines for it.

// fixedOperands is the operand count of every content-stream operator with a
// fixed arity, per ISO 32000-1:2008 Annex A (Table A.1, Operator Summary) and
// the sections defining each group. BI is absent: the scanner handles inline
// images itself. SC, SCN, sc and scn take a variable count (operandsFit).
var fixedOperands = map[string]int{
	// General and special graphics state (§8.4.4).
	"w": 1, "J": 1, "j": 1, "M": 1, "d": 2, "ri": 1, "i": 1, "gs": 1,
	"q": 0, "Q": 0, "cm": 6,
	// Path construction (§8.5.2), painting (§8.5.3) and clipping (§8.5.4).
	"m": 2, "l": 2, "c": 6, "v": 4, "y": 4, "h": 0, "re": 4,
	"S": 0, "s": 0, "f": 0, "F": 0, "f*": 0, "B": 0, "B*": 0, "b": 0, "b*": 0, "n": 0,
	"W": 0, "W*": 0,
	// Text objects (§9.4.1), state (§9.3), positioning (§9.4.2), showing (§9.4.3).
	"BT": 0, "ET": 0,
	"Tc": 1, "Tw": 1, "Tz": 1, "TL": 1, "Tf": 2, "Tr": 1, "Ts": 1,
	"Td": 2, "TD": 2, "Tm": 6, "T*": 0,
	"Tj": 1, "'": 1, "\"": 3, "TJ": 1,
	// Type 3 glyph metrics (§9.6.5).
	"d0": 2, "d1": 6,
	// Colour (§8.6.8), shading (§8.7.4.2), XObjects (§8.8).
	"CS": 1, "cs": 1, "G": 1, "g": 1, "RG": 3, "rg": 3, "K": 4, "k": 4,
	"sh": 1,
	"Do": 1,
	// Inline-image delimiters (§8.9.7), marked content (§14.6), compatibility
	// sections (§7.8.2).
	"ID": 0, "EI": 0,
	"MP": 1, "DP": 2, "BMC": 1, "BDC": 2, "EMC": 0,
	"BX": 0, "EX": 0,
}

// operandsFit reports whether operands are exactly what op takes. Operators
// that can show or position text, or whose operands could be a string, are also
// type-checked: Tj and ' take a string, " two numbers and a string, TJ an
// array, d an array and a number, SC/sc one to four numbers, SCN/scn numbers
// optionally followed by a pattern name. An operator not in Annex A — inside a
// BX/EX compatibility section or not — fits only with no operands, since what a
// viewer does with operands it pairs to an unknown operator is undefined.
func operandsFit(op string, operands []Object) bool {
	n := len(operands)
	switch op {
	case "Tj", "'":
		return n == 1 && isString(operands[0])
	case "\"":
		return n == 3 && isNumber(operands[0]) && isNumber(operands[1]) && isString(operands[2])
	case "TJ":
		return n == 1 && isArray(operands[0])
	case "d":
		return n == 2 && isArray(operands[0]) && isNumber(operands[1])
	case "SC", "sc":
		return n >= 1 && n <= 4 && allNumbers(operands)
	case "SCN", "scn":
		if n >= 1 {
			if _, ok := operands[n-1].(Name); ok {
				return allNumbers(operands[:n-1])
			}
		}
		return n >= 1 && allNumbers(operands)
	}
	want, known := fixedOperands[op]
	if !known {
		return n == 0
	}
	return n == want
}

func isNumber(o Object) bool {
	switch o.(type) {
	case Integer, Real:
		return true
	}
	return false
}

func isString(o Object) bool { _, ok := o.(String); return ok }

func isArray(o Object) bool { _, ok := o.(Array); return ok }

func allNumbers(objs []Object) bool {
	for _, o := range objs {
		if !isNumber(o) {
			return false
		}
	}
	return true
}
