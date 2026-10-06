package handler

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/teleology-io/yayPI/internal/dialect"
	"github.com/teleology-io/yayPI/internal/middleware"
	"github.com/teleology-io/yayPI/internal/policy"
	"github.com/teleology-io/yayPI/internal/query"
	"github.com/teleology-io/yayPI/internal/schema"
	"github.com/teleology-io/yayPI/pkg/types"
)

// parseID reads the {id} URL param and converts it to the primary key's type.
func parseID(w http.ResponseWriter, r *http.Request, entity *schema.Entity) (any, bool) {
	raw := chi.URLParam(r, "id")
	if raw == "" {
		writeError(w, r, http.StatusBadRequest, "missing id parameter")
		return nil, false
	}
	pk := entity.PrimaryKey()
	if pk == nil {
		return raw, true
	}
	switch pk.Type {
	case types.FieldTypeUUID:
		u, err := uuid.Parse(raw)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid id format")
			return nil, false
		}
		return u.String(), true
	case types.FieldTypeInteger, types.FieldTypeBigint:
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid id format")
			return nil, false
		}
		return n, true
	}
	return raw, true
}

// resolveRowFilter evaluates row_access rules. When no rule matches it writes
// deniedStatus (403 for lists, 404 for single records so existence is not revealed).
func resolveRowFilter(w http.ResponseWriter, r *http.Request, rules []schema.RowAccessRule, sub *middleware.Subject, deniedStatus int) (query.RowFilter, bool) {
	if len(rules) == 0 {
		return query.RowFilter{}, true
	}
	sqlFrag, args, err := policy.ResolveRowFilter(rules, sub)
	if errors.Is(err, policy.ErrRowAccessDenied) {
		msg := "access denied"
		if deniedStatus == http.StatusNotFound {
			msg = "record not found"
		}
		writeError(w, r, deniedStatus, msg)
		return query.RowFilter{}, false
	}
	if err != nil {
		log.Error().Err(err).Str("request_id", middleware.GetRequestID(r)).Msg("row access evaluation failed")
		writeError(w, r, http.StatusInternalServerError, "row access evaluation failed")
		return query.RowFilter{}, false
	}
	return query.RowFilter{SQL: sqlFrag, Args: args}, true
}

// scopeTenant adds the tenant isolation clause for tenant_scoped entities. A caller
// without a tenant claim is refused with deniedStatus.
func scopeTenant(w http.ResponseWriter, r *http.Request, entity *schema.Entity, row query.RowFilter, sub *middleware.Subject, d dialect.Dialect, deniedStatus int) (query.RowFilter, bool) {
	if entity.TenantColumn == "" {
		return row, true
	}
	if sub == nil || sub.Tenant == "" {
		writeError(w, r, deniedStatus, "no tenant in credentials")
		return row, false
	}
	clause := d.QuoteIdent(entity.TenantColumn) + " = $" + strconv.Itoa(len(row.Args)+1)
	if row.SQL != "" {
		clause = "(" + row.SQL + ") AND " + clause
	}
	return query.RowFilter{SQL: clause, Args: append(append([]any(nil), row.Args...), sub.Tenant)}, true
}

// parseFields reads ?fields=a,b for sparse responses (nil = all fields).
func parseFields(w http.ResponseWriter, r *http.Request, entity *schema.Entity) (map[string]bool, bool) {
	raw := r.URL.Query().Get("fields")
	if raw == "" {
		return nil, true
	}
	out := map[string]bool{}
	for _, name := range strings.Split(raw, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		col := ""
		for _, f := range entity.Fields {
			if f.Name == name || f.ColumnName == name {
				col = f.ColumnName
			}
		}
		if col == "" {
			writeError(w, r, http.StatusBadRequest, "unknown field in fields: "+name)
			return nil, false
		}
		out[col] = true
	}
	return out, true
}

// selectFields drops columns not requested via ?fields (included relations are kept).
func selectFields(row map[string]any, fields map[string]bool, rels []schema.Relation) {
	if fields == nil {
		return
	}
	keep := map[string]bool{}
	for _, rel := range rels {
		keep[rel.Name] = true
	}
	for k := range row {
		if !fields[k] && !keep[k] {
			delete(row, k)
		}
	}
}

// parseIncludes reads ?include=a,b, allowing only relations listed in the endpoint's
// include: config.
func parseIncludes(w http.ResponseWriter, r *http.Request, entity *schema.Entity, allowed []string) ([]schema.Relation, bool) {
	raw := r.URL.Query().Get("include")
	if raw == "" {
		return nil, true
	}
	var out []schema.Relation
	for _, name := range strings.Split(raw, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if !sliceContainsStr(allowed, name) {
			writeError(w, r, http.StatusBadRequest, "include not allowed: "+name)
			return nil, false
		}
		found := false
		for _, rel := range entity.Relations {
			if rel.Name == name {
				out = append(out, rel)
				found = true
			}
		}
		if !found {
			writeError(w, r, http.StatusBadRequest, "unknown relation: "+name)
			return nil, false
		}
	}
	return out, true
}

// loadIncludes attaches the requested relations to rows. Related rows get the related
// entity's omit_response and read_roles stripping. Note: included rows are not filtered
// by the related entity's endpoint row_access — only list relations whose rows every
// caller of this endpoint may see.
func (f *Factory) loadIncludes(w http.ResponseWriter, r *http.Request, b *query.Builder, rows []map[string]any, rels []schema.Relation, sub *middleware.Subject) bool {
	for _, rel := range rels {
		err := b.Include(r.Context(), f.registry, rows, rel, func(e *schema.Entity, row map[string]any) {
			presentRecord(e, row, sub)
		})
		if err != nil {
			log.Error().Err(err).Str("request_id", middleware.GetRequestID(r)).Str("relation", rel.Name).Msg("include failed")
			writeError(w, r, http.StatusInternalServerError, "loading "+rel.Name+" failed")
			return false
		}
	}
	return true
}
