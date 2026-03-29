package handler

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/csullivan/yaypi/internal/middleware"
	"github.com/csullivan/yaypi/internal/policy"
	"github.com/csullivan/yaypi/internal/query"
	"github.com/csullivan/yaypi/internal/schema"
	"github.com/google/uuid"
)

// Get creates a handler that retrieves a single record by ID.
func (f *Factory) Get(entity *schema.Entity, opts *schema.GetOpts) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if id == "" {
			writeError(w, http.StatusBadRequest, "missing id parameter")
			return
		}

		// Validate UUID format for UUID primary keys
		if isPKUUID(entity) {
			if _, err := uuid.Parse(id); err != nil {
				writeError(w, http.StatusBadRequest, "invalid id format")
				return
			}
		}

		dbc, err := f.db.ForEntity(entity.Name)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "database unavailable")
			return
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
		row, err := builder.Get(r.Context(), id, rowFilter, rowArgs)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "query failed")
			return
		}
		if row == nil {
			writeError(w, http.StatusNotFound, "record not found")
			return
		}

		stripOmitFields(entity, row)
		applyFieldAccess(entity, row, sub)

		writeJSON(w, http.StatusOK, map[string]interface{}{
			"data": row,
		})
	}
}

// isPKUUID returns true if the entity's primary key field is of type uuid.
func isPKUUID(entity *schema.Entity) bool {
	for _, f := range entity.Fields {
		if f.PrimaryKey {
			return f.Type == "uuid"
		}
	}
	// Default: assume uuid
	return true
}
