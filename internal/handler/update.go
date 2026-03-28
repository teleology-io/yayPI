package handler

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/csullivan/yaypi/internal/query"
	"github.com/csullivan/yaypi/internal/schema"
	"github.com/google/uuid"
)

// Update creates a handler that modifies an existing record.
func (f *Factory) Update(entity *schema.Entity, opts *schema.UpdateOpts) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Enforce Content-Type
		if !isJSONContentType(r) {
			writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
			return
		}

		id := chi.URLParam(r, "id")
		if id == "" {
			writeError(w, http.StatusBadRequest, "missing id parameter")
			return
		}

		if isPKUUID(entity) {
			if _, err := uuid.Parse(id); err != nil {
				writeError(w, http.StatusBadRequest, "invalid id format")
				return
			}
		}

		var data map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}

		// Filter to allowed fields if specified
		if opts != nil && len(opts.AllowedFields) > 0 {
			allowed := make(map[string]struct{})
			for _, f := range opts.AllowedFields {
				allowed[f] = struct{}{}
			}
			for k := range data {
				if _, ok := allowed[k]; !ok {
					delete(data, k)
				}
			}
		}

		// Run before hooks
		if f.plugins != nil {
			var err error
			data, err = f.plugins.BeforeUpdate(r.Context(), entity.Name, id, data)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "pre-update hook failed")
				return
			}
		}

		pool, err := f.db.ForEntity(entity.Name)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "database unavailable")
			return
		}

		builder := query.NewBuilder(entity, pool)
		record, err := builder.Update(r.Context(), id, data)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "update failed")
			return
		}
		if record == nil {
			writeError(w, http.StatusNotFound, "record not found")
			return
		}

		// Run after hooks
		if f.plugins != nil {
			_ = f.plugins.AfterUpdate(r.Context(), entity.Name, record)
		}

		stripOmitFields(entity, record)

		writeJSON(w, http.StatusOK, map[string]interface{}{
			"data": record,
		})
	}
}
