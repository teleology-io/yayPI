package handler

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/teleology-io/yayPI/internal/middleware"
	"github.com/teleology-io/yayPI/internal/plugin"
	"github.com/teleology-io/yayPI/internal/policy"
	"github.com/teleology-io/yayPI/internal/query"
	"github.com/teleology-io/yayPI/internal/schema"
)

// ReadRequest reads rows the way a list endpoint serves them to Subject.
type ReadRequest struct {
	Entity  string
	Subject *middleware.Subject
	// Via is the list endpoint whose rules apply ("GET /cycles"); "" picks
	// the entity's only list endpoint.
	Via string
	// Where filters like list query parameters: "field" (equality; []any is
	// IN, nil IS NULL) or "field[op]" with gt gte lt lte ne in nin contains
	// starts_with is_null.
	Where  map[string]any
	Sort   string // "field" or "-field"; default primary key
	Limit  int    // page size; default and cap: 1000
	Cursor string // ReadPage.Next of the previous page
}

// ReadPage is one page of a Read; Next is "" on the last page.
type ReadPage struct {
	Rows []map[string]any
	Next string
}

// ErrInvalidRead marks a read that can't be served as asked.
var ErrInvalidRead = errors.New("invalid read")

const maxReadRows = 1000

// Read returns the entity's rows visible to the subject through a list
// endpoint: its row_access rules, the tenant scope, and field visibility
// (omit_response, read_roles) apply exactly as for an HTTP list. Inside an
// entity hook it reads in the write's transaction.
func (f *Factory) Read(ctx context.Context, req ReadRequest) (ReadPage, error) {
	rows, next, err := f.read(ctx, req)
	return ReadPage{Rows: rows, Next: next}, err
}

// ReadAll follows Read's pages and returns every matching row (Limit is
// the page size).
func (f *Factory) ReadAll(ctx context.Context, req ReadRequest) ([]map[string]any, error) {
	var all []map[string]any
	for {
		rows, next, err := f.read(ctx, req)
		if err != nil {
			return nil, err
		}
		all = append(all, rows...)
		if next == "" {
			return all, nil
		}
		req.Cursor = next
	}
}

func (f *Factory) read(ctx context.Context, req ReadRequest) ([]map[string]any, string, error) {
	entity, ok := f.registry.GetEntity(req.Entity)
	if !ok || entity.Internal {
		return nil, "", fmt.Errorf("%w: unknown entity %q", ErrInvalidRead, req.Entity)
	}
	if req.Subject == nil {
		return nil, "", fmt.Errorf("%w: Read applies a subject's rules; pass Subject (use Query for system reads)", ErrInvalidRead)
	}
	opts, err := f.listEndpoint(entity.Name, req.Via)
	if err != nil {
		return nil, "", err
	}
	dbc, err := f.db.ForEntity(entity.Name)
	if err != nil {
		return nil, "", err
	}

	row := query.RowFilter{}
	if len(opts.RowAccess) > 0 {
		frag, args, err := policy.ResolveRowFilter(opts.RowAccess, req.Subject)
		if errors.Is(err, policy.ErrRowAccessDenied) {
			return []map[string]any{}, "", nil // the endpoint would refuse this subject
		}
		if err != nil {
			return nil, "", err
		}
		row = query.RowFilter{SQL: frag, Args: args}
	}
	if entity.TenantColumn != "" {
		if req.Subject.Tenant == "" {
			return []map[string]any{}, "", nil
		}
		clause := dbc.Dialect.QuoteIdent(entity.TenantColumn) + " = $" + strconv.Itoa(len(row.Args)+1)
		if row.SQL != "" {
			clause = "(" + row.SQL + ") AND " + clause
		}
		row = query.RowFilter{SQL: clause, Args: append(append([]any(nil), row.Args...), req.Subject.Tenant)}
	}

	var filters []query.Filter
	keys := make([]string, 0, len(req.Where))
	for k := range req.Where {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		name, op := k, ""
		if i := strings.IndexByte(k, '['); i > 0 && strings.HasSuffix(k, "]") {
			name, op = k[:i], k[i+1:len(k)-1]
			if !query.FilterOps[op] {
				return nil, "", fmt.Errorf("%w: unknown operator %q in %q", ErrInvalidRead, op, k)
			}
		}
		field, ok := fieldByKey(entity, name)
		if !ok {
			return nil, "", fmt.Errorf("%w: %s has no field %q", ErrInvalidRead, entity.Name, name)
		}
		v := req.Where[k]
		list, isList := v.([]any)
		switch {
		case op == "is_null":
			b, _ := v.(bool)
			filters = append(filters, query.Filter{Column: field.ColumnName, Op: op, Value: b})
			continue
		case v == nil && (op == "" || op == "eq"):
			filters = append(filters, query.Filter{Column: field.ColumnName, Op: "is_null", Value: true})
			continue
		case isList && (op == "" || op == "in" || op == "nin"):
			if op == "" {
				op = "in"
			}
			vals := make([]any, len(list))
			for i, x := range list {
				if vals[i], err = whereValue(field, x, dbc.Dialect.Name()); err != nil {
					return nil, "", err
				}
			}
			filters = append(filters, query.Filter{Column: field.ColumnName, Op: op, Value: vals})
			continue
		case op == "in" || op == "nin":
			return nil, "", fmt.Errorf("%w: %s takes a []any", ErrInvalidRead, k)
		case op == "":
			op = "eq"
		}
		var cv any = v
		if op != "contains" && op != "starts_with" {
			if cv, err = whereValue(field, v, dbc.Dialect.Name()); err != nil {
				return nil, "", err
			}
		}
		filters = append(filters, query.Filter{Column: field.ColumnName, Op: op, Value: cv})
	}

	srt := query.Sort{Column: entity.PKColumn()}
	if s := strings.TrimSpace(req.Sort); s != "" {
		desc := strings.HasPrefix(s, "-")
		field, ok := fieldByKey(entity, strings.TrimPrefix(s, "-"))
		if !ok {
			return nil, "", fmt.Errorf("%w: %s has no field %q to sort by", ErrInvalidRead, entity.Name, s)
		}
		srt = query.Sort{Column: field.ColumnName, Desc: desc}
	}
	limit := req.Limit
	if limit <= 0 || limit > maxReadRows {
		limit = maxReadRows
	}

	lq := query.ListQuery{Filters: filters, Sort: srt, Limit: limit + 1, Row: row}
	if req.Cursor != "" {
		c, err := query.DecodeCursor(req.Cursor, f.secret)
		if err != nil {
			return nil, "", fmt.Errorf("%w: invalid cursor", ErrInvalidRead)
		}
		lq.Cursor = c
	}
	var x query.Executor = dbc.SQL
	if tx := plugin.WriteTx(ctx); tx != nil {
		x = tx
	}
	rows, err := query.NewBuilder(entity, x, dbc.Dialect).List(ctx, lq)
	if errors.Is(err, query.ErrCursorMismatch) {
		return nil, "", fmt.Errorf("%w: the cursor was issued for a different sort", ErrInvalidRead)
	}
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		next = query.EncodeCursor(query.NewCursor(entity, srt, rows[len(rows)-1]), f.secret)
	}
	for _, r := range rows {
		presentRecord(entity, r, req.Subject)
	}
	return rows, next, nil
}

func whereValue(f *schema.Field, v any, dialectName string) (any, error) {
	cv, msg := coerceValue(f, v, dialectName)
	if msg != "" {
		return nil, fmt.Errorf("%w: where %s", ErrInvalidRead, msg)
	}
	return cv, nil
}

// listEndpoint finds the list options whose rules a Read applies.
func (f *Factory) listEndpoint(entity, via string) (*schema.ListOpts, error) {
	var found []*schema.Endpoint
	for _, ep := range f.registry.Endpoints() {
		if ep.Entity != entity || !hasOp(ep.CRUD, "list") {
			continue
		}
		if via != "" {
			method, path, _ := strings.Cut(strings.TrimSpace(via), " ")
			if !strings.EqualFold(method, "GET") || strings.TrimSpace(path) != ep.Path {
				continue
			}
		}
		found = append(found, ep)
	}
	switch {
	case len(found) == 1:
		if found[0].List == nil {
			return &schema.ListOpts{}, nil
		}
		return found[0].List, nil
	case len(found) == 0 && via != "":
		return nil, fmt.Errorf("%w: no list endpoint %q for %s", ErrInvalidRead, via, entity)
	case len(found) == 0:
		return nil, fmt.Errorf("%w: %s has no list endpoint, so no read rules to apply (declare one, or use Query)", ErrInvalidRead, entity)
	}
	paths := make([]string, len(found))
	for i, ep := range found {
		paths[i] = "GET " + ep.Path
	}
	return nil, fmt.Errorf("%w: %s has several list endpoints; set Via to one of: %s", ErrInvalidRead, entity, strings.Join(paths, ", "))
}

func hasOp(ops []string, op string) bool {
	for _, o := range ops {
		if o == op {
			return true
		}
	}
	return false
}
