package dialect

import (
	"fmt"
	"strings"

	"github.com/teleology-io/yayPI/internal/schema"
)

// SQLite implements Dialect for SQLite (via modernc.org/sqlite — pure Go, no CGO).
// SQLite 3.35+ supports RETURNING; modernc.org/sqlite bundles a recent version.
type SQLite struct{}

func (SQLite) Name() string { return "sqlite" }

func (SQLite) QuoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func (SQLite) Rebind(query string) string { return RebindQuestion(query) }

func (SQLite) SupportsReturning() bool { return true }

func (SQLite) SupportsConcurrentIndex() bool { return false }

func (SQLite) UpsertIgnore(table string, cols []string, placeholders []string) string {
	return fmt.Sprintf(
		`INSERT OR IGNORE INTO %s (%s) VALUES (%s) RETURNING *`,
		table, strings.Join(cols, ", "), strings.Join(placeholders, ", "),
	)
}

func (SQLite) IsUniqueViolation(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique constraint failed")
}

func (SQLite) FieldTypeToSQL(f schema.Field) string {
	switch f.Type {
	case "uuid":
		return "TEXT"
	case "string":
		return "TEXT"
	case "text":
		return "TEXT"
	case "integer":
		return "INTEGER"
	case "bigint":
		return "INTEGER"
	case "float":
		return "REAL"
	case "decimal":
		return "REAL"
	case "boolean":
		return "INTEGER" // 0/1
	case "timestamptz":
		return "TEXT" // ISO-8601 stored as text
	case "date":
		return "TEXT"
	case "jsonb":
		return "TEXT"
	case "enum":
		return "TEXT"
	case "array":
		return "TEXT"
	case "bytea":
		return "BLOB"
	default:
		return "TEXT"
	}
}

func (SQLite) ListTablesQuery(_ string) string {
	return `SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`
}

func (SQLite) ListColumnsQuery(_ string) string {
	// SQLite doesn't have information_schema; we handle per-table via PRAGMA.
	// Return a sentinel — the migration engine must special-case SQLite column
	// discovery using PRAGMA table_info().
	return "__sqlite_pragma__"
}

func (SQLite) ListIndexesQuery(_ string) string {
	return `SELECT tbl_name, name FROM sqlite_master WHERE type='index' AND name NOT LIKE 'sqlite_%'`
}

func (d SQLite) MigrationsTableDDL(tableName string) string {
	return fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL UNIQUE,
			checksum TEXT NOT NULL,
			applied_at TEXT NOT NULL DEFAULT (datetime('now'))
		)`, d.QuoteIdent(tableName))
}
