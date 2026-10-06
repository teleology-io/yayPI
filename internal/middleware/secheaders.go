package middleware

import (
	"net/http"
	"strconv"
)

// SecurityHeaders sets conservative defaults for a JSON API: no MIME sniffing, no
// framing, no referrer leakage, and a CSP that forbids loading anything should a
// response ever be rendered as a document. hstsMaxAge > 0 adds Strict-Transport-Security
// (enable it when the API is only reachable over HTTPS, including behind a TLS proxy).
func SecurityHeaders(hstsMaxAge int) func(http.Handler) http.Handler {
	hsts := ""
	if hstsMaxAge > 0 {
		hsts = "max-age=" + strconv.Itoa(hstsMaxAge) + "; includeSubDomains"
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("X-Frame-Options", "DENY")
			h.Set("Referrer-Policy", "no-referrer")
			h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
			h.Set("Cross-Origin-Resource-Policy", "same-site")
			if hsts != "" {
				h.Set("Strict-Transport-Security", hsts)
			}
			next.ServeHTTP(w, r)
		})
	}
}
