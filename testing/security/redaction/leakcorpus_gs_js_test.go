//go:build js

package redaction

import "testing"

// gsText reports the Ghostscript oracle as absent: exec is unavailable under
// js/wasm, so the WASM job runs the leak corpus without it (§12.3).
func gsText(*testing.T, []byte) (string, bool) { return "", false }
