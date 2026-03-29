package query

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/teleology-io/yayPI/internal/dialect"
	"github.com/teleology-io/yayPI/internal/schema"
)

// Builder generates parameterized SQL for a given entity.
type Builder struct {
	entity  *schema.Entity
	db      *sql.DB
	dialect dialect.Dialect
}

// NewBuilder creates a Builder for the given entity, db connection, and dialect.
func NewBuilder(entity *schema.Entity, db *sql.DB, d dialect.Dialect) *Builder {
	return &Builder{entity: entity, db: db, dialect: d}
}

// ListQuery contains parameters for a list query.
type ListQuery struct {
	Filters     map[string]interface{}
	Sort        string // "column:asc" or "column:desc"
	Limit       int
	Cursor      *Cursor
	AllowedCols map[string]struct{} // validated allowed columns for filter/sort
}

// reindexFilter shifts $N placeholders in a filter string by offset.
// e.g. offset=2, filter="user_id = $1 OR team = $2" → "user_id = $3 OR team = $4"
// This ensures the extra-filter placeholders don't collide with those already in the query.
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
			b.WriteString(fmt.Sprintf("$%d", n+offset))
			i = j
		} else {
			b.WriteByte(filter[i])
			i++
		}
	}
	return b.String()
}

// List queries the database and returns matching rows.
func (b *Builder) List(ctx context.Context, q ListQuery, extraFilter string, extraArgs []interface{}) ([]map[string]interface{}, error) {
	cols := b.selectColumns()
	table := b.entity.Table

	var args []interface{}
	var whereClauses []string
	argIdx := 1

	if q.Cursor != nil {
		whereClauses = append(whereClauses, fmt.Sprintf("id > %s", b.ph(argIdx)))
		args = append(args, q.Cursor.ID)
		argIdx++
	}

	for col, val := range q.Filters {
		if !b.isAllowedColumn(col) {
			continue
		}
		whereClauses = append(whereClauses, fmt.Sprintf("%s = %s", b.qi(col), b.ph(argIdx)))
		args = append(args, val)
		argIdx++
	}

	if b.entity.SoftDelete {
		whereClauses = append(whereClauses, "deleted_at IS NULL")
	}

	if extraFilter != "" {
		whereClauses = append(whereClauses, "("+reindexFilter(extraFilter, argIdx-1)+")")
		args = append(args, extraArgs...)
		argIdx += len(extraArgs)
	}

	sqlStr := fmt.Sprintf("SELECT %s FROM %s", cols, b.qi(table))
	if len(whereClauses) > 0 {
		sqlStr += " WHERE " + strings.Join(whereClauses, " AND ")
	}

	if q.Sort != "" {
		orderClause, err := b.buildOrderClause(q.Sort)
		if err != nil {
			return nil, err
		}
		sqlStr += " ORDER BY " + orderClause
	} else {
		sqlStr += " ORDER BY id ASC"
	}

	limit := q.Limit
	if limit <= 0 {
		limit = 20
	}
	sqlStr += fmt.Sprintf(" LIMIT %s", b.ph(argIdx))
	args = append(args, limit)

	sqlStr = b.dialect.Rebind(sqlStr)
	rows, err := b.db.QueryContext(ctx, sqlStr, args...)
	if err != nil {
		return nil, fmt.Errorf("list query: %w", err)
	}
	defer rows.Close()
	return scanRows(rows)
}

// Get retrieves a single record by ID.
func (b *Builder) Get(ctx context.Context, id string, extraFilter string, extraArgs []interface{}) (map[string]interface{}, error) {
	cols := b.selectColumns()
	table := b.entity.Table

	args := []interface{}{id}
	argIdx := 2 // id is $1

	var extraClauses []string
	if b.entity.SoftDelete {
		extraClauses = append(extraClauses, "deleted_at IS NULL")
	}
	if extraFilter != "" {
		extraClauses = append(extraClauses, "("+reindexFilter(extraFilter, argIdx-1)+")")
		args = append(args, extraArgs...)
	}

	sqlStr := fmt.Sprintf("SELECT %s FROM %s WHERE id = $1", cols, b.qi(table))
	for _, c := range extraClauses {
		sqlStr += " AND " + c
	}
	sqlStr = b.dialect.Rebind(sqlStr)

	rows, err := b.db.QueryContext(ctx, sqlStr, args...)
	if err != nil {
		return nil, fmt.Errorf("get query: %w", err)
	}
	defer rows.Close()

	results, err := scanRows(rows)
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, nil
	}
	return results[0], nil
}

// Create inserts a new record and returns the inserted row.
func (b *Builder) Create(ctx context.Context, data map[string]interface{}) (map[string]interface{}, error) {
	var colNames []string
	var placeholders []string
	var args []interface{}
	argIdx := 1

	for _, field := range b.entity.Fields {
		if field.PrimaryKey {
			if _, ok := data[field.ColumnName]; !ok {
				continue
			}
		}
		val, ok := data[field.ColumnName]
		if !ok {
			val, ok = data[field.Name]
			if !ok {
				continue
			}
		}
		colNames = append(colNames, b.qi(field.ColumnName))
		placeholders = append(placeholders, b.ph(argIdx))
		args = append(args, val)
		argIdx++
	}

	if len(colNames) == 0 {
		return nil, fmt.Errorf("no valid fields to insert")
	}

	table := b.entity.Table

	if b.dialect.SupportsReturning() {
		sqlStr := b.dialect.Rebind(fmt.Sprintf(
			"INSERT INTO %s (%s) VALUES (%s) RETURNING %s",
			b.qi(table), strings.Join(colNames, ", "),
			strings.Join(placeholders, ", "), b.selectColumns(),
		))
		rows, err := b.db.QueryContext(ctx, sqlStr, args...)
		if err != nil {
			return nil, fmt.Errorf("create query: %w", err)
		}
		defer rows.Close()
		results, err := scanRows(rows)
		if err != nil {
			return nil, err
		}
		if len(results) == 0 {
			return nil, fmt.Errorf("create returned no rows")
		}
		return results[0], nil
	}

	// MySQL fallback: INSERT then SELECT by last insert id
	sqlStr := b.dialect.Rebind(fmt.Sprintf(
		"INSERT INTO %s (%s) VALUES (%s)",
		b.qi(table), strings.Join(colNames, ", "), strings.Join(placeholders, ", "),
	))
	result, err := b.db.ExecContext(ctx, sqlStr, args...)
	if err != nil {
		return nil, fmt.Errorf("create query: %w", err)
	}
	lastID, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("create: getting last insert id: %w", err)
	}
	return b.Get(ctx, fmt.Sprintf("%d", lastID), "", nil)
}

// Update modifies an existing record and returns the updated row.
func (b *Builder) Update(ctx context.Context, id string, data map[string]interface{}, extraFilter string, extraArgs []interface{}) (map[string]interface{}, error) {
	var setClauses []string
	var args []interface{}
	argIdx := 1

	for _, field := range b.entity.Fields {
		if field.PrimaryKey {
			continue
		}
		val, ok := data[field.ColumnName]
		if !ok {
			val, ok = data[field.Name]
			if !ok {
				continue
			}
		}
		setClauses = append(setClauses, fmt.Sprintf("%s = %s", b.qi(field.ColumnName), b.ph(argIdx)))
		args = append(args, val)
		argIdx++
	}

	if len(setClauses) == 0 {
		return nil, fmt.Errorf("no valid fields to update")
	}

	if b.entity.Timestamps {
		setClauses = append(setClauses, fmt.Sprintf("updated_at = %s", b.ph(argIdx)))
		args = append(args, time.Now().UTC())
		argIdx++
	}

	args = append(args, id)
	table := b.entity.Table

	whereClause := fmt.Sprintf("id = %s", b.ph(argIdx))
	argIdx++
	if extraFilter != "" {
		whereClause += " AND (" + reindexFilter(extraFilter, argIdx-1) + ")"
		args = append(args, extraArgs...)
		argIdx += len(extraArgs)
	}

	if b.dialect.SupportsReturning() {
		sqlStr := b.dialect.Rebind(fmt.Sprintf(
			"UPDATE %s SET %s WHERE %s RETURNING %s",
			b.qi(table), strings.Join(setClauses, ", "), whereClause, b.selectColumns(),
		))
		rows, err := b.db.QueryContext(ctx, sqlStr, args...)
		if err != nil {
			return nil, fmt.Errorf("update query: %w", err)
		}
		defer rows.Close()
		results, err := scanRows(rows)
		if err != nil {
			return nil, err
		}
		if len(results) == 0 {
			return nil, nil
		}
		return results[0], nil
	}

	// MySQL fallback: UPDATE then SELECT
	sqlStr := b.dialect.Rebind(fmt.Sprintf(
		"UPDATE %s SET %s WHERE %s",
		b.qi(table), strings.Join(setClauses, ", "), whereClause,
	))
	if _, err := b.db.ExecContext(ctx, sqlStr, args...); err != nil {
		return nil, fmt.Errorf("update query: %w", err)
	}
	return b.Get(ctx, id, "", nil)
}

// Delete removes a record by ID (hard or soft delete).
func (b *Builder) Delete(ctx context.Context, id string, soft bool, extraFilter string, extraArgs []interface{}) error {
	table := b.entity.Table
	var sqlStr string
	var args []interface{}

	if soft && b.entity.SoftDelete {
		argIdx := 3 // $1=now, $2=id, extra starts at $3
		whereClause := "id = $2 AND deleted_at IS NULL"
		if extraFilter != "" {
			whereClause += " AND (" + reindexFilter(extraFilter, argIdx-1) + ")"
		}
		sqlStr = b.dialect.Rebind(fmt.Sprintf(
			"UPDATE %s SET deleted_at = $1 WHERE %s", b.qi(table), whereClause,
		))
		args = []interface{}{time.Now().UTC(), id}
		args = append(args, extraArgs...)
	} else {
		argIdx := 2 // $1=id, extra starts at $2
		whereClause := "id = $1"
		if extraFilter != "" {
			whereClause += " AND (" + reindexFilter(extraFilter, argIdx-1) + ")"
		}
		sqlStr = b.dialect.Rebind(fmt.Sprintf("DELETE FROM %s WHERE %s", b.qi(table), whereClause))
		args = []interface{}{id}
		args = append(args, extraArgs...)
	}

	result, err := b.db.ExecContext(ctx, sqlStr, args...)
	if err != nil {
		return fmt.Errorf("delete query: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete: rows affected: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("record not found")
	}
	return nil
}

// LoadRelation loads related records using IN-clause batching.
func (b *Builder) LoadRelation(ctx context.Context, rel schema.Relation, ids []string) (map[string][]map[string]interface{}, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = b.ph(i + 1)
		args[i] = id
	}

	fk := rel.ForeignKey
	if fk == "" {
		fk = "id"
	}

	table := rel.Entity
	sqlStr := b.dialect.Rebind(fmt.Sprintf(
		"SELECT * FROM %s WHERE %s IN (%s)",
		b.qi(table), b.qi(fk), strings.Join(placeholders, ", "),
	))

	rows, err := b.db.QueryContext(ctx, sqlStr, args...)
	if err != nil {
		return nil, fmt.Errorf("load relation %q: %w", rel.Name, err)
	}
	defer rows.Close()

	all, err := scanRows(rows)
	if err != nil {
		return nil, err
	}

	result := make(map[string][]map[string]interface{})
	for _, row := range all {
		key := fmt.Sprintf("%v", row[fk])
		result[key] = append(result[key], row)
	}
	return result, nil
}

// selectColumns returns a comma-separated list of quoted column names.
func (b *Builder) selectColumns() string {
	cols := make([]string, 0, len(b.entity.Fields))
	for _, f := range b.entity.Fields {
		cols = append(cols, b.qi(f.ColumnName))
	}
	return strings.Join(cols, ", ")
}

func (b *Builder) isAllowedColumn(col string) bool {
	for _, f := range b.entity.Fields {
		if f.ColumnName == col || f.Name == col {
			return true
		}
	}
	return false
}

func (b *Builder) buildOrderClause(sort string) (string, error) {
	parts := strings.SplitN(sort, ":", 2)
	col := parts[0]
	dir := "ASC"
	if len(parts) == 2 {
		switch strings.ToUpper(parts[1]) {
		case "ASC", "DESC":
			dir = strings.ToUpper(parts[1])
		default:
			return "", fmt.Errorf("invalid sort direction %q", parts[1])
		}
	}
	if !b.isAllowedColumn(col) {
		return "", fmt.Errorf("sort column %q is not a known entity field", col)
	}
	return b.qi(col) + " " + dir, nil
}

// qi is a shorthand for dialect.QuoteIdent.
func (b *Builder) qi(name string) string {
	return b.dialect.QuoteIdent(name)
}

// ph returns the Nth positional placeholder in the dialect's format ($N or ?).
func (b *Builder) ph(n int) string {
	if b.dialect.Name() == "postgres" {
		return fmt.Sprintf("$%d", n)
	}
	return "?"
}

// scanRows converts database/sql rows to a slice of string-keyed maps.
func scanRows(rows *sql.Rows) ([]map[string]interface{}, error) {
	cols, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("getting columns: %w", err)
	}

	var results []map[string]interface{}
	for rows.Next() {
		vals := make([]interface{}, len(cols))
		ptrs := make([]interface{}, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, fmt.Errorf("scanning row: %w", err)
		}
		row := make(map[string]interface{}, len(cols))
		for i, col := range cols {
			row[col] = vals[i]
		}
		results = append(results, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating rows: %w", err)
	}
	return results, nil
}
