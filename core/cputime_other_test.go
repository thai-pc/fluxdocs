//go:build !unix

package core

import "time"

// cpuTime is not available on this target.
func cpuTime() time.Duration { return 0 }
