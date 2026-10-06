package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/teleology-io/yayPI/internal/db"
	"github.com/teleology-io/yayPI/internal/middleware"
	"github.com/teleology-io/yayPI/internal/plugin"
	"github.com/teleology-io/yayPI/internal/schema"
	"github.com/teleology-io/yayPI/internal/testutil"
	"github.com/teleology-io/yayPI/pkg/sdk"
	"github.com/teleology-io/yayPI/pkg/types"
)

func f64(v float64) *float64 { return &v }

func testRegistry() *schema.Registry {
	reg := schema.NewRegistry()
	reg.RegisterEntity(schema.NewIdempotencyEntity())
	reg.RegisterEntity(&schema.Entity{
		Name: "Author", Table: "authors",
		Fields: []schema.Field{
			{Name: "id", ColumnName: "id", Type: types.FieldTypeString, PrimaryKey: true},
			{Name: "name", ColumnName: "name", Type: types.FieldTypeString},
			{Name: "secret", ColumnName: "secret", Type: types.FieldTypeString, Nullable: true, OmitResponse: true},
		},
	})
	reg.RegisterEntity(&schema.Entity{
		Name: "Tag", Table: "tags",
		Fields: []schema.Field{
			{Name: "id", ColumnName: "id", Type: types.FieldTypeInteger, PrimaryKey: true},
			{Name: "label", ColumnName: "label", Type: types.FieldTypeString},
		},
	})
	reg.RegisterEntity(&schema.Entity{
		Name: "PostTag", Table: "post_tags",
		Fields: []schema.Field{
			{Name: "post_id", ColumnName: "post_id", Type: types.FieldTypeUUID},
			{Name: "tag_id", ColumnName: "tag_id", Type: types.FieldTypeInteger},
		},
	})
	reg.RegisterEntity(&schema.Entity{
		Name: "Post", Table: "posts", SoftDelete: true, Timestamps: true,
		Fields: []schema.Field{
			{Name: "id", ColumnName: "id", Type: types.FieldTypeUUID, PrimaryKey: true},
			{Name: "title", ColumnName: "title", Type: types.FieldTypeString, Validate: &schema.FieldValidation{Required: true, MaxLength: 50}},
			{Name: "slug", ColumnName: "slug", Type: types.FieldTypeString, Unique: true, Nullable: true},
			{Name: "score", ColumnName: "score", Type: types.FieldTypeInteger, Nullable: true, Validate: &schema.FieldValidation{Min: f64(0), Max: f64(100)}},
			{Name: "author_id", ColumnName: "author_id", Type: types.FieldTypeString, DefaultFrom: "subject.id"},
			{Name: "featured", ColumnName: "featured", Type: types.FieldTypeBoolean, Nullable: true, WriteRoles: []string{"admin"}},
			{Name: "created_at", ColumnName: "created_at", Type: types.FieldTypeTimestamptz, Default: "now()"},
			{Name: "updated_at", ColumnName: "updated_at", Type: types.FieldTypeTimestamptz, Default: "now()"},
			{Name: "deleted_at", ColumnName: "deleted_at", Type: types.FieldTypeTimestamptz, Nullable: true},
		},
		Relations: []schema.Relation{
			{Name: "author", Type: types.RelationBelongsTo, Entity: "Author", ForeignKey: "author_id"},
			{Name: "tags", Type: types.RelationManyToMany, Entity: "Tag", Through: "PostTag", ForeignKey: "post_id", OtherKey: "tag_id"},
		},
	})
	return reg
}

type hookPlugin struct {
	beforeCreate func(map[string]any) (map[string]any, error)
	deletes      int
}

func (h *hookPlugin) Info() sdk.PluginInfo                                      { return sdk.PluginInfo{Name: "t"} }
func (h *hookPlugin) Init(sdk.InitContext) error                                { return nil }
func (h *hookPlugin) Shutdown(context.Context) error                            { return nil }
func (h *hookPlugin) AfterCreate(sdk.HookContext, string, map[string]any) error { return nil }
func (h *hookPlugin) AfterUpdate(sdk.HookContext, string, map[string]any) error { return nil }
func (h *hookPlugin) AfterDelete(sdk.HookContext, string, string) error         { return nil }
func (h *hookPlugin) BeforeUpdate(_ sdk.HookContext, _ string, _ string, d map[string]any) (map[string]any, error) {
	return d, nil
}
func (h *hookPlugin) BeforeDelete(sdk.HookContext, string, string) error { h.deletes++; return nil }
func (h *hookPlugin) BeforeCreate(_ sdk.HookContext, _ string, d map[string]any) (map[string]any, error) {
	if h.beforeCreate != nil {
		return h.beforeCreate(d)
	}
	return d, nil
}

type env struct {
	t     *testing.T
	srv   http.Handler
	dbm   *db.Manager
	hooks *hookPlugin
}

func newEnv(t *testing.T, bulkMode string) *env {
	t.Helper()
	reg := testRegistry()
	dbm := testutil.SQLiteDB(t, reg)
	hooks := &hookPlugin{}
	disp := plugin.NewDispatcher()
	disp.RegisterHook("Post", hooks)
	f := NewFactory(reg, dbm, nil, disp, []byte(strings.Repeat("c", 32)))
	post, _ := reg.GetEntity("Post")

	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if id := req.Header.Get("X-Test-User"); id != "" {
				req = req.WithContext(middleware.WithSubject(req.Context(), &middleware.Subject{ID: id, Role: req.Header.Get("X-Test-Role")}))
			}
			next.ServeHTTP(w, req)
		})
	})
	list := &schema.ListOpts{
		AllowFilterBy: []string{"score", "title", "author_id"},
		AllowSortBy:   []string{"title", "score", "created_at", "id"},
		Include:       []string{"author", "tags"},
		Pagination:    schema.Pagination{DefaultLimit: 20, MaxLimit: 100},
	}
	r.Get("/posts", f.List(post, list))
	r.Get("/posts/{id}", f.Get(post, &schema.GetOpts{Include: []string{"author", "tags"}}))
	r.Post("/posts", f.Create(post, &schema.CreateOpts{BulkMax: 10}))
	r.Post("/posts/bulk", f.Create(post, &schema.CreateOpts{Bulk: true, BulkMax: 10, BulkErrorMode: bulkMode}))
	r.Patch("/posts/{id}", f.Update(post, &schema.UpdateOpts{}))
	r.Delete("/posts/{id}", f.Delete(post, &schema.DeleteOpts{}))
	return &env{t: t, srv: r, dbm: dbm, hooks: hooks}
}

func (e *env) req(method, path string, body any, headers ...string) (*httptest.ResponseRecorder, map[string]any) {
	e.t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	r := httptest.NewRequest(method, path, &buf)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Test-User", "user-1")
	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}
	rr := httptest.NewRecorder()
	e.srv.ServeHTTP(rr, r)
	var out map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	return rr, out
}

func (e *env) create(body map[string]any) map[string]any {
	e.t.Helper()
	rr, out := e.req("POST", "/posts", body)
	if rr.Code != http.StatusCreated {
		e.t.Fatalf("create: %d %s", rr.Code, rr.Body)
	}
	return out["data"].(map[string]any)
}

func (e *env) count() int {
	var n int
	_ = e.dbm.Default().SQL.QueryRow(`SELECT COUNT(*) FROM posts`).Scan(&n)
	return n
}

func TestCreateCoercesAndProtectsFields(t *testing.T) {
	e := newEnv(t, "")
	rec := e.create(map[string]any{"title": "Hello", "score": 7, "author_id": "someone-else", "featured": true, "created_at": "2001-01-01T00:00:00Z", "nonsense": 1})
	if rec["author_id"] != "user-1" {
		t.Errorf("author_id = %v, want caller id (default_from)", rec["author_id"])
	}
	if rec["featured"] != nil {
		t.Errorf("featured set without write role: %v", rec["featured"])
	}
	if strings.HasPrefix(fmt.Sprint(rec["created_at"]), "2001") {
		t.Errorf("created_at was client-settable")
	}
	if rec["score"] != float64(7) {
		t.Errorf("score = %v", rec["score"])
	}
}

func TestCreateValidation(t *testing.T) {
	e := newEnv(t, "")
	cases := []map[string]any{
		{"score": 5},                       // title required
		{"title": "x", "score": "high"},    // wrong type
		{"title": "x", "score": 1.5},       // not an integer
		{"title": "x", "score": 101},       // above max
		{"title": strings.Repeat("y", 51)}, // too long
		{"title": nil},                     // null for non-nullable
	}
	for _, body := range cases {
		rr, out := e.req("POST", "/posts", body)
		if rr.Code != http.StatusBadRequest || out["code"] != "validation_failed" {
			t.Errorf("body %v: %d %s", body, rr.Code, rr.Body)
		}
	}
}

func TestUniqueViolationIs409(t *testing.T) {
	e := newEnv(t, "")
	e.create(map[string]any{"title": "a", "slug": "same"})
	rr, out := e.req("POST", "/posts", map[string]any{"title": "b", "slug": "same"})
	if rr.Code != http.StatusConflict || out["code"] != "conflict" {
		t.Fatalf("got %d %s", rr.Code, rr.Body)
	}
}

func TestBulkAbortIsAtomic(t *testing.T) {
	e := newEnv(t, "abort")
	rr, _ := e.req("POST", "/posts/bulk", []map[string]any{{"title": "a", "slug": "dup"}, {"title": "b", "slug": "dup"}})
	if rr.Code != http.StatusConflict {
		t.Fatalf("got %d %s", rr.Code, rr.Body)
	}
	if n := e.count(); n != 0 {
		t.Fatalf("%d rows committed from a failed bulk insert", n)
	}
}

func TestBulkPartial(t *testing.T) {
	e := newEnv(t, "partial")
	rr, out := e.req("POST", "/posts/bulk", []map[string]any{{"title": "a", "slug": "dup"}, {"title": "b", "slug": "dup"}})
	if rr.Code != http.StatusMultiStatus || e.count() != 1 {
		t.Fatalf("got %d rows=%d", rr.Code, e.count())
	}
	res := out["results"].([]any)[1].(map[string]any)
	if strings.Contains(fmt.Sprint(res["error"]), "UNIQUE") {
		t.Fatalf("driver error leaked: %v", res["error"])
	}
}

// Every row must appear exactly once when paging with a non-unique sort key in either
// direction (the old cursor compared only id and skipped/duplicated rows).
func TestCursorPaginationIsComplete(t *testing.T) {
	e := newEnv(t, "")
	for i := 0; i < 13; i++ {
		e.create(map[string]any{"title": fmt.Sprintf("t%d", i%3), "score": i % 4})
	}
	for _, sort := range []string{"title", "-title", "score:desc", "-created_at", ""} {
		seen := map[string]int{}
		next := ""
		for page := 0; page < 20; page++ {
			u := "/posts?limit=4&sort=" + url.QueryEscape(sort)
			if next != "" {
				u += "&cursor=" + url.QueryEscape(next)
			}
			rr, out := e.req("GET", u, nil)
			if rr.Code != http.StatusOK {
				t.Fatalf("sort %q: %d %s", sort, rr.Code, rr.Body)
			}
			for _, row := range out["data"].([]any) {
				seen[row.(map[string]any)["id"].(string)]++
			}
			meta := out["meta"].(map[string]any)
			nc, _ := meta["next_cursor"].(string)
			if nc == "" {
				break
			}
			next = nc
		}
		if len(seen) != 13 {
			t.Errorf("sort %q: saw %d distinct rows, want 13", sort, len(seen))
		}
		for id, n := range seen {
			if n != 1 {
				t.Errorf("sort %q: row %s seen %d times", sort, id, n)
			}
		}
	}
}

func TestCursorRejectedForDifferentSort(t *testing.T) {
	e := newEnv(t, "")
	for i := 0; i < 3; i++ {
		e.create(map[string]any{"title": fmt.Sprint(i)})
	}
	_, out := e.req("GET", "/posts?limit=1&sort=title", nil)
	c := out["meta"].(map[string]any)["next_cursor"].(string)
	rr, _ := e.req("GET", "/posts?limit=1&sort=score&cursor="+url.QueryEscape(c), nil)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("got %d", rr.Code)
	}
}

func TestFilterOperators(t *testing.T) {
	e := newEnv(t, "")
	for i, title := range []string{"Alpha", "beta", "Gamma_1", "delta"} {
		e.create(map[string]any{"title": title, "score": i * 10})
	}
	check := func(q string, want int) {
		t.Helper()
		rr, out := e.req("GET", "/posts?"+q, nil)
		if rr.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", q, rr.Code, rr.Body)
		}
		if got := len(out["data"].([]any)); got != want {
			t.Errorf("%s: got %d rows, want %d", q, got, want)
		}
	}
	check("score[gte]=10", 3)
	check("score[in]=0,30", 2)
	check("title[contains]=PH", 1)   // Alpha (case-insensitive)
	check("title[starts_with]=g", 1) // Gamma_1
	check("title[contains]=_", 1)    // underscore is literal, not a wildcard
	check("score[is_null]=true", 0)
	if rr, _ := e.req("GET", "/posts?score[gte]=abc", nil); rr.Code != http.StatusBadRequest {
		t.Errorf("bad filter value: %d", rr.Code)
	}
	if rr, _ := e.req("GET", "/posts?slug=x", nil); rr.Code != http.StatusBadRequest {
		t.Errorf("non-filterable field: %d", rr.Code)
	}
}

func TestIfMatch(t *testing.T) {
	e := newEnv(t, "")
	rec := e.create(map[string]any{"title": "v1"})
	id := rec["id"].(string)
	rr, _ := e.req("GET", "/posts/"+id, nil)
	etag := rr.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag on GET")
	}
	if rr, _ := e.req("GET", "/posts/"+id, nil, "If-None-Match", etag); rr.Code != http.StatusNotModified {
		t.Errorf("If-None-Match: %d", rr.Code)
	}
	if rr, _ := e.req("PATCH", "/posts/"+id, map[string]any{"title": "v2"}, "If-Match", etag); rr.Code != http.StatusOK {
		t.Fatalf("matching If-Match: %d %s", rr.Code, rr.Body)
	}
	if rr, _ := e.req("PATCH", "/posts/"+id, map[string]any{"title": "v3"}, "If-Match", etag); rr.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale If-Match: %d", rr.Code)
	}
}

func TestIdempotencyKey(t *testing.T) {
	e := newEnv(t, "")
	body := map[string]any{"title": "once"}
	rr1, out1 := e.req("POST", "/posts", body, "Idempotency-Key", "k-1")
	rr2, out2 := e.req("POST", "/posts", body, "Idempotency-Key", "k-1")
	if rr1.Code != http.StatusCreated || rr2.Code != http.StatusCreated || rr2.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("codes %d %d replayed=%q", rr1.Code, rr2.Code, rr2.Header().Get("Idempotent-Replayed"))
	}
	if out1["data"].(map[string]any)["id"] != out2["data"].(map[string]any)["id"] || e.count() != 1 {
		t.Fatalf("duplicate created; rows=%d", e.count())
	}
	if rr, _ := e.req("POST", "/posts", map[string]any{"title": "different"}, "Idempotency-Key", "k-1"); rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("key reuse with different body: %d", rr.Code)
	}
}

func TestIncludes(t *testing.T) {
	e := newEnv(t, "")
	sqlDB := e.dbm.Default().SQL
	if _, err := sqlDB.Exec(`INSERT INTO authors (id, name, secret) VALUES ('user-1', 'Ann', 'hidden')`); err != nil {
		t.Fatal(err)
	}
	rec := e.create(map[string]any{"title": "p"})
	id := rec["id"].(string)
	if _, err := sqlDB.Exec(`INSERT INTO tags (id, label) VALUES (1, 'go'), (2, 'sql')`); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.Exec(`INSERT INTO post_tags (post_id, tag_id) VALUES (?, 1), (?, 2)`, id, id); err != nil {
		t.Fatal(err)
	}
	rr, out := e.req("GET", "/posts/"+id+"?include=author,tags&fields=title", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("%d %s", rr.Code, rr.Body)
	}
	data := out["data"].(map[string]any)
	author := data["author"].(map[string]any)
	if author["name"] != "Ann" || author["secret"] != nil {
		t.Errorf("author include wrong or leaked omit_response field: %v", author)
	}
	if tags := data["tags"].([]any); len(tags) != 2 {
		t.Errorf("tags = %v", tags)
	}
	if _, has := data["score"]; has {
		t.Errorf("fields= not applied: %v", data)
	}
	if rr, _ := e.req("GET", "/posts?include=secret_stuff", nil); rr.Code != http.StatusBadRequest {
		t.Errorf("unlisted include: %d", rr.Code)
	}
}

func TestHookErrorStatus(t *testing.T) {
	e := newEnv(t, "")
	e.hooks.beforeCreate = func(d map[string]any) (map[string]any, error) {
		return nil, &sdk.HookError{Status: 422, Message: "titles may not mention cats", Code: "no_cats"}
	}
	rr, out := e.req("POST", "/posts", map[string]any{"title": "cats"})
	if rr.Code != 422 || out["code"] != "no_cats" {
		t.Fatalf("got %d %s", rr.Code, rr.Body)
	}
}

func TestDeleteHookSkipsMissingRows(t *testing.T) {
	e := newEnv(t, "")
	rr, _ := e.req("DELETE", "/posts/00000000-0000-4000-8000-000000000000", nil)
	if rr.Code != http.StatusNotFound || e.hooks.deletes != 0 {
		t.Fatalf("status %d, hook calls %d", rr.Code, e.hooks.deletes)
	}
	rec := e.create(map[string]any{"title": "x"})
	if rr, _ := e.req("DELETE", "/posts/"+rec["id"].(string), nil); rr.Code != http.StatusNoContent || e.hooks.deletes != 1 {
		t.Fatalf("status %d, hook calls %d", rr.Code, e.hooks.deletes)
	}
	if rr, _ := e.req("GET", "/posts/"+rec["id"].(string), nil); rr.Code != http.StatusNotFound {
		t.Fatalf("soft-deleted row still visible: %d", rr.Code)
	}
}

// Hooks receive body keys that aren't entity fields (e.g. an "email" a hook resolves to an
// owner id); they never reach SQL.
func TestHooksSeeNonColumnKeys(t *testing.T) {
	e := newEnv(t, "")
	var seen any
	e.hooks.beforeCreate = func(d map[string]any) (map[string]any, error) {
		seen = d["invite_email"]
		delete(d, "invite_email")
		d["slug"] = "from-hook"
		return d, nil
	}
	rec := e.create(map[string]any{"title": "t", "invite_email": "a@b.co"})
	if seen != "a@b.co" {
		t.Fatalf("hook saw %v", seen)
	}
	if rec["slug"] != "from-hook" {
		t.Fatalf("hook output not persisted: %v", rec)
	}
}
