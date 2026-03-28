package handler

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/csullivan/yaypi/internal/query"
	"github.com/csullivan/yaypi/internal/schema"
)

// List creates a handler that lists records for the given entity.
func (f *Factory) List(entity *schema.Entity, opts *schema.ListOpts) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pool, err := f.db.ForEntity(entity.Name)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "database unavailable")
			return
		}

		builder := query.NewBuilder(entity, pool)

		// Build allowed columns set from opts
		allowedFilter := make(map[string]struct{})
		if opts != nil {
			for _, col := range opts.AllowFilterBy {
				allowedFilter[col] = struct{}{}
			}
		}
		allowedSort := make(map[string]struct{})
		if opts != nil {
			for _, col := range opts.AllowSortBy {
				allowedSort[col] = struct{}{}
			}
		}

		// Parse query parameters
		q := r.URL.Query()

		// Filters
		filters := make(map[string]interface{})
		for col := range allowedFilter {
			if v := q.Get(col); v != "" {
				filters[col] = v
			}
		}

		// Sort
		sortParam := q.Get("sort")
		if sortParam == "" && opts != nil {
			sortParam = opts.DefaultSort
		}
		// Validate sort column
		if sortParam != "" {
			parts := strings.SplitN(sortParam, ":", 2)
			if _, ok := allowedSort[parts[0]]; !ok && len(allowedSort) > 0 {
				writeError(w, http.StatusBadRequest, "invalid sort column")
				return
			}
		}

		// Pagination
		limit := 20
		maxLimit := 100
		if opts != nil && opts.Pagination.DefaultLimit > 0 {
			limit = opts.Pagination.DefaultLimit
		}
		if opts != nil && opts.Pagination.MaxLimit > 0 {
			maxLimit = opts.Pagination.MaxLimit
		}
		if lStr := q.Get("limit"); lStr != "" {
			parsed, err := strconv.Atoi(lStr)
			if err != nil || parsed < 1 {
				writeError(w, http.StatusBadRequest, "invalid limit")
				return
			}
			if parsed > maxLimit {
				parsed = maxLimit
			}
			limit = parsed
		}

		// Cursor
		var cursor *query.Cursor
		if cursorStr := q.Get("cursor"); cursorStr != "" {
			c, err := query.DecodeCursor(cursorStr, f.secret)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid cursor")
				return
			}
			cursor = c
		}

		lq := query.ListQuery{
			Filters: filters,
			Sort:    sortParam,
			Limit:   limit,
			Cursor:  cursor,
		}

		rows, err := builder.List(r.Context(), lq)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "query failed")
			return
		}

		// Strip omit_response fields
		for _, row := range rows {
			stripOmitFields(entity, row)
		}

		// Build next cursor if results fill the page
		var nextCursor string
		if len(rows) == limit && len(rows) > 0 {
			last := rows[len(rows)-1]
			c := query.Cursor{ID: stringVal(last["id"])}
			if ts, ok := last["created_at"]; ok {
				c.CreatedAt = stringVal(ts)
			}
			nextCursor = query.EncodeCursor(c, f.secret)
		}

		meta := map[string]interface{}{
			"count": len(rows),
		}
		if nextCursor != "" {
			meta["next_cursor"] = nextCursor
		}

		writeJSON(w, http.StatusOK, map[string]interface{}{
			"data": rows,
			"meta": meta,
		})
	}
}

// stringVal converts an interface{} to its string representation.
func stringVal(v interface{}) string {
	if v == nil {
		return ""
	}
	switch s := v.(type) {
	case string:
		return s
	default:
		return ""
	}
}

// stripOmitFields removes fields marked omit_response from a row.
func stripOmitFields(entity *schema.Entity, row map[string]interface{}) {
	for _, f := range entity.Fields {
		if f.OmitResponse {
			delete(row, f.ColumnName)
			delete(row, f.Name)
		}
	}
}

// writeJSON encodes v as JSON and writes to w with the given status code.
func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes a JSON error response.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
