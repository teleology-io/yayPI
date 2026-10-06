package middleware

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
)

// DefaultCORSHeaders are the request headers allowed on cross-origin calls when the
// config does not list its own.
var DefaultCORSHeaders = []string{"Authorization", "Content-Type", "Accept", "X-API-Key", "X-Request-ID", "If-Match", "Idempotency-Key"}

// DefaultCORSMethods are the methods allowed on cross-origin calls when the config does
// not list its own.
var DefaultCORSMethods = []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"}

// CORSOptions configures the CORS middleware.
type CORSOptions struct {
	// AllowedOrigins is the list of permitted origins; ["*"] allows any origin but
	// never with credentials. Empty disables CORS headers entirely.
	AllowedOrigins []string
	AllowedHeaders []string // default: DefaultCORSHeaders
	AllowedMethods []string // default: DefaultCORSMethods
	ExposedHeaders []string // response headers readable by browser JS
	MaxAge         int      // preflight cache seconds (default: 600)
}

// CORS returns a middleware that adds CORS headers and handles preflight requests.
//
// Credentialed requests (cookies, Authorization from fetch credentials: "include") are
// only allowed for explicitly listed origins. A "*" entry answers with a literal
// `Access-Control-Allow-Origin: *` and no Allow-Credentials header, so a wildcard can
// never be combined with credentials — browsers forbid that pairing for good reason.
func CORS(opts CORSOptions) func(http.Handler) http.Handler {
	wildcard := slices.Contains(opts.AllowedOrigins, "*")
	headers := opts.AllowedHeaders
	if len(headers) == 0 {
		headers = DefaultCORSHeaders
	}
	methods := opts.AllowedMethods
	if len(methods) == 0 {
		methods = DefaultCORSMethods
	}
	maxAge := opts.MaxAge
	if maxAge <= 0 {
		maxAge = 600
	}
	allowHeaders := strings.Join(headers, ", ")
	allowMethods := strings.Join(methods, ", ")
	exposeHeaders := strings.Join(opts.ExposedHeaders, ", ")

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin == "" || len(opts.AllowedOrigins) == 0 {
				next.ServeHTTP(w, r)
				return
			}
			w.Header().Add("Vary", "Origin")

			explicit := slices.Contains(opts.AllowedOrigins, origin)
			if !explicit && !wildcard {
				next.ServeHTTP(w, r)
				return
			}

			if explicit {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Credentials", "true")
			} else {
				w.Header().Set("Access-Control-Allow-Origin", "*")
			}
			if exposeHeaders != "" {
				w.Header().Set("Access-Control-Expose-Headers", exposeHeaders)
			}

			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				w.Header().Set("Access-Control-Allow-Methods", allowMethods)
				w.Header().Set("Access-Control-Allow-Headers", allowHeaders)
				w.Header().Set("Access-Control-Max-Age", strconv.Itoa(maxAge))
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
