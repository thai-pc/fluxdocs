package extract

import "testing"

// TestCollectRunsTJ covers the TJ (positioned-array) show operator, which the
// core-level tests don't exercise, plus grid detection over those runs.
func TestCollectRunsTJ(t *testing.T) {
	// Two rows, two columns, each cell shown via a TJ array.
	content := []byte(
		"BT /F1 10 Tf " +
			"1 0 0 1 72 700 Tm [(Item)] TJ " +
			"1 0 0 1 300 700 Tm [(Price)] TJ " +
			"1 0 0 1 72 680 Tm [(Pen)] TJ " +
			"1 0 0 1 300 680 Tm [(1)-250(.99)] TJ " +
			"ET")

	runs := collectRuns(content, nil)
	if len(runs) != 4 {
		t.Fatalf("got %d runs, want 4: %+v", len(runs), runs)
	}
	// The negative adjustment (-250) in the last TJ inserts a space.
	if runs[3].text != "1 .99" {
		t.Errorf("TJ run text = %q, want %q", runs[3].text, "1 .99")
	}

	tbl, ok := detectTable(runs)
	if !ok {
		t.Fatal("expected a table from the 2x2 grid")
	}
	if len(tbl.Rows) != 2 || tbl.Rows[0][0] != "Item" || tbl.Rows[1][1] != "1 .99" {
		t.Errorf("grid = %v", tbl.Rows)
	}
}
