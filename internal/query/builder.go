package query

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/csullivan/yaypi/internal/schema"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Builder generates parameterized SQL for a given entity.
type Builder struct {
	entity *schema.Entity
	pool   *pgxpool.Pool
}

// NewBuilder creates a Builder for the given entity and pool.
func NewBuilder(entity *schema.Entity, pool *pgxpool.Pool) *Builder {
	return &Builder{entity: entity, pool: pool}
}

// ListQuery contains parameters for a list query.
type ListQuery struct {
	Filters    map[string]interface{}
	Sort       string // "column:asc" or "column:desc"
	Limit      int
	Cursor     *Cursor
	AllowedCols map[string]struct{} // validated allowed columns for filter/sort
}

// List queries the database and returns matching rows.
func (b *Builder) List(ctx context.Context, q ListQuery) ([]map[string]interface{}, error) {
	cols := b.selectColumns()
	table := b.entity.Table

	var args []interface{}
	var whereClauses []string
	argIdx := 1

	// Apply cursor for cursor-based pagination
	if q.Cursor != nil {
		whereClauses = append(whereClauses, fmt.Sprintf("id > $%d", argIdx))
		args = append(args, q.Cursor.ID)
		argIdx++
	}

	// Apply filters — only allowed columns (already validated by caller)
	for col, val := range q.Filters {
		if !b.isAllowedColumn(col) {
			continue
		}
		whereClauses = append(whereClauses, fmt.Sprintf("%s = $%d", pgQuoteIdent(col), argIdx))
		args = append(args, val)
		argIdx++
	}

	// Soft delete filter
	if b.entity.SoftDelete {
		whereClauses = append(whereClauses, "deleted_at IS NULL")
	}

	sql := fmt.Sprintf("SELECT %s FROM %s", cols, pgQuoteIdent(table))
	if len(whereClauses) > 0 {
		sql += " WHERE " + strings.Join(whereClauses, " AND ")
	}

	// Sort
	if q.Sort != "" {
		orderClause, err := b.buildOrderClause(q.Sort)
		if err != nil {
			return nil, err
		}
		sql += " ORDER BY " + orderClause
	} else {
		sql += " ORDER BY id ASC"
	}

	// Limit
	limit := q.Limit
	if limit <= 0 {
		limit = 20
	}
	sql += fmt.Sprintf(" LIMIT $%d", argIdx)
	args = append(args, limit)

	rows, err := b.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("list query: %w", err)
	}
	defer rows.Close()

	return scanRows(rows)
}

// Get retrieves a single record by ID.
func (b *Builder) Get(ctx context.Context, id string) (map[string]interface{}, error) {
	cols := b.selectColumns()
	table := b.entity.Table

	sql := fmt.Sprintf("SELECT %s FROM %s WHERE id = $1", cols, pgQuoteIdent(table))
	if b.entity.SoftDelete {
		sql += " AND deleted_at IS NULL"
	}

	rows, err := b.pool.Query(ctx, sql, id)
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
	// Only insert columns that exist in the entity field definitions.
	var cols []string
	var placeholders []string
	var args []interface{}
	argIdx := 1

	for _, field := range b.entity.Fields {
		if field.PrimaryKey {
			// Skip primary key if it will be auto-generated
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
		cols = append(cols, pgQuoteIdent(field.ColumnName))
		placeholders = append(placeholders, fmt.Sprintf("$%d", argIdx))
		args = append(args, val)
		argIdx++
	}

	if len(cols) == 0 {
		return nil, fmt.Errorf("no valid fields to insert")
	}

	table := b.entity.Table
	sql := fmt.Sprintf(
		"INSERT INTO %s (%s) VALUES (%s) RETURNING %s",
		pgQuoteIdent(table),
		strings.Join(cols, ", "),
		strings.Join(placeholders, ", "),
		b.selectColumns(),
	)

	rows, err := b.pool.Query(ctx, sql, args...)
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

// Update modifies an existing record and returns the updated row.
func (b *Builder) Update(ctx context.Context, id string, data map[string]interface{}) (map[string]interface{}, error) {
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
		setClauses = append(setClauses, fmt.Sprintf("%s = $%d", pgQuoteIdent(field.ColumnName), argIdx))
		args = append(args, val)
		argIdx++
	}

	if len(setClauses) == 0 {
		return nil, fmt.Errorf("no valid fields to update")
	}

	// Update updated_at if the entity has timestamps
	if b.entity.Timestamps {
		setClauses = append(setClauses, fmt.Sprintf("updated_at = $%d", argIdx))
		args = append(args, time.Now().UTC())
		argIdx++
	}

	args = append(args, id)
	table := b.entity.Table
	sql := fmt.Sprintf(
		"UPDATE %s SET %s WHERE id = $%d RETURNING %s",
		pgQuoteIdent(table),
		strings.Join(setClauses, ", "),
		argIdx,
		b.selectColumns(),
	)

	rows, err := b.pool.Query(ctx, sql, args...)
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

// Delete removes a record by ID (hard or soft delete).
func (b *Builder) Delete(ctx context.Context, id string, soft bool) error {
	table := b.entity.Table
	var sql string
	var args []interface{}

	if soft && b.entity.SoftDelete {
		sql = fmt.Sprintf("UPDATE %s SET deleted_at = $1 WHERE id = $2 AND deleted_at IS NULL", pgQuoteIdent(table))
		args = []interface{}{time.Now().UTC(), id}
	} else {
		sql = fmt.Sprintf("DELETE FROM %s WHERE id = $1", pgQuoteIdent(table))
		args = []interface{}{id}
	}

	result, err := b.pool.Exec(ctx, sql, args...)
	if err != nil {
		return fmt.Errorf("delete query: %w", err)
	}
	if result.RowsAffected() == 0 {
		return fmt.Errorf("record not found")
	}
	return nil
}

// LoadRelation loads related records using IN-clause batching.
func (b *Builder) LoadRelation(ctx context.Context, rel schema.Relation, ids []string) (map[string][]map[string]interface{}, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	// Build $1,$2,... placeholders
	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = id
	}

	fk := rel.ForeignKey
	if fk == "" {
		fk = "id"
	}

	table := rel.Entity // Will be resolved to table name by caller
	sql := fmt.Sprintf(
		"SELECT * FROM %s WHERE %s IN (%s)",
		pgQuoteIdent(table),
		pgQuoteIdent(fk),
		strings.Join(placeholders, ", "),
	)

	rows, err := b.pool.Query(ctx, sql, args...)
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

// selectColumns returns a comma-separated list of quoted column names for SELECT.
func (b *Builder) selectColumns() string {
	cols := make([]string, 0, len(b.entity.Fields))
	for _, f := range b.entity.Fields {
		cols = append(cols, pgQuoteIdent(f.ColumnName))
	}
	return strings.Join(cols, ", ")
}

// isAllowedColumn checks whether a column name corresponds to a field in the entity.
func (b *Builder) isAllowedColumn(col string) bool {
	for _, f := range b.entity.Fields {
		if f.ColumnName == col || f.Name == col {
			return true
		}
	}
	return false
}

// buildOrderClause builds a validated ORDER BY clause from a "col:dir" string.
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
	return pgQuoteIdent(col) + " " + dir, nil
}

// scanRows converts pgx rows to a slice of string-keyed maps.
func scanRows(rows pgx.Rows) ([]map[string]interface{}, error) {
	var results []map[string]interface{}
	fields := rows.FieldDescriptions()
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			return nil, fmt.Errorf("scanning row: %w", err)
		}
		row := make(map[string]interface{}, len(fields))
		for i, fd := range fields {
			row[string(fd.Name)] = vals[i]
		}
		results = append(results, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating rows: %w", err)
	}
	return results, nil
}

// pgQuoteIdent safely double-quotes a PostgreSQL identifier.
func pgQuoteIdent(ident string) string {
	return `"` + strings.ReplaceAll(ident, `"`, `""`) + `"`
}
