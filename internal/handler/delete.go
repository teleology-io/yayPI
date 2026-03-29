package handler

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/teleology-io/yayPI/internal/middleware"
	"github.com/teleology-io/yayPI/internal/policy"
	"github.com/teleology-io/yayPI/internal/query"
	"github.com/teleology-io/yayPI/internal/schema"
	"github.com/google/uuid"
)

// Delete creates a handler that removes a record by ID.
func (f *Factory) Delete(entity *schema.Entity, opts *schema.DeleteOpts) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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

		// Run before delete hook
		if f.plugins != nil {
			if err := f.plugins.BeforeDelete(r.Context(), entity.Name, id); err != nil {
				writeError(w, http.StatusInternalServerError, "pre-delete hook failed")
				return
			}
		}

		dbc, err := f.db.ForEntity(entity.Name)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "database unavailable")
			return
		}

		soft := false
		if opts != nil {
			soft = opts.SoftDelete
		}
		if !soft && entity.SoftDelete {
			soft = true
		}

		// Resolve row-level access filter
		sub := middleware.GetSubject(r)
		var rowFilter string
		var rowArgs []interface{}
		if opts != nil && len(opts.RowAccess) > 0 {
			var rowErr error
			rowFilter, rowArgs, rowErr = policy.ResolveRowFilter(opts.RowAccess, sub)
			if errors.Is(rowErr, policy.ErrRowAccessDenied) {
				writeError(w, http.StatusNotFound, "record not found")
				return
			}
			if rowErr != nil {
				writeError(w, http.StatusInternalServerError, "row access evaluation failed")
				return
			}
		}

		builder := query.NewBuilder(entity, dbc.SQL, dbc.Dialect)
		if err := builder.Delete(r.Context(), id, soft, rowFilter, rowArgs); err != nil {
			if err.Error() == "record not found" {
				writeError(w, http.StatusNotFound, "record not found")
				return
			}
			writeError(w, http.StatusInternalServerError, "delete failed")
			return
		}

		// Run after delete hook
		if f.plugins != nil {
			_ = f.plugins.AfterDelete(r.Context(), entity.Name, id)
		}

		w.WriteHeader(http.StatusNoContent)
	}
}
