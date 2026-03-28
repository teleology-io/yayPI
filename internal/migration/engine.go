package migration

import (
	"context"
	"fmt"
	"strings"

	"github.com/csullivan/yaypi/internal/schema"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DBColumn represents a column as reported by information_schema.
type DBColumn struct {
	TableName  string
	ColumnName string
	DataType   string
	IsNullable string
	Default    string
}

// DBIndex represents an index as reported by pg_indexes.
type DBIndex struct {
	TableName  string
	IndexName  string
	IndexDef   string
	IsUnique   bool
}

// DDLStatement holds a single DDL statement with metadata.
type DDLStatement struct {
	SQL         string
	Description string
	Concurrent  bool // true for CREATE INDEX CONCURRENTLY
}

// Engine computes schema diffs between entity definitions and the live database.
type Engine struct {
	pool     *pgxpool.Pool
	registry *schema.Registry
}

// NewEngine creates a migration Engine.
func NewEngine(pool *pgxpool.Pool, registry *schema.Registry) *Engine {
	return &Engine{pool: pool, registry: registry}
}

// Diff computes DDL statements needed to bring the database schema up to date.
func (e *Engine) Diff(ctx context.Context) ([]DDLStatement, error) {
	existingTables, err := e.fetchTables(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetching tables: %w", err)
	}

	existingCols, err := e.fetchColumns(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetching columns: %w", err)
	}

	existingIndexes, err := e.fetchIndexes(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetching indexes: %w", err)
	}

	var stmts []DDLStatement

	for _, entity := range e.registry.Entities() {
		table := entity.Table

		if !existingTables[table] {
			// CREATE TABLE
			stmts = append(stmts, DDLStatement{
				SQL:         e.createTableSQL(entity),
				Description: fmt.Sprintf("create table %s", table),
			})
		} else {
			// ADD missing columns
			for _, field := range entity.Fields {
				key := table + "." + field.ColumnName
				if _, exists := existingCols[key]; !exists {
					stmts = append(stmts, DDLStatement{
						SQL:         fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s;", quoteIdent(table), columnDef(field)),
						Description: fmt.Sprintf("add column %s.%s", table, field.ColumnName),
					})
				}
			}
		}

		// CREATE missing indexes
		for _, idx := range entity.Indexes {
			idxKey := table + "." + idx.Name
			if !existingIndexes[idxKey] {
				unique := ""
				if idx.Unique {
					unique = "UNIQUE "
				}
				idxType := idx.Type
				if idxType == "" {
					idxType = "btree"
				}
				cols := make([]string, len(idx.Columns))
				for i, c := range idx.Columns {
					cols[i] = quoteIdent(c)
				}
				sql := fmt.Sprintf(
					"CREATE %sINDEX CONCURRENTLY IF NOT EXISTS %s ON %s USING %s (%s);",
					unique, quoteIdent(idx.Name), quoteIdent(table), idxType, strings.Join(cols, ", "),
				)
				stmts = append(stmts, DDLStatement{
					SQL:         sql,
					Description: fmt.Sprintf("create index %s", idx.Name),
					Concurrent:  true,
				})
			}
		}
	}

	return stmts, nil
}

// fetchTables returns a set of existing table names.
func (e *Engine) fetchTables(ctx context.Context) (map[string]bool, error) {
	rows, err := e.pool.Query(ctx, `
		SELECT table_name
		FROM information_schema.tables
		WHERE table_schema = 'public' AND table_type = 'BASE TABLE'
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		result[name] = true
	}
	return result, rows.Err()
}

// fetchColumns returns a map of "table.column" → DBColumn.
func (e *Engine) fetchColumns(ctx context.Context) (map[string]DBColumn, error) {
	rows, err := e.pool.Query(ctx, `
		SELECT table_name, column_name, data_type, is_nullable, column_default
		FROM information_schema.columns
		WHERE table_schema = 'public'
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]DBColumn)
	for rows.Next() {
		var col DBColumn
		var def *string
		if err := rows.Scan(&col.TableName, &col.ColumnName, &col.DataType, &col.IsNullable, &def); err != nil {
			return nil, err
		}
		if def != nil {
			col.Default = *def
		}
		result[col.TableName+"."+col.ColumnName] = col
	}
	return result, rows.Err()
}

// fetchIndexes returns a set of "table.indexname" keys.
func (e *Engine) fetchIndexes(ctx context.Context) (map[string]bool, error) {
	rows, err := e.pool.Query(ctx, `
		SELECT tablename, indexname FROM pg_indexes WHERE schemaname = 'public'
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]bool)
	for rows.Next() {
		var table, idx string
		if err := rows.Scan(&table, &idx); err != nil {
			return nil, err
		}
		result[table+"."+idx] = true
	}
	return result, rows.Err()
}

// createTableSQL generates a CREATE TABLE statement for an entity.
func (e *Engine) createTableSQL(entity *schema.Entity) string {
	var cols []string
	var fkConstraints []string

	for _, f := range entity.Fields {
		cols = append(cols, "  "+columnDef(f))

		if f.Reference != nil {
			fk := fmt.Sprintf(
				"  CONSTRAINT %s FOREIGN KEY (%s) REFERENCES %s(%s) ON DELETE %s ON UPDATE %s",
				quoteIdent(fmt.Sprintf("fk_%s_%s", entity.Table, f.ColumnName)),
				quoteIdent(f.ColumnName),
				quoteIdent(f.Reference.Entity),
				quoteIdent(f.Reference.Field),
				string(f.Reference.OnDelete),
				string(f.Reference.OnUpdate),
			)
			fkConstraints = append(fkConstraints, fk)
		}
	}

	for _, c := range entity.Constraints {
		switch c.Type {
		case "check":
			cols = append(cols, fmt.Sprintf("  CONSTRAINT %s CHECK (%s)", quoteIdent(c.Name), c.Check))
		case "unique":
			quotedCols := make([]string, len(c.Columns))
			for i, col := range c.Columns {
				quotedCols[i] = quoteIdent(col)
			}
			cols = append(cols, fmt.Sprintf("  CONSTRAINT %s UNIQUE (%s)", quoteIdent(c.Name), strings.Join(quotedCols, ", ")))
		}
	}

	cols = append(cols, fkConstraints...)

	return fmt.Sprintf(
		"CREATE TABLE IF NOT EXISTS %s (\n%s\n);",
		quoteIdent(entity.Table),
		strings.Join(cols, ",\n"),
	)
}

// columnDef generates a SQL column definition for a field.
func columnDef(f schema.Field) string {
	def := quoteIdent(f.ColumnName) + " " + fieldTypeToSQL(f)

	if f.PrimaryKey {
		def += " PRIMARY KEY"
	} else {
		if !f.Nullable {
			def += " NOT NULL"
		}
		if f.Unique {
			def += " UNIQUE"
		}
	}

	if f.Default != "" {
		def += " DEFAULT " + f.Default
	}

	return def
}

// fieldTypeToSQL converts a schema.Field type to a PostgreSQL type string.
func fieldTypeToSQL(f schema.Field) string {
	switch f.Type {
	case "uuid":
		return "uuid"
	case "string":
		if f.Length > 0 {
			return fmt.Sprintf("varchar(%d)", f.Length)
		}
		return "varchar(255)"
	case "text":
		return "text"
	case "integer":
		return "integer"
	case "bigint":
		return "bigint"
	case "float":
		return "double precision"
	case "decimal":
		if f.Precision > 0 {
			return fmt.Sprintf("numeric(%d,%d)", f.Precision, f.Scale)
		}
		return "numeric"
	case "boolean":
		return "boolean"
	case "timestamptz":
		return "timestamptz"
	case "date":
		return "date"
	case "jsonb":
		return "jsonb"
	case "enum":
		return "text" // enums use text with CHECK constraint in this implementation
	case "array":
		return "text[]"
	case "bytea":
		return "bytea"
	default:
		return "text"
	}
}

// quoteIdent safely double-quotes a PostgreSQL identifier.
func quoteIdent(ident string) string {
	return `"` + strings.ReplaceAll(ident, `"`, `""`) + `"`
}
