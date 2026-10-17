//go:build unix

package core

import (
	"syscall"
	"time"
)

// cpuTime is the process's user plus system CPU time, or 0 when it cannot be
// read.
func cpuTime() time.Duration {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}
