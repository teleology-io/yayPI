package handler

import (
	"encoding/json"
	"net/http"

	"github.com/teleology-io/yayPI/internal/middleware"
	"github.com/teleology-io/yayPI/internal/query"
	"github.com/teleology-io/yayPI/internal/schema"
)

// Create creates a handler that inserts a new record (or multiple records when bulk: true).
func (f *Factory) Create(entity *schema.Entity, opts *schema.CreateOpts) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !isJSONContentType(r) {
			writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
			return
		}

		// Peek at the first non-whitespace byte to detect array vs object.
		bulk := opts != nil && opts.Bulk
		if bulk {
			f.createBulk(w, r, entity, opts)
		} else {
			f.createSingle(w, r, entity, opts)
		}
	}
}

func (f *Factory) createSingle(w http.ResponseWriter, r *http.Request, entity *schema.Entity, opts *schema.CreateOpts) {
	var data map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	sub := middleware.GetSubject(r)

	if verrs := validateFields(entity, data, false); verrs != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"errors": verrs})
		return
	}

	applyWriteRoles(entity, data, sub)

	if f.plugins != nil {
		var err error
		data, err = f.plugins.BeforeCreate(r.Context(), entity.Name, data)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "pre-create hook failed")
			return
		}
	}

	dbc, err := f.db.ForEntity(entity.Name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database unavailable")
		return
	}

	builder := query.NewBuilder(entity, dbc.SQL, dbc.Dialect)
	record, err := builder.Create(r.Context(), data)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "create failed")
		return
	}

	if f.plugins != nil {
		_ = f.plugins.AfterCreate(r.Context(), entity.Name, record)
	}

	stripOmitFields(entity, record)
	applyFieldAccess(entity, record, sub)

	writeJSON(w, http.StatusCreated, map[string]interface{}{"data": record})
}

func (f *Factory) createBulk(w http.ResponseWriter, r *http.Request, entity *schema.Entity, opts *schema.CreateOpts) {
	var items []map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&items); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: expected array")
		return
	}

	bulkMax := 500
	if opts != nil && opts.BulkMax > 0 {
		bulkMax = opts.BulkMax
	}
	if len(items) > bulkMax {
		writeError(w, http.StatusBadRequest, "too many items in bulk request")
		return
	}

	partial := opts != nil && opts.BulkErrorMode == "partial"
	sub := middleware.GetSubject(r)

	dbc, err := f.db.ForEntity(entity.Name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database unavailable")
		return
	}
	builder := query.NewBuilder(entity, dbc.SQL, dbc.Dialect)

	type bulkResult struct {
		Index  int                    `json:"index"`
		Data   map[string]interface{} `json:"data,omitempty"`
		Error  string                 `json:"error,omitempty"`
	}

	results := make([]bulkResult, 0, len(items))
	hasError := false

	for i, data := range items {
		if verrs := validateFields(entity, data, false); verrs != nil {
			if !partial {
				writeJSON(w, http.StatusBadRequest, map[string]interface{}{
					"error": "validation failed at index " + itoa(i),
					"errors": verrs,
				})
				return
			}
			results = append(results, bulkResult{Index: i, Error: verrs.Error()})
			hasError = true
			continue
		}

		applyWriteRoles(entity, data, sub)

		if f.plugins != nil {
			data, err = f.plugins.BeforeCreate(r.Context(), entity.Name, data)
			if err != nil {
				if !partial {
					writeError(w, http.StatusInternalServerError, "pre-create hook failed")
					return
				}
				results = append(results, bulkResult{Index: i, Error: "hook failed"})
				hasError = true
				continue
			}
		}

		record, err := builder.Create(r.Context(), data)
		if err != nil {
			if !partial {
				writeError(w, http.StatusInternalServerError, "create failed at index "+itoa(i))
				return
			}
			results = append(results, bulkResult{Index: i, Error: err.Error()})
			hasError = true
			continue
		}

		if f.plugins != nil {
			_ = f.plugins.AfterCreate(r.Context(), entity.Name, record)
		}

		stripOmitFields(entity, record)
		applyFieldAccess(entity, record, sub)
		results = append(results, bulkResult{Index: i, Data: record})
	}

	status := http.StatusCreated
	if hasError {
		status = http.StatusMultiStatus
	}
	writeJSON(w, status, map[string]interface{}{"results": results})
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	buf := [20]byte{}
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[pos:])
}

// isJSONContentType checks if the request Content-Type is application/json.
func isJSONContentType(r *http.Request) bool {
	ct := r.Header.Get("Content-Type")
	if ct == "" {
		return false
	}
	for _, allowed := range []string{"application/json"} {
		if len(ct) >= len(allowed) && ct[:len(allowed)] == allowed {
			return true
		}
	}
	return false
}
