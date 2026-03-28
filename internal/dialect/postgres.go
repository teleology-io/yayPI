package dialect

import (
	"fmt"
	"strings"

	"github.com/csullivan/yaypi/internal/schema"
)

// Postgres implements Dialect for PostgreSQL (via pgx/v5 stdlib or pgx directly).
type Postgres struct{}

func (Postgres) Name() string { return "postgres" }

func (Postgres) QuoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func (Postgres) Rebind(query string) string { return query }

func (Postgres) SupportsReturning() bool { return true }

func (Postgres) SupportsConcurrentIndex() bool { return true }

func (Postgres) UpsertIgnore(table string, cols []string, placeholders []string) string {
	return fmt.Sprintf(
		`INSERT INTO %s (%s) VALUES (%s) ON CONFLICT DO NOTHING RETURNING *`,
		table, strings.Join(cols, ", "), strings.Join(placeholders, ", "),
	)
}

func (Postgres) IsUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "23505")
}

func (Postgres) FieldTypeToSQL(f schema.Field) string {
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
		return "text"
	case "array":
		return "text[]"
	case "bytea":
		return "bytea"
	default:
		return "text"
	}
}

func (Postgres) ListTablesQuery(schemaName string) string {
	if schemaName == "" {
		schemaName = "public"
	}
	return fmt.Sprintf(`
		SELECT table_name FROM information_schema.tables
		WHERE table_schema = '%s' AND table_type = 'BASE TABLE'`, schemaName)
}

func (Postgres) ListColumnsQuery(schemaName string) string {
	if schemaName == "" {
		schemaName = "public"
	}
	return fmt.Sprintf(`
		SELECT table_name, column_name, data_type, is_nullable, column_default
		FROM information_schema.columns WHERE table_schema = '%s'`, schemaName)
}

func (Postgres) ListIndexesQuery(schemaName string) string {
	if schemaName == "" {
		schemaName = "public"
	}
	return fmt.Sprintf(
		`SELECT tablename, indexname FROM pg_indexes WHERE schemaname = '%s'`, schemaName)
}

func (d Postgres) MigrationsTableDDL(tableName string) string {
	return fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id SERIAL PRIMARY KEY,
			name TEXT NOT NULL UNIQUE,
			checksum TEXT NOT NULL,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`, d.QuoteIdent(tableName))
}
