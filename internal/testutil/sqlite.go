// Package testutil holds helpers shared by package tests: a migrated on-disk SQLite
// database built from a schema registry.
package testutil

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/teleology-io/yayPI/internal/config"
	"github.com/teleology-io/yayPI/internal/db"
	"github.com/teleology-io/yayPI/internal/migration"
	"github.com/teleology-io/yayPI/internal/schema"
)

// SQLiteDB opens a fresh SQLite database in t.TempDir(), applies the schema diff for reg
// (exercising the real migration engine), and closes it when the test ends.
func SQLiteDB(t *testing.T, reg *schema.Registry) *db.Manager {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	m, err := db.NewManager([]config.DBConfig{{
		Name: "primary", Driver: "sqlite", DSN: "file:" + path + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)", Default: true,
	}})
	if err != nil {
		t.Fatalf("opening sqlite: %v", err)
	}
	t.Cleanup(m.Close)

	ctx := context.Background()
	d := m.Default()
	stmts, err := migration.NewEngine(d.SQL, d.Dialect, reg).Diff(ctx)
	if err != nil {
		t.Fatalf("schema diff: %v", err)
	}
	for _, s := range stmts {
		if _, err := d.SQL.ExecContext(ctx, s.SQL); err != nil {
			t.Fatalf("applying %q: %v\n%s", s.Description, err, s.SQL)
		}
	}
	return m
}
