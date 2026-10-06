package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
)

type traceKey struct{}

// TraceContext holds W3C trace-context identifiers for a request.
type TraceContext struct {
	TraceID string // 32 hex
	SpanID  string // 16 hex, this server's span
	Parent  string // caller's span id, "" when we started the trace
}

// Trace honours an incoming W3C `traceparent` header (continuing the caller's trace) or
// starts a new trace, stores it in the context for logs, and echoes the trace id as
// X-Trace-Id. This gives end-to-end correlation through load balancers and services
// that speak trace-context, without an OpenTelemetry SDK dependency.
func Trace(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tc := TraceContext{SpanID: randHex(8)}
		if tid, pid, ok := parseTraceparent(r.Header.Get("traceparent")); ok {
			tc.TraceID, tc.Parent = tid, pid
		} else {
			tc.TraceID = randHex(16)
		}
		w.Header().Set("X-Trace-Id", tc.TraceID)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), traceKey{}, tc)))
	})
}

// GetTrace returns the request's trace context (zero value if none).
func GetTrace(ctx context.Context) TraceContext {
	tc, _ := ctx.Value(traceKey{}).(TraceContext)
	return tc
}

// Traceparent renders a header value for outbound calls made while handling ctx.
func Traceparent(ctx context.Context) string {
	tc := GetTrace(ctx)
	if tc.TraceID == "" {
		return ""
	}
	return "00-" + tc.TraceID + "-" + tc.SpanID + "-01"
}

func parseTraceparent(h string) (traceID, parentID string, ok bool) {
	parts := strings.Split(strings.TrimSpace(h), "-")
	if len(parts) < 4 || len(parts[0]) != 2 || parts[0] == "ff" || len(parts[1]) != 32 || len(parts[2]) != 16 {
		return "", "", false
	}
	if !isHex(parts[1]) || !isHex(parts[2]) || strings.Trim(parts[1], "0") == "" || strings.Trim(parts[2], "0") == "" {
		return "", "", false
	}
	return strings.ToLower(parts[1]), strings.ToLower(parts[2]), true
}

func isHex(s string) bool {
	_, err := hex.DecodeString(s)
	return err == nil
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
