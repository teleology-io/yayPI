package inbox

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/teleology-io/yayPI/internal/config"
	"github.com/teleology-io/yayPI/internal/db"
	"github.com/teleology-io/yayPI/internal/handler"
	"github.com/teleology-io/yayPI/internal/schema"
	"github.com/teleology-io/yayPI/internal/signature"
	"github.com/teleology-io/yayPI/internal/testutil"
	"github.com/teleology-io/yayPI/pkg/sdk"
)

const secret = "whsec_test"

func crmWebhook() config.WebhookDef {
	return config.WebhookDef{
		Name: "crm", Mode: config.WebhookInbound, Path: "/webhooks/crm", Secret: secret,
		DedupeKey: "body.id", Event: "body.type",
		Routes: map[string]config.WebhookRouteDef{
			"contact.upserted": {Entity: "Contact", Op: "upsert", Map: map[string]any{
				"external_id": "{{body.data.id}}", "email": "{{body.data.email}}",
				"name": "{{body.data.first}} {{body.data.last}}", "score": "{{body.data.score}}",
			}},
			"contact.deleted": {Entity: "Contact", Op: "delete", Key: map[string]any{"external_id": "{{body.data.id}}"}},
			"*":               {Handler: "crm.Other"},
		},
	}
}

type env struct {
	t        *testing.T
	dbm      *db.Manager
	worker   *Worker
	handled  []sdk.WebhookEvent
	otherErr error
}

func newEnv(t *testing.T, defs ...config.WebhookDef) *env {
	t.Helper()
	cfg := &config.RootConfig{
		Entities: []*config.EntityConfig{{Entity: config.EntityDef{
			Name: "Contact", Table: "contacts", NaturalKey: []string{"external_id"},
			Fields: []config.FieldDef{
				{Name: "id", Type: "uuid", PrimaryKey: true},
				{Name: "external_id", Type: "string", Unique: true},
				{Name: "email", Type: "string"},
				{Name: "name", Type: "string", Nullable: true},
				{Name: "score", Type: "integer", Nullable: true},
			},
		}}},
		WebhookFiles: []*config.WebhookFileConfig{{Webhooks: defs}},
	}
	if errs := config.Validate(cfg); len(errs) > 0 {
		t.Fatalf("config: %v", errs)
	}
	reg, err := schema.Build(cfg)
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, dbm: testutil.SQLiteDB(t, reg)}
	factory := handler.NewFactory(reg, e.dbm, nil, nil, []byte(strings.Repeat("c", 32)))
	handlers := func(name string) (sdk.WebhookHandlerFunc, bool) {
		if name != "crm.Other" {
			return nil, false
		}
		return func(c sdk.WebhookContext) error {
			e.handled = append(e.handled, c.Event)
			return e.otherErr
		}, true
	}
	if e.worker, err = NewWorker(e.dbm.Default(), defs, Options{Writer: factory, Registry: reg, Handlers: handlers}); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) receiver(def config.WebhookDef) *Receiver {
	e.t.Helper()
	rc, err := NewReceiver(def, e.dbm.Default(), nil, "")
	if err != nil {
		e.t.Fatal(err)
	}
	return rc
}

func post(rc *Receiver, body string, headers map[string]string, target string) *httptest.ResponseRecorder {
	if target == "" {
		target = "/webhooks/x"
	}
	r := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	rr := httptest.NewRecorder()
	rc.ServeHTTP(rr, r)
	return rr
}

func signed(body string) map[string]string {
	s, _ := signature.Resolve(nil, secret)
	return s.Sign(secret, []byte(body), time.Now())
}

func (e *env) scalar(q string, args ...any) string {
	var v string
	_ = e.dbm.Default().SQL.QueryRow(q, args...).Scan(&v)
	return v
}

func TestSignedEventUpsertsAndDedupes(t *testing.T) {
	def := crmWebhook()
	e := newEnv(t, def)
	rc := e.receiver(def)

	body := `{"id":"evt_1","type":"contact.upserted","data":{"id":123,"email":"ann@x.io","first":"Ann","last":"Lee","score":"7"}}`
	if rr := post(rc, body, signed(body), ""); rr.Code != 200 || !strings.Contains(rr.Body.String(), "accepted") {
		t.Fatalf("first delivery: %d %s", rr.Code, rr.Body)
	}
	if rr := post(rc, body, signed(body), ""); rr.Code != 200 || !strings.Contains(rr.Body.String(), "duplicate") {
		t.Fatalf("repeat delivery: %d %s", rr.Code, rr.Body)
	}
	e.worker.RunOnce(context.Background())

	// The numeric id lands in a string column; "{{first}} {{last}}" renders as text;
	// the string "7" lands in an integer column.
	if got := e.scalar(`SELECT email || '|' || name || '|' || score FROM contacts WHERE external_id = '123'`); got != "ann@x.io|Ann Lee|7" {
		t.Fatalf("contact = %q", got)
	}
	if st := e.scalar(`SELECT status FROM yaypi_inbox WHERE dedupe_key = 'evt_1'`); st != "done" {
		t.Fatalf("inbox status = %q", st)
	}

	del := `{"id":"evt_2","type":"contact.deleted","data":{"id":"123"}}`
	post(rc, del, signed(del), "")
	e.worker.RunOnce(context.Background())
	if n := e.scalar(`SELECT COUNT(*) FROM contacts`); n != "0" {
		t.Fatalf("contact not deleted (rows=%s)", n)
	}
}

func TestBadSignatureRejected(t *testing.T) {
	def := crmWebhook()
	e := newEnv(t, def)
	rc := e.receiver(def)
	body := `{"id":"evt_1","type":"contact.upserted","data":{}}`
	for name, h := range map[string]map[string]string{
		"none":     nil,
		"tampered": signed(`{"id":"evt_1","type":"other"}`),
	} {
		if rr := post(rc, body, h, ""); rr.Code != http.StatusUnauthorized {
			t.Errorf("%s: got %d, want 401", name, rr.Code)
		}
	}
	if n := e.scalar(`SELECT COUNT(*) FROM yaypi_inbox`); n != "0" {
		t.Fatalf("rejected requests were stored: %s", n)
	}
}

func TestUnsignedReceiverGuards(t *testing.T) {
	def := config.WebhookDef{
		Name: "feed", Mode: config.WebhookInbound, Path: "/webhooks/feed", DedupeKey: "body.id",
		Token:      &config.WebhookTokenDef{Header: "X-Feed-Token", Value: "t0ken"},
		AllowedIPs: []string{"192.0.2.0/24"}, // httptest's RemoteAddr
		Handler:    "crm.Other",
	}
	e := newEnv(t, def)
	rc := e.receiver(def)
	body := `{"id":"f1"}`
	if rr := post(rc, body, nil, ""); rr.Code != http.StatusUnauthorized {
		t.Fatalf("missing token: %d", rr.Code)
	}
	if rr := post(rc, body, map[string]string{"X-Feed-Token": "wrong"}, ""); rr.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token: %d", rr.Code)
	}
	if rr := post(rc, `{"no":"id"}`, map[string]string{"X-Feed-Token": "t0ken"}, ""); rr.Code != http.StatusBadRequest {
		t.Fatalf("missing dedupe key: %d", rr.Code)
	}
	if rr := post(rc, body, map[string]string{"X-Feed-Token": "t0ken", "Authorization": "Bearer abc"}, ""); rr.Code != 200 {
		t.Fatalf("valid token: %d %s", rr.Code, rr.Body)
	}
	if p := e.scalar(`SELECT payload FROM yaypi_inbox`); strings.Contains(p, "t0ken") || strings.Contains(p, "Bearer") {
		t.Fatalf("credentials stored in the inbox: %s", p)
	}

	def.AllowedIPs = []string{"10.0.0.0/8"}
	if rr := post(e.receiver(def), body, map[string]string{"X-Feed-Token": "t0ken"}, ""); rr.Code != http.StatusForbidden {
		t.Fatalf("ip outside allowed_ips: %d", rr.Code)
	}
}

func TestChallenges(t *testing.T) {
	get := crmWebhook()
	get.Challenge = &config.WebhookChallengeDef{QueryParam: "challenge"}
	e := newEnv(t, get)
	rr := httptest.NewRecorder()
	e.receiver(get).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/webhooks/crm?challenge=abc123", nil))
	if rr.Code != 200 || rr.Body.String() != "abc123" || !strings.HasPrefix(rr.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("GET challenge: %d %q %s", rr.Code, rr.Body, rr.Header().Get("Content-Type"))
	}

	postDef := crmWebhook()
	postDef.Challenge = &config.WebhookChallengeDef{Method: "POST", BodyPath: "body.challenge", When: "url_verification"}
	body := `{"type":"url_verification","challenge":"xyz"}`
	if rr := post(e.receiver(postDef), body, signed(body), ""); rr.Code != 200 || rr.Body.String() != "xyz" {
		t.Fatalf("POST challenge: %d %q", rr.Code, rr.Body)
	}
}

func TestHandlerRouteRetriesAndPermanentFailures(t *testing.T) {
	def := crmWebhook()
	e := newEnv(t, def)
	rc := e.receiver(def)

	body := `{"id":"evt_9","type":"invoice.paid"}` // no route of its own → "*" handler
	post(rc, body, signed(body), "")
	e.otherErr = errors.New("downstream unavailable")
	e.worker.RunOnce(context.Background())
	if len(e.handled) != 1 || e.handled[0].Event != "invoice.paid" || e.handled[0].Attempt != 1 {
		t.Fatalf("handler calls: %+v", e.handled)
	}
	if st := e.scalar(`SELECT status || ':' || attempts FROM yaypi_inbox`); st != "pending:1" {
		t.Fatalf("after a transient failure: %s", st)
	}

	// Make it due again, then fail permanently.
	_, _ = e.dbm.Default().SQL.Exec(`UPDATE yaypi_inbox SET next_attempt_at = 0`)
	e.otherErr = sdk.Permanent(errors.New("bad payload"))
	e.worker.RunOnce(context.Background())
	if st := e.scalar(`SELECT status FROM yaypi_inbox`); st != "dead" {
		t.Fatalf("after a permanent failure: %s", st)
	}
}

func TestMissingPluginHandlerFailsAtBoot(t *testing.T) {
	def := crmWebhook()
	def.Routes["*"] = config.WebhookRouteDef{Handler: "nope.Missing"}
	cfg := &config.RootConfig{}
	reg, _ := schema.Build(cfg)
	if _, err := NewWorker(nil, []config.WebhookDef{def}, Options{Registry: reg}); err == nil {
		t.Fatal("expected an error for an unregistered handler")
	}
}
