package query

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/teleology-io/yayPI/internal/dialect"
	"github.com/teleology-io/yayPI/internal/schema"
	"github.com/teleology-io/yayPI/pkg/types"
)

// Sentinel errors returned by Builder methods.
var (
	ErrNotFound = errors.New("record not found")
	ErrNoFields = errors.New("no valid fields")
)

// Executor is satisfied by *sql.DB, *sql.Tx and *sql.Conn, so the same Builder runs
// inside or outside a transaction.
type Executor interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Builder generates and runs parameterized SQL for one entity.
type Builder struct {
	entity  *schema.Entity
	db      Executor
	dialect dialect.Dialect
}

// NewBuilder creates a Builder for the given entity, executor, and dialect.
func NewBuilder(entity *schema.Entity, db Executor, d dialect.Dialect) *Builder {
	return &Builder{entity: entity, db: db, dialect: d}
}

// RowFilter is an extra WHERE fragment (row-level access) using $1..$n placeholders
// local to the fragment; the builder renumbers them into the full query.
type RowFilter struct {
	SQL  string
	Args []any
}

// ListQuery contains parameters for a list query.
type ListQuery struct {
	Filters []Filter
	Sort    Sort
	Limit   int
	// Exactly one of Cursor (keyset) or Offset applies; Cursor wins when set.
	Cursor *Cursor
	Offset int
	Row    RowFilter
	Search Search
}

// Search matches Term as a case-insensitive substring in any of Columns.
type Search struct {
	Columns []string
	Term    string
}

// Sort is a single ORDER BY column. The primary key is always appended as a tiebreaker
// so ordering (and keyset pagination) is total.
type Sort struct {
	Column string
	Desc   bool
}

// args accumulates query arguments and hands out $N placeholders in textual order, which
// dialect.Rebind relies on when rewriting to ? for MySQL/SQLite.
type args struct{ vals []any }

func (a *args) add(v any) string {
	a.vals = append(a.vals, v)
	return "$" + strconv.Itoa(len(a.vals))
}

// addFragment appends a fragment that uses its own $1..$n numbering.
func (a *args) addFragment(sqlFrag string, fragArgs []any) string {
	out := reindexFilter(sqlFrag, len(a.vals))
	a.vals = append(a.vals, fragArgs...)
	return out
}

// reindexFilter shifts $N placeholders in a filter string by offset.
// e.g. offset=2, filter="user_id = $1 OR team = $2" → "user_id = $3 OR team = $4"
func reindexFilter(filter string, offset int) string {
	var b strings.Builder
	i := 0
	for i < len(filter) {
		if filter[i] == '$' && i+1 < len(filter) && filter[i+1] >= '1' && filter[i+1] <= '9' {
			j := i + 1
			for j < len(filter) && filter[j] >= '0' && filter[j] <= '9' {
				j++
			}
			n, _ := strconv.Atoi(filter[i+1 : j])
			b.WriteString("$" + strconv.Itoa(n+offset))
			i = j
		} else {
			b.WriteByte(filter[i])
			i++
		}
	}
	return b.String()
}

// baseWhere renders filters, soft-delete and row-access clauses.
func (b *Builder) baseWhere(a *args, filters []Filter, row RowFilter) ([]string, error) {
	var where []string
	for _, f := range filters {
		clause, err := b.filterClause(a, f)
		if err != nil {
			return nil, err
		}
		where = append(where, clause)
	}
	if b.entity.SoftDelete {
		where = append(where, b.qi("deleted_at")+" IS NULL")
	}
	if row.SQL != "" {
		where = append(where, "("+a.addFragment(row.SQL, row.Args)+")")
	}
	return where, nil
}

// List queries the database and returns matching rows. With a Cursor it uses keyset
// pagination on (sort column, primary key); otherwise LIMIT/OFFSET.
func (b *Builder) List(ctx context.Context, q ListQuery) ([]map[string]any, error) {
	a := &args{}
	where, err := b.baseWhere(a, q.Filters, q.Row)
	if err != nil {
		return nil, err
	}
	if clause := b.searchClause(a, q.Search); clause != "" {
		where = append(where, clause)
	}
	sort, err := b.resolveSort(q.Sort)
	if err != nil {
		return nil, err
	}
	if q.Cursor != nil {
		clause, err := b.keysetClause(a, sort, q.Cursor)
		if err != nil {
			return nil, err
		}
		where = append(where, clause)
	}

	sqlStr := fmt.Sprintf("SELECT %s FROM %s", b.selectColumns(), b.qi(b.entity.Table))
	if len(where) > 0 {
		sqlStr += " WHERE " + strings.Join(where, " AND ")
	}
	sqlStr += " ORDER BY " + b.orderBy(sort)

	limit := q.Limit
	if limit <= 0 {
		limit = 20
	}
	sqlStr += " LIMIT " + a.add(limit)
	if q.Cursor == nil && q.Offset > 0 {
		sqlStr += " OFFSET " + a.add(q.Offset)
	}

	rows, err := b.db.QueryContext(ctx, b.dialect.Rebind(sqlStr), a.vals...)
	if err != nil {
		return nil, fmt.Errorf("list query: %w", err)
	}
	defer rows.Close()
	return b.scanRows(rows)
}

// searchClause renders (LOWER(c1) LIKE $n OR LOWER(c2) LIKE $m …) for a search term.
func (b *Builder) searchClause(a *args, s Search) string {
	if s.Term == "" || len(s.Columns) == 0 {
		return ""
	}
	pattern := "%" + escapeLike(strings.ToLower(s.Term)) + "%"
	parts := make([]string, 0, len(s.Columns))
	for _, c := range s.Columns {
		if f, ok := b.field(c); ok {
			parts = append(parts, "LOWER("+b.qi(f.ColumnName)+") LIKE "+a.add(pattern)+" ESCAPE '!'")
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "(" + strings.Join(parts, " OR ") + ")"
}

// Count returns the number of rows matching the filters, search and row filter.
func (b *Builder) Count(ctx context.Context, filters []Filter, row RowFilter, search ...Search) (int64, error) {
	a := &args{}
	where, err := b.baseWhere(a, filters, row)
	if err != nil {
		return 0, err
	}
	if len(search) > 0 {
		if clause := b.searchClause(a, search[0]); clause != "" {
			where = append(where, clause)
		}
	}
	sqlStr := fmt.Sprintf("SELECT COUNT(*) FROM %s", b.qi(b.entity.Table))
	if len(where) > 0 {
		sqlStr += " WHERE " + strings.Join(where, " AND ")
	}
	var count int64
	if err := b.db.QueryRowContext(ctx, b.dialect.Rebind(sqlStr), a.vals...).Scan(&count); err != nil {
		return 0, fmt.Errorf("count query: %w", err)
	}
	return count, nil
}

// Get retrieves a single record by primary key; (nil, nil) when not found or hidden by
// the row filter. forUpdate locks the row (Postgres/MySQL) for read-modify-write in a tx.
func (b *Builder) Get(ctx context.Context, id any, row RowFilter, forUpdate ...bool) (map[string]any, error) {
	a := &args{}
	where := []string{b.qi(b.pk()) + " = " + a.add(id)}
	rest, err := b.baseWhere(a, nil, row)
	if err != nil {
		return nil, err
	}
	where = append(where, rest...)
	sqlStr := fmt.Sprintf("SELECT %s FROM %s WHERE %s", b.selectColumns(), b.qi(b.entity.Table), strings.Join(where, " AND "))
	if len(forUpdate) > 0 && forUpdate[0] && b.dialect.Name() != "sqlite" {
		sqlStr += " FOR UPDATE"
	}
	rows, err := b.db.QueryContext(ctx, b.dialect.Rebind(sqlStr), a.vals...)
	if err != nil {
		return nil, fmt.Errorf("get query: %w", err)
	}
	defer rows.Close()
	results, err := b.scanRows(rows)
	if err != nil || len(results) == 0 {
		return nil, err
	}
	return results[0], nil
}

// Create inserts a new record and returns the inserted row. A uuid primary key the
// caller did not supply is generated here, so every dialect can read the row back by id
// (MySQL has no RETURNING and LastInsertId is meaningless for uuids).
func (b *Builder) Create(ctx context.Context, data map[string]any) (map[string]any, error) {
	pk := b.entity.PrimaryKey()
	if pk != nil && pk.Type == types.FieldTypeUUID {
		if v, ok := lookup(data, *pk); !ok || v == nil || v == "" {
			if pk.Name != pk.ColumnName {
				delete(data, pk.Name)
			}
			data[pk.ColumnName] = uuid.NewString()
		}
	}

	a := &args{}
	var colNames, placeholders []string
	for _, field := range b.entity.Fields {
		val, ok := lookup(data, field)
		if !ok {
			continue
		}
		colNames = append(colNames, b.qi(field.ColumnName))
		placeholders = append(placeholders, a.add(val))
	}
	if len(colNames) == 0 {
		return nil, ErrNoFields
	}
	table := b.qi(b.entity.Table)

	if b.dialect.SupportsReturning() {
		sqlStr := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s) RETURNING %s",
			table, strings.Join(colNames, ", "), strings.Join(placeholders, ", "), b.selectColumns())
		rows, err := b.db.QueryContext(ctx, b.dialect.Rebind(sqlStr), a.vals...)
		if err != nil {
			return nil, fmt.Errorf("create query: %w", err)
		}
		defer rows.Close()
		results, err := b.scanRows(rows)
		if err != nil {
			return nil, err
		}
		if len(results) == 0 {
			return nil, errors.New("create returned no rows")
		}
		return results[0], nil
	}

	sqlStr := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
		table, strings.Join(colNames, ", "), strings.Join(placeholders, ", "))
	result, err := b.db.ExecContext(ctx, b.dialect.Rebind(sqlStr), a.vals...)
	if err != nil {
		return nil, fmt.Errorf("create query: %w", err)
	}
	var id any
	if pk != nil {
		if v, ok := lookup(data, *pk); ok {
			id = v
		}
	}
	if id == nil {
		lastID, err := result.LastInsertId()
		if err != nil {
			return nil, fmt.Errorf("create: getting last insert id: %w", err)
		}
		id = lastID
	}
	return b.Get(ctx, id, RowFilter{})
}

// Update modifies an existing record and returns the updated row; ErrNotFound when no
// row matches the id and row filter.
func (b *Builder) Update(ctx context.Context, id any, data map[string]any, row RowFilter) (map[string]any, error) {
	a := &args{}
	var setClauses []string
	for _, field := range b.entity.Fields {
		if field.PrimaryKey || field.ColumnName == "updated_at" || field.ColumnName == "created_at" || field.ColumnName == "deleted_at" {
			continue
		}
		val, ok := lookup(data, field)
		if !ok {
			continue
		}
		setClauses = append(setClauses, b.qi(field.ColumnName)+" = "+a.add(val))
	}
	if len(setClauses) == 0 {
		return nil, ErrNoFields
	}
	if b.entity.Timestamps {
		setClauses = append(setClauses, b.qi("updated_at")+" = "+a.add(time.Now().UTC()))
	}

	where := []string{b.qi(b.pk()) + " = " + a.add(id)}
	rest, err := b.baseWhere(a, nil, row)
	if err != nil {
		return nil, err
	}
	where = append(where, rest...)
	table := b.qi(b.entity.Table)

	if b.dialect.SupportsReturning() {
		sqlStr := fmt.Sprintf("UPDATE %s SET %s WHERE %s RETURNING %s",
			table, strings.Join(setClauses, ", "), strings.Join(where, " AND "), b.selectColumns())
		rows, err := b.db.QueryContext(ctx, b.dialect.Rebind(sqlStr), a.vals...)
		if err != nil {
			return nil, fmt.Errorf("update query: %w", err)
		}
		defer rows.Close()
		results, err := b.scanRows(rows)
		if err != nil {
			return nil, err
		}
		if len(results) == 0 {
			return nil, ErrNotFound
		}
		return results[0], nil
	}

	sqlStr := fmt.Sprintf("UPDATE %s SET %s WHERE %s", table, strings.Join(setClauses, ", "), strings.Join(where, " AND "))
	if _, err := b.db.ExecContext(ctx, b.dialect.Rebind(sqlStr), a.vals...); err != nil {
		return nil, fmt.Errorf("update query: %w", err)
	}
	// MySQL reports 0 affected rows when values are unchanged, so confirm with a read
	// that re-applies the row filter rather than trusting RowsAffected.
	updated, err := b.Get(ctx, id, row)
	if err != nil {
		return nil, err
	}
	if updated == nil {
		return nil, ErrNotFound
	}
	return updated, nil
}

// Delete removes a record by primary key (soft delete when the entity supports it and
// soft is true). ErrNotFound when no row matches.
func (b *Builder) Delete(ctx context.Context, id any, soft bool, row RowFilter) error {
	a := &args{}
	var sqlStr string
	table := b.qi(b.entity.Table)
	if soft && b.entity.SoftDelete {
		set := b.qi("deleted_at") + " = " + a.add(time.Now().UTC())
		where := []string{b.qi(b.pk()) + " = " + a.add(id)}
		rest, err := b.baseWhere(a, nil, row) // includes deleted_at IS NULL
		if err != nil {
			return err
		}
		where = append(where, rest...)
		sqlStr = fmt.Sprintf("UPDATE %s SET %s WHERE %s", table, set, strings.Join(where, " AND "))
	} else {
		where := []string{b.qi(b.pk()) + " = " + a.add(id)}
		if row.SQL != "" {
			where = append(where, "("+a.addFragment(row.SQL, row.Args)+")")
		}
		sqlStr = fmt.Sprintf("DELETE FROM %s WHERE %s", table, strings.Join(where, " AND "))
	}

	result, err := b.db.ExecContext(ctx, b.dialect.Rebind(sqlStr), a.vals...)
	if err != nil {
		return fmt.Errorf("delete query: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete: rows affected: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// lookup finds a field's value in data by column name, then by field name.
func lookup(data map[string]any, f schema.Field) (any, bool) {
	if v, ok := data[f.ColumnName]; ok {
		return v, true
	}
	v, ok := data[f.Name]
	return v, ok
}

// pk returns the primary key column.
func (b *Builder) pk() string { return b.entity.PKColumn() }

// selectColumns returns a comma-separated list of quoted column names.
func (b *Builder) selectColumns() string {
	cols := make([]string, 0, len(b.entity.Fields))
	for _, f := range b.entity.Fields {
		cols = append(cols, b.qi(f.ColumnName))
	}
	return strings.Join(cols, ", ")
}

// field resolves a column or field name to the entity field.
func (b *Builder) field(name string) (*schema.Field, bool) {
	for i := range b.entity.Fields {
		f := &b.entity.Fields[i]
		if f.ColumnName == name || f.Name == name {
			return f, true
		}
	}
	return nil, false
}

// qi is a shorthand for dialect.QuoteIdent.
func (b *Builder) qi(name string) string {
	return b.dialect.QuoteIdent(name)
}

// jsonColumns returns the set of this entity's column names whose field type is jsonb.
// database/sql scans jsonb values as []byte; left alone, encoding/json would
// base64-encode them. Only declared jsonb columns get the json.RawMessage treatment, so
// a text column holding JSON-looking text is never misinterpreted.
func (b *Builder) jsonColumns() map[string]bool {
	cols := make(map[string]bool)
	for _, f := range b.entity.Fields {
		if f.Type == types.FieldTypeJSONB {
			cols[f.ColumnName] = true
		}
	}
	return cols
}

// scanRows converts database/sql rows to a slice of string-keyed maps. Text that MySQL
// hands back as []byte is converted to string so JSON encoding stays readable.
func (b *Builder) scanRows(rows *sql.Rows) ([]map[string]any, error) {
	cols, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("getting columns: %w", err)
	}
	jsonCols := b.jsonColumns()

	results := []map[string]any{}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, fmt.Errorf("scanning row: %w", err)
		}
		row := make(map[string]any, len(cols))
		for i, col := range cols {
			v := vals[i]
			switch raw := v.(type) {
			case []byte:
				v = b.convertBytes(col, raw, jsonCols[col])
			case string:
				// SQLite (and some MySQL setups) return JSON columns as text.
				if jsonCols[col] && json.Valid([]byte(raw)) {
					v = json.RawMessage(raw)
				}
			}
			row[col] = v
		}
		results = append(results, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating rows: %w", err)
	}
	return results, nil
}

// convertBytes turns driver []byte values into JSON-friendly types: jsonb → raw JSON,
// 16-byte uuid → canonical string, bytea stays binary, everything else → string.
func (b *Builder) convertBytes(col string, raw []byte, isJSON bool) any {
	if isJSON && len(raw) > 0 {
		return json.RawMessage(append([]byte(nil), raw...))
	}
	if f, ok := b.field(col); ok {
		switch f.Type {
		case types.FieldTypeBytea:
			return append([]byte(nil), raw...)
		case types.FieldTypeUUID:
			if len(raw) == 16 {
				if u, err := uuid.FromBytes(raw); err == nil {
					return u.String()
				}
			}
		}
	}
	return string(raw)
}
