package middleware

import (
	"context"
	"net/http"
)

// APIKeyLookup resolves an API key string to a Subject.
// Returns nil if the key is not found or is invalid.
type APIKeyLookup func(ctx context.Context, key string) *Subject

// APIKeyAuth returns a middleware that authenticates requests via an API key.
//
// The key is read from the specified header (default: X-API-Key) and, if
// queryParam is non-empty, also from the query string as a fallback.
//
//   - Key present and valid  → Subject set in context, next called.
//   - Key present but invalid → 401 immediately.
//   - Key absent             → pass through (a downstream middleware such as
//     RequireAuth will enforce authentication if needed).
func APIKeyAuth(header, queryParam string, lookup APIKeyLookup) func(http.Handler) http.Handler {
	if header == "" {
		header = "X-API-Key"
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := r.Header.Get(header)
			if key == "" && queryParam != "" {
				key = r.URL.Query().Get(queryParam)
			}
			if key == "" {
				next.ServeHTTP(w, r)
				return
			}
			sub := lookup(r.Context(), key)
			if sub == nil {
				writeJSONError(w, http.StatusUnauthorized, "invalid API key")
				return
			}
			ctx := context.WithValue(r.Context(), ctxKeySubject, sub)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
