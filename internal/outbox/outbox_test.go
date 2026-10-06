package outbox

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/teleology-io/yayPI/internal/config"
	"github.com/teleology-io/yayPI/internal/db"
	"github.com/teleology-io/yayPI/internal/handler"
	"github.com/teleology-io/yayPI/internal/schema"
	"github.com/teleology-io/yayPI/internal/testutil"
)

func TestJSONEscaperPreventsInjection(t *testing.T) {
	tmpl := `{"title": "{{record.title}}", "n": {{record.n}}}`
	out := Render(tmpl, map[string]any{"title": `x", "admin": true, "y": "`, "n": int64(3)}, JSONEscaper)
	var parsed map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("invalid JSON %q: %v", out, err)
	}
	if _, injected := parsed["admin"]; injected || len(parsed) != 2 {
		t.Fatalf("payload injection: %v", parsed)
	}
}

func TestConditions(t *testing.T) {
	cases := []struct {
		expr string
		rec  map[string]any
		want bool
	}{
		{`record.status == "published"`, map[string]any{"status": "published"}, true},
		{`record.status != "published"`, map[string]any{"status": "draft"}, true},
		{`record.token != ""`, map[string]any{"token": nil}, false},
		{`record.deleted_at == null`, map[string]any{"deleted_at": nil}, true},
		{`record.score >= 10 and record.status == "x"`, map[string]any{"score": int64(12), "status": "x"}, true},
		{`record.score >= 10`, map[string]any{"score": "9"}, false},
	}
	for _, c := range cases {
		cond, err := CompileCondition(c.expr)
		if err != nil {
			t.Fatalf("%s: %v", c.expr, err)
		}
		if got := cond(c.rec); got != c.want {
			t.Errorf("%s on %v = %v, want %v", c.expr, c.rec, got, c.want)
		}
	}
	if _, err := CompileCondition("record.status ~= 'x'"); err == nil {
		t.Error("unsupported operator accepted")
	}
}

type outboxEnv struct {
	dbm  *db.Manager
	obs  *Observer
	post *schema.Entity
}

func newOutboxEnv(t *testing.T, defs []config.WebhookDef) *outboxEnv {
	reg := schema.NewRegistry()
	reg.RegisterEntity(schema.NewOutboxEntity())
	post := &schema.Entity{Name: "Post", Table: "posts"}
	dbm := testutil.SQLiteDB(t, reg)
	obs, err := NewObserver(defs, nil)
	if err != nil {
		t.Fatal(err)
	}
	return &outboxEnv{dbm: dbm, obs: obs, post: post}
}

func (e *outboxEnv) write(t *testing.T, record map[string]any) {
	d := e.dbm.Default()
	tx, err := d.SQL.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := e.obs.OnWrite(context.Background(), tx, d.Dialect, handler.WriteEvent{Entity: e.post, Action: "create", ID: "p1", After: record}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func status(t *testing.T, sqlDB *sql.DB) (string, int) {
	var s string
	var n int
	if err := sqlDB.QueryRow(`SELECT status, attempts FROM yaypi_outbox`).Scan(&s, &n); err != nil {
		t.Fatal(err)
	}
	return s, n
}

func TestWebhookDeliveryRetryAndSignature(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	var gotSig, gotTS, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		b, _ := io.ReadAll(r.Body)
		gotBody, gotSig, gotTS = string(b), r.Header.Get("X-Yaypi-Signature"), r.Header.Get("X-Yaypi-Timestamp")
	}))
	defer srv.Close()

	def := config.WebhookDef{
		Name: "notify", Entity: "Post", Trigger: "after_create", Condition: `record.status == "published"`,
		URL: srv.URL + "/hook", Secret: "s3cret", AllowPrivateNetwork: true,
		Retry: &config.RetryConfig{InitialDelay: "1ns", MaxDelay: "1ns"},
	}
	e := newOutboxEnv(t, []config.WebhookDef{def})
	e.write(t, map[string]any{"id": "p0", "status": "draft"}) // condition false: nothing queued
	e.write(t, map[string]any{"id": "p1", "status": "published"})

	w := NewWorker(e.dbm.Default(), []config.WebhookDef{def}, nil, nil, WorkerOptions{})
	sqlDB := e.dbm.Default().SQL
	w.RunOnce(context.Background())
	if s, n := status(t, sqlDB); s != "pending" || n != 1 {
		t.Fatalf("after failure: %s/%d", s, n)
	}
	// Make the retry due now.
	if _, err := sqlDB.Exec(`UPDATE yaypi_outbox SET next_attempt_at = 0`); err != nil {
		t.Fatal(err)
	}
	w.RunOnce(context.Background())
	if s, n := status(t, sqlDB); s != "done" || n != 2 {
		t.Fatalf("after retry: %s/%d", s, n)
	}
	if gotSig != "v1="+Sign("s3cret", gotTS, gotBody) {
		t.Fatalf("bad signature %q", gotSig)
	}
	var env map[string]any
	if err := json.Unmarshal([]byte(gotBody), &env); err != nil || env["event"] != "Post.created" {
		t.Fatalf("default envelope wrong: %s", gotBody)
	}
}

func TestWebhookBlocksPrivateTargetsByDefault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("request reached a loopback target")
	}))
	defer srv.Close()
	def := config.WebhookDef{Name: "x", Entity: "Post", Trigger: "after_create", URL: srv.URL, Retry: &config.RetryConfig{MaxAttempts: 1}}
	e := newOutboxEnv(t, []config.WebhookDef{def})
	e.write(t, map[string]any{"id": "p1"})
	NewWorker(e.dbm.Default(), []config.WebhookDef{def}, nil, nil, WorkerOptions{}).RunOnce(context.Background())
	if s, _ := status(t, e.dbm.Default().SQL); s != "dead" {
		t.Fatalf("status %s", s)
	}
}
