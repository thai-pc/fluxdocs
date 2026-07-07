package api

import (
	"net/http"
	"sync"
	"time"
)

// ratelimit.go implements per-org request throttling with a token-bucket
// limiter. Each org gets its own bucket, so one tenant's traffic can't starve
// another's (fairness on top of the isolation in SECURITY.md §4). The clock is
// injectable so refill behavior is testable without sleeping.

// RateLimiter is a per-key token-bucket limiter. It is safe for concurrent use.
type RateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*tokenBucket
	rate    float64 // tokens added per second
	burst   float64 // bucket capacity (max tokens)
	now     func() time.Time
}

type tokenBucket struct {
	tokens float64
	last   time.Time
}

// NewRateLimiter returns a limiter that refills at rate tokens/second up to a
// capacity of burst tokens (each request costs one token).
func NewRateLimiter(rate, burst float64) *RateLimiter {
	if burst < 1 {
		burst = 1
	}
	return &RateLimiter{
		buckets: map[string]*tokenBucket{},
		rate:    rate,
		burst:   burst,
		now:     time.Now,
	}
}

// Allow reports whether a request for key may proceed, consuming a token if so.
func (rl *RateLimiter) Allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := rl.now()
	b := rl.buckets[key]
	if b == nil {
		b = &tokenBucket{tokens: rl.burst, last: now}
		rl.buckets[key] = b
	}
	// Lazily refill based on elapsed time, capped at burst.
	b.tokens += now.Sub(b.last).Seconds() * rl.rate
	if b.tokens > rl.burst {
		b.tokens = rl.burst
	}
	b.last = now

	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}

// rateLimit is middleware that throttles per authenticated org. It is a no-op
// when no limiter is configured, and must run after authenticate so the org is
// known.
func (s *Server) rateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.limiter != nil && !s.limiter.Allow(orgID(r.Context())) {
			w.Header().Set("Retry-After", "1")
			writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}
		next.ServeHTTP(w, r)
	})
}
