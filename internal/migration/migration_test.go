package migration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/teleology-io/yayPI/internal/config"
	"github.com/teleology-io/yayPI/internal/db"
	"github.com/teleology-io/yayPI/internal/schema"
	"github.com/teleology-io/yayPI/pkg/types"
)

func sqliteDB(t *testing.T) *db.DB {
	t.Helper()
	m, err := db.NewManager([]config.DBConfig{{Name: "p", Driver: "sqlite", DSN: "file:" + filepath.Join(t.TempDir(), "m.db"), Default: true}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	return m.Default()
}

func TestSplitStatements(t *testing.T) {
	script := `BEGIN;
CREATE TABLE a (x text DEFAULT 'semi;colon'); -- trailing; comment
/* block; comment */
CREATE FUNCTION f() RETURNS int AS $$ SELECT 1; $$ LANGUAGE sql;
INSERT INTO a VALUES ('it''s; fine');
COMMIT;`
	got := SplitStatements(script)
	if len(got) != 3 {
		t.Fatalf("got %d statements: %q", len(got), got)
	}
	if !strings.Contains(got[1], "SELECT 1; $$") {
		t.Errorf("dollar-quoted body split: %q", got[1])
	}
}

func TestGenerateUpAndDrift(t *testing.T) {
	d := sqliteDB(t)
	reg := schema.NewRegistry()
	reg.RegisterEntity(&schema.Entity{Name: "Thing", Table: "things", Fields: []schema.Field{
		{Name: "id", ColumnName: "id", Type: types.FieldTypeUUID, PrimaryKey: true, Default: "gen_random_uuid()"},
		{Name: "created_at", ColumnName: "created_at", Type: types.FieldTypeTimestamptz, Default: "now()"},
	}})
	ctx := context.Background()
	stmts, err := NewEngine(d.SQL, d.Dialect, reg).Diff(ctx)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	m, err := NewGenerator(dir).Generate("init", stmts)
	if err != nil {
		t.Fatal(err)
	}
	r := NewRunner(d.SQL, d.Dialect, dir)
	if err := r.Up(ctx, 0); err != nil {
		t.Fatalf("up: %v", err)
	}
	if _, err := d.SQL.Exec(`INSERT INTO things DEFAULT VALUES`); err != nil {
		t.Fatalf("table not usable (defaults not translated?): %v", err)
	}
	st, err := r.Status(ctx)
	if err != nil || len(st) != 1 || st[0].Pending {
		t.Fatalf("status %+v %v", st, err)
	}
	// Second diff is empty: the schema is in sync.
	if again, _ := NewEngine(d.SQL, d.Dialect, reg).Diff(ctx); len(again) != 0 {
		t.Fatalf("diff after up not empty: %+v", again)
	}
	// Edit an applied file → Up refuses.
	if err := os.WriteFile(m.UpPath, []byte("-- edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.Up(ctx, 0); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("expected drift error, got %v", err)
	}
}

func TestFailedMigrationLeavesNothing(t *testing.T) {
	d := sqliteDB(t)
	dir := t.TempDir()
	bad := "CREATE TABLE ok_table (id integer);\nCREATE TABLE broken (;\n"
	if err := os.WriteFile(filepath.Join(dir, "001_bad.up.sql"), []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	r := NewRunner(d.SQL, d.Dialect, dir)
	if err := r.Up(context.Background(), 0); err == nil {
		t.Fatal("expected failure")
	}
	var n int
	_ = d.SQL.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = 'ok_table'`).Scan(&n)
	if n != 0 {
		t.Fatal("partial migration left ok_table behind")
	}
	st, _ := r.Status(context.Background())
	if len(st) != 1 || !st[0].Pending {
		t.Fatalf("failed migration recorded as applied: %+v", st)
	}
}
