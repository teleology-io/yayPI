package middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBodyLimitRejectsDeclaredOversize(t *testing.T) {
	h := BodyLimit(10)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not run")
	}))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(strings.Repeat("x", 11))))
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status %d", rr.Code)
	}
}

func TestBodyLimitCapsStreamedBody(t *testing.T) {
	var readErr error
	h := BodyLimit(10)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, readErr = io.ReadAll(r.Body)
	}))
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(strings.Repeat("x", 50)))
	req.ContentLength = -1 // chunked / unknown length
	h.ServeHTTP(httptest.NewRecorder(), req)
	if !IsBodyTooLarge(readErr) {
		t.Fatalf("expected MaxBytesError, got %v", readErr)
	}
}
