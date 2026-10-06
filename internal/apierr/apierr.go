// Package apierr writes yayPi's single JSON error envelope:
//
//	{"error": "human message", "code": "machine_code", "request_id": "…", "errors": {"field": "msg"}}
//
// "error" stays a plain string so existing clients that read it keep working; "code" is
// stable and meant for programmatic handling; "errors" is present for validation failures.
package apierr

import (
	"encoding/json"
	"net/http"
)

// Body is the error response shape.
type Body struct {
	Error     string            `json:"error"`
	Code      string            `json:"code"`
	RequestID string            `json:"request_id,omitempty"`
	Errors    map[string]string `json:"errors,omitempty"`
}

// Stable error codes.
const (
	CodeBadRequest       = "bad_request"
	CodeValidation       = "validation_failed"
	CodeUnauthorized     = "unauthorized"
	CodeForbidden        = "forbidden"
	CodeNotFound         = "not_found"
	CodeMethodNotAllowed = "method_not_allowed"
	CodeConflict         = "conflict"
	CodePrecondition     = "precondition_failed"
	CodeTooLarge         = "payload_too_large"
	CodeUnsupportedMedia = "unsupported_media_type"
	CodeUnprocessable    = "unprocessable"
	CodeRateLimited      = "rate_limited"
	CodeInternal         = "internal_error"
	CodeUnavailable      = "service_unavailable"
	CodeTimeout          = "timeout"
)

// CodeFor returns the default code for an HTTP status.
func CodeFor(status int) string {
	switch status {
	case http.StatusBadRequest:
		return CodeBadRequest
	case http.StatusUnauthorized:
		return CodeUnauthorized
	case http.StatusForbidden:
		return CodeForbidden
	case http.StatusNotFound:
		return CodeNotFound
	case http.StatusMethodNotAllowed:
		return CodeMethodNotAllowed
	case http.StatusConflict:
		return CodeConflict
	case http.StatusPreconditionFailed:
		return CodePrecondition
	case http.StatusRequestEntityTooLarge:
		return CodeTooLarge
	case http.StatusUnsupportedMediaType:
		return CodeUnsupportedMedia
	case http.StatusUnprocessableEntity:
		return CodeUnprocessable
	case http.StatusTooManyRequests:
		return CodeRateLimited
	case http.StatusServiceUnavailable:
		return CodeUnavailable
	case http.StatusGatewayTimeout:
		return CodeTimeout
	}
	if status >= 500 {
		return CodeInternal
	}
	return CodeBadRequest
}

// Write sends an error with the default code for status.
func Write(w http.ResponseWriter, requestID string, status int, msg string) {
	WriteBody(w, status, Body{Error: msg, Code: CodeFor(status), RequestID: requestID})
}

// WriteBody sends a fully specified error body.
func WriteBody(w http.ResponseWriter, status int, b Body) {
	if b.Code == "" {
		b.Code = CodeFor(status)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(b)
}
