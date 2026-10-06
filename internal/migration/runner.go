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
		var appliedAt any // time.Time on Postgres/MySQL, text on SQLite
		if err := rows.Scan(&s.Name, &s.Checksum, &appliedAt); err != nil {
			return nil, err
		}
		switch v := appliedAt.(type) {
		case time.Time:
			s.AppliedAt = &v
		case string:
			if t, err := time.Parse("2006-01-02 15:04:05", v); err == nil {
				s.AppliedAt = &t
			}
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

// Up applies pending migrations, up to steps (0 = all). It holds a cross-process lock
// for the duration, and refuses to run if an already-applied migration file was edited
// (checksum drift) unless allowDrift is set.
func (r *Runner) Up(ctx context.Context, steps int, allowDrift ...bool) error {
	unlock, err := AcquireLock(ctx, r.db, r.dialect)
	if err != nil {
		return err
	}
	defer unlock()

	if err := r.ensureTable(ctx); err != nil {
		return fmt.Errorf("ensuring migrations table: %w", err)
	}
	if len(allowDrift) == 0 || !allowDrift[0] {
		if err := r.Verify(ctx); err != nil {
			return fmt.Errorf("refusing to migrate: %w (fix the files or pass --allow-drift)", err)
		}
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
	unlock, err := AcquireLock(ctx, r.db, r.dialect)
	if err != nil {
		return err
	}
	defer unlock()

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

// applyMigration executes a migration file. The transactional part and the bookkeeping
// row commit together, so a failure leaves neither behind (on Postgres and SQLite; MySQL
// commits DDL implicitly, so a failed MySQL migration may be partially applied).
// Statements after "-- Run outside transaction:" (Postgres CREATE INDEX CONCURRENTLY)
// run afterwards; they are IF NOT EXISTS, so re-running them by hand is safe.
func (r *Runner) applyMigration(ctx context.Context, path, name string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading migration file: %w", err)
	}
	checksum := fmt.Sprintf("%x", sha256.Sum256(data))
	txSQL, concurrentSQL := splitConcurrent(string(data))

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	for _, stmt := range SplitStatements(txSQL) {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("executing %q: %w", firstLine(stmt), err)
		}
	}
	insert := r.dialect.Rebind(fmt.Sprintf(
		"INSERT INTO %s (name, checksum) VALUES ($1, $2)",
		r.dialect.QuoteIdent(migrationsTable),
	))
	if _, err := tx.ExecContext(ctx, insert, name, checksum); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	for _, stmt := range concurrentSQL {
		if _, err := r.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migration recorded, but a statement outside the transaction failed (re-run it manually): %q: %w", stmt, err)
		}
	}
	return nil
}

// rollbackMigration executes a down migration file and removes the bookkeeping row,
// in one transaction.
func (r *Runner) rollbackMigration(ctx context.Context, path, upName string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading down migration file: %w", err)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	for _, stmt := range SplitStatements(string(data)) {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("executing %q: %w", firstLine(stmt), err)
		}
	}
	del := r.dialect.Rebind(fmt.Sprintf(
		"DELETE FROM %s WHERE name = $1",
		r.dialect.QuoteIdent(migrationsTable),
	))
	if _, err := tx.ExecContext(ctx, del, upName); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i] + " …"
	}
	return s
}

// ApplyStatements runs DDL produced by Engine.Diff directly (auto_migrate), under the
// migration lock, transactional statements in one transaction and CONCURRENTLY ones
// after it. Nothing is written to disk or recorded in the migrations table.
func ApplyStatements(ctx context.Context, db *sql.DB, d dialect.Dialect, stmts []DDLStatement) error {
	unlock, err := AcquireLock(ctx, db, d)
	if err != nil {
		return err
	}
	defer unlock()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	var concurrent []DDLStatement
	for _, s := range stmts {
		if s.Concurrent {
			concurrent = append(concurrent, s)
			continue
		}
		if _, err := tx.ExecContext(ctx, s.SQL); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("%s: %w", s.Description, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	for _, s := range concurrent {
		if _, err := db.ExecContext(ctx, s.SQL); err != nil {
			return fmt.Errorf("%s: %w", s.Description, err)
		}
	}
	return nil
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
