//go:build unix

package reltime

import (
	"syscall"
	"time"
)

func cpuTime() time.Duration {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return wallNow()
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}
