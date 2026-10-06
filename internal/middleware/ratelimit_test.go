package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestResolveClientIPIgnoresHeadersFromUntrustedPeer(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.9:1234"
	req.Header.Set("X-Forwarded-For", "1.2.3.4")
	req.Header.Set("X-Real-IP", "5.6.7.8")
	if got := ResolveClientIP(req, nil); got != "203.0.113.9" {
		t.Fatalf("got %s", got)
	}
}

func TestResolveClientIPWalksXFFFromRight(t *testing.T) {
	trusted, err := ParseTrustedProxies([]string{"10.0.0.0/8"})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.2:1234"
	// Client spoofed "1.1.1.1"; the real client is 198.51.100.7, appended by our proxies.
	req.Header.Set("X-Forwarded-For", "1.1.1.1, 198.51.100.7, 10.0.0.5")
	if got := ResolveClientIP(req, trusted); got != "198.51.100.7" {
		t.Fatalf("got %s", got)
	}
}

func TestRateLimiterSpoofedHeaderDoesNotBypass(t *testing.T) {
	rl := NewRateLimiter(1, 0.001, "ip")
	h := ClientIP(nil)(rl.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})))
	codes := []int{}
	for i, xff := range []string{"1.1.1.1", "2.2.2.2"} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "203.0.113.9:1"
		req.Header.Set("X-Forwarded-For", xff)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		codes = append(codes, rr.Code)
		_ = i
	}
	if codes[1] != http.StatusTooManyRequests {
		t.Fatalf("second request should be limited despite new XFF, got %v", codes)
	}
}

func TestRateLimiterEvictsIdleBuckets(t *testing.T) {
	rl := NewRateLimiter(1, 1000, "ip") // refills instantly → idleTTL ≈ 1m
	rl.Allow("a")
	rl.Allow("b")
	if rl.BucketCount() != 2 {
		t.Fatalf("buckets = %d", rl.BucketCount())
	}
	// Age the buckets and the last sweep past the TTL, then trigger a sweep.
	past := time.Now().Add(-2 * rl.idleTTL)
	rl.buckets.Range(func(_, v any) bool { v.(*tokenBucket).lastTime = past; return true })
	rl.lastSweep.Store(past.UnixNano())
	rl.Allow("c")
	if rl.BucketCount() != 1 {
		t.Fatalf("idle buckets not evicted, have %d", rl.BucketCount())
	}
}

func TestValidRequestID(t *testing.T) {
	if !validRequestID("abc-123_x.y:z") || validRequestID("bad\nid") || validRequestID("") {
		t.Fatal("request ID validation wrong")
	}
}
