package migration

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// GeneratedMigration holds the paths and content of a generated migration pair.
type GeneratedMigration struct {
	UpPath   string
	DownPath string
	UpSQL    string
	DownSQL  string
}

// Generator writes migration files to a directory.
type Generator struct {
	migrationsDir string
}

// NewGenerator creates a Generator that writes to migrationsDir.
func NewGenerator(migrationsDir string) *Generator {
	return &Generator{migrationsDir: migrationsDir}
}

// Generate writes up and down migration files from the given DDL statements.
func (g *Generator) Generate(name string, stmts []DDLStatement) (*GeneratedMigration, error) {
	if len(stmts) == 0 {
		return nil, fmt.Errorf("no schema changes detected")
	}

	if err := os.MkdirAll(g.migrationsDir, 0755); err != nil {
		return nil, fmt.Errorf("creating migrations directory: %w", err)
	}

	ts := time.Now().UTC().Format("20060102150405")
	safeName := strings.ReplaceAll(strings.ToLower(name), " ", "_")

	upFile := filepath.Join(g.migrationsDir, fmt.Sprintf("%s_%s.up.sql", ts, safeName))
	downFile := filepath.Join(g.migrationsDir, fmt.Sprintf("%s_%s.down.sql", ts, safeName))

	upSQL := buildUpSQL(stmts)
	downSQL := buildDownSQL(stmts)

	upChecksum := fmt.Sprintf("%x", sha256.Sum256([]byte(upSQL)))
	downChecksum := fmt.Sprintf("%x", sha256.Sum256([]byte(downSQL)))

	upContent := fmt.Sprintf("-- Migration: %s\n-- Direction: up\n-- Checksum: %s\n\n%s\n", name, upChecksum, upSQL)
	downContent := fmt.Sprintf("-- Migration: %s\n-- Direction: down\n-- Checksum: %s\n\n%s\n", name, downChecksum, downSQL)

	if err := os.WriteFile(upFile, []byte(upContent), 0644); err != nil {
		return nil, fmt.Errorf("writing up migration: %w", err)
	}
	if err := os.WriteFile(downFile, []byte(downContent), 0644); err != nil {
		return nil, fmt.Errorf("writing down migration: %w", err)
	}

	return &GeneratedMigration{
		UpPath:   upFile,
		DownPath: downFile,
		UpSQL:    upSQL,
		DownSQL:  downSQL,
	}, nil
}

// buildUpSQL concatenates DDL statements for the up migration.
// Concurrent statements are separated from transaction-safe ones.
func buildUpSQL(stmts []DDLStatement) string {
	var txStmts []string
	var concurrentStmts []string

	for _, s := range stmts {
		if s.Concurrent {
			concurrentStmts = append(concurrentStmts, s.SQL)
		} else {
			txStmts = append(txStmts, s.SQL)
		}
	}

	var parts []string

	if len(txStmts) > 0 {
		parts = append(parts, "BEGIN;")
		parts = append(parts, txStmts...)
		parts = append(parts, "COMMIT;")
	}

	// Concurrent statements run outside transaction
	if len(concurrentStmts) > 0 {
		parts = append(parts, "-- Run outside transaction:")
		parts = append(parts, concurrentStmts...)
	}

	return strings.Join(parts, "\n")
}

// buildDownSQL generates placeholder down migration comments.
// Actual down migrations require manual review to avoid data loss.
func buildDownSQL(stmts []DDLStatement) string {
	var lines []string
	lines = append(lines, "-- WARNING: Review this down migration carefully before running.")
	lines = append(lines, "-- Dropping columns and tables will result in data loss.")
	lines = append(lines, "")
	lines = append(lines, "BEGIN;")

	for _, s := range stmts {
		lines = append(lines, fmt.Sprintf("-- Reverse of: %s", s.Description))
		lines = append(lines, fmt.Sprintf("-- TODO: %s", reverseStatement(s)))
	}

	lines = append(lines, "COMMIT;")
	return strings.Join(lines, "\n")
}

// reverseStatement generates a best-effort reverse DDL statement.
func reverseStatement(s DDLStatement) string {
	sql := strings.TrimSpace(s.SQL)
	upper := strings.ToUpper(sql)

	switch {
	case strings.HasPrefix(upper, "CREATE TABLE IF NOT EXISTS "):
		table := extractTableName(sql, "CREATE TABLE IF NOT EXISTS ")
		return fmt.Sprintf("DROP TABLE IF EXISTS %s;", table)

	case strings.HasPrefix(upper, "ALTER TABLE ") && strings.Contains(upper, "ADD COLUMN "):
		// Parse: ALTER TABLE "tbl" ADD COLUMN "col" ...
		parts := strings.SplitN(sql, " ADD COLUMN ", 2)
		if len(parts) == 2 {
			tblPart := strings.TrimPrefix(parts[0], "ALTER TABLE ")
			colDef := strings.TrimSpace(parts[1])
			// Extract just the column name
			colName := strings.Fields(colDef)[0]
			return fmt.Sprintf("ALTER TABLE %s DROP COLUMN IF EXISTS %s;", strings.TrimSpace(tblPart), colName)
		}
		return "-- REVERSE: manual review required"

	case strings.HasPrefix(upper, "CREATE") && strings.Contains(upper, "INDEX"):
		// Extract index name
		fields := strings.Fields(sql)
		for i, f := range fields {
			if strings.ToUpper(f) == "INDEX" || strings.ToUpper(f) == "EXISTS" {
				if i+1 < len(fields) {
					return fmt.Sprintf("DROP INDEX CONCURRENTLY IF EXISTS %s;", fields[i+1])
				}
			}
		}
		return "-- REVERSE: manual review required"

	default:
		return "-- manual review required"
	}
}

// extractTableName extracts a table name after a prefix in a SQL string.
func extractTableName(sql, prefix string) string {
	rest := strings.TrimPrefix(sql, prefix)
	fields := strings.Fields(rest)
	if len(fields) > 0 {
		return fields[0]
	}
	return "unknown"
}
