package middleware

import (
	"context"
	"net/http"

	"github.com/google/uuid"
)

const RequestIDHeader = "X-Request-ID"

// ctxKey is a private type for context keys in this package.
type ctxKey int

const (
	ctxKeyRequestID ctxKey = iota
	ctxKeySubject
	ctxKeyClientIP
)

// RequestID injects a unique request ID into each request's context and response headers.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(RequestIDHeader)
		if !validRequestID(id) {
			id = uuid.New().String()
		}
		w.Header().Set(RequestIDHeader, id)
		ctx := context.WithValue(r.Context(), ctxKeyRequestID, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequestIDFromContext retrieves the request ID from a context.
func RequestIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(ctxKeyRequestID).(string)
	return id
}

// GetRequestID retrieves the request ID from the context.
func GetRequestID(r *http.Request) string {
	if id, ok := r.Context().Value(ctxKeyRequestID).(string); ok {
		return id
	}
	return ""
}

// validRequestID accepts a client-supplied request ID only if it is short and limited to
// [A-Za-z0-9._:-], so it can't inject log lines or bloat headers.
func validRequestID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '_', c == ':', c == '-':
		default:
			return false
		}
	}
	return true
}
