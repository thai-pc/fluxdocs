//go:build unix

package render

import (
	"syscall"
	"time"
)

// cpuTime returns the process's CPU time (user + system). BenchmarkCalibration
// times with it so a loaded host, which delays a benchmark but does not make it
// use more CPU, skews the cost ratios less than wall-clock time would.
func cpuTime() time.Duration {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return time.Duration(time.Now().UnixNano())
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}
