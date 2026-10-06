package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func corsRequest(t *testing.T, opts CORSOptions, origin string, preflight bool) *httptest.ResponseRecorder {
	t.Helper()
	h := CORS(opts)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	method := http.MethodGet
	if preflight {
		method = http.MethodOptions
	}
	req := httptest.NewRequest(method, "/x", nil)
	req.Header.Set("Origin", origin)
	if preflight {
		req.Header.Set("Access-Control-Request-Method", "POST")
		req.Header.Set("Access-Control-Request-Headers", "X-Evil")
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func TestCORSWildcardNeverSendsCredentials(t *testing.T) {
	rr := corsRequest(t, CORSOptions{AllowedOrigins: []string{"*"}}, "https://evil.example", false)
	if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("ACAO = %q, want *", got)
	}
	if rr.Header().Get("Access-Control-Allow-Credentials") != "" {
		t.Fatal("wildcard must not allow credentials")
	}
}

func TestCORSExplicitOriginGetsCredentials(t *testing.T) {
	opts := CORSOptions{AllowedOrigins: []string{"*", "https://app.example"}}
	rr := corsRequest(t, opts, "https://app.example", false)
	if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example" {
		t.Fatalf("ACAO = %q", got)
	}
	if rr.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Fatal("explicit origin should allow credentials")
	}
}

func TestCORSUnknownOriginGetsNothing(t *testing.T) {
	rr := corsRequest(t, CORSOptions{AllowedOrigins: []string{"https://app.example"}}, "https://evil.example", false)
	if rr.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("unlisted origin must not be allowed")
	}
}

func TestCORSPreflightDoesNotEchoRequestedHeaders(t *testing.T) {
	rr := corsRequest(t, CORSOptions{AllowedOrigins: []string{"https://app.example"}}, "https://app.example", true)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status %d", rr.Code)
	}
	if h := rr.Header().Get("Access-Control-Allow-Headers"); h == "" || contains(h, "X-Evil") {
		t.Fatalf("Allow-Headers = %q", h)
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
