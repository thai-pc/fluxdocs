// Package jsopt converts JavaScript numbers (float64) from WASM call options into
// Go integers and durations with explicit range checks. Go leaves the conversion
// of an out-of-range float to an integer implementation-defined (it saturates
// under js/wasm and yields MinInt64 on amd64), so a raw cast of an untrusted
// option is never used. Untagged so it is tested by the native CI.
package jsopt

import (
	"math"
	"time"
)

// Int64 converts a JS number to an int64, truncating toward zero. ok is false
// for NaN, ±Inf, or a value outside the int64 range; callers then ignore the
// option (use the default).
func Int64(f float64) (int64, bool) {
	// -2^63 is exactly representable; 2^63 is the first value past MaxInt64.
	// NaN fails both comparisons.
	if !(f >= -(1<<63) && f < 1<<63) {
		return 0, false
	}
	return int64(f), true
}

// Int converts a JS number to an int, truncating toward zero, like Int64. ok is
// false for NaN, ±Inf, or a value outside the int range; callers then reject
// the argument.
func Int(f float64) (int, bool) {
	n, ok := Int64(f)
	if !ok || n < math.MinInt || n > math.MaxInt {
		return 0, false
	}
	return int(n), true
}

// TimeoutMs converts a JS millisecond count to a positive duration. ok is false
// unless ms is finite and > 0 and the duration fits time.Duration (≈ 292 years);
// callers then apply no deadline. A fraction of a millisecond is kept down to
// the nanosecond; a positive ms that rounds to 0 ns becomes 1 ns.
func TimeoutMs(ms float64) (time.Duration, bool) {
	if !(ms > 0) || math.IsInf(ms, 0) {
		return 0, false
	}
	ns := ms * float64(time.Millisecond)
	if !(ns < 1<<63) {
		return 0, false
	}
	d := time.Duration(ns)
	if d < 1 {
		d = 1
	}
	return d, true
}
