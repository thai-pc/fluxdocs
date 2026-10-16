//go:build unix

package dos

import (
	"syscall"
	"time"
)

// cpuTime returns the process's CPU time (user + system). The gates time renders
// with it so a loaded host, which delays a render but does not make it use more
// CPU, skews their ratios less than wall-clock time would.
func cpuTime() time.Duration {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return time.Duration(time.Now().UnixNano())
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}
