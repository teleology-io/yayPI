package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/rs/zerolog/log"

	"github.com/teleology-io/yayPI/internal/apierr"
	"github.com/teleology-io/yayPI/internal/dialect"
	"github.com/teleology-io/yayPI/internal/middleware"
	"github.com/teleology-io/yayPI/internal/query"
	"github.com/teleology-io/yayPI/pkg/sdk"
)

// writeJSON encodes v as JSON and writes to w with the given status code.
func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes the standard error envelope.
func writeError(w http.ResponseWriter, r *http.Request, status int, msg string) {
	apierr.Write(w, middleware.GetRequestID(r), status, msg)
}

// writeValidation writes a 400 with per-field messages.
func writeValidation(w http.ResponseWriter, r *http.Request, fields map[string]string) {
	apierr.WriteBody(w, http.StatusBadRequest, apierr.Body{
		Error: "validation failed", Code: apierr.CodeValidation,
		RequestID: middleware.GetRequestID(r), Errors: fields,
	})
}

// writeHookError maps a hook failure: *sdk.HookError carries a client-facing status and
// message; anything else is logged and becomes a generic 500.
func writeHookError(w http.ResponseWriter, r *http.Request, stage string, err error) {
	var he *sdk.HookError
	if errors.As(err, &he) && he.Status >= 400 && he.Status < 600 {
		apierr.WriteBody(w, he.Status, apierr.Body{
			Error: he.Message, Code: he.Code, Errors: he.Fields, RequestID: middleware.GetRequestID(r),
		})
		return
	}
	log.Error().Err(err).Str("request_id", middleware.GetRequestID(r)).Str("stage", stage).Msg("hook failed")
	writeError(w, r, http.StatusInternalServerError, stage+" hook failed")
}

// logHookError records a failed After* hook. The write already committed, so the client
// still gets success.
func logHookError(r *http.Request, stage string, err error) {
	log.Error().Err(err).Str("request_id", middleware.GetRequestID(r)).Str("stage", stage).Msg("hook failed")
}

// writeDBError maps a database/query error to a client response without leaking driver
// details: constraint violations become 409/422, unknown errors are logged as 500.
func writeDBError(w http.ResponseWriter, r *http.Request, d dialect.Dialect, err error, action string) {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		writeError(w, r, http.StatusGatewayTimeout, "request timed out")
	case errors.Is(err, context.Canceled):
		writeError(w, r, 499, "client closed request")
	case errors.Is(err, query.ErrNotFound):
		writeError(w, r, http.StatusNotFound, "record not found")
	case errors.Is(err, query.ErrNoFields):
		writeError(w, r, http.StatusBadRequest, "no valid fields provided")
	case errors.Is(err, query.ErrCursorMismatch):
		writeError(w, r, http.StatusBadRequest, "cursor does not match the requested sort")
	case d.IsUniqueViolation(err):
		writeError(w, r, http.StatusConflict, "a record with the same unique value already exists")
	case d.IsForeignKeyViolation(err):
		apierr.WriteBody(w, http.StatusUnprocessableEntity, apierr.Body{
			Error: "a referenced record does not exist or is still referenced",
			Code:  "reference_violation", RequestID: middleware.GetRequestID(r),
		})
	case d.IsNotNullViolation(err):
		apierr.WriteBody(w, http.StatusUnprocessableEntity, apierr.Body{
			Error: "a required field is missing", Code: "required_field_missing", RequestID: middleware.GetRequestID(r),
		})
	case d.IsCheckViolation(err):
		apierr.WriteBody(w, http.StatusUnprocessableEntity, apierr.Body{
			Error: "a value violates a constraint", Code: "constraint_violation", RequestID: middleware.GetRequestID(r),
		})
	default:
		log.Error().Err(err).Str("request_id", middleware.GetRequestID(r)).Str("action", action).Msg("database error")
		writeError(w, r, http.StatusInternalServerError, action+" failed")
	}
}

// decodeBody decodes the JSON request body into v, keeping numbers as json.Number so
// large integers survive until they are coerced to the field type. On failure it writes
// 413 (over the size cap) or 400 and returns false.
func decodeBody(w http.ResponseWriter, r *http.Request, v interface{}, badMsg string) bool {
	dec := json.NewDecoder(r.Body)
	dec.UseNumber()
	if err := dec.Decode(v); err != nil {
		if middleware.IsBodyTooLarge(err) {
			writeError(w, r, http.StatusRequestEntityTooLarge, "request body too large")
		} else {
			writeError(w, r, http.StatusBadRequest, badMsg)
		}
		return false
	}
	return true
}

// isJSONContentType checks if the request Content-Type is application/json (any params).
func isJSONContentType(r *http.Request) bool {
	ct := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Type")))
	return ct == "application/json" || strings.HasPrefix(ct, "application/json;") || strings.HasSuffix(strings.SplitN(ct, ";", 2)[0], "+json")
}
