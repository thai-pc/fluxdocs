//go:build nostd14

// Package faces would embed the standard-14 substitute faces; this build
// (-tags nostd14) leaves them out, so non-embedded standard text advances
// by the AFM widths but draws nothing. See faces.go.
package faces

import "github.com/thai-pc/fluxdocs/core/internal/std14"

// Enabled reports whether the faces are compiled in (false with -tags
// nostd14).
const Enabled = false

// Data returns nil: no face is compiled in.
func Data(std14.Face) []byte { return nil }
