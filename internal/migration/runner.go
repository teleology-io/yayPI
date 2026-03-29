package migration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/teleology-io/yayPI/internal/dialect"
)

const migrationsTable = "yaypi_migrations"

// MigrationStatus represents the state of a single migration file.
type MigrationStatus struct {
	Name      string
	AppliedAt *time.Time
	Checksum  string
	Pending   bool
}

// Runner applies and rolls back migrations.
type Runner struct {
	db            *sql.DB
	dialect       dialect.Dialect
	migrationsDir string
}

// NewRunner creates a Runner.
func NewRunner(db *sql.DB, d dialect.Dialect, migrationsDir string) *Runner {
	return &Runner{db: db, dialect: d, migrationsDir: migrationsDir}
}

// ensureTable creates the migrations tracking table if it does not exist.
func (r *Runner) ensureTable(ctx context.Context) error {
	_, err := r.db.ExecContext(ctx, r.dialect.MigrationsTableDDL(migrationsTable))
	return err
}

// Status returns the status of all known migration files.
func (r *Runner) Status(ctx context.Context) ([]MigrationStatus, error) {
	if err := r.ensureTable(ctx); err != nil {
		return nil, fmt.Errorf("ensuring migrations table: %w", err)
	}

	q := r.dialect.Rebind(fmt.Sprintf(
		"SELECT name, checksum, applied_at FROM %s ORDER BY name",
		r.dialect.QuoteIdent(migrationsTable),
	))
	rows, err := r.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("querying migrations table: %w", err)
	}
	defer rows.Close()

	applied := make(map[string]MigrationStatus)
	for rows.Next() {
		var s MigrationStatus
		if err := rows.Scan(&s.Name, &s.Checksum, &s.AppliedAt); err != nil {
			return nil, err
		}
		s.Pending = false
		applied[s.Name] = s
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	files, err := filepath.Glob(filepath.Join(r.migrationsDir, "*.up.sql"))
	if err != nil {
		return nil, fmt.Errorf("globbing migrations: %w", err)
	}
	sort.Strings(files)

	var statuses []MigrationStatus
	for _, f := range files {
		name := filepath.Base(f)
		if s, ok := applied[name]; ok {
			statuses = append(statuses, s)
		} else {
			statuses = append(statuses, MigrationStatus{Name: name, Pending: true})
		}
	}

	return statuses, nil
}

// Up applies pending migrations, up to steps (0 = all).
func (r *Runner) Up(ctx context.Context, steps int) error {
	if err := r.ensureTable(ctx); err != nil {
		return fmt.Errorf("ensuring migrations table: %w", err)
	}

	statuses, err := r.Status(ctx)
	if err != nil {
		return err
	}

	applied := 0
	for _, s := range statuses {
		if !s.Pending {
			continue
		}
		if steps > 0 && applied >= steps {
			break
		}
		upFile := filepath.Join(r.migrationsDir, s.Name)
		if err := r.applyMigration(ctx, upFile, s.Name); err != nil {
			return fmt.Errorf("applying migration %s: %w", s.Name, err)
		}
		applied++
	}

	return nil
}

// Down rolls back applied migrations, exactly steps migrations.
func (r *Runner) Down(ctx context.Context, steps int) error {
	if err := r.ensureTable(ctx); err != nil {
		return fmt.Errorf("ensuring migrations table: %w", err)
	}

	if steps <= 0 {
		return fmt.Errorf("steps must be > 0 for rollback")
	}

	q := r.dialect.Rebind(fmt.Sprintf(
		"SELECT name FROM %s ORDER BY name DESC LIMIT $1",
		r.dialect.QuoteIdent(migrationsTable),
	))
	rows, err := r.db.QueryContext(ctx, q, steps)
	if err != nil {
		return err
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, name := range names {
		downName := strings.Replace(name, ".up.sql", ".down.sql", 1)
		downFile := filepath.Join(r.migrationsDir, downName)
		if err := r.rollbackMigration(ctx, downFile, name); err != nil {
			return fmt.Errorf("rolling back migration %s: %w", name, err)
		}
	}

	return nil
}

// Verify re-checks checksums of applied migrations against file contents.
func (r *Runner) Verify(ctx context.Context) error {
	if err := r.ensureTable(ctx); err != nil {
		return fmt.Errorf("ensuring migrations table: %w", err)
	}

	q := fmt.Sprintf("SELECT name, checksum FROM %s", r.dialect.QuoteIdent(migrationsTable))
	rows, err := r.db.QueryContext(ctx, q)
	if err != nil {
		return err
	}
	defer rows.Close()

	var errs []string
	for rows.Next() {
		var name, checksum string
		if err := rows.Scan(&name, &checksum); err != nil {
			return err
		}
		path := filepath.Join(r.migrationsDir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: file not found", name))
			continue
		}
		computed := fmt.Sprintf("%x", sha256.Sum256(data))
		if computed != checksum {
			errs = append(errs, fmt.Sprintf("%s: checksum mismatch (expected %s, got %s)", name, checksum, computed))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("verification failures:\n%s", strings.Join(errs, "\n"))
	}
	return nil
}

// applyMigration executes a migration file against the database.
func (r *Runner) applyMigration(ctx context.Context, path, name string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading migration file: %w", err)
	}

	checksum := fmt.Sprintf("%x", sha256.Sum256(data))
	sqlContent := string(data)

	txSQL, concurrentSQL := splitConcurrent(sqlContent)

	if strings.TrimSpace(txSQL) != "" {
		if _, err := r.db.ExecContext(ctx, txSQL); err != nil {
			return fmt.Errorf("executing migration SQL: %w", err)
		}
	}

	// Concurrent statements (Postgres CONCURRENTLY indexes) run outside a transaction
	for _, stmt := range concurrentSQL {
		if _, err := r.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("executing concurrent statement: %w", err)
		}
	}

	insert := r.dialect.Rebind(fmt.Sprintf(
		"INSERT INTO %s (name, checksum) VALUES ($1, $2)",
		r.dialect.QuoteIdent(migrationsTable),
	))
	_, err = r.db.ExecContext(ctx, insert, name, checksum)
	return err
}

// rollbackMigration executes a down migration file.
func (r *Runner) rollbackMigration(ctx context.Context, path, upName string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading down migration file: %w", err)
	}

	if _, err := r.db.ExecContext(ctx, string(data)); err != nil {
		return fmt.Errorf("executing down migration SQL: %w", err)
	}

	del := r.dialect.Rebind(fmt.Sprintf(
		"DELETE FROM %s WHERE name = $1",
		r.dialect.QuoteIdent(migrationsTable),
	))
	_, err = r.db.ExecContext(ctx, del, upName)
	return err
}

// splitConcurrent separates regular SQL from lines after "-- Run outside transaction:".
func splitConcurrent(sqlContent string) (string, []string) {
	lines := strings.Split(sqlContent, "\n")
	var txLines []string
	var concurrentStmts []string
	inConcurrent := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "-- Run outside transaction:" {
			inConcurrent = true
			continue
		}
		if inConcurrent {
			if trimmed != "" && !strings.HasPrefix(trimmed, "--") {
				concurrentStmts = append(concurrentStmts, trimmed)
			}
		} else {
			txLines = append(txLines, line)
		}
	}

	return strings.Join(txLines, "\n"), concurrentStmts
}
