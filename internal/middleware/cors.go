package middleware

import (
	"net/http"
	"slices"
	"strings"
)

// CORS returns a middleware that adds CORS headers and handles preflight requests.
// allowedOrigins is the list of permitted origins; pass ["*"] to allow all.
// If the list is empty, no CORS headers are added.
func CORS(allowedOrigins []string) func(http.Handler) http.Handler {
	wildcard := slices.Contains(allowedOrigins, "*")

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin == "" || len(allowedOrigins) == 0 {
				next.ServeHTTP(w, r)
				return
			}

			allowed := wildcard || slices.Contains(allowedOrigins, origin)
			if !allowed {
				next.ServeHTTP(w, r)
				return
			}

			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Vary", "Origin")

			if r.Method == http.MethodOptions {
				// Preflight
				requestedMethod := r.Header.Get("Access-Control-Request-Method")
				if requestedMethod == "" {
					next.ServeHTTP(w, r)
					return
				}

				requestedHeaders := r.Header.Get("Access-Control-Request-Headers")
				allowMethods := "GET, POST, PUT, PATCH, DELETE, OPTIONS"
				allowHeaders := "Authorization, Content-Type, Accept"
				if requestedHeaders != "" {
					// Echo back the requested headers rather than maintaining a hard-coded list
					allowHeaders = strings.Join([]string{allowHeaders, requestedHeaders}, ", ")
				}

				w.Header().Set("Access-Control-Allow-Methods", allowMethods)
				w.Header().Set("Access-Control-Allow-Headers", allowHeaders)
				w.Header().Set("Access-Control-Max-Age", "86400")
				w.WriteHeader(http.StatusNoContent)
				return
			}

			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Accept")
			next.ServeHTTP(w, r)
		})
	}
}
