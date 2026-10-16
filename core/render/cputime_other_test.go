//go:build !unix

package render

import "time"

var cpuEpoch = time.Now()

// cpuTime falls back to monotonic wall-clock time where the process CPU time
// is not available (js/wasm); see cputime_unix_test.go.
func cpuTime() time.Duration { return time.Since(cpuEpoch) }
