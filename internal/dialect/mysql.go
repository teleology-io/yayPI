package dialect

import (
	"fmt"
	"strings"

	"github.com/csullivan/yaypi/internal/schema"
)

// MySQL implements Dialect for MySQL / MariaDB.
type MySQL struct{}

func (MySQL) Name() string { return "mysql" }

func (MySQL) QuoteIdent(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}

func (MySQL) Rebind(query string) string { return RebindQuestion(query) }

// MySQL 8.0.21+ supports RETURNING on DELETE but not INSERT/UPDATE in standard form.
func (MySQL) SupportsReturning() bool { return false }

func (MySQL) SupportsConcurrentIndex() bool { return false }

func (MySQL) UpsertIgnore(table string, cols []string, placeholders []string) string {
	return fmt.Sprintf(
		`INSERT IGNORE INTO %s (%s) VALUES (%s)`,
		table, strings.Join(cols, ", "), strings.Join(placeholders, ", "),
	)
}

func (MySQL) IsUniqueViolation(err error) bool {
	// MySQL error 1062: Duplicate entry
	return err != nil && strings.Contains(err.Error(), "1062")
}

func (MySQL) FieldTypeToSQL(f schema.Field) string {
	switch f.Type {
	case "uuid":
		return "CHAR(36)"
	case "string":
		if f.Length > 0 {
			return fmt.Sprintf("VARCHAR(%d)", f.Length)
		}
		return "VARCHAR(255)"
	case "text":
		return "TEXT"
	case "integer":
		return "INT"
	case "bigint":
		return "BIGINT"
	case "float":
		return "DOUBLE"
	case "decimal":
		if f.Precision > 0 {
			return fmt.Sprintf("DECIMAL(%d,%d)", f.Precision, f.Scale)
		}
		return "DECIMAL"
	case "boolean":
		return "TINYINT(1)"
	case "timestamptz":
		return "DATETIME"
	case "date":
		return "DATE"
	case "jsonb":
		return "JSON"
	case "enum":
		return "TEXT"
	case "array":
		return "TEXT" // serialized
	case "bytea":
		return "BLOB"
	default:
		return "TEXT"
	}
}

func (MySQL) ListTablesQuery(schemaName string) string {
	if schemaName == "" {
		return `SELECT table_name FROM information_schema.tables
			WHERE table_schema = DATABASE() AND table_type = 'BASE TABLE'`
	}
	return fmt.Sprintf(`SELECT table_name FROM information_schema.tables
		WHERE table_schema = '%s' AND table_type = 'BASE TABLE'`, schemaName)
}

func (MySQL) ListColumnsQuery(schemaName string) string {
	if schemaName == "" {
		return `SELECT table_name, column_name, data_type, is_nullable, column_default
			FROM information_schema.columns WHERE table_schema = DATABASE()`
	}
	return fmt.Sprintf(`SELECT table_name, column_name, data_type, is_nullable, column_default
		FROM information_schema.columns WHERE table_schema = '%s'`, schemaName)
}

func (MySQL) ListIndexesQuery(schemaName string) string {
	if schemaName == "" {
		return `SELECT table_name, index_name FROM information_schema.statistics
			WHERE table_schema = DATABASE() GROUP BY table_name, index_name`
	}
	return fmt.Sprintf(`SELECT table_name, index_name FROM information_schema.statistics
		WHERE table_schema = '%s' GROUP BY table_name, index_name`, schemaName)
}

func (d MySQL) MigrationsTableDDL(tableName string) string {
	return fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id INT AUTO_INCREMENT PRIMARY KEY,
			name TEXT NOT NULL,
			checksum TEXT NOT NULL,
			applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE KEY uq_name (name(255))
		)`, d.QuoteIdent(tableName))
}
