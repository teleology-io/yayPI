package middleware

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// tokenBucket is a simple token bucket rate limiter per key.
type tokenBucket struct {
	mu       sync.Mutex
	tokens   float64
	max      float64
	rate     float64 // tokens per second
	lastTime time.Time
}

func newTokenBucket(max float64, rate float64) *tokenBucket {
	return &tokenBucket{
		tokens:   max,
		max:      max,
		rate:     rate,
		lastTime: time.Now(),
	}
}

// Allow returns true if a request is permitted, false if rate-limited.
func (tb *tokenBucket) Allow() bool {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(tb.lastTime).Seconds()
	tb.lastTime = now

	tb.tokens += elapsed * tb.rate
	if tb.tokens > tb.max {
		tb.tokens = tb.max
	}

	if tb.tokens >= 1 {
		tb.tokens--
		return true
	}
	return false
}

// RateLimiter is an IP-keyed sliding-window rate limiter middleware.
type RateLimiter struct {
	buckets sync.Map
	max     float64
	rate    float64
}

// NewRateLimiter creates a RateLimiter allowing max requests, refilling at rate per second.
func NewRateLimiter(maxRequests int, ratePerSecond float64) *RateLimiter {
	return &RateLimiter{
		max:  float64(maxRequests),
		rate: ratePerSecond,
	}
}

// Handler returns the rate-limiting middleware.
func (rl *RateLimiter) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := extractIP(r)

		bucketVal, _ := rl.buckets.LoadOrStore(ip, newTokenBucket(rl.max, rl.rate))
		bucket := bucketVal.(*tokenBucket)

		if !bucket.Allow() {
			w.Header().Set("Retry-After", "1")
			writeJSONError(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}

		next.ServeHTTP(w, r)
	})
}

// extractIP extracts the client IP from the request.
func extractIP(r *http.Request) string {
	// Check common proxy headers first
	for _, header := range []string{"X-Real-IP", "X-Forwarded-For"} {
		if ip := r.Header.Get(header); ip != "" {
			// Take only the first IP if comma-separated
			if idx := len(ip); idx > 0 {
				return ip
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
