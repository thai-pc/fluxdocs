//go:build race

package parse

// The race detector slows the scanner several-fold. timingScale is unused
// (robust_test.go): the timing gates are relative (internal/reltime).
func init() { timingScale = 10 }
