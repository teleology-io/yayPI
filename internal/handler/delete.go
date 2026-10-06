package handler

import (
	"database/sql"
	"net/http"

	"github.com/teleology-io/yayPI/internal/middleware"
	"github.com/teleology-io/yayPI/internal/query"
	"github.com/teleology-io/yayPI/internal/schema"
)

// Delete creates a handler that removes a record by ID (soft delete when the entity has
// soft_delete). Row access is resolved and the record's existence confirmed before any
// BeforeDelete hook runs. Supports If-Match.
func (f *Factory) Delete(entity *schema.Entity, opts *schema.DeleteOpts) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := parseID(w, r, entity)
		if !ok {
			return
		}
		dbc, err := f.db.ForEntity(entity.Name)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "database unavailable")
			return
		}
		sub := middleware.GetSubject(r)

		var rules []schema.RowAccessRule
		if opts != nil {
			rules = opts.RowAccess
		}
		row, ok := resolveRowFilter(w, r, rules, sub, http.StatusNotFound)
		if !ok {
			return
		}
		if row, ok = scopeTenant(w, r, entity, row, sub, dbc.Dialect, http.StatusForbidden); !ok {
			return
		}

		var hookErr error
		preconditionFailed := false
		err = withTx(r.Context(), dbc.SQL, func(tx *sql.Tx) error {
			b := query.NewBuilder(entity, tx, dbc.Dialect)
			before, err := b.Get(r.Context(), id, row, true)
			if err != nil {
				return err
			}
			if before == nil {
				return query.ErrNotFound
			}
			if h := r.Header.Get("If-Match"); h != "" && !etagMatches(h, computeETag(before)) {
				preconditionFailed = true
				return query.ErrNotFound
			}
			if f.plugins != nil {
				if hookErr = f.plugins.BeforeDelete(r.Context(), entity.Name, idString(id)); hookErr != nil {
					return hookErr
				}
			}
			if err := b.Delete(r.Context(), id, entity.SoftDelete, row); err != nil {
				return err
			}
			return f.observe(r.Context(), tx, dbc.Dialect, WriteEvent{
				Entity: entity, Action: "delete", ID: idString(id), Before: before,
				Subject: sub, RequestID: middleware.GetRequestID(r),
			})
		})
		switch {
		case hookErr != nil:
			writeHookError(w, r, "pre-delete", hookErr)
			return
		case preconditionFailed:
			writeError(w, r, http.StatusPreconditionFailed, "the record was modified; fetch it again and retry")
			return
		case err != nil:
			writeDBError(w, r, dbc.Dialect, err, "delete")
			return
		}

		if f.plugins != nil {
			if err := f.plugins.AfterDelete(r.Context(), entity.Name, idString(id)); err != nil {
				logHookError(r, "after-delete", err)
			}
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
