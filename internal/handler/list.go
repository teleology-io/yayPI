package handler

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/teleology-io/yayPI/internal/middleware"
	"github.com/teleology-io/yayPI/internal/query"
	"github.com/teleology-io/yayPI/internal/schema"
)

// reservedListParams are query parameters with built-in meaning, never filters.
var reservedListParams = map[string]bool{
	"sort": true, "limit": true, "offset": true, "cursor": true, "fields": true, "include": true, "q": true,
}

// List creates a handler that lists records for the given entity.
//
// Query parameters:
//
//	?col=v / ?col[op]=v   filters on allow_filter_by columns (ops: eq ne gt gte lt lte in nin contains starts_with is_null)
//	?sort=col[:asc|desc]  or ?sort=-col
//	?limit=N              capped at pagination.max_limit
//	?cursor=…             keyset pagination (default style) — pass meta.next_cursor
//	?offset=N             offset pagination (pagination.style: offset)
//	?fields=a,b           sparse fieldset
//	?include=rel          relations listed in the endpoint's include:
func (f *Factory) List(entity *schema.Entity, opts *schema.ListOpts) http.HandlerFunc {
	if opts == nil {
		opts = &schema.ListOpts{}
	}
	allowedFilter := map[string]bool{}
	for _, c := range opts.AllowFilterBy {
		allowedFilter[c] = true
	}
	allowedSort := map[string]bool{}
	for _, c := range opts.AllowSortBy {
		allowedSort[c] = true
	}

	return func(w http.ResponseWriter, r *http.Request) {
		dbc, err := f.db.ForEntity(entity.Name)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "database unavailable")
			return
		}
		q := r.URL.Query()
		sub := middleware.GetSubject(r)

		filters, ok := parseListFilters(w, r, entity, allowedFilter)
		if !ok {
			return
		}

		sortParam := q.Get("sort")
		if sortParam == "" {
			sortParam = opts.DefaultSort
		}
		sort, ok := parseSort(w, r, sortParam, allowedSort)
		if !ok {
			return
		}

		limit := opts.Pagination.DefaultLimit
		if limit <= 0 {
			limit = 20
		}
		maxLimit := opts.Pagination.MaxLimit
		if maxLimit <= 0 {
			maxLimit = 100
		}
		if lStr := q.Get("limit"); lStr != "" {
			parsed, err := strconv.Atoi(lStr)
			if err != nil || parsed < 1 {
				writeError(w, r, http.StatusBadRequest, "invalid limit")
				return
			}
			limit = min(parsed, maxLimit)
		}

		row, ok := resolveRowFilter(w, r, opts.RowAccess, sub, http.StatusForbidden)
		if !ok {
			return
		}
		if row, ok = scopeTenant(w, r, entity, row, sub, dbc.Dialect, http.StatusForbidden); !ok {
			return
		}
		var search query.Search
		if term := q.Get("q"); term != "" {
			if len(opts.Search) == 0 {
				writeError(w, r, http.StatusBadRequest, "search is not enabled for this endpoint")
				return
			}
			search = query.Search{Columns: opts.Search, Term: term}
		}
		fields, ok := parseFields(w, r, entity)
		if !ok {
			return
		}
		rels, ok := parseIncludes(w, r, entity, opts.Include)
		if !ok {
			return
		}

		lq := query.ListQuery{Filters: filters, Sort: sort, Limit: limit + 1, Row: row, Search: search}
		useOffset := opts.Pagination.Style == "offset"
		if useOffset {
			if oStr := q.Get("offset"); oStr != "" {
				parsed, err := strconv.Atoi(oStr)
				if err != nil || parsed < 0 {
					writeError(w, r, http.StatusBadRequest, "invalid offset")
					return
				}
				lq.Offset = parsed
			}
		} else if cursorStr := q.Get("cursor"); cursorStr != "" {
			c, err := query.DecodeCursor(cursorStr, f.secret)
			if err != nil {
				writeError(w, r, http.StatusBadRequest, "invalid cursor")
				return
			}
			lq.Cursor = c
		}

		builder := query.NewBuilder(entity, dbc.SQL, dbc.Dialect)
		rows, err := builder.List(r.Context(), lq)
		if err != nil {
			writeDBError(w, r, dbc.Dialect, err, "query")
			return
		}
		hasMore := len(rows) > limit
		if hasMore {
			rows = rows[:limit]
		}

		meta := map[string]any{"count": len(rows), "limit": limit, "has_more": hasMore}
		if useOffset {
			meta["offset"] = lq.Offset
			meta["page"] = lq.Offset/limit + 1
		} else if hasMore {
			resolved := sort
			if resolved.Column == "" {
				resolved.Column = entity.PKColumn()
			}
			if f, ok := fieldByKey(entity, resolved.Column); ok {
				resolved.Column = f.ColumnName
			}
			meta["next_cursor"] = query.EncodeCursor(query.NewCursor(entity, resolved, rows[len(rows)-1]), f.secret)
		}
		if opts.Pagination.IncludeTotal {
			if total, err := builder.Count(r.Context(), filters, row, search); err == nil {
				meta["total"] = total
			}
		}

		if !f.loadIncludes(w, r, builder, rows, rels, sub) {
			return
		}
		for _, rec := range rows {
			presentRecord(entity, rec, sub)
			selectFields(rec, fields, rels)
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": rows, "meta": meta})
	}
}

// parseListFilters turns ?col=v and ?col[op]=v parameters into typed filters. Unknown
// plain parameters are ignored (cache busters etc.); a known field that is not in
// allow_filter_by, or a malformed operator, is a 400 so callers never silently get
// unfiltered results.
func parseListFilters(w http.ResponseWriter, r *http.Request, entity *schema.Entity, allowed map[string]bool) ([]query.Filter, bool) {
	var filters []query.Filter
	for key, vals := range r.URL.Query() {
		name, op := key, ""
		if i := strings.IndexByte(key, '['); i > 0 && strings.HasSuffix(key, "]") {
			name, op = key[:i], key[i+1:len(key)-1]
		}
		if reservedListParams[name] {
			continue
		}
		field, isField := fieldByKey(entity, name)
		if !isField {
			if op != "" {
				writeError(w, r, http.StatusBadRequest, "unknown filter field: "+name)
				return nil, false
			}
			continue
		}
		if !allowed[field.Name] && !allowed[field.ColumnName] {
			writeError(w, r, http.StatusBadRequest, "filtering by "+name+" is not allowed")
			return nil, false
		}
		for _, v := range vals {
			flt, err := query.ParseFilter(field, op, v)
			if err != nil {
				writeError(w, r, http.StatusBadRequest, err.Error())
				return nil, false
			}
			filters = append(filters, flt)
		}
	}
	return filters, true
}

// parseSort accepts "col", "col:asc", "col:desc" or "-col".
func parseSort(w http.ResponseWriter, r *http.Request, s string, allowed map[string]bool) (query.Sort, bool) {
	if s == "" {
		return query.Sort{}, true
	}
	var out query.Sort
	if strings.HasPrefix(s, "-") {
		out = query.Sort{Column: s[1:], Desc: true}
	} else {
		parts := strings.SplitN(s, ":", 2)
		out.Column = parts[0]
		if len(parts) == 2 {
			switch strings.ToLower(parts[1]) {
			case "asc":
			case "desc":
				out.Desc = true
			default:
				writeError(w, r, http.StatusBadRequest, "invalid sort direction")
				return query.Sort{}, false
			}
		}
	}
	if len(allowed) > 0 && !allowed[out.Column] {
		writeError(w, r, http.StatusBadRequest, "invalid sort column")
		return query.Sort{}, false
	}
	return out, true
}

func fieldByKey(entity *schema.Entity, key string) (*schema.Field, bool) {
	for i := range entity.Fields {
		f := &entity.Fields[i]
		if f.Name == key || f.ColumnName == key {
			return f, true
		}
	}
	return nil, false
}
