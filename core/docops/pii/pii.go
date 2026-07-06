// Package pii detects personally identifiable information in plain text so it
// can be redacted. Detection is pattern-based and pure Go — no network, no AI —
// which keeps it usable from the MIT core and the WASM build. It is the
// deterministic counterpart to the Cloud tier's AI-based auto-redaction: the
// same feature, without sending the document anywhere.
//
// The detectors are deliberately conservative in shape but err toward
// over-matching where redaction is the goal — for a redaction tool, redacting a
// non-PII lookalike is a safe failure while missing real PII is not
// (.claude/SECURITY.md §1).
package pii

import (
	"regexp"
	"strings"
)

// Kind identifies a category of PII.
type Kind string

const (
	Email      Kind = "email"
	SSN        Kind = "ssn"         // US Social Security Number, ###-##-####
	Phone      Kind = "phone"       // North-American-style phone number
	CreditCard Kind = "credit_card" // 13–19 digit card number (Luhn-checked in Detect)
)

// All is the default set of kinds, in a stable order.
var All = []Kind{Email, SSN, Phone, CreditCard}

// patterns holds the compiled regex for each kind. Credit-card matches are
// Luhn-filtered in Detect; the regex only finds candidates.
var patterns = map[Kind]*regexp.Regexp{
	Email:      regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`),
	SSN:        regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`),
	Phone:      regexp.MustCompile(`\b(?:\+?1[ .\-]?)?\(?\d{3}\)?[ .\-]?\d{3}[ .\-]?\d{4}\b`),
	CreditCard: regexp.MustCompile(`\b(?:\d[ \-]?){13,19}\b`),
}

// Pattern returns the regex source for a kind, for callers (like the redaction
// engine) that match against a content stream rather than plain text. The
// returned string is a valid RE2 pattern. It returns "" for an unknown kind.
func Pattern(k Kind) string {
	re, ok := patterns[k]
	if !ok {
		return ""
	}
	return re.String()
}

// Match is one detected PII occurrence.
type Match struct {
	Kind  Kind
	Value string
	Start int // byte offset into the input text
	End   int
}

// Detect returns every PII occurrence of the given kinds in text (all kinds when
// none are named), in order of appearance. Credit-card candidates are validated
// with the Luhn checksum so that arbitrary long digit runs are not reported.
func Detect(text string, kinds ...Kind) []Match {
	if len(kinds) == 0 {
		kinds = All
	}
	var out []Match
	for _, k := range kinds {
		re := patterns[k]
		if re == nil {
			continue
		}
		for _, loc := range re.FindAllStringIndex(text, -1) {
			val := text[loc[0]:loc[1]]
			if k == CreditCard && !luhn(val) {
				continue
			}
			out = append(out, Match{Kind: k, Value: val, Start: loc[0], End: loc[1]})
		}
	}
	return out
}

// luhn reports whether the digits in s (ignoring spaces and hyphens) satisfy the
// Luhn checksum and number 13–19 long — the shape of a real payment card.
func luhn(s string) bool {
	digits := make([]int, 0, len(s))
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			digits = append(digits, int(r-'0'))
		case r == ' ' || r == '-':
		default:
			return false
		}
	}
	if len(digits) < 13 || len(digits) > 19 {
		return false
	}
	sum := 0
	double := false
	for i := len(digits) - 1; i >= 0; i-- {
		d := digits[i]
		if double {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
		double = !double
	}
	return sum%10 == 0
}

// KindsFromStrings maps user-supplied names to Kinds, ignoring unknown names.
// Case-insensitive; accepts both "credit_card" and "creditcard".
func KindsFromStrings(names []string) []Kind {
	var out []Kind
	for _, n := range names {
		switch strings.ToLower(strings.TrimSpace(n)) {
		case "email":
			out = append(out, Email)
		case "ssn":
			out = append(out, SSN)
		case "phone":
			out = append(out, Phone)
		case "credit_card", "creditcard", "card":
			out = append(out, CreditCard)
		}
	}
	return out
}
