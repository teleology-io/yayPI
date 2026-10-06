package handler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/teleology-io/yayPI/internal/db"
	"github.com/teleology-io/yayPI/internal/dialect"
	"github.com/teleology-io/yayPI/internal/middleware"
	"github.com/teleology-io/yayPI/internal/query"
	"github.com/teleology-io/yayPI/internal/schema"
)

// Create creates a handler that inserts a new record (or multiple records when bulk: true).
// An Idempotency-Key header makes retries safe (see withIdempotency).
func (f *Factory) Create(entity *schema.Entity, opts *schema.CreateOpts) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !isJSONContentType(r) {
			writeError(w, r, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
			return
		}
		f.withIdempotency(w, r, func(w http.ResponseWriter, r *http.Request) {
			if opts != nil && opts.Bulk {
				f.createBulk(w, r, entity, opts)
			} else {
				f.createSingle(w, r, entity, opts)
			}
		})
	}
}

// itemError is a failure for one create input, already shaped for the client.
type itemError struct {
	status int
	msg    string
	fields ValidationErrors
	err    error // underlying cause (DB / hook), for writeDBError / writeHookError
	stage  string
}

func (e *itemError) Error() string { return e.msg }

// prepareCreate validates and normalises one create body and runs BeforeCreate hooks.
func (f *Factory) prepareCreate(r *http.Request, entity *schema.Entity, opts *schema.CreateOpts, raw map[string]any, dialectName string) (map[string]any, *itemError) {
	sub := middleware.GetSubject(r)
	strict := opts != nil && opts.Strict

	applyWriteRoles(entity, raw, sub)
	stripServerManaged(entity, raw, false)
	data, verrs := prepareInput(entity, raw, false, strict, dialectName)
	if verrs != nil {
		return nil, &itemError{status: http.StatusBadRequest, msg: "validation failed", fields: verrs}
	}
	passThroughExtras(entity, raw, data, strict)
	if missing := applySubjectDefaults(entity, data, sub); missing != "" {
		return nil, &itemError{status: http.StatusUnauthorized, msg: "authentication required to set " + missing}
	}
	if f.plugins != nil {
		var err error
		data, err = f.plugins.BeforeCreate(r.Context(), entity.Name, data)
		if err != nil {
			return nil, &itemError{err: err, stage: "pre-create"}
		}
	}
	return data, nil
}

// insert writes one record inside tx and notifies observers.
func (f *Factory) insert(ctx context.Context, r *http.Request, tx *sql.Tx, dbc *db.DB, entity *schema.Entity, data map[string]any) (map[string]any, error) {
	record, err := query.NewBuilder(entity, tx, dbc.Dialect).Create(ctx, data)
	if err != nil {
		return nil, err
	}
	err = f.observe(ctx, tx, dbc.Dialect, WriteEvent{
		Entity: entity, Action: "create", ID: idString(record[entity.PKColumn()]), After: record,
		Subject: middleware.GetSubject(r), RequestID: middleware.GetRequestID(r),
	})
	return record, err
}

func (f *Factory) writeItemError(w http.ResponseWriter, r *http.Request, dbc *db.DB, e *itemError) {
	switch {
	case e.fields != nil:
		writeValidation(w, r, e.fields)
	case e.stage != "":
		writeHookError(w, r, e.stage, e.err)
	case e.err != nil:
		writeDBError(w, r, dbc.Dialect, e.err, "create")
	default:
		writeError(w, r, e.status, e.msg)
	}
}

func (f *Factory) createSingle(w http.ResponseWriter, r *http.Request, entity *schema.Entity, opts *schema.CreateOpts) {
	dbc, err := f.db.ForEntity(entity.Name)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "database unavailable")
		return
	}
	var raw map[string]any
	if !decodeBody(w, r, &raw, "invalid JSON body") {
		return
	}
	data, ierr := f.prepareCreate(r, entity, opts, raw, dbc.Dialect.Name())
	if ierr != nil {
		f.writeItemError(w, r, dbc, ierr)
		return
	}

	var record map[string]any
	err = withTx(r.Context(), dbc.SQL, func(tx *sql.Tx) error {
		var err error
		record, err = f.insert(r.Context(), r, tx, dbc, entity, data)
		return err
	})
	if err != nil {
		writeDBError(w, r, dbc.Dialect, err, "create")
		return
	}

	f.afterCreate(r, entity, record)
	w.Header().Set("ETag", computeETag(record))
	presentRecord(entity, record, middleware.GetSubject(r))
	writeJSON(w, http.StatusCreated, map[string]any{"data": record})
}

// afterCreate runs AfterCreate hooks once the row is committed. Hook errors are logged;
// the record exists, so the request still succeeds.
func (f *Factory) afterCreate(r *http.Request, entity *schema.Entity, record map[string]any) {
	if f.plugins != nil {
		if err := f.plugins.AfterCreate(r.Context(), entity.Name, copyMap(record)); err != nil {
			logHookError(r, "after-create", err)
		}
	}
}

// createBulk inserts an array of records. In "abort" mode (default) the whole batch is
// one transaction: any failure leaves nothing inserted. In "partial" mode each item
// commits independently and per-item results are returned with 207.
func (f *Factory) createBulk(w http.ResponseWriter, r *http.Request, entity *schema.Entity, opts *schema.CreateOpts) {
	dbc, err := f.db.ForEntity(entity.Name)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "database unavailable")
		return
	}
	var items []map[string]any
	if !decodeBody(w, r, &items, "invalid JSON body: expected array") {
		return
	}
	bulkMax := 500
	if opts != nil && opts.BulkMax > 0 {
		bulkMax = opts.BulkMax
	}
	if len(items) > bulkMax {
		writeError(w, r, http.StatusBadRequest, fmt.Sprintf("too many items in bulk request (max %d)", bulkMax))
		return
	}
	partial := opts != nil && opts.BulkErrorMode == "partial"
	sub := middleware.GetSubject(r)
	d := dbc.Dialect

	type bulkResult struct {
		Index  int              `json:"index"`
		Data   map[string]any   `json:"data,omitempty"`
		Error  string           `json:"error,omitempty"`
		Errors ValidationErrors `json:"errors,omitempty"`
	}

	if !partial {
		prepared := make([]map[string]any, len(items))
		for i, raw := range items {
			data, ierr := f.prepareCreate(r, entity, opts, raw, d.Name())
			if ierr != nil {
				if ierr.fields != nil {
					writeJSON(w, http.StatusBadRequest, map[string]any{
						"error": "validation failed at index " + strconv.Itoa(i), "code": "validation_failed",
						"index": i, "errors": ierr.fields, "request_id": middleware.GetRequestID(r),
					})
					return
				}
				f.writeItemError(w, r, dbc, ierr)
				return
			}
			prepared[i] = data
		}
		records := make([]map[string]any, len(prepared))
		failedAt := -1
		err := withTx(r.Context(), dbc.SQL, func(tx *sql.Tx) error {
			for i, data := range prepared {
				rec, err := f.insert(r.Context(), r, tx, dbc, entity, data)
				if err != nil {
					failedAt = i
					return err
				}
				records[i] = rec
			}
			return nil
		})
		if err != nil {
			if failedAt >= 0 {
				w.Header().Set("X-Failed-Index", strconv.Itoa(failedAt))
			}
			writeDBError(w, r, d, err, "bulk create (nothing was inserted)")
			return
		}
		results := make([]bulkResult, len(records))
		for i, rec := range records {
			f.afterCreate(r, entity, rec)
			presentRecord(entity, rec, sub)
			results[i] = bulkResult{Index: i, Data: rec}
		}
		writeJSON(w, http.StatusCreated, map[string]any{"results": results})
		return
	}

	results := make([]bulkResult, 0, len(items))
	hasError := false
	for i, raw := range items {
		data, ierr := f.prepareCreate(r, entity, opts, raw, d.Name())
		if ierr != nil {
			hasError = true
			res := bulkResult{Index: i, Error: ierr.msg, Errors: ierr.fields}
			if ierr.stage != "" {
				res.Error = ierr.stage + " hook failed"
			}
			results = append(results, res)
			continue
		}
		var rec map[string]any
		err := withTx(r.Context(), dbc.SQL, func(tx *sql.Tx) error {
			var err error
			rec, err = f.insert(r.Context(), r, tx, dbc, entity, data)
			return err
		})
		if err != nil {
			hasError = true
			results = append(results, bulkResult{Index: i, Error: dbErrorMessage(d, err)})
			continue
		}
		f.afterCreate(r, entity, rec)
		presentRecord(entity, rec, sub)
		results = append(results, bulkResult{Index: i, Data: rec})
	}
	status := http.StatusCreated
	if hasError {
		status = http.StatusMultiStatus
	}
	writeJSON(w, status, map[string]any{"results": results})
}

// dbErrorMessage is the client-safe message for a per-item DB failure.
func dbErrorMessage(d dialect.Dialect, err error) string {
	switch {
	case errors.Is(err, query.ErrNoFields):
		return "no valid fields provided"
	case d.IsUniqueViolation(err):
		return "a record with the same unique value already exists"
	case d.IsForeignKeyViolation(err):
		return "a referenced record does not exist"
	case d.IsNotNullViolation(err):
		return "a required field is missing"
	case d.IsCheckViolation(err):
		return "a value violates a constraint"
	}
	return "create failed"
}

func copyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// idString renders a primary key value as a string.
func idString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case []byte:
		return string(x)
	}
	return fmt.Sprint(v)
}
