//go:build js && wasm

package parse

// The WASM target runs the scanner a few times slower than native.
// timingScale is unused (robust_test.go): the timing gates are relative
// (internal/reltime).
func init() { timingScale = 5 }
