package handler

import (
	"database/sql"
	"net/http"

	"github.com/teleology-io/yayPI/internal/middleware"
	"github.com/teleology-io/yayPI/internal/query"
	"github.com/teleology-io/yayPI/internal/schema"
)

// Update creates a handler that modifies an existing record (PATCH: partial update).
// With an If-Match header the update only applies if the record's current ETag
// matches; otherwise it returns 412 (optimistic concurrency).
func (f *Factory) Update(entity *schema.Entity, opts *schema.UpdateOpts) http.HandlerFunc {
	return f.update(entity, opts, false)
}

// Replace creates a PUT handler: the body is the complete writable representation, so
// every writable field not supplied is reset to null/its default.
func (f *Factory) Replace(entity *schema.Entity, opts *schema.UpdateOpts) http.HandlerFunc {
	return f.update(entity, opts, true)
}

func (f *Factory) update(entity *schema.Entity, opts *schema.UpdateOpts, replace bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !isJSONContentType(r) {
			writeError(w, r, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
			return
		}
		id, ok := parseID(w, r, entity)
		if !ok {
			return
		}
		dbc, err := f.db.ForEntity(entity.Name)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "database unavailable")
			return
		}

		var raw map[string]any
		if !decodeBody(w, r, &raw, "invalid JSON body") {
			return
		}
		sub := middleware.GetSubject(r)
		strict := opts != nil && opts.Strict

		// Filter to allowed fields if specified
		if opts != nil && len(opts.AllowedFields) > 0 {
			for k := range raw {
				if !sliceContainsStr(opts.AllowedFields, k) {
					delete(raw, k)
				}
			}
		}
		applyWriteRoles(entity, raw, sub)
		stripServerManaged(entity, raw, true)

		data, verrs := prepareInput(entity, raw, !replace, strict, dbc.Dialect.Name())
		if verrs != nil {
			writeValidation(w, r, verrs)
			return
		}
		if replace {
			fillReplaceDefaults(entity, data, opts, sub)
		}
		passThroughExtras(entity, raw, data, strict)

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

		var record map[string]any
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
				return query.ErrNotFound // any error rolls back; flag decides the response
			}
			// Hooks run only once the row is known to exist and be accessible.
			if f.plugins != nil {
				if data, hookErr = f.plugins.BeforeUpdate(r.Context(), entity.Name, idString(id), data); hookErr != nil {
					return hookErr
				}
			}
			record, err = b.Update(r.Context(), id, data, row)
			if err != nil {
				return err
			}
			return f.observe(r.Context(), tx, dbc.Dialect, WriteEvent{
				Entity: entity, Action: "update", ID: idString(id), Before: before, After: record,
				Subject: sub, RequestID: middleware.GetRequestID(r),
			})
		})
		if hookErr != nil {
			writeHookError(w, r, "pre-update", hookErr)
			return
		}
		if preconditionFailed {
			writeError(w, r, http.StatusPreconditionFailed, "the record was modified; fetch it again and retry")
			return
		}
		if err != nil {
			writeDBError(w, r, dbc.Dialect, err, "update")
			return
		}

		if f.plugins != nil {
			if err := f.plugins.AfterUpdate(r.Context(), entity.Name, copyMap(record)); err != nil {
				logHookError(r, "after-update", err)
			}
		}

		w.Header().Set("ETag", computeETag(record))
		presentRecord(entity, record, sub)
		writeJSON(w, http.StatusOK, map[string]any{"data": record})
	}
}

// fillReplaceDefaults makes a PUT body complete: writable fields the caller is allowed
// to set but omitted are reset to NULL (nullable) so the stored row matches the body.
func fillReplaceDefaults(entity *schema.Entity, data map[string]any, opts *schema.UpdateOpts, sub *middleware.Subject) {
	role := ""
	if sub != nil {
		role = sub.Role
	}
	for _, f := range entity.Fields {
		if _, ok := data[f.ColumnName]; ok {
			continue
		}
		if f.PrimaryKey || f.Immutable || f.DefaultFrom != "" || !f.Nullable ||
			f.ColumnName == "created_at" || f.ColumnName == "updated_at" || f.ColumnName == "deleted_at" {
			continue
		}
		if len(f.WriteRoles) > 0 && !sliceContainsStr(f.WriteRoles, role) {
			continue
		}
		if opts != nil && len(opts.AllowedFields) > 0 && !sliceContainsStr(opts.AllowedFields, f.Name) {
			continue
		}
		data[f.ColumnName] = nil
	}
}
