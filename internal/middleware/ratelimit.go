package middleware

import (
	"math"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// tokenBucket is a simple token bucket rate limiter per key.
type tokenBucket struct {
	mu       sync.Mutex
	tokens   float64
	lastTime time.Time
}

// take refills the bucket and tries to consume one token. It returns whether the request
// is allowed, the tokens left, and how long until the next token when denied.
func (tb *tokenBucket) take(now time.Time, max, rate float64) (bool, float64, time.Duration) {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	tb.tokens = math.Min(max, tb.tokens+now.Sub(tb.lastTime).Seconds()*rate)
	tb.lastTime = now

	if tb.tokens >= 1 {
		tb.tokens--
		return true, tb.tokens, 0
	}
	wait := time.Duration((1 - tb.tokens) / rate * float64(time.Second))
	return false, tb.tokens, wait
}

func (tb *tokenBucket) idleSince(now time.Time) time.Duration {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	return now.Sub(tb.lastTime)
}

// RateLimiter is a keyed token-bucket rate limiter middleware. Keys are the client IP
// (see ClientIP) or, with KeyByUser, the authenticated subject ID.
type RateLimiter struct {
	buckets   sync.Map // key → *tokenBucket
	max       float64
	rate      float64
	keyByUser bool
	idleTTL   time.Duration // a bucket idle this long is full again; safe to drop
	lastSweep atomic.Int64  // unix nanos
}

// NewRateLimiter creates a RateLimiter allowing bursts of maxRequests, refilling at
// ratePerSecond. keyBy is "ip" (default) or "user" (subject ID, falling back to IP for
// anonymous callers — only meaningful when mounted after auth middleware).
func NewRateLimiter(maxRequests int, ratePerSecond float64, keyBy string) *RateLimiter {
	rl := &RateLimiter{
		max:       float64(maxRequests),
		rate:      ratePerSecond,
		keyByUser: keyBy == "user",
	}
	rl.idleTTL = time.Duration(rl.max/rl.rate*float64(time.Second)) + time.Minute
	rl.lastSweep.Store(time.Now().UnixNano())
	return rl
}

// KeyByUser reports whether the limiter keys on the authenticated subject.
func (rl *RateLimiter) KeyByUser() bool { return rl.keyByUser }

func (rl *RateLimiter) key(r *http.Request) string {
	if rl.keyByUser {
		if sub := GetSubject(r); sub != nil && sub.ID != "" {
			return "u:" + sub.ID
		}
	}
	return "ip:" + GetClientIP(r)
}

// sweep drops buckets idle long enough to have fully refilled, so spoofed or one-off
// keys cannot grow memory without bound. Runs at most once per idleTTL, inline.
func (rl *RateLimiter) sweep(now time.Time) {
	last := rl.lastSweep.Load()
	if now.UnixNano()-last < int64(rl.idleTTL) || !rl.lastSweep.CompareAndSwap(last, now.UnixNano()) {
		return
	}
	rl.buckets.Range(func(k, v any) bool {
		if v.(*tokenBucket).idleSince(now) > rl.idleTTL {
			rl.buckets.Delete(k)
		}
		return true
	})
}

// Allow consumes a token for key. Exposed for non-HTTP limiters (e.g. login throttling).
func (rl *RateLimiter) Allow(key string) (bool, float64, time.Duration) {
	now := time.Now()
	rl.sweep(now)
	v, _ := rl.buckets.LoadOrStore(key, &tokenBucket{tokens: rl.max, lastTime: now})
	return v.(*tokenBucket).take(now, rl.max, rl.rate)
}

// Handler returns the rate-limiting middleware.
func (rl *RateLimiter) Handler(next http.Handler) http.Handler {
	limit := strconv.Itoa(int(rl.max))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ok, remaining, wait := rl.Allow(rl.key(r))
		w.Header().Set("X-RateLimit-Limit", limit)
		w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(int(remaining)))
		if !ok {
			w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
			writeJSONError(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// BucketCount returns the number of live buckets (for tests and metrics).
func (rl *RateLimiter) BucketCount() int {
	n := 0
	rl.buckets.Range(func(_, _ any) bool { n++; return true })
	return n
}
