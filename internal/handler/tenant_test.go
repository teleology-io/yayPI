package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/teleology-io/yayPI/internal/middleware"
	"github.com/teleology-io/yayPI/internal/schema"
	"github.com/teleology-io/yayPI/internal/testutil"
	"github.com/teleology-io/yayPI/pkg/types"
)

func TestTenantIsolationAndSearch(t *testing.T) {
	reg := schema.NewRegistry()
	doc := &schema.Entity{
		Name: "Doc", Table: "docs", TenantColumn: "tenant_id",
		Fields: []schema.Field{
			{Name: "id", ColumnName: "id", Type: types.FieldTypeUUID, PrimaryKey: true},
			{Name: "title", ColumnName: "title", Type: types.FieldTypeString},
			{Name: "body", ColumnName: "body", Type: types.FieldTypeText, Nullable: true},
			{Name: "tenant_id", ColumnName: "tenant_id", Type: types.FieldTypeString, DefaultFrom: "subject.tenant"},
		},
	}
	reg.RegisterEntity(doc)
	dbm := testutil.SQLiteDB(t, reg)
	f := NewFactory(reg, dbm, nil, nil, []byte(strings.Repeat("c", 32)))

	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			sub := &middleware.Subject{ID: "u", Tenant: req.Header.Get("X-Tenant")}
			next.ServeHTTP(w, req.WithContext(middleware.WithSubject(req.Context(), sub)))
		})
	})
	r.Get("/docs", f.List(doc, &schema.ListOpts{Search: []string{"title", "body"}}))
	r.Get("/docs/{id}", f.Get(doc, nil))
	r.Post("/docs", f.Create(doc, nil))

	call := func(method, path, tenant string, body any) (int, map[string]any) {
		var buf bytes.Buffer
		if body != nil {
			_ = json.NewEncoder(&buf).Encode(body)
		}
		req := httptest.NewRequest(method, path, &buf)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Tenant", tenant)
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		var out map[string]any
		_ = json.Unmarshal(rr.Body.Bytes(), &out)
		return rr.Code, out
	}

	_, a := call("POST", "/docs", "acme", map[string]any{"title": "Quarterly Plan", "tenant_id": "globex"})
	if a["data"].(map[string]any)["tenant_id"] != "acme" {
		t.Fatalf("tenant_id not forced from claim: %v", a)
	}
	call("POST", "/docs", "acme", map[string]any{"title": "Other", "body": "contains the PLAN too"})
	call("POST", "/docs", "globex", map[string]any{"title": "Globex plan"})

	if _, out := call("GET", "/docs", "acme", nil); len(out["data"].([]any)) != 2 {
		t.Fatalf("acme sees %d docs", len(out["data"].([]any)))
	}
	id := a["data"].(map[string]any)["id"].(string)
	if code, _ := call("GET", "/docs/"+id, "globex", nil); code != http.StatusNotFound {
		t.Fatalf("cross-tenant get: %d", code)
	}
	if code, _ := call("GET", "/docs", "", nil); code != http.StatusForbidden {
		t.Fatalf("no tenant claim: %d", code)
	}
	if _, out := call("GET", "/docs?q=plan", "acme", nil); len(out["data"].([]any)) != 2 {
		t.Fatalf("search across title/body: %v", out["data"])
	}
	if _, out := call("GET", "/docs?q=quarterly", "acme", nil); len(out["data"].([]any)) != 1 {
		t.Fatalf("search narrow: %v", out["data"])
	}
}
