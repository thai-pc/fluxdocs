package jsopt

import (
	"math"
	"math/bits"
	"testing"
	"time"
)

func TestInt64(t *testing.T) {
	tests := []struct {
		in     float64
		want   int64
		wantOK bool
	}{
		{0, 0, true},
		{-1, -1, true},
		{1e9, 1e9, true},
		{2.9, 2, true},   // truncates toward zero
		{-2.9, -2, true}, // truncates toward zero
		{-(1 << 63), math.MinInt64, true},
		{1 << 63, 0, false}, // first value past MaxInt64
		{math.MaxInt64, 0, false},
		{1e300, 0, false},
		{-1e300, 0, false},
		{math.Inf(1), 0, false},
		{math.Inf(-1), 0, false},
		{math.NaN(), 0, false},
	}
	for _, tc := range tests {
		got, ok := Int64(tc.in)
		if got != tc.want || ok != tc.wantOK {
			t.Errorf("Int64(%g) = %d, %v; want %d, %v", tc.in, got, ok, tc.want, tc.wantOK)
		}
	}
}

func TestInt(t *testing.T) {
	pastMaxInt := math.Ldexp(1, bits.UintSize-1) // 2^63 (2^31 on 32-bit targets)
	tests := []struct {
		in     float64
		want   int
		wantOK bool
	}{
		{0, 0, true},
		{150, 150, true},
		{-1, -1, true},
		{2.9, 2, true}, // truncates toward zero
		{-pastMaxInt, math.MinInt, true},
		{pastMaxInt, 0, false},
		{1e300, 0, false},
		{-1e300, 0, false},
		{math.Inf(1), 0, false},
		{math.Inf(-1), 0, false},
		{math.NaN(), 0, false},
	}
	for _, tc := range tests {
		got, ok := Int(tc.in)
		if got != tc.want || ok != tc.wantOK {
			t.Errorf("Int(%g) = %d, %v; want %d, %v", tc.in, got, ok, tc.want, tc.wantOK)
		}
	}
}

func TestTimeoutMs(t *testing.T) {
	tests := []struct {
		in     float64
		want   time.Duration
		wantOK bool
	}{
		{200, 200 * time.Millisecond, true},
		{0.5, 500 * time.Microsecond, true},
		{1e-9, 1, true}, // rounds to 0 ns: kept as the smallest positive deadline
		{0, 0, false},
		{-5, 0, false},
		{math.NaN(), 0, false},
		{math.Inf(1), 0, false},
		{1e13, 0, false}, // ≈ 317 years: past time.Duration
		{9e12, 9e12 * time.Millisecond, true},
	}
	for _, tc := range tests {
		got, ok := TimeoutMs(tc.in)
		if got != tc.want || ok != tc.wantOK {
			t.Errorf("TimeoutMs(%g) = %v, %v; want %v, %v", tc.in, got, ok, tc.want, tc.wantOK)
		}
	}
}
