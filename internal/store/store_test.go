package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/teleology-io/yayPI/internal/config"
	"github.com/teleology-io/yayPI/internal/handler"
	"github.com/teleology-io/yayPI/internal/plugin"
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

// Before* hooks run inside the write's transaction: they get it as c.Tx with
// the stored row as c.Before, and Store calls made with c.Ctx join it, so a
// one-connection pool (SQLite) doesn't deadlock.
func TestHooksRunInTheWriteTransaction(t *testing.T) {
	cfg := &config.RootConfig{Entities: []*config.EntityConfig{{Entity: config.EntityDef{
		Name: "Event", Table: "events",
		Fields: []config.FieldDef{
			{Name: "id", Type: "uuid", PrimaryKey: true},
			{Name: "title", Type: "string"},
			{Name: "starts", Type: "integer"},
			{Name: "ends", Type: "integer"},
		},
	}}}}
	reg, err := schema.Build(cfg)
	if err != nil {
		t.Fatal(err)
	}
	dbm := testutil.SQLiteDB(t, reg)
	dbm.Default().SQL.SetMaxOpenConns(1)
	disp := plugin.NewDispatcher()
	var s *Store
	var seenBefore map[string]any
	var counted []map[string]any
	disp.RegisterHooks("Event", sdk.Hooks{
		BeforeUpdate: func(c sdk.HookContext, id string, d map[string]any) (map[string]any, error) {
			seenBefore = c.Before
			if c.Tx == nil {
				t.Error("no Tx in BeforeUpdate")
			}
			// A partial update checked against the stored row.
			if e, ok := d["ends"]; ok && toInt(e) <= toInt(c.Before["starts"]) {
				return nil, sdk.Reject(422, "ends must be after starts")
			}
			var err error
			counted, err = s.Query(c.Ctx, `SELECT COUNT(*) AS n FROM events`)
			return d, err
		},
		BeforeDelete: func(c sdk.HookContext, id string) error {
			_, err := c.Tx.Exec(c.Ctx, `UPDATE events SET title = $1 WHERE id <> $2`, "sibling deleted", id)
			return err
		},
	})
	s = New(dbm, handler.NewFactory(reg, dbm, nil, disp, []byte(strings.Repeat("c", 32))), reg)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := s.Write(ctx, sdk.WriteRequest{Entity: "Event", Action: "create", Data: map[string]any{"title": "a", "starts": 10, "ends": 20}},
		sdk.WriteRequest{Entity: "Event", Action: "create", Data: map[string]any{"title": "b", "starts": 1, "ends": 2}})
	if err != nil {
		t.Fatal(err)
	}
	id := res[0].Record["id"]
	if _, err := s.Write(ctx, sdk.WriteRequest{Entity: "Event", Action: "update", Key: map[string]any{"id": id}, Data: map[string]any{"ends": 30}}); err != nil {
		t.Fatalf("update (deadlock or error): %v", err)
	}
	if seenBefore["title"] != "a" || len(counted) != 1 {
		t.Fatalf("before %v, counted %v", seenBefore, counted)
	}
	_, err = s.Write(ctx, sdk.WriteRequest{Entity: "Event", Action: "update", Key: map[string]any{"id": id}, Data: map[string]any{"ends": 5}})
	var he *sdk.HookError
	if !errors.As(err, &he) || he.Status != 422 {
		t.Fatalf("cross-field rejection: %v", err)
	}
	if _, err := s.Write(ctx, sdk.WriteRequest{Entity: "Event", Action: "delete", Key: map[string]any{"id": id}}); err != nil {
		t.Fatal(err)
	}
	rows, _ := s.Query(ctx, `SELECT title FROM events`)
	if len(rows) != 1 || rows[0]["title"] != "sibling deleted" {
		t.Fatalf("hook write in the transaction: %v", rows)
	}
}

func toInt(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	}
	return 0
}

// Read applies a list endpoint's rules for the subject, so plugin reads can't
// skip the owner filter the HTTP API enforces.
func TestReadAppliesEndpointRules(t *testing.T) {
	var ent config.EntityDef
	if err := yaml.Unmarshal([]byte(`
name: Note
table: notes
fields:
  - {name: id, type: uuid, primary_key: true, default: uuid}
  - {name: owner_id, type: string}
  - {name: title, type: string}
  - {name: secret, type: string, nullable: true, serialization: {omit_response: true}}
`), &ent); err != nil {
		t.Fatal(err)
	}
	var eps config.EndpointFileConfig
	if err := yaml.Unmarshal([]byte(`
endpoints:
  - path: /notes
    entity: Note
    crud: [list]
    list:
      row_access:
        - {when: 'subject.role == "admin"', filter: ""}
        - {when: "*", filter: "owner_id = :subject.id"}
`), &eps); err != nil {
		t.Fatal(err)
	}
	reg, err := schema.Build(&config.RootConfig{Entities: []*config.EntityConfig{{Entity: ent}}, Endpoints: []*config.EndpointFileConfig{&eps}})
	if err != nil {
		t.Fatal(err)
	}
	dbm := testutil.SQLiteDB(t, reg)
	s := New(dbm, handler.NewFactory(reg, dbm, nil, nil, []byte(strings.Repeat("c", 32))), reg)
	ctx := context.Background()
	for _, n := range []map[string]any{{"owner_id": "u1", "title": "a", "secret": "x"}, {"owner_id": "u1", "title": "b"}, {"owner_id": "u2", "title": "c"}} {
		if _, err := s.Write(ctx, sdk.WriteRequest{Entity: "Note", Action: "create", Data: n}); err != nil {
			t.Fatal(err)
		}
	}
	u1 := &sdk.Subject{ID: "u1", Role: "member"}
	rows, err := s.Read(ctx, sdk.ReadRequest{Entity: "Note", Subject: u1, Sort: "-title"})
	if err != nil || len(rows) != 2 || rows[0]["title"] != "b" {
		t.Fatalf("owner rows: %v %v", err, rows)
	}
	if _, leaked := rows[1]["secret"]; leaked {
		t.Error("omit_response field returned")
	}
	if rows, _ := s.Read(ctx, sdk.ReadRequest{Entity: "Note", Subject: u1, Where: map[string]any{"title": []any{"a", "c"}}}); len(rows) != 1 || rows[0]["title"] != "a" {
		t.Fatalf("where IN under the owner rule: %v", rows)
	}
	if rows, _ := s.Read(ctx, sdk.ReadRequest{Entity: "Note", Subject: u1, Where: map[string]any{"title[gt]": "a", "secret": nil}}); len(rows) != 1 || rows[0]["title"] != "b" {
		t.Fatalf("operators: %v", rows)
	}
	if _, err := s.Read(ctx, sdk.ReadRequest{Entity: "Note", Subject: u1, Where: map[string]any{"title[near]": "a"}}); !sdk.IsPermanent(err) {
		t.Fatalf("unknown operator: %v", err)
	}
	if rows, _ := s.Read(ctx, sdk.ReadRequest{Entity: "Note", Subject: &sdk.Subject{ID: "x", Role: "admin"}, Via: "GET /notes"}); len(rows) != 3 {
		t.Fatalf("admin sees all: %v", rows)
	}
	for name, req := range map[string]sdk.ReadRequest{
		"no subject":    {Entity: "Note"},
		"unknown via":   {Entity: "Note", Subject: u1, Via: "GET /other"},
		"unknown field": {Entity: "Note", Subject: u1, Where: map[string]any{"nope": 1}},
		"bad sort":      {Entity: "Note", Subject: u1, Sort: "nope"},
	} {
		if _, err := s.Read(ctx, req); !sdk.IsPermanent(err) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestRulesAndStamps(t *testing.T) {
	var task, event config.EntityDef
	for src, dst := range map[string]*config.EntityDef{`
name: Task
table: tasks
fields:
  - {name: id, type: uuid, primary_key: true, default: uuid}
  - {name: title, type: string}
  - {name: done, type: boolean, default: false}
  - {name: completed_at, type: timestamptz, nullable: true, stamp_when: done}
`: &task, `
name: Slot
table: slots
fields:
  - {name: id, type: uuid, primary_key: true, default: uuid}
  - {name: starts_at, type: timestamptz}
  - {name: ends_at, type: timestamptz, nullable: true}
  - {name: seats, type: integer, default: 1}
rules:
  - {check: ends_at > starts_at, message: End must be after start}
  - {check: seats >= 1}
`: &event} {
		if err := yaml.Unmarshal([]byte(src), dst); err != nil {
			t.Fatal(err)
		}
	}
	reg, err := schema.Build(&config.RootConfig{Entities: []*config.EntityConfig{{Entity: task}, {Entity: event}}})
	if err != nil {
		t.Fatal(err)
	}
	dbm := testutil.SQLiteDB(t, reg)
	s := New(dbm, handler.NewFactory(reg, dbm, nil, nil, []byte(strings.Repeat("c", 32))), reg)
	ctx := context.Background()
	write := func(r sdk.WriteRequest) (map[string]any, error) {
		res, err := s.Write(ctx, r)
		if err != nil {
			return nil, err
		}
		return res[0].Record, nil
	}

	rec, err := write(sdk.WriteRequest{Entity: "Task", Action: "create", Data: map[string]any{"title": "a", "completed_at": "2020-01-01T00:00:00Z"}})
	if err != nil || rec["completed_at"] != nil {
		t.Fatalf("stamp is server-managed: %v %v", err, rec)
	}
	key := map[string]any{"id": rec["id"]}
	rec, _ = write(sdk.WriteRequest{Entity: "Task", Action: "update", Key: key, Data: map[string]any{"done": true}})
	stamped := rec["completed_at"]
	if stamped == nil {
		t.Fatalf("done → stamped: %v", rec)
	}
	rec, _ = write(sdk.WriteRequest{Entity: "Task", Action: "update", Key: key, Data: map[string]any{"done": true, "title": "b"}})
	if rec["completed_at"] != stamped {
		t.Fatalf("still done keeps the stamp: %v vs %v", rec["completed_at"], stamped)
	}
	rec, _ = write(sdk.WriteRequest{Entity: "Task", Action: "update", Key: key, Data: map[string]any{"done": false}})
	if rec["completed_at"] != nil {
		t.Fatalf("undone clears: %v", rec)
	}

	_, err = write(sdk.WriteRequest{Entity: "Slot", Action: "create", Data: map[string]any{"starts_at": "2026-01-01T10:00:00Z", "ends_at": "2026-01-01T09:00:00Z"}})
	var he *sdk.HookError
	if !errors.As(err, &he) || he.Status != 422 || he.Message != "End must be after start" || he.Fields["ends_at"] == "" || he.Fields["starts_at"] == "" {
		t.Fatalf("rule on create: %v %+v", err, he)
	}
	rec, err = write(sdk.WriteRequest{Entity: "Slot", Action: "create", Data: map[string]any{"starts_at": "2026-01-01T10:00:00Z"}})
	if err != nil {
		t.Fatalf("null ends_at passes: %v", err)
	}
	key = map[string]any{"id": rec["id"]}
	// A partial update is checked against the stored starts_at.
	if _, err = write(sdk.WriteRequest{Entity: "Slot", Action: "update", Key: key, Data: map[string]any{"ends_at": "2026-01-01T09:30:00Z"}}); !errors.As(err, &he) {
		t.Fatalf("partial update rule: %v", err)
	}
	if _, err = write(sdk.WriteRequest{Entity: "Slot", Action: "update", Key: key, Data: map[string]any{"ends_at": "2026-01-01T11:00:00Z"}}); err != nil {
		t.Fatal(err)
	}
	if _, err = write(sdk.WriteRequest{Entity: "Slot", Action: "update", Key: key, Data: map[string]any{"seats": 0}}); !errors.As(err, &he) || he.Message != "must satisfy seats >= 1" {
		t.Fatalf("default message: %v", err)
	}
}

func TestRuleConfigErrors(t *testing.T) {
	cases := map[string]string{
		"unknown field":      "rules: [{check: nope > 1}]",
		"number vs time":     "rules: [{check: at > 3}]",
		"bool ordering":      "rules: [{check: on > off}]",
		"no operator":        "rules: [{check: n}]",
		"two constants":      "rules: [{check: 1 < 2}]",
		"stamp not nullable": "fields: [{name: at2, type: timestamptz, stamp_when: on}]",
		"stamp not a time":   "fields: [{name: s, type: string, nullable: true, stamp_when: on}]",
	}
	for name, extra := range cases {
		src := "name: X\ntable: xs\nfields:\n  - {name: id, type: uuid, primary_key: true}\n  - {name: at, type: timestamptz}\n  - {name: n, type: integer}\n  - {name: on, type: boolean}\n  - {name: off, type: boolean}\n"
		var def config.EntityDef
		if err := yaml.Unmarshal([]byte(src), &def); err != nil {
			t.Fatal(err)
		}
		var more config.EntityDef
		if err := yaml.Unmarshal([]byte(extra), &more); err != nil {
			t.Fatal(err)
		}
		def.Rules = more.Rules
		def.Fields = append(def.Fields, more.Fields...)
		if _, err := schema.Build(&config.RootConfig{Entities: []*config.EntityConfig{{Entity: def}}}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
