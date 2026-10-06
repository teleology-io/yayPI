package middleware

import (
	"context"
	"net/http"
	"time"
)

// Timeout sets a deadline on the request context. Database calls made with the request
// context are cancelled when it passes, so a slow query cannot hold a connection past
// the point the client has been answered (handlers map the error to 504).
func Timeout(d time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), d)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
