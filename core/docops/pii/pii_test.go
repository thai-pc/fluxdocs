package pii

import "testing"

func TestDetect(t *testing.T) {
	text := "Contact alice@example.com or bob@corp.co. SSN 123-45-6789. " +
		"Call (415) 555-0132 or 415.555.0199. Card 4111 1111 1111 1111. " +
		"Not a card 1234 5678 9012 3456."

	got := Detect(text)
	byKind := map[Kind]int{}
	for _, m := range got {
		byKind[m.Kind]++
	}
	if byKind[Email] != 2 {
		t.Errorf("emails = %d, want 2", byKind[Email])
	}
	if byKind[SSN] != 1 {
		t.Errorf("ssn = %d, want 1", byKind[SSN])
	}
	if byKind[Phone] < 2 {
		t.Errorf("phones = %d, want >= 2", byKind[Phone])
	}
	// Luhn: 4111 1111 1111 1111 is valid; 1234 5678 9012 3456 is not.
	if byKind[CreditCard] != 1 {
		t.Errorf("credit_card = %d, want 1 (Luhn must reject the invalid number)", byKind[CreditCard])
	}
}

func TestDetectFiltersByKind(t *testing.T) {
	text := "a@b.com and SSN 111-22-3333"
	got := Detect(text, SSN)
	if len(got) != 1 || got[0].Kind != SSN {
		t.Fatalf("filtered detect = %+v, want a single SSN", got)
	}
}

func TestLuhn(t *testing.T) {
	cases := []struct {
		s    string
		want bool
	}{
		{"4111 1111 1111 1111", true}, // Visa test number
		{"4111-1111-1111-1111", true}, // hyphen separators
		{"5500005555555559", true},    // Mastercard test number
		{"1234567890123456", false},   // fails Luhn
		{"411111111111111", false},    // 15 digits, fails Luhn
		{"abcd", false},               // non-digits
	}
	for _, c := range cases {
		if got := luhn(c.s); got != c.want {
			t.Errorf("luhn(%q) = %v, want %v", c.s, got, c.want)
		}
	}
}

func TestKindsFromStrings(t *testing.T) {
	got := KindsFromStrings([]string{"Email", "creditcard", "bogus", " ssn "})
	want := []Kind{Email, CreditCard, SSN}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("kind[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
