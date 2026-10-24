package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/teleology-io/yayPI/internal/config"
	"github.com/teleology-io/yayPI/internal/db"
	"github.com/teleology-io/yayPI/internal/middleware"
	"github.com/teleology-io/yayPI/internal/query"
	"github.com/teleology-io/yayPI/internal/schema"
	"github.com/teleology-io/yayPI/internal/testutil"
)

// contactsConfig is an entity synced from an outside system: external_id is its natural
// key and source_updated_at its stale guard.
func contactsConfig() *config.RootConfig {
	return &config.RootConfig{Entities: []*config.EntityConfig{{Entity: config.EntityDef{
		Name: "Contact", Table: "contacts", Timestamps: true, SoftDelete: true,
		NaturalKey: []string{"external_id"}, StaleGuard: "source_updated_at",
		Fields: []config.FieldDef{
			{Name: "id", Type: "uuid", PrimaryKey: true},
			{Name: "external_id", Type: "string", Unique: true},
			{Name: "email", Type: "string"},
			{Name: "name", Type: "string", Nullable: true},
			{Name: "score", Type: "integer", Nullable: true},
			{Name: "source_updated_at", Type: "timestamptz", Nullable: true},
		},
	}}}}
}

type upsertEnv struct {
	t   *testing.T
	f   *Factory
	srv http.Handler
	dbm *db.Manager
}

func newUpsertEnv(t *testing.T) *upsertEnv {
	t.Helper()
	reg, err := schema.Build(contactsConfig())
	if err != nil {
		t.Fatal(err)
	}
	dbm := testutil.SQLiteDB(t, reg)
	f := NewFactory(reg, dbm, nil, nil, []byte(strings.Repeat("c", 32)))
	e, _ := reg.GetEntity("Contact")
	r := chi.NewRouter()
	r.Put("/contacts", f.Create(e, &schema.CreateOpts{OnConflict: ConflictUpdate}))
	r.Post("/contacts", f.Create(e, &schema.CreateOpts{}))
	r.Post("/contacts/ignore", f.Create(e, &schema.CreateOpts{OnConflict: ConflictIgnore}))
	r.Post("/contacts/bulk", f.Create(e, &schema.CreateOpts{Bulk: true, BulkMax: 10, OnConflict: ConflictUpdate}))
	return &upsertEnv{t: t, f: f, srv: r, dbm: dbm}
}

func (e *upsertEnv) do(method, path string, body any) (*httptest.ResponseRecorder, map[string]any) {
	e.t.Helper()
	b, _ := json.Marshal(body)
	r := httptest.NewRequest(method, path, bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	e.srv.ServeHTTP(rr, r)
	var out map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	return rr, out
}

func (e *upsertEnv) count() int {
	var n int
	_ = e.dbm.Default().SQL.QueryRow(`SELECT COUNT(*) FROM contacts`).Scan(&n)
	return n
}

func TestUpsertCreateUpdateAndStaleGuard(t *testing.T) {
	e := newUpsertEnv(t)
	rr, out := e.do("PUT", "/contacts", map[string]any{"external_id": "c1", "email": "a@x.io", "source_updated_at": "2024-01-01T00:00:00Z"})
	if rr.Code != http.StatusCreated || rr.Header().Get("X-Write-Action") != ActionCreated {
		t.Fatalf("first upsert: %d %s %s", rr.Code, rr.Header().Get("X-Write-Action"), rr.Body)
	}
	id := out["data"].(map[string]any)["id"]

	rr, out = e.do("PUT", "/contacts", map[string]any{"external_id": "c1", "email": "b@x.io", "source_updated_at": "2024-02-01T00:00:00Z"})
	if rr.Code != http.StatusOK || rr.Header().Get("X-Write-Action") != ActionUpdated {
		t.Fatalf("newer upsert: %d %s %s", rr.Code, rr.Header().Get("X-Write-Action"), rr.Body)
	}
	if d := out["data"].(map[string]any); d["email"] != "b@x.io" || d["id"] != id {
		t.Fatalf("update did not apply in place: %v", d)
	}

	rr, out = e.do("PUT", "/contacts", map[string]any{"external_id": "c1", "email": "stale@x.io", "source_updated_at": "2023-12-01T00:00:00Z"})
	if rr.Code != http.StatusOK || rr.Header().Get("X-Write-Action") != ActionUnchanged {
		t.Fatalf("older upsert: %d %s %s", rr.Code, rr.Header().Get("X-Write-Action"), rr.Body)
	}
	if out["data"].(map[string]any)["email"] != "b@x.io" {
		t.Fatalf("stale write overwrote newer data: %v", out["data"])
	}
	if n := e.count(); n != 1 {
		t.Fatalf("rows = %d, want 1", n)
	}
}

func TestUpsertNeedsNaturalKey(t *testing.T) {
	e := newUpsertEnv(t)
	rr, out := e.do("PUT", "/contacts", map[string]any{"email": "a@x.io"})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("got %d %s", rr.Code, rr.Body)
	}
	if _, ok := out["errors"].(map[string]any)["external_id"]; !ok {
		t.Fatalf("expected external_id error: %v", out)
	}
}

func TestUpsertRevivesSoftDeleted(t *testing.T) {
	e := newUpsertEnv(t)
	e.do("PUT", "/contacts", map[string]any{"external_id": "c1", "email": "a@x.io"})
	if _, err := e.dbm.Default().SQL.Exec(`UPDATE contacts SET deleted_at = '2024-01-01 00:00:00'`); err != nil {
		t.Fatal(err)
	}
	rr, out := e.do("PUT", "/contacts", map[string]any{"external_id": "c1", "email": "back@x.io"})
	if rr.Code != http.StatusOK || rr.Header().Get("X-Write-Action") != ActionUpdated {
		t.Fatalf("got %d %s", rr.Code, rr.Body)
	}
	if d := out["data"].(map[string]any); d["deleted_at"] != nil || d["email"] != "back@x.io" {
		t.Fatalf("row not revived: %v", d)
	}
}

func TestOnConflictErrorAndIgnore(t *testing.T) {
	e := newUpsertEnv(t)
	e.do("POST", "/contacts", map[string]any{"external_id": "c1", "email": "a@x.io"})
	if rr, _ := e.do("POST", "/contacts", map[string]any{"external_id": "c1", "email": "b@x.io"}); rr.Code != http.StatusConflict {
		t.Fatalf("plain create of a duplicate key: %d, want 409", rr.Code)
	}
	rr, out := e.do("POST", "/contacts/ignore", map[string]any{"external_id": "c1", "email": "b@x.io"})
	if rr.Code != http.StatusOK || rr.Header().Get("X-Write-Action") != ActionIgnored || out["data"].(map[string]any)["email"] != "a@x.io" {
		t.Fatalf("ignore: %d %s %s", rr.Code, rr.Header().Get("X-Write-Action"), rr.Body)
	}
}

func TestBulkUpsertReportsActions(t *testing.T) {
	e := newUpsertEnv(t)
	e.do("PUT", "/contacts", map[string]any{"external_id": "c1", "email": "a@x.io"})
	rr, out := e.do("POST", "/contacts/bulk", []map[string]any{
		{"external_id": "c1", "email": "a2@x.io"},
		{"external_id": "c2", "email": "c2@x.io"},
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("got %d %s", rr.Code, rr.Body)
	}
	res := out["results"].([]any)
	if a, b := res[0].(map[string]any)["action"], res[1].(map[string]any)["action"]; a != ActionUpdated || b != ActionCreated {
		t.Fatalf("actions = %v, %v", a, b)
	}
	if n := e.count(); n != 2 {
		t.Fatalf("rows = %d, want 2", n)
	}
}

func TestWriteAPI(t *testing.T) {
	e := newUpsertEnv(t)
	ctx := context.Background()
	sys := &middleware.Subject{ID: "webhook:crm"}

	res, err := e.f.Write(ctx,
		WriteRequest{Entity: "Contact", Action: "create", Data: map[string]any{"external_id": "w1", "email": "w1@x.io"}, Subject: sys},
		WriteRequest{Entity: "Contact", Action: "upsert", Data: map[string]any{"external_id": "w2", "email": "w2@x.io", "score": json.Number("3")}, Subject: sys},
	)
	if err != nil || res[0].Action != ActionCreated || res[1].Action != ActionCreated {
		t.Fatalf("create+upsert: %v %+v", err, res)
	}

	res, err = e.f.Write(ctx, WriteRequest{Entity: "Contact", Action: "update", Key: map[string]any{"external_id": "w1"}, Data: map[string]any{"name": "Wanda"}})
	if err != nil || res[0].Action != ActionUpdated || res[0].Record["name"] != "Wanda" {
		t.Fatalf("update: %v %+v", err, res)
	}

	if res, err = e.f.Write(ctx, WriteRequest{Entity: "Contact", Action: "delete", Key: map[string]any{"external_id": "w1"}}); err != nil || res[0].Action != ActionDeleted {
		t.Fatalf("delete: %v %+v", err, res)
	}
	if res, err = e.f.Write(ctx, WriteRequest{Entity: "Contact", Action: "delete", Key: map[string]any{"external_id": "nope"}}); err != nil || res[0].Action != ActionNotFound {
		t.Fatalf("delete of a missing row should be a no-op: %v %+v", err, res)
	}

	_, err = e.f.Write(ctx, WriteRequest{Entity: "Contact", Action: "update", Key: map[string]any{"external_id": "nope"}, Data: map[string]any{"name": "x"}})
	if !errors.Is(err, ErrInvalidWrite) || !errors.Is(err, query.ErrNotFound) {
		t.Fatalf("update of a missing row: %v", err)
	}
	for _, bad := range []WriteRequest{
		{Entity: "Contact", Action: "update", Key: map[string]any{"email": "w2@x.io"}, Data: map[string]any{"name": "x"}}, // non-unique key
		{Entity: "Contact", Action: "create", Data: map[string]any{"external_id": "w9", "score": "lots"}},                 // validation
		{Entity: "Contact", Action: "upsert", Data: map[string]any{"email": "nokey@x.io"}},                                // missing natural key
		{Entity: "Contact", Action: "merge", Data: map[string]any{}},
		{Entity: "Nope", Action: "create"},
	} {
		if _, err := e.f.Write(ctx, bad); !errors.Is(err, ErrInvalidWrite) {
			t.Errorf("%+v: got %v, want ErrInvalidWrite", bad, err)
		}
	}

	// One transaction: a failing second write rolls back the first.
	before := e.count()
	_, err = e.f.Write(ctx,
		WriteRequest{Entity: "Contact", Action: "create", Data: map[string]any{"external_id": "t1", "email": "t1@x.io"}},
		WriteRequest{Entity: "Contact", Action: "create", Data: map[string]any{"external_id": "w2", "email": "dup@x.io"}},
	)
	if err == nil || e.count() != before {
		t.Fatalf("partial batch committed: err=%v rows %d → %d", err, before, e.count())
	}
}
