package migration

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/csullivan/yaypi/internal/dialect"
	"github.com/csullivan/yaypi/internal/schema"
)

// DBColumn represents a column as reported by information_schema.
type DBColumn struct {
	TableName  string
	ColumnName string
	DataType   string
	IsNullable string
	Default    string
}

// DBIndex represents an index as reported by the catalog.
type DBIndex struct {
	TableName string
	IndexName string
}

// DDLStatement holds a single DDL statement with metadata.
type DDLStatement struct {
	SQL         string
	Description string
	Concurrent  bool // true for CREATE INDEX CONCURRENTLY (Postgres only)
}

// Engine computes schema diffs between entity definitions and the live database.
type Engine struct {
	db       *sql.DB
	dialect  dialect.Dialect
	registry *schema.Registry
}

// NewEngine creates a migration Engine.
func NewEngine(db *sql.DB, d dialect.Dialect, registry *schema.Registry) *Engine {
	return &Engine{db: db, dialect: d, registry: registry}
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

	for _, entity := range topoSortEntities(e.registry.Entities()) {
		table := entity.Table

		if !existingTables[table] {
			stmts = append(stmts, DDLStatement{
				SQL:         e.createTableSQL(entity),
				Description: fmt.Sprintf("create table %s", table),
			})
		} else {
			for _, field := range entity.Fields {
				key := table + "." + field.ColumnName
				if _, exists := existingCols[key]; !exists {
					stmts = append(stmts, DDLStatement{
						SQL: fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s;",
							e.dialect.QuoteIdent(table), e.columnDef(field)),
						Description: fmt.Sprintf("add column %s.%s", table, field.ColumnName),
					})
				}
			}
		}

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
					cols[i] = e.dialect.QuoteIdent(c)
				}

				if e.dialect.SupportsConcurrentIndex() {
					stmts = append(stmts, DDLStatement{
						SQL: fmt.Sprintf(
							"CREATE %sINDEX CONCURRENTLY IF NOT EXISTS %s ON %s USING %s (%s);",
							unique, e.dialect.QuoteIdent(idx.Name),
							e.dialect.QuoteIdent(table), idxType, strings.Join(cols, ", "),
						),
						Description: fmt.Sprintf("create index %s", idx.Name),
						Concurrent:  true,
					})
				} else {
					stmts = append(stmts, DDLStatement{
						SQL: fmt.Sprintf(
							"CREATE %sINDEX IF NOT EXISTS %s ON %s (%s);",
							unique, e.dialect.QuoteIdent(idx.Name),
							e.dialect.QuoteIdent(table), strings.Join(cols, ", "),
						),
						Description: fmt.Sprintf("create index %s", idx.Name),
					})
				}
			}
		}
	}

	return stmts, nil
}

func (e *Engine) fetchTables(ctx context.Context) (map[string]bool, error) {
	rows, err := e.db.QueryContext(ctx, e.dialect.ListTablesQuery(""))
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

func (e *Engine) fetchColumns(ctx context.Context) (map[string]DBColumn, error) {
	// SQLite requires per-table PRAGMA — handled specially
	if e.dialect.Name() == "sqlite" {
		return e.fetchColumnsSQLite(ctx)
	}

	rows, err := e.db.QueryContext(ctx, e.dialect.ListColumnsQuery(""))
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

func (e *Engine) fetchColumnsSQLite(ctx context.Context) (map[string]DBColumn, error) {
	tables, err := e.fetchTables(ctx)
	if err != nil {
		return nil, err
	}
	result := make(map[string]DBColumn)
	for table := range tables {
		rows, err := e.db.QueryContext(ctx,
			fmt.Sprintf("PRAGMA table_info(%s)", e.dialect.QuoteIdent(table)))
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var cid int
			var name, colType string
			var notNull int
			var dfltValue *string
			var pk int
			if err := rows.Scan(&cid, &name, &colType, &notNull, &dfltValue, &pk); err != nil {
				rows.Close()
				return nil, err
			}
			col := DBColumn{TableName: table, ColumnName: name, DataType: colType}
			if notNull == 0 {
				col.IsNullable = "YES"
			} else {
				col.IsNullable = "NO"
			}
			if dfltValue != nil {
				col.Default = *dfltValue
			}
			result[table+"."+name] = col
		}
		rows.Close()
	}
	return result, nil
}

func (e *Engine) fetchIndexes(ctx context.Context) (map[string]bool, error) {
	rows, err := e.db.QueryContext(ctx, e.dialect.ListIndexesQuery(""))
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
		cols = append(cols, "  "+e.columnDef(f))

		if f.Reference != nil {
			refTable := f.Reference.Entity
			if refEntity, ok := e.registry.GetEntity(f.Reference.Entity); ok {
				refTable = refEntity.Table
			}
			fk := fmt.Sprintf(
				"  CONSTRAINT %s FOREIGN KEY (%s) REFERENCES %s(%s) ON DELETE %s ON UPDATE %s",
				e.dialect.QuoteIdent(fmt.Sprintf("fk_%s_%s", entity.Table, f.ColumnName)),
				e.dialect.QuoteIdent(f.ColumnName),
				e.dialect.QuoteIdent(refTable),
				e.dialect.QuoteIdent(f.Reference.Field),
				string(f.Reference.OnDelete),
				string(f.Reference.OnUpdate),
			)
			fkConstraints = append(fkConstraints, fk)
		}
	}

	for _, c := range entity.Constraints {
		quotedCols := make([]string, len(c.Columns))
		for i, col := range c.Columns {
			quotedCols[i] = e.dialect.QuoteIdent(col)
		}
		switch c.Type {
		case "check":
			cols = append(cols, fmt.Sprintf("  CONSTRAINT %s CHECK (%s)",
				e.dialect.QuoteIdent(c.Name), c.Check))
		case "unique":
			cols = append(cols, fmt.Sprintf("  CONSTRAINT %s UNIQUE (%s)",
				e.dialect.QuoteIdent(c.Name), strings.Join(quotedCols, ", ")))
		case "primary_key":
			cols = append(cols, fmt.Sprintf("  CONSTRAINT %s PRIMARY KEY (%s)",
				e.dialect.QuoteIdent(c.Name), strings.Join(quotedCols, ", ")))
		}
	}

	cols = append(cols, fkConstraints...)

	return fmt.Sprintf(
		"CREATE TABLE IF NOT EXISTS %s (\n%s\n);",
		e.dialect.QuoteIdent(entity.Table),
		strings.Join(cols, ",\n"),
	)
}

// columnDef generates a SQL column definition for a field.
func (e *Engine) columnDef(f schema.Field) string {
	def := e.dialect.QuoteIdent(f.ColumnName) + " " + e.dialect.FieldTypeToSQL(f)

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

// topoSortEntities returns entities ordered so referenced tables come first.
func topoSortEntities(entities []*schema.Entity) []*schema.Entity {
	byName := make(map[string]*schema.Entity, len(entities))
	for _, e := range entities {
		byName[e.Name] = e
	}

	inDegree := make(map[string]int, len(entities))
	deps := make(map[string][]string, len(entities))

	for _, e := range entities {
		if _, ok := inDegree[e.Name]; !ok {
			inDegree[e.Name] = 0
		}
		for _, f := range e.Fields {
			if f.Reference == nil || f.Reference.Entity == e.Name {
				continue
			}
			ref := f.Reference.Entity
			if _, ok := byName[ref]; !ok {
				continue
			}
			deps[e.Name] = append(deps[e.Name], ref)
			inDegree[ref] = inDegree[ref]
			inDegree[e.Name]++
		}
	}

	var queue []string
	for _, e := range entities {
		if inDegree[e.Name] == 0 {
			queue = append(queue, e.Name)
		}
	}

	var sorted []*schema.Entity
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		sorted = append(sorted, byName[name])
		for _, e := range entities {
			for _, dep := range deps[e.Name] {
				if dep == name {
					inDegree[e.Name]--
					if inDegree[e.Name] == 0 {
						queue = append(queue, e.Name)
					}
				}
			}
		}
	}

	seen := make(map[string]bool, len(sorted))
	for _, e := range sorted {
		seen[e.Name] = true
	}
	for _, e := range entities {
		if !seen[e.Name] {
			sorted = append(sorted, e)
		}
	}

	return sorted
}
