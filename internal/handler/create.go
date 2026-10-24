package handler

import (
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
	if isUpsert(conflictMode(opts)) {
		if field := missingNaturalKey(entity, data); field != "" {
			return nil, &itemError{status: http.StatusBadRequest, msg: "validation failed", fields: naturalKeyError(entity, field)}
		}
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

func (f *Factory) writeItemError(w http.ResponseWriter, r *http.Request, dbc *db.DB, e *itemError) {
	switch {
	case e.fields != nil:
		writeValidation(w, r, e.fields)
	case e.stage != "":
		writeHookError(w, r, e.stage, e.err)
	case errors.Is(e.err, errKeyHidden):
		writeError(w, r, http.StatusConflict, errKeyHidden.Error())
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
	row, ok := f.conflictFilter(w, r, entity, opts, dbc)
	if !ok {
		return
	}

	sub, mode := middleware.GetSubject(r), conflictMode(opts)
	var out writeOutcome
	err = withTx(r.Context(), dbc.SQL, func(tx *sql.Tx) error {
		var err error
		out, err = f.createOrUpsert(r.Context(), tx, dbc, entity, data, mode, row, sub, middleware.GetRequestID(r))
		return err
	})
	if err != nil {
		f.writeItemError(w, r, dbc, &itemError{err: err})
		return
	}

	f.afterCreate(r, entity, out)
	status := http.StatusCreated
	if out.Action != ActionCreated {
		status = http.StatusOK
	}
	if isUpsert(mode) {
		w.Header().Set("X-Write-Action", out.Action)
	}
	record := out.Record
	w.Header().Set("ETag", computeETag(record))
	presentRecord(entity, record, sub)
	writeJSON(w, status, map[string]any{"data": record})
}

// conflictFilter resolves which existing rows an upsert may overwrite: the endpoint's
// update.row_access plus tenant scope. Plain creates need none.
func (f *Factory) conflictFilter(w http.ResponseWriter, r *http.Request, entity *schema.Entity, opts *schema.CreateOpts, dbc *db.DB) (query.RowFilter, bool) {
	if !isUpsert(conflictMode(opts)) {
		return query.RowFilter{}, true
	}
	sub := middleware.GetSubject(r)
	row, ok := resolveRowFilter(w, r, opts.ConflictRowAccess, sub, http.StatusForbidden)
	if !ok {
		return row, false
	}
	return scopeTenant(w, r, entity, row, sub, dbc.Dialect, http.StatusForbidden)
}

// afterCreate runs the After* hook for what was written once it is committed. Hook
// errors are logged; the record exists, so the request still succeeds.
func (f *Factory) afterCreate(r *http.Request, entity *schema.Entity, out writeOutcome) {
	if err := f.afterWrite(r.Context(), entity, out); err != nil {
		stage := "after-create"
		if out.Action == ActionUpdated {
			stage = "after-update"
		}
		logHookError(r, stage, err)
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
	sub, mode, reqID := middleware.GetSubject(r), conflictMode(opts), middleware.GetRequestID(r)
	d := dbc.Dialect
	row, ok := f.conflictFilter(w, r, entity, opts, dbc)
	if !ok {
		return
	}

	type bulkResult struct {
		Index  int              `json:"index"`
		Action string           `json:"action,omitempty"`
		Data   map[string]any   `json:"data,omitempty"`
		Error  string           `json:"error,omitempty"`
		Errors ValidationErrors `json:"errors,omitempty"`
	}
	action := func(out writeOutcome) string {
		if isUpsert(mode) {
			return out.Action
		}
		return ""
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
		outs := make([]writeOutcome, len(prepared))
		failedAt := -1
		err := withTx(r.Context(), dbc.SQL, func(tx *sql.Tx) error {
			for i, data := range prepared {
				out, err := f.createOrUpsert(r.Context(), tx, dbc, entity, data, mode, row, sub, reqID)
				if err != nil {
					failedAt = i
					return err
				}
				outs[i] = out
			}
			return nil
		})
		if err != nil {
			if failedAt >= 0 {
				w.Header().Set("X-Failed-Index", strconv.Itoa(failedAt))
			}
			if errors.Is(err, errKeyHidden) {
				writeError(w, r, http.StatusConflict, errKeyHidden.Error()+" (nothing was written)")
				return
			}
			writeDBError(w, r, d, err, "bulk create (nothing was inserted)")
			return
		}
		results := make([]bulkResult, len(outs))
		for i, out := range outs {
			f.afterCreate(r, entity, out)
			presentRecord(entity, out.Record, sub)
			results[i] = bulkResult{Index: i, Action: action(out), Data: out.Record}
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
		var out writeOutcome
		err := withTx(r.Context(), dbc.SQL, func(tx *sql.Tx) error {
			var err error
			out, err = f.createOrUpsert(r.Context(), tx, dbc, entity, data, mode, row, sub, reqID)
			return err
		})
		if err != nil {
			hasError = true
			results = append(results, bulkResult{Index: i, Error: dbErrorMessage(d, err)})
			continue
		}
		f.afterCreate(r, entity, out)
		presentRecord(entity, out.Record, sub)
		results = append(results, bulkResult{Index: i, Action: action(out), Data: out.Record})
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
	case errors.Is(err, errKeyHidden):
		return errKeyHidden.Error()
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
