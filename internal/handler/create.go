package handler

import (
	"encoding/json"
	"net/http"

	"github.com/teleology-io/yayPI/internal/middleware"
	"github.com/teleology-io/yayPI/internal/query"
	"github.com/teleology-io/yayPI/internal/schema"
)

// Create creates a handler that inserts a new record.
func (f *Factory) Create(entity *schema.Entity, opts *schema.CreateOpts) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Enforce Content-Type
		if !isJSONContentType(r) {
			writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
			return
		}

		var data map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}

		// Enforce field-level write restrictions before touching the DB
		sub := middleware.GetSubject(r)
		applyWriteRoles(entity, data, sub)

		// Run before hooks via plugin dispatcher
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

		// Run after hooks
		if f.plugins != nil {
			if err := f.plugins.AfterCreate(r.Context(), entity.Name, record); err != nil {
				// Log but don't fail the request
				_ = err
			}
		}

		stripOmitFields(entity, record)
		applyFieldAccess(entity, record, sub)

		writeJSON(w, http.StatusCreated, map[string]interface{}{
			"data": record,
		})
	}
}

// isJSONContentType checks if the request Content-Type is application/json.
func isJSONContentType(r *http.Request) bool {
	ct := r.Header.Get("Content-Type")
	if ct == "" {
		return false
	}
	// Allow "application/json" and "application/json; charset=utf-8"
	for _, allowed := range []string{"application/json"} {
		if len(ct) >= len(allowed) && ct[:len(allowed)] == allowed {
			return true
		}
	}
	return false
}
