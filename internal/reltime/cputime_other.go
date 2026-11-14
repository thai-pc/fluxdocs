//go:build !unix

package reltime

import "time"

func cpuTime() time.Duration { return wallNow() }
