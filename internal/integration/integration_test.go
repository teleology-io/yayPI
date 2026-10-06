// Package integration runs the full HTTP stack — migrations, auth, CRUD, pagination,
// optimistic concurrency — against every configured database dialect.
//
// SQLite always runs. Postgres and MySQL run when these are set (CI provides them):
//
//	YAYPI_TEST_POSTGRES_DSN=postgres://postgres:postgres@localhost:5432/yaypi?sslmode=disable
//	YAYPI_TEST_MYSQL_DSN=root:root@tcp(localhost:3306)/yaypi?parseTime=true
package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/teleology-io/yayPI/internal/auth"
	"github.com/teleology-io/yayPI/internal/config"
	"github.com/teleology-io/yayPI/internal/db"
	"github.com/teleology-io/yayPI/internal/handler"
	"github.com/teleology-io/yayPI/internal/migration"
	"github.com/teleology-io/yayPI/internal/router"
	"github.com/teleology-io/yayPI/internal/schema"
	"github.com/teleology-io/yayPI/internal/token"
)

func dialects(t *testing.T) map[string]config.DBConfig {
	out := map[string]config.DBConfig{
		"sqlite": {Name: "primary", Driver: "sqlite", DSN: "file:" + filepath.Join(t.TempDir(), "it.db") + "?_pragma=foreign_keys(1)", Default: true},
	}
	if dsn := os.Getenv("YAYPI_TEST_POSTGRES_DSN"); dsn != "" {
		out["postgres"] = config.DBConfig{Name: "primary", Driver: "postgres", DSN: dsn, Default: true}
	}
	if dsn := os.Getenv("YAYPI_TEST_MYSQL_DSN"); dsn != "" {
		out["mysql"] = config.DBConfig{Name: "primary", Driver: "mysql", DSN: dsn, Default: true}
	}
	return out
}

func rootConfig() *config.RootConfig {
	f := false
	return &config.RootConfig{
		Project: config.ProjectConfig{Name: "it", BaseURL: "/api"},
		AuthEndpoint: &config.AuthEndpointFileConfig{Auth: config.AuthEndpointDef{
			Register: &config.RegisterDef{Enabled: true, DefaultRole: "member"},
			Login:    &config.LoginDef{Enabled: true},
			Me:       &config.MeDef{Enabled: true},
			Refresh:  &config.RefreshDef{Enabled: true, Store: "body"},
			Cookie:   &config.CookieDef{Secure: &f},
		}},
		Entities: []*config.EntityConfig{{Entity: config.EntityDef{
			Name: "Note", Table: "notes", Timestamps: true, SoftDelete: true,
			Fields: []config.FieldDef{
				{Name: "id", Type: "uuid", PrimaryKey: true, Default: "gen_random_uuid()"},
				{Name: "title", Type: "string", Length: 100, Validate: &config.FieldValidateDef{Required: true}},
				{Name: "rank", Type: "integer", Nullable: true},
				{Name: "meta", Type: "jsonb", Nullable: true},
				{Name: "owner_id", Type: "uuid", DefaultFrom: "subject.id",
					References: &config.ReferenceDef{Entity: "User", Field: "id", OnDelete: "CASCADE", OnUpdate: "NO ACTION"}},
			},
		}}},
		Endpoints: []*config.EndpointFileConfig{{Endpoints: []config.EndpointDef{{
			Path: "/notes", Entity: "Note", CRUD: []string{"list", "get", "create", "update", "replace", "delete"},
			Auth: &config.AuthRequirement{Require: true},
			List: &config.ListConfig{
				AllowFilterBy: []string{"rank", "title"}, AllowSortBy: []string{"title", "rank", "created_at"},
				RowAccess: []config.RowAccessRule{{When: "*", Filter: "owner_id = :subject.id"}},
			},
			Get:    &config.GetConfig{RowAccess: []config.RowAccessRule{{When: "*", Filter: "owner_id = :subject.id"}}},
			Update: &config.UpdateConfig{RowAccess: []config.RowAccessRule{{When: "*", Filter: "owner_id = :subject.id"}}},
			Delete: &config.DeleteConfig{RowAccess: []config.RowAccessRule{{When: "*", Filter: "owner_id = :subject.id"}}},
		}}}},
	}
}

func setup(t *testing.T, dbc config.DBConfig) http.Handler {
	t.Helper()
	cfg := rootConfig()
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

	// Start clean on shared servers: drop our tables, dependents first.
	for _, table := range []string{"notes", schema.RefreshTokenTable, schema.AuthTokenTable, schema.IdempotencyTable, "users"} {
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

	keys, err := token.New(token.Config{Secret: []byte(strings.Repeat("s", 32)), Issuer: "it", AccessTTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	authH := auth.New(auth.Options{Config: cfg.AuthEndpoint.Auth, Registry: reg, DB: dbm, Tokens: keys, RevocationCheck: true, MountPrefix: "/api"})
	factory := handler.NewFactory(reg, dbm, nil, nil, []byte(strings.Repeat("c", 32)))
	return router.Build(reg, factory, router.Config{
		BaseURL: "/api", Tokens: keys, ValidateSubject: authH.SubjectValidator(), AuthHandler: authH,
	})
}

type client struct {
	t     *testing.T
	h     http.Handler
	token string
}

func (c *client) do(method, path string, body any, headers ...string) (*httptest.ResponseRecorder, map[string]any) {
	c.t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	r := httptest.NewRequest(method, path, &buf)
	r.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		r.Header.Set("Authorization", "Bearer "+c.token)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}
	rr := httptest.NewRecorder()
	c.h.ServeHTTP(rr, r)
	var out map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	return rr, out
}

func (c *client) must(code int, method, path string, body any, headers ...string) map[string]any {
	c.t.Helper()
	rr, out := c.do(method, path, body, headers...)
	if rr.Code != code {
		c.t.Fatalf("%s %s: want %d, got %d: %s", method, path, code, rr.Code, rr.Body)
	}
	return out
}

func TestFullStack(t *testing.T) {
	for name, dbc := range dialects(t) {
		t.Run(name, func(t *testing.T) {
			h := setup(t, dbc)
			alice := &client{t: t, h: h}
			bob := &client{t: t, h: h}

			reg := alice.must(201, "POST", "/api/auth/register", map[string]any{"email": "Alice@Example.com", "password": "password1", "role": "admin"})
			if reg["user"].(map[string]any)["role"] != "member" {
				t.Fatal("register accepted a client-chosen role")
			}
			login := alice.must(200, "POST", "/api/auth/login", map[string]any{"email": "alice@example.com", "password": "password1"})
			alice.token = login["token"].(string)
			bob.token = bob.must(201, "POST", "/api/auth/register", map[string]any{"email": "bob@example.com", "password": "password1"})["token"].(string)

			for i := 0; i < 7; i++ {
				alice.must(201, "POST", "/api/notes", map[string]any{"title": fmt.Sprintf("n%d", i%3), "rank": i, "meta": map[string]any{"i": i}})
			}
			bob.must(201, "POST", "/api/notes", map[string]any{"title": "bob's"})

			// Keyset pagination over a non-unique sort returns each of alice's rows once;
			// bob's row is invisible to her (row_access).
			seen := map[string]bool{}
			next := ""
			for page := 0; page < 10; page++ {
				u := "/api/notes?limit=3&sort=-title"
				if next != "" {
					u += "&cursor=" + url.QueryEscape(next)
				}
				out := alice.must(200, "GET", u, nil)
				for _, row := range out["data"].([]any) {
					id := row.(map[string]any)["id"].(string)
					if seen[id] {
						t.Fatalf("row %s returned twice", id)
					}
					seen[id] = true
				}
				nc, _ := out["meta"].(map[string]any)["next_cursor"].(string)
				if nc == "" {
					break
				}
				next = nc
			}
			if len(seen) != 7 {
				t.Fatalf("paged %d rows, want 7", len(seen))
			}

			filtered := alice.must(200, "GET", "/api/notes?rank[gte]=5", nil)
			if n := len(filtered["data"].([]any)); n != 2 {
				t.Fatalf("rank[gte]=5 → %d rows", n)
			}

			created := alice.must(201, "POST", "/api/notes", map[string]any{"title": "edit me", "meta": map[string]any{"k": "v"}})["data"].(map[string]any)
			id := created["id"].(string)
			if meta, ok := created["meta"].(map[string]any); !ok || meta["k"] != "v" {
				t.Fatalf("jsonb round-trip: %#v", created["meta"])
			}
			rr, _ := alice.do("GET", "/api/notes/"+id, nil)
			etag := rr.Header().Get("ETag")
			alice.must(200, "PATCH", "/api/notes/"+id, map[string]any{"title": "edited"}, "If-Match", etag)
			alice.must(412, "PATCH", "/api/notes/"+id, map[string]any{"title": "stale"}, "If-Match", etag)
			bob.must(404, "PATCH", "/api/notes/"+id, map[string]any{"title": "not yours"})
			put := alice.must(200, "PUT", "/api/notes/"+id, map[string]any{"title": "replaced"})["data"].(map[string]any)
			if put["meta"] != nil || put["rank"] != nil {
				t.Fatalf("PUT did not reset omitted nullable fields: %v", put)
			}

			refresh := alice.must(200, "POST", "/api/auth/refresh", map[string]any{"refresh_token": login["refresh_token"]})
			alice.must(401, "POST", "/api/auth/refresh", map[string]any{"refresh_token": login["refresh_token"]})
			if refresh["token"] == "" {
				t.Fatal("no access token from refresh")
			}

			alice.must(204, "DELETE", "/api/notes/"+id, nil)
			alice.must(404, "GET", "/api/notes/"+id, nil)

			alice.must(204, "POST", "/api/auth/logout-all", nil)
			alice.must(401, "GET", "/api/notes", nil)
		})
	}
}
