package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/teleology-io/yayPI/internal/config"
	"github.com/teleology-io/yayPI/internal/db"
	"github.com/teleology-io/yayPI/internal/outbox"
	"github.com/teleology-io/yayPI/internal/schema"
	"github.com/teleology-io/yayPI/internal/testutil"
	"github.com/teleology-io/yayPI/internal/token"
)

type fakeMailer struct {
	sent chan string
}

func (m *fakeMailer) Send(_ context.Context, to, subject, body string) error {
	m.sent <- body
	return nil
}

type harness struct {
	t      *testing.T
	srv    http.Handler
	dbm    *db.Manager
	mailer *fakeMailer
	worker *outbox.Worker
}

func newHarness(t *testing.T, mutate func(*config.AuthEndpointDef)) *harness {
	t.Helper()
	f := false
	ae := config.AuthEndpointDef{
		Register:          &config.RegisterDef{Enabled: true, DefaultRole: "member"},
		Login:             &config.LoginDef{Enabled: true, MaxAttempts: 3},
		Me:                &config.MeDef{Enabled: true},
		Refresh:           &config.RefreshDef{Enabled: true, Store: "body"},
		Cookie:            &config.CookieDef{Secure: &f},
		PasswordReset:     &config.PasswordResetDef{Enabled: true, ResetURL: "https://app/reset?token={{token}}"},
		EmailVerification: &config.EmailVerificationDef{Enabled: true, VerifyURL: "https://app/verify?token={{token}}"},
	}
	if mutate != nil {
		mutate(&ae)
	}
	reg, err := schema.Build(&config.RootConfig{AuthEndpoint: &config.AuthEndpointFileConfig{Auth: ae}})
	if err != nil {
		t.Fatal(err)
	}
	dbm := testutil.SQLiteDB(t, reg)
	keys, err := token.New(token.Config{Secret: []byte(strings.Repeat("k", 32)), Issuer: "test", AccessTTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	m := &fakeMailer{sent: make(chan string, 10)}
	h := New(Options{Config: ae, Registry: reg, DB: dbm, Tokens: keys, Mailer: m, RevocationCheck: true})
	r := chi.NewRouter()
	h.Mount(r)
	w := outbox.NewWorker(dbm.Default(), nil, nil, m, outbox.WorkerOptions{})
	return &harness{t: t, srv: r, dbm: dbm, mailer: m, worker: w}
}

func (h *harness) do(method, path string, body any, bearer string, cookies ...*http.Cookie) (*httptest.ResponseRecorder, map[string]any) {
	h.t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rr := httptest.NewRecorder()
	h.srv.ServeHTTP(rr, req)
	var out map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	return rr, out
}

func (h *harness) register(email, password string, extra map[string]any) map[string]any {
	h.t.Helper()
	body := map[string]any{"email": email, "password": password}
	for k, v := range extra {
		body[k] = v
	}
	rr, out := h.do("POST", "/auth/register", body, "")
	if rr.Code != http.StatusCreated {
		h.t.Fatalf("register: %d %s", rr.Code, rr.Body)
	}
	return out
}

// waitMail runs the outbox worker until an email is delivered (auth emails are queued).
func (h *harness) waitMail() string {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		h.worker.RunOnce(context.Background())
		select {
		case b := <-h.mailer.sent:
			return b
		case <-time.After(20 * time.Millisecond):
		}
	}
	h.t.Fatal("no email sent")
	return ""
}

var tokenInLink = regexp.MustCompile(`token=([A-Za-z0-9_-]+)`)

func TestRegisterIgnoresRoleAndReservedFields(t *testing.T) {
	h := newHarness(t, nil)
	out := h.register("a@example.com", "password1", map[string]any{
		"role": "admin", "id": "attacker-chosen", "email_verified_at": "2020-01-01", "token_version": 99,
	})
	h.waitMail() // verification email
	user := out["user"].(map[string]any)
	if user["role"] != "member" {
		t.Fatalf("role = %v, want member", user["role"])
	}
	if user["id"] == "attacker-chosen" || user["email_verified_at"] != nil {
		t.Fatalf("reserved fields were settable: %v", user)
	}
}

func TestRegisterLoginCaseInsensitive(t *testing.T) {
	h := newHarness(t, nil)
	h.register("  Mixed@Example.COM ", "password1", nil)
	rr, out := h.do("POST", "/auth/login", map[string]any{"email": "mixed@example.com", "password": "password1"}, "")
	if rr.Code != http.StatusOK || out["token"] == nil || out["refresh_token"] == nil {
		t.Fatalf("login: %d %s", rr.Code, rr.Body)
	}
}

func TestPasswordLengthLimits(t *testing.T) {
	h := newHarness(t, nil)
	for _, pw := range []string{"short", strings.Repeat("x", 73)} {
		rr, _ := h.do("POST", "/auth/register", map[string]any{"email": "p@example.com", "password": pw}, "")
		if rr.Code != http.StatusBadRequest {
			t.Errorf("password len %d: got %d", len(pw), rr.Code)
		}
	}
}

func TestLoginThrottle(t *testing.T) {
	h := newHarness(t, nil)
	h.register("t@example.com", "password1", nil)
	for i := 0; i < 3; i++ {
		rr, _ := h.do("POST", "/auth/login", map[string]any{"email": "t@example.com", "password": "wrong-pass"}, "")
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d", i, rr.Code)
		}
	}
	// Even the right password is refused while locked out.
	rr, _ := h.do("POST", "/auth/login", map[string]any{"email": "t@example.com", "password": "password1"}, "")
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", rr.Code)
	}
}

func TestRefreshRotationAndReuseDetection(t *testing.T) {
	h := newHarness(t, nil)
	out := h.register("r@example.com", "password1", nil)
	rt1 := out["refresh_token"].(string)

	rr, out2 := h.do("POST", "/auth/refresh", map[string]any{"refresh_token": rt1}, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("refresh: %d %s", rr.Code, rr.Body)
	}
	rt2 := out2["refresh_token"].(string)
	if rt2 == rt1 {
		t.Fatal("refresh token not rotated")
	}

	// Replaying the consumed token is reuse: rejected, and the whole family dies.
	if rr, _ := h.do("POST", "/auth/refresh", map[string]any{"refresh_token": rt1}, ""); rr.Code != http.StatusUnauthorized {
		t.Fatalf("reused token: %d", rr.Code)
	}
	if rr, _ := h.do("POST", "/auth/refresh", map[string]any{"refresh_token": rt2}, ""); rr.Code != http.StatusUnauthorized {
		t.Fatalf("family not revoked after reuse: %d", rr.Code)
	}
}

func TestLogoutRevokesRefreshToken(t *testing.T) {
	h := newHarness(t, nil)
	out := h.register("l@example.com", "password1", nil)
	rt := out["refresh_token"].(string)
	if rr, _ := h.do("POST", "/auth/logout", map[string]any{"refresh_token": rt}, ""); rr.Code != http.StatusNoContent {
		t.Fatalf("logout: %d", rr.Code)
	}
	if rr, _ := h.do("POST", "/auth/refresh", map[string]any{"refresh_token": rt}, ""); rr.Code != http.StatusUnauthorized {
		t.Fatalf("refresh after logout: %d", rr.Code)
	}
}

func TestLogoutAllRevokesAccessTokens(t *testing.T) {
	h := newHarness(t, nil)
	out := h.register("la@example.com", "password1", nil)
	access := out["token"].(string)
	if rr, _ := h.do("GET", "/auth/me", nil, access); rr.Code != http.StatusOK {
		t.Fatalf("me: %d", rr.Code)
	}
	if rr, _ := h.do("POST", "/auth/logout-all", nil, access); rr.Code != http.StatusNoContent {
		t.Fatalf("logout-all: %d", rr.Code)
	}
	if rr, _ := h.do("GET", "/auth/me", nil, access); rr.Code != http.StatusUnauthorized {
		t.Fatalf("access token still valid after logout-all: %d", rr.Code)
	}
}

func TestPasswordResetFlow(t *testing.T) {
	h := newHarness(t, nil)
	out := h.register("reset@example.com", "password1", nil)
	h.waitMail() // verification email
	oldAccess := out["token"].(string)

	if rr, _ := h.do("POST", "/auth/password/forgot", map[string]any{"email": "nobody@example.com"}, ""); rr.Code != http.StatusAccepted {
		t.Fatalf("forgot unknown: %d", rr.Code)
	}
	if rr, _ := h.do("POST", "/auth/password/forgot", map[string]any{"email": "Reset@Example.com"}, ""); rr.Code != http.StatusAccepted {
		t.Fatalf("forgot: %d", rr.Code)
	}
	m := tokenInLink.FindStringSubmatch(h.waitMail())
	if m == nil {
		t.Fatal("no token in reset email")
	}

	if rr, _ := h.do("POST", "/auth/password/reset", map[string]any{"token": m[1], "password": "newpassword1"}, ""); rr.Code != http.StatusOK {
		t.Fatalf("reset: %d %s", rr.Code, rr.Body)
	}
	// Single use.
	if rr, _ := h.do("POST", "/auth/password/reset", map[string]any{"token": m[1], "password": "another-pass"}, ""); rr.Code != http.StatusBadRequest {
		t.Fatalf("token reused: %d", rr.Code)
	}
	if rr, _ := h.do("POST", "/auth/login", map[string]any{"email": "reset@example.com", "password": "newpassword1"}, ""); rr.Code != http.StatusOK {
		t.Fatalf("login with new password: %d", rr.Code)
	}
	if rr, _ := h.do("GET", "/auth/me", nil, oldAccess); rr.Code != http.StatusUnauthorized {
		t.Fatalf("pre-reset access token should be revoked: %d", rr.Code)
	}
}

func TestEmailVerificationRequired(t *testing.T) {
	h := newHarness(t, func(ae *config.AuthEndpointDef) { ae.EmailVerification.Required = true })
	h.register("v@example.com", "password1", nil)
	link := tokenInLink.FindStringSubmatch(h.waitMail())

	if rr, _ := h.do("POST", "/auth/login", map[string]any{"email": "v@example.com", "password": "password1"}, ""); rr.Code != http.StatusForbidden {
		t.Fatalf("unverified login: %d", rr.Code)
	}
	if rr, _ := h.do("POST", "/auth/verify-email", map[string]any{"token": link[1]}, ""); rr.Code != http.StatusOK {
		t.Fatalf("verify: %d %s", rr.Code, rr.Body)
	}
	if rr, _ := h.do("POST", "/auth/login", map[string]any{"email": "v@example.com", "password": "password1"}, ""); rr.Code != http.StatusOK {
		t.Fatalf("verified login: %d", rr.Code)
	}
}

// ── OAuth ─────────────────────────────────────────────────────────────────────

type fakeProvider struct {
	*httptest.Server
	verified bool
	email    string
	sub      string
}

func newFakeProvider(t *testing.T) *fakeProvider {
	p := &fakeProvider{verified: true, email: "o@example.com", sub: "provider-user-1"}
	var challenge string
	mux := http.NewServeMux()
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		challenge = r.URL.Query().Get("code_challenge")
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		if base64.RawURLEncoding.EncodeToString(sum[:]) != challenge {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at"})
	})
	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"sub": p.sub, "email": p.email, "email_verified": p.verified})
	})
	p.Server = httptest.NewServer(mux)
	t.Cleanup(p.Close)
	return p
}

func oauthHarness(t *testing.T, p *fakeProvider) *harness {
	return newHarness(t, func(ae *config.AuthEndpointDef) {
		ae.OAuth2 = &config.OAuth2Def{Providers: []config.OAuth2ProviderDef{{
			Name: "corp", ClientID: "cid", RedirectURI: "http://api/auth/callback/corp",
			AuthURL: p.URL + "/authorize", TokenURL: p.URL + "/token", UserInfoURL: p.URL + "/userinfo",
			SuccessRedirect: "https://app/done",
		}}}
	})
}

// oauthLogin runs initiate → (simulated provider consent) → callback and returns the response.
func (h *harness) oauthLogin(p *fakeProvider, tamperState bool) *httptest.ResponseRecorder {
	h.t.Helper()
	rr, _ := h.do("GET", "/auth/corp", nil, "")
	loc, _ := url.Parse(rr.Header().Get("Location"))
	// Let the fake provider observe the PKCE challenge.
	resp, err := http.Get(p.URL + "/authorize?" + loc.RawQuery)
	if err != nil {
		h.t.Fatal(err)
	}
	resp.Body.Close()
	state := loc.Query().Get("state")
	if tamperState {
		state = "forged"
	}
	cookies := rr.Result().Cookies()
	rr2, _ := h.do("GET", "/auth/callback/corp?code=c&state="+state, nil, "", cookies...)
	return rr2
}

func TestOAuthPKCEAndFragmentDelivery(t *testing.T) {
	p := newFakeProvider(t)
	h := oauthHarness(t, p)
	rr := h.oauthLogin(p, false)
	loc := rr.Header().Get("Location")
	if rr.Code != http.StatusFound || !strings.HasPrefix(loc, "https://app/done#") || !strings.Contains(loc, "token=") {
		t.Fatalf("callback: %d %s %s", rr.Code, loc, rr.Body)
	}
}

func TestOAuthRejectsForgedState(t *testing.T) {
	p := newFakeProvider(t)
	h := oauthHarness(t, p)
	rr := h.oauthLogin(p, true)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("forged state accepted: %d %s", rr.Code, rr.Header().Get("Location"))
	}
}

func TestOAuthUnverifiedEmailCannotTakeOverAccount(t *testing.T) {
	p := newFakeProvider(t)
	h := oauthHarness(t, p)
	h.register("victim@example.com", "password1", nil)
	h.waitMail()

	p.email, p.verified = "victim@example.com", false
	rr := h.oauthLogin(p, false)
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "verified") {
		t.Fatalf("unverified email linked: %d %s", rr.Code, rr.Body)
	}
}
