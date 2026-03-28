// Package dialect abstracts SQL syntax differences between database engines.
package dialect

import "github.com/csullivan/yaypi/internal/schema"

// Dialect encapsulates all database-engine-specific SQL behaviour.
type Dialect interface {
	// Name returns a short identifier, e.g. "postgres", "mysql", "sqlite".
	Name() string

	// QuoteIdent wraps an identifier in the engine's quoting characters.
	// Postgres: "name", MySQL: `name`, SQLite: "name"
	QuoteIdent(name string) string

	// Rebind rewrites a query that uses Postgres-style $1,$2,… positional
	// placeholders into the format expected by this driver (? for MySQL/SQLite).
	Rebind(query string) string

	// SupportsReturning reports whether INSERT/UPDATE … RETURNING is supported.
	SupportsReturning() bool

	// SupportsConcurrentIndex reports whether CREATE INDEX CONCURRENTLY is
	// supported (Postgres only).
	SupportsConcurrentIndex() bool

	// UpsertIgnore returns a dialect-appropriate INSERT that silently ignores
	// duplicate-key conflicts. cols and placeholders are already quoted/formatted.
	UpsertIgnore(table string, cols []string, placeholders []string) string

	// IsUniqueViolation returns true when err represents a unique-constraint
	// violation from the driver.
	IsUniqueViolation(err error) bool

	// FieldTypeToSQL maps a yayPi schema field to a SQL column type string.
	FieldTypeToSQL(f schema.Field) string

	// ListTablesQuery returns a SQL query that yields a single column of table
	// names visible to the current connection in the given schema (pass ""
	// for the default/only schema on drivers that don't use schemas).
	ListTablesQuery(schemaName string) string

	// ListColumnsQuery returns a SQL query that yields (table_name, column_name,
	// data_type, is_nullable, column_default) for all columns in schemaName.
	ListColumnsQuery(schemaName string) string

	// ListIndexesQuery returns a SQL query that yields (table_name, index_name)
	// for all indexes in schemaName.
	ListIndexesQuery(schemaName string) string

	// MigrationsTableDDL returns the CREATE TABLE … statement for the
	// yaypi_migrations tracking table, using the dialect's own types.
	MigrationsTableDDL(tableName string) string
}
