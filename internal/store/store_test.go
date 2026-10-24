package store

import (
	"context"
	"strings"
	"testing"

	"github.com/teleology-io/yayPI/internal/config"
	"github.com/teleology-io/yayPI/internal/handler"
	"github.com/teleology-io/yayPI/internal/schema"
	"github.com/teleology-io/yayPI/internal/testutil"
	"github.com/teleology-io/yayPI/pkg/sdk"
)

func newStore(t *testing.T, plugins bool) *Store {
	t.Helper()
	cfg := &config.RootConfig{Entities: []*config.EntityConfig{{Entity: config.EntityDef{
		Name: "Event", Table: "events", NaturalKey: []string{"external_id"},
		Fields: []config.FieldDef{
			{Name: "id", Type: "uuid", PrimaryKey: true},
			{Name: "external_id", Type: "string", Unique: true},
			{Name: "title", Type: "string"},
		},
	}}}}
	if plugins {
		cfg.Plugins = []config.PluginConfig{{Name: "calendar"}}
	}
	reg, err := schema.Build(cfg)
	if err != nil {
		t.Fatal(err)
	}
	dbm := testutil.SQLiteDB(t, reg)
	return New(dbm, handler.NewFactory(reg, dbm, nil, nil, []byte(strings.Repeat("c", 32))), reg)
}

func TestCursors(t *testing.T) {
	s := newStore(t, true)
	ctx := context.Background()
	if v, err := s.Cursor(ctx, "calendar", "grant-1"); err != nil || v != "" {
		t.Fatalf("unset cursor: %q %v", v, err)
	}
	for _, v := range []string{"page-1", "page-2", "page-2"} { // insert, update, unchanged
		if err := s.SetCursor(ctx, "calendar", "grant-1", v); err != nil {
			t.Fatal(err)
		}
	}
	_ = s.SetCursor(ctx, "calendar", "grant-2", "other")
	if v, _ := s.Cursor(ctx, "calendar", "grant-1"); v != "page-2" {
		t.Fatalf("cursor = %q", v)
	}
	if err := newStore(t, false).SetCursor(ctx, "x", "y", "z"); err == nil || !strings.Contains(err.Error(), "plugins:") {
		t.Fatalf("cursor without the table: %v", err)
	}
}

func TestQueryExecAndUpsert(t *testing.T) {
	s := newStore(t, false)
	ctx := context.Background()
	res, err := s.Upsert(ctx, "Event", map[string]any{"external_id": "e1", "title": "Standup"}, map[string]any{"external_id": "e1", "title": "Standup (moved)"})
	if err != nil || res[0].Action != "created" || res[1].Action != "updated" {
		t.Fatalf("upsert: %v %+v", err, res)
	}
	rows, err := s.Query(ctx, `SELECT title FROM `+s.QuoteIdent("events")+` WHERE external_id = $1`, "e1")
	if err != nil || len(rows) != 1 || rows[0]["title"] != "Standup (moved)" {
		t.Fatalf("query: %v %v", err, rows)
	}
	err = s.WithTx(ctx, func(tx sdk.Tx) error {
		n, err := tx.Exec(ctx, `UPDATE events SET title = $1 WHERE external_id = $2`, "Retro", "e1")
		if n != 1 {
			t.Errorf("affected = %d", n)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Upsert(ctx, "Event", map[string]any{"title": "no key"}); !sdk.IsPermanent(err) {
		t.Fatalf("invalid write should be permanent: %v", err)
	}
}
