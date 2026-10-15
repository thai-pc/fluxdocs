//go:build race

package parse

// The race detector slows the scanner several-fold; timing bounds in tests
// scale by timingScale so they still catch recursion or quadratic work.
func init() { timingScale = 10 }
