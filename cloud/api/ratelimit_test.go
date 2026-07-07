package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRateLimiterBurstThenBlock(t *testing.T) {
	rl := NewRateLimiter(1, 3) // 3 burst, 1/sec refill
	now := time.Now()
	rl.now = func() time.Time { return now } // frozen clock: no refill

	for i := 0; i < 3; i++ {
		if !rl.Allow("org-a") {
			t.Fatalf("request %d should be allowed within burst", i+1)
		}
	}
	if rl.Allow("org-a") {
		t.Fatal("4th request should be blocked (burst exhausted)")
	}
}

func TestRateLimiterRefills(t *testing.T) {
	rl := NewRateLimiter(2, 2) // 2/sec, burst 2
	now := time.Now()
	rl.now = func() time.Time { return now }

	rl.Allow("org-a")
	rl.Allow("org-a")
	if rl.Allow("org-a") {
		t.Fatal("should be blocked before refill")
	}
	// Advance 1s -> +2 tokens.
	now = now.Add(time.Second)
	if !rl.Allow("org-a") || !rl.Allow("org-a") {
		t.Fatal("should be allowed after refill")
	}
	if rl.Allow("org-a") {
		t.Fatal("refill is capped at burst")
	}
}

func TestRateLimiterPerKey(t *testing.T) {
	rl := NewRateLimiter(1, 1)
	now := time.Now()
	rl.now = func() time.Time { return now }

	if !rl.Allow("org-a") || rl.Allow("org-a") {
		t.Fatal("org-a should get exactly one token")
	}
	// org-b has its own bucket, unaffected by org-a.
	if !rl.Allow("org-b") {
		t.Fatal("org-b should be allowed (separate bucket)")
	}
}

// TestRateLimitMiddleware verifies the throttle returns 429 + Retry-After once
// an org's burst is spent, while a different org is unaffected.
func TestRateLimitMiddleware(t *testing.T) {
	keys := NewMemKeyStore()
	keys.Add(HashKey("key-a"), "org-a")
	keys.Add(HashKey("key-b"), "org-b")
	s := New(NewMemDocumentStore(), keys, NewMemShareStore(), nil, nil)
	s.UseRateLimit(1, 2) // burst 2
	now := time.Now()
	s.limiter.now = func() time.Time { return now } // freeze so refill can't help

	// First two requests from org-a pass; the third is throttled.
	get := func(key string) (int, string) {
		req := httptest.NewRequest(http.MethodGet, "/v1/documents/none", nil)
		req.Header.Set("X-Api-Key", key)
		rr := httptest.NewRecorder()
		s.Handler().ServeHTTP(rr, req)
		return rr.Code, rr.Header().Get("Retry-After")
	}
	if code, _ := get("key-a"); code == http.StatusTooManyRequests {
		t.Fatal("first request should not be throttled")
	}
	get("key-a")
	code, retry := get("key-a")
	if code != http.StatusTooManyRequests {
		t.Fatalf("third request code = %d, want 429", code)
	}
	if retry == "" {
		t.Error("429 should carry a Retry-After header")
	}
	// org-b has its own bucket.
	if code, _ := get("key-b"); code == http.StatusTooManyRequests {
		t.Error("org-b should not be throttled by org-a's traffic")
	}
}
