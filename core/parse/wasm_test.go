//go:build js && wasm

package parse

// The WASM target runs the scanner a few times slower than native; timing
// bounds in tests scale by timingScale (see race_test.go).
func init() { timingScale = 5 }
