package parse

import (
	"reflect"
	"testing"
)

// joinSegments assembles streams the way PageContentSegments does for a
// /Contents array: each stream followed by a newline, the newlines between
// streams recorded as joins.
func joinSegments(streams ...string) ContentSegments {
	var c ContentSegments
	for i, s := range streams {
		c.Data = append(c.Data, s...)
		if i < len(streams)-1 {
			c.Joins = append(c.Joins, len(c.Data))
		}
		c.Data = append(c.Data, '\n')
	}
	return c
}

// TestScanContentStrict_Joins pins N5 (docs/design/redaction-hardening.md):
// a page's content streams may split only between tokens (ISO 32000-1
// §7.8.2). A token, comment or inline image across a join, or two tokens that
// plain concatenation would merge, is a loss — readers that concatenate, that
// separate and that parse each stream alone would read it differently.
func TestScanContentStrict_Joins(t *testing.T) {
	tests := []struct {
		name    string
		streams []string
		ok      bool
	}{
		{"one stream", []string{"BT /F1 12 Tf (SECRET-1234) Tj ET"}, true},
		{"split between operand and operator", []string{"BT /F1 12 Tf (SECRET-1234)", "Tj ET"}, true},
		{"split at white-space", []string{"BT /F1 12 Tf (x) ", " Tj ET"}, true},
		{"split inside an array, between elements", []string{"BT [(a) -20", "(b)] TJ ET"}, true},
		{"split after a delimiter", []string{"BT /F1 12 Tf [", "(a)] TJ ET"}, true},
		{"split before a name", []string{"BT", "/F1 12 Tf ET"}, true},
		{"split after a name before a delimiter", []string{"/Span", "<< /MCID 1 >> BDC EMC"}, true},
		{"comment ended before the join", []string{"BT %c\n", "/F1 12 Tf ET"}, true},
		{"comment in the next stream", []string{"BT", "%c\n/F1 12 Tf ET"}, true},
		{"empty stream between tokens", []string{"(x)", "", "Tj"}, true},
		{"inline image inside one stream", []string{"BI /W 1 /H 1 /BPC 8 /CS /G ID A EI", " Q"}, true},
		{"EI merged with the next stream's operator", []string{"BI /W 1 /H 1 /BPC 8 /CS /G ID A EI", "Q"}, false},

		{"split inside a string (repro split_string)", []string{"BT /F1 12 Tf (SECRET-12", "34) Tj ET"}, false},
		{"split inside a string at white-space", []string{"BT /F1 12 Tf (SECRET ", "1234) Tj ET"}, false},
		{"split inside a hex string", []string{"BT /F1 12 Tf <5345", "4352> Tj ET"}, false},
		{"split inside a keyword (repro split_keyword)", []string{"BT /F1 12 Tf (x) T", "j ET"}, false},
		{"split inside a name", []string{"BT /F", "1 12 Tf ET"}, false},
		{"split after '/'", []string{"BT /", "F1 12 Tf ET"}, false},
		{"split inside a number", []string{"BT /F1 1", "2 Tf ET"}, false},
		{"split inside a comment", []string{"BT /F1 12 Tf %c", "(SECRET) Tj ET"}, false},
		{"comment right before the join", []string{"BT /F1 12 Tf (x) Tj %", "ET"}, false},
		{"split inside '<<'", []string{"/Span <", "< /MCID 1 >> BDC EMC"}, false},
		{"split inside '>>'", []string{"/Span << /MCID 1 >", "> BDC EMC"}, false},
		{"split inside an inline image dictionary", []string{"BI /W 1 /H 1", "/BPC 8 /CS /G ID A EI"}, false},
		{"split inside inline image data", []string{"BI /W 2 /H 1 /BPC 8 /CS /G ID A", "B EI"}, false},
		{"split right after ID", []string{"BI /W 1 /H 1 /BPC 8 /CS /G ID", " A EI"}, false},
		{"split before EI", []string{"BI /W 1 /H 1 /BPC 8 /CS /G ID A ", "EI"}, false},
		{"empty stream inside a keyword", []string{"(x) T", "", "j"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := joinSegments(tc.streams...)
			spans, err := ScanContentStrict(c, StrictOptions{})
			if tc.ok != (err == nil) {
				t.Fatalf("ScanContentStrict err = %v, want ok=%v", err, tc.ok)
			}
			if err == nil {
				if want := ScanContentSpans(c.Data); !reflect.DeepEqual(spans, want) {
					t.Errorf("strict spans = %v, want the lenient spans %v", spans, want)
				}
			}
		})
	}
}

// TestScanContentStrict_InvalidJoins: joins that do not index the data in
// increasing order are an error, never a panic.
func TestScanContentStrict_InvalidJoins(t *testing.T) {
	data := []byte("(a) Tj\n(b) Tj\n")
	for _, joins := range [][]int{{-1}, {len(data)}, {6, 6}, {13, 6}} {
		if _, err := ScanContentStrict(ContentSegments{Data: data, Joins: joins}, StrictOptions{}); err == nil {
			t.Errorf("joins %v accepted", joins)
		}
	}
}

// TestPageContentSegments: the data is exactly PageContent's, and the joins
// are the separators between streams (none for a single stream or a one-
// element array; adjacent ones around an empty stream; one per repeat).
func TestPageContentSegments(t *testing.T) {
	a, b, empty := streamObj("(a) Tj"), streamObj("(b) Tj"), streamObj("")
	tests := []struct {
		name      string
		contents  string
		streams   []string
		wantJoins []int
	}{
		{"single stream", "4 0 R", []string{a}, nil},
		{"array of one", "[4 0 R]", []string{a}, []int{}},
		{"array of two", "[4 0 R 5 0 R]", []string{a, b}, []int{6}},
		{"empty stream between", "[4 0 R 5 0 R 6 0 R]", []string{a, empty, b}, []int{6, 7}},
		{"repeated stream", "[4 0 R 4 0 R 5 0 R]", []string{a, b}, []int{6, 13}},
		{"flate streams", "[4 0 R 5 0 R]", []string{flateStreamObj([]byte("(a) Tj")), flateStreamObj([]byte("(bb) Tj"))}, []int{6}},
		{"non-stream element skipped", "[4 0 R null 5 0 R]", []string{a, b}, []int{6}},
		{"no contents", "null", nil, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, pi := contentsPage(t, tc.contents, tc.streams...)
			want, err := r.PageContent(pi)
			if err != nil {
				t.Fatal(err)
			}
			got, err := r.PageContentSegments(pi)
			if err != nil {
				t.Fatal(err)
			}
			if string(got.Data) != string(want) {
				t.Errorf("Data = %q, want PageContent's %q", got.Data, want)
			}
			if len(got.Joins) != len(tc.wantJoins) || (len(got.Joins) > 0 && !reflect.DeepEqual(got.Joins, tc.wantJoins)) {
				t.Errorf("Joins = %v, want %v", got.Joins, tc.wantJoins)
			}
			for _, j := range got.Joins {
				if got.Data[j] != '\n' {
					t.Errorf("join %d is %q, want the newline separator", j, got.Data[j])
				}
			}
		})
	}
}

// TestPageContentSegments_Limit: the segments share PageContent's cap.
func TestPageContentSegments_Limit(t *testing.T) {
	big := flateStreamObj(make([]byte, MaxPageContent/2+1))
	r, pi := contentsPage(t, "[4 0 R 4 0 R]", big)
	if _, err := r.PageContentSegments(pi); err == nil {
		t.Errorf("content past MaxPageContent accepted")
	}
}
