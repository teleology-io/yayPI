package integration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/teleology-io/yayPI/internal/config"
	"github.com/teleology-io/yayPI/internal/db"
	"github.com/teleology-io/yayPI/internal/handler"
	"github.com/teleology-io/yayPI/internal/inbox"
	"github.com/teleology-io/yayPI/internal/migration"
	"github.com/teleology-io/yayPI/internal/router"
	"github.com/teleology-io/yayPI/internal/schema"
	"github.com/teleology-io/yayPI/internal/signature"
	"github.com/teleology-io/yayPI/internal/store"
)

// syncConfig: an entity mirrored from an outside system, written three ways — the upsert
// endpoint, a signed inbound webhook, and a plugin Store.
func syncConfig() *config.RootConfig {
	return &config.RootConfig{
		Project: config.ProjectConfig{Name: "it", BaseURL: "/api"},
		Plugins: []config.PluginConfig{{Name: "calendar"}},
		Entities: []*config.EntityConfig{{Entity: config.EntityDef{
			Name: "Contact", Table: "contacts", Timestamps: true, SoftDelete: true,
			NaturalKey: []string{"external_id"}, StaleGuard: "source_updated_at",
			Fields: []config.FieldDef{
				{Name: "id", Type: "uuid", PrimaryKey: true},
				{Name: "external_id", Type: "string", Length: 64, Unique: true},
				{Name: "email", Type: "string", Length: 200},
				{Name: "source_updated_at", Type: "timestamptz", Nullable: true},
			},
		}}},
		Endpoints: []*config.EndpointFileConfig{{Endpoints: []config.EndpointDef{{
			Path: "/contacts", Entity: "Contact", CRUD: []string{"get", "upsert"},
		}}}},
		WebhookFiles: []*config.WebhookFileConfig{{Webhooks: []config.WebhookDef{{
			Name: "crm", Mode: config.WebhookInbound, Path: "/webhooks/crm", Secret: "whsec",
			DedupeKey: "body.id", Event: "body.type",
			Routes: map[string]config.WebhookRouteDef{
				"contact.updated": {Entity: "Contact", Op: "upsert", Map: map[string]any{
					"external_id": "{{body.data.id}}", "email": "{{body.data.email}}", "source_updated_at": "{{body.data.updated}}",
				}},
				"contact.deleted": {Entity: "Contact", Op: "delete", Key: map[string]any{"external_id": "{{body.data.id}}"}},
			},
		}}}},
	}
}

func TestSyncPrimitives(t *testing.T) {
	for name, dbc := range dialects(t) {
		t.Run(name, func(t *testing.T) {
			cfg := syncConfig()
			if errs := config.Validate(cfg); len(errs) > 0 {
				t.Fatal(errs)
			}
			reg, err := schema.Build(cfg)
			if err != nil {
				t.Fatal(err)
			}
			dbm, err := db.NewManager([]config.DBConfig{dbc})
			if err != nil {
				t.Fatalf("connect: %v", err)
			}
			t.Cleanup(dbm.Close)
			d := dbm.Default()
			ctx := context.Background()
			for _, table := range []string{"contacts", "notes", schema.InboxTable, schema.SyncCursorTable, schema.RefreshTokenTable, schema.AuthTokenTable, schema.IdempotencyTable, "users"} {
				if _, err := d.SQL.ExecContext(ctx, "DROP TABLE IF EXISTS "+d.Dialect.QuoteIdent(table)); err != nil {
					t.Fatalf("drop %s: %v", table, err)
				}
			}
			stmts, err := migration.NewEngine(d.SQL, d.Dialect, reg).Diff(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if err := migration.ApplyStatements(ctx, d.SQL, d.Dialect, stmts); err != nil {
				t.Fatalf("migrate: %v", err)
			}

			factory := handler.NewFactory(reg, dbm, nil, nil, []byte(strings.Repeat("c", 32)))
			rc, err := inbox.NewReceiver(reg.InboundWebhooks[0], d, nil, "")
			if err != nil {
				t.Fatal(err)
			}
			worker, err := inbox.NewWorker(d, reg.InboundWebhooks, inbox.Options{Writer: factory, Registry: reg})
			if err != nil {
				t.Fatal(err)
			}
			h := router.Build(reg, factory, router.Config{BaseURL: "/api", Inbound: []*inbox.Receiver{rc}})
			c := &client{t: t, h: h}

			// Upsert endpoint: create, newer update, stale update.
			c.must(201, "PUT", "/api/contacts", map[string]any{"external_id": "c1", "email": "a@x.io", "source_updated_at": "2024-01-01T00:00:00Z"})
			rr, out := c.do("PUT", "/api/contacts", map[string]any{"external_id": "c1", "email": "b@x.io", "source_updated_at": "2024-02-01T00:00:00Z"})
			if rr.Code != 200 || rr.Header().Get("X-Write-Action") != "updated" {
				t.Fatalf("newer upsert: %d %s", rr.Code, rr.Body)
			}
			id := out["data"].(map[string]any)["id"].(string)
			rr, _ = c.do("PUT", "/api/contacts", map[string]any{"external_id": "c1", "email": "old@x.io", "source_updated_at": "2023-01-01T00:00:00Z"})
			if rr.Header().Get("X-Write-Action") != "unchanged" {
				t.Fatalf("stale upsert: %d %s %s", rr.Code, rr.Header().Get("X-Write-Action"), rr.Body)
			}

			// Signed inbound webhook → inbox → worker → same row (unix-seconds timestamp).
			send := func(body string) int {
				s, _ := signature.Resolve(nil, "whsec")
				r := httptest.NewRequest(http.MethodPost, "/api/webhooks/crm", strings.NewReader(body))
				r.Header.Set("Content-Type", "application/json")
				for k, v := range s.Sign("whsec", []byte(body), time.Now()) {
					r.Header.Set(k, v)
				}
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				return w.Code
			}
			body := `{"id":"evt_1","type":"contact.updated","data":{"id":"c1","email":"hook@x.io","updated":1735689600}}` // 2025-01-01
			if code := send(body); code != 200 {
				t.Fatalf("webhook: %d", code)
			}
			send(body) // duplicate delivery
			worker.RunOnce(ctx)
			got := c.must(200, "GET", "/api/contacts/"+id, nil)["data"].(map[string]any)
			if got["email"] != "hook@x.io" {
				t.Fatalf("webhook upsert not applied: %v", got)
			}

			// Delete through the webhook, then an upsert revives the soft-deleted row.
			send(`{"id":"evt_2","type":"contact.deleted","data":{"id":"c1"}}`)
			worker.RunOnce(ctx)
			c.must(404, "GET", "/api/contacts/"+id, nil)
			c.must(200, "PUT", "/api/contacts", map[string]any{"external_id": "c1", "email": "back@x.io", "source_updated_at": "2026-01-01T00:00:00Z"})
			c.must(200, "GET", "/api/contacts/"+id, nil)

			// Plugin Store: upsert + cursors.
			st := store.New(dbm, factory, reg)
			if res, err := st.Upsert(ctx, "Contact", map[string]any{"external_id": "c2", "email": "c2@x.io"}); err != nil || res[0].Action != "created" {
				t.Fatalf("store upsert: %v %+v", err, res)
			}
			for _, v := range []string{"p1", "p2", "p2"} {
				if err := st.SetCursor(ctx, "crm", "contacts", v); err != nil {
					t.Fatal(err)
				}
			}
			if v, err := st.Cursor(ctx, "crm", "contacts"); err != nil || v != "p2" {
				t.Fatalf("cursor = %q, %v", v, err)
			}
		})
	}
}
