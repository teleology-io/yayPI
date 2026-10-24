// Package inbox receives inbound webhooks (`kind: webhooks`, `mode: inbound`) and
// processes them reliably — the mirror image of the outbox.
//
// A Receiver verifies each request (signature, or a token / IP / auth guard), answers
// provider handshakes, drops repeats by dedupe key and stores the request in the
// yaypi_inbox table before acknowledging it. A Worker then routes stored events — to an
// entity write (create/update/upsert/delete through the same path as the CRUD API) or a
// plugin handler — retrying failures with backoff across replicas.
package inbox

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/teleology-io/yayPI/internal/apierr"
	"github.com/teleology-io/yayPI/internal/config"
	"github.com/teleology-io/yayPI/internal/db"
	"github.com/teleology-io/yayPI/internal/middleware"
	"github.com/teleology-io/yayPI/internal/schema"
	"github.com/teleology-io/yayPI/internal/signature"
	"github.com/teleology-io/yayPI/pkg/sdk"
)

// Receive outcomes (metrics label).
const (
	ResultAccepted  = "accepted"
	ResultDuplicate = "duplicate"
	ResultRejected  = "rejected"
	ResultChallenge = "challenge"
	ResultError     = "error"
)

// maxKeyLen is the dedupe_key column width; longer keys are stored as their SHA-256.
const maxKeyLen = 191

// alwaysStripped are credential headers never written to the inbox.
var alwaysStripped = []string{"Authorization", "Cookie", "Proxy-Authorization", "X-Api-Key"}

// storedRequest is the inbox payload column.
type storedRequest struct {
	Body        string              `json:"body"`
	ContentType string              `json:"content_type,omitempty"`
	Headers     map[string][]string `json:"headers,omitempty"`
	Query       map[string][]string `json:"query,omitempty"`
}

// Receiver is the http.Handler for one inbound webhook.
type Receiver struct {
	def      config.WebhookDef
	scheme   *signature.Scheme // nil = unsigned (guarded)
	verifier sdk.WebhookVerifyFunc
	db       *db.DB
	allowed  []*net.IPNet
	strip    []string
	now      func() time.Time

	// OnReceive, when set, observes every request (metrics).
	OnReceive func(webhook, result string)
}

// NewReceiver compiles an inbound webhook. verifiers resolves `signature.scheme: plugin`
// handlers; apiKeyHeader is stripped from stored headers.
func NewReceiver(def config.WebhookDef, d *db.DB, verifiers func(string) (sdk.WebhookVerifyFunc, bool), apiKeyHeader string) (*Receiver, error) {
	scheme, err := signature.Resolve(def.Signature, def.Secret)
	if err != nil {
		return nil, fmt.Errorf("webhook %q: %w", def.Name, err)
	}
	rc := &Receiver{def: def, scheme: scheme, db: d, now: time.Now}
	if scheme.IsPlugin() {
		var ok bool
		if verifiers != nil {
			rc.verifier, ok = verifiers(scheme.Handler)
		}
		if !ok {
			return nil, fmt.Errorf("webhook %q: no signature verifier %q is registered", def.Name, scheme.Handler)
		}
	}
	for _, s := range def.AllowedIPs {
		s = strings.TrimSpace(s)
		if !strings.Contains(s, "/") {
			if ip := net.ParseIP(s); ip != nil && ip.To4() != nil {
				s += "/32"
			} else {
				s += "/128"
			}
		}
		_, n, err := net.ParseCIDR(s)
		if err != nil {
			return nil, fmt.Errorf("webhook %q: allowed_ips: %w", def.Name, err)
		}
		rc.allowed = append(rc.allowed, n)
	}
	rc.strip = append(rc.strip, alwaysStripped...)
	if apiKeyHeader != "" {
		rc.strip = append(rc.strip, apiKeyHeader)
	}
	if def.Token != nil && def.Token.Header != "" {
		rc.strip = append(rc.strip, def.Token.Header)
	}
	return rc, nil
}

// Def returns the webhook definition.
func (rc *Receiver) Def() config.WebhookDef { return rc.def }

// HasGetChallenge reports whether the receiver answers GET handshakes.
func (rc *Receiver) HasGetChallenge() bool {
	c := rc.def.Challenge
	return c != nil && (c.Method == "" || strings.EqualFold(c.Method, http.MethodGet))
}

func (rc *Receiver) observe(result string) {
	if rc.OnReceive != nil {
		rc.OnReceive(rc.def.Name, result)
	}
}

func (rc *Receiver) reject(w http.ResponseWriter, r *http.Request, status int, msg, reason string) {
	log.Warn().Str("webhook", rc.def.Name).Str("request_id", middleware.GetRequestID(r)).
		Str("client_ip", middleware.GetClientIP(r)).Str("reason", reason).Msg("inbound webhook rejected")
	rc.observe(ResultRejected)
	apierr.Write(w, middleware.GetRequestID(r), status, msg)
}

func (rc *Receiver) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !rc.ipAllowed(r) {
		rc.reject(w, r, http.StatusForbidden, "forbidden", "client ip not in allowed_ips")
		return
	}
	if r.Method == http.MethodGet {
		if !rc.HasGetChallenge() {
			apierr.Write(w, middleware.GetRequestID(r), http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		// Handshakes are answered without a signature (providers send them unsigned) but
		// only echo the provider's own value back, as text/plain.
		v := r.URL.Query().Get(rc.def.Challenge.QueryParam)
		if v == "" {
			apierr.Write(w, middleware.GetRequestID(r), http.StatusBadRequest, "missing "+rc.def.Challenge.QueryParam)
			return
		}
		rc.observe(ResultChallenge)
		writeText(w, v)
		return
	}
	if !rc.tokenOK(r) {
		rc.reject(w, r, http.StatusUnauthorized, "invalid webhook token", "token mismatch")
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		if middleware.IsBodyTooLarge(err) {
			apierr.Write(w, middleware.GetRequestID(r), http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		apierr.Write(w, middleware.GetRequestID(r), http.StatusBadRequest, "could not read body")
		return
	}
	switch {
	case rc.verifier != nil:
		if err := rc.verifier(r, body, rc.def.Secret); err != nil {
			rc.reject(w, r, http.StatusUnauthorized, "invalid webhook signature", err.Error())
			return
		}
	case rc.scheme != nil:
		if err := rc.scheme.Verify(rc.def.Secret, r.Header, body, rc.now()); err != nil {
			rc.reject(w, r, http.StatusUnauthorized, "invalid webhook signature", err.Error())
			return
		}
	}

	sc := newScope(body, r.Header.Get("Content-Type"), r.Header, r.URL.Query())
	event := sc.text(rc.def.Event)
	if c := rc.def.Challenge; c != nil && strings.EqualFold(c.Method, http.MethodPost) && event == c.When {
		rc.observe(ResultChallenge)
		writeText(w, sc.text(c.BodyPath))
		return
	}
	key := uuid.NewString()
	if rc.def.DedupeKey != "" {
		if key = sc.text(rc.def.DedupeKey); key == "" {
			rc.reject(w, r, http.StatusBadRequest, "missing "+rc.def.DedupeKey, "no dedupe key")
			return
		}
	}
	if len(key) > maxKeyLen {
		sum := sha256.Sum256([]byte(key))
		key = "sha256:" + hex.EncodeToString(sum[:])
	}
	if len(event) > maxKeyLen {
		event = event[:maxKeyLen]
	}

	dup, err := rc.store(r.Context(), key, event, body, r)
	switch {
	case err != nil:
		log.Error().Err(err).Str("webhook", rc.def.Name).Str("request_id", middleware.GetRequestID(r)).Msg("inbound webhook: storing failed")
		rc.observe(ResultError)
		apierr.Write(w, middleware.GetRequestID(r), http.StatusServiceUnavailable, "could not accept webhook; retry")
	case dup:
		rc.observe(ResultDuplicate)
		writeStatus(w, "duplicate")
	default:
		rc.observe(ResultAccepted)
		writeStatus(w, "accepted")
	}
}

func (rc *Receiver) ipAllowed(r *http.Request) bool {
	if len(rc.allowed) == 0 {
		return true
	}
	ip := net.ParseIP(middleware.GetClientIP(r))
	for _, n := range rc.allowed {
		if ip != nil && n.Contains(ip) {
			return true
		}
	}
	return false
}

func (rc *Receiver) tokenOK(r *http.Request) bool {
	t := rc.def.Token
	if t == nil {
		return true
	}
	got := r.Header.Get(t.Header)
	if t.QueryParam != "" {
		got = r.URL.Query().Get(t.QueryParam)
	}
	return got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(t.Value)) == 1
}

// store inserts the request; dup is true when (webhook, dedupe_key) already exists.
func (rc *Receiver) store(ctx context.Context, key, event string, body []byte, r *http.Request) (dup bool, err error) {
	headers := r.Header.Clone()
	for _, h := range rc.strip {
		headers.Del(h)
	}
	query := r.URL.Query()
	if t := rc.def.Token; t != nil && t.QueryParam != "" {
		query.Del(t.QueryParam)
	}
	payload, _ := json.Marshal(storedRequest{Body: string(body), ContentType: r.Header.Get("Content-Type"), Headers: headers, Query: query})
	d := rc.db.Dialect
	now := rc.now().Unix()
	_, err = rc.db.SQL.ExecContext(ctx, d.Rebind(fmt.Sprintf(
		`INSERT INTO %s (id, webhook, dedupe_key, event, payload, status, attempts, next_attempt_at, locked_until, created_at) VALUES ($1, $2, $3, $4, $5, 'pending', 0, $6, 0, $7)`,
		d.QuoteIdent(schema.InboxTable))),
		uuid.NewString(), rc.def.Name, key, nullable(event), string(payload), now, now)
	if err != nil && d.IsUniqueViolation(err) {
		return true, nil
	}
	return false, err
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func writeText(w http.ResponseWriter, v string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, v)
}

func writeStatus(w http.ResponseWriter, status string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, `{"status":"`+status+`"}`)
}
