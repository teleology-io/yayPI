package handler

import (
	"net/http"

	"github.com/teleology-io/yayPI/internal/middleware"
	"github.com/teleology-io/yayPI/internal/query"
	"github.com/teleology-io/yayPI/internal/schema"
)

// Get creates a handler that retrieves a single record by ID. Supports ?include= for
// configured relations, ?fields= for sparse responses, and ETag / If-None-Match.
func (f *Factory) Get(entity *schema.Entity, opts *schema.GetOpts) http.HandlerFunc {
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
		var include []string
		if opts != nil {
			rules, include = opts.RowAccess, opts.Include
		}
		row, ok := resolveRowFilter(w, r, rules, sub, http.StatusNotFound)
		if !ok {
			return
		}
		if row, ok = scopeTenant(w, r, entity, row, sub, dbc.Dialect, http.StatusForbidden); !ok {
			return
		}
		fields, ok := parseFields(w, r, entity)
		if !ok {
			return
		}
		rels, ok := parseIncludes(w, r, entity, include)
		if !ok {
			return
		}

		builder := query.NewBuilder(entity, dbc.SQL, dbc.Dialect)
		rec, err := builder.Get(r.Context(), id, row)
		if err != nil {
			writeDBError(w, r, dbc.Dialect, err, "query")
			return
		}
		if rec == nil {
			writeError(w, r, http.StatusNotFound, "record not found")
			return
		}

		etag := computeETag(rec)
		w.Header().Set("ETag", etag)
		if inm := r.Header.Get("If-None-Match"); inm != "" && etagMatches(inm, etag) {
			w.WriteHeader(http.StatusNotModified)
			return
		}

		if !f.loadIncludes(w, r, builder, []map[string]any{rec}, rels, sub) {
			return
		}
		presentRecord(entity, rec, sub)
		selectFields(rec, fields, rels)
		writeJSON(w, http.StatusOK, map[string]any{"data": rec})
	}
}
