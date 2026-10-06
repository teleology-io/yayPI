package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/teleology-io/yayPI/internal/config"
	"github.com/teleology-io/yayPI/internal/db"
	"github.com/teleology-io/yayPI/internal/schema"
)

const (
	oauthCookiePrefix = "yaypi_oauth_"
	oauthStateTTL     = 10 * time.Minute
	maxProviderBody   = 1 << 20
)

var errEmailUnverified = errors.New("the provider did not confirm this email address is verified")

func newOAuthClient() *http.Client {
	return &http.Client{Timeout: 10 * time.Second}
}

func randomURLToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func pkceEnabled(p config.OAuth2ProviderDef) bool {
	return p.PKCE == nil || *p.PKCE
}

// oauthInitiate redirects to the provider. The state value is bound to this browser via
// a short-lived HttpOnly cookie (defeats login CSRF), and a PKCE verifier is kept in the
// same cookie so an intercepted authorization code is useless on its own.
func (h *Handler) oauthInitiate(p config.OAuth2ProviderDef) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		authURL := resolveProviderURL(p, "auth")
		if authURL == "" {
			writeError(w, http.StatusInternalServerError, "provider not configured")
			return
		}
		state, err := randomURLToken(24)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not generate state")
			return
		}
		verifier := ""
		if pkceEnabled(p) {
			if verifier, err = randomURLToken(32); err != nil {
				writeError(w, http.StatusInternalServerError, "could not generate state")
				return
			}
		}

		scopes := p.Scopes
		if len(scopes) == 0 {
			scopes = defaultScopes(p.Name)
		}
		params := url.Values{
			"client_id":     {p.ClientID},
			"redirect_uri":  {p.RedirectURI},
			"response_type": {"code"},
			"scope":         {strings.Join(scopes, " ")},
			"state":         {state},
		}
		if verifier != "" {
			sum := sha256.Sum256([]byte(verifier))
			params.Set("code_challenge", base64.RawURLEncoding.EncodeToString(sum[:]))
			params.Set("code_challenge_method", "S256")
		}

		// The callback is a top-level cross-site navigation from the provider, so this
		// cookie must be SameSite=Lax regardless of the configured auth cookie policy.
		cookieVal := state + "." + verifier + "." + strconv.FormatInt(time.Now().Unix(), 10)
		c := &http.Cookie{
			Name:     oauthCookiePrefix + p.Name,
			Value:    cookieVal,
			Path:     strings.TrimRight(h.mountPrefix, "/") + h.basePath(),
			HttpOnly: true,
			Secure:   h.cookieSecure(),
			SameSite: http.SameSiteLaxMode,
			MaxAge:   int(oauthStateTTL.Seconds()),
		}
		if h.cfg.Cookie != nil {
			c.Domain = h.cfg.Cookie.Domain
		}
		http.SetCookie(w, c)
		http.Redirect(w, r, authURL+"?"+params.Encode(), http.StatusFound)
	}
}

// checkOAuthState validates the callback state against the browser-bound cookie and
// returns the PKCE verifier.
func (h *Handler) checkOAuthState(r *http.Request, p config.OAuth2ProviderDef) (string, error) {
	c, err := r.Cookie(oauthCookiePrefix + p.Name)
	if err != nil || c.Value == "" {
		return "", errors.New("missing state cookie")
	}
	parts := strings.Split(c.Value, ".")
	if len(parts) != 3 {
		return "", errors.New("malformed state cookie")
	}
	state := r.URL.Query().Get("state")
	if state == "" || subtle.ConstantTimeCompare([]byte(state), []byte(parts[0])) != 1 {
		return "", errors.New("state mismatch")
	}
	ts, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || time.Since(time.Unix(ts, 0)) > oauthStateTTL {
		return "", errors.New("state expired")
	}
	return parts[1], nil
}

func (h *Handler) oauthCallback(p config.OAuth2ProviderDef) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		verifier, err := h.checkOAuthState(r, p)
		// One-shot: clear the state cookie whatever the outcome.
		h.setCookieAt(w, oauthCookiePrefix+p.Name, "", -1, http.SameSiteLaxMode)
		if err != nil {
			h.oauthError(w, r, p, "invalid state parameter")
			return
		}

		code := r.URL.Query().Get("code")
		if code == "" {
			msg := r.URL.Query().Get("error_description")
			if msg == "" {
				msg = "authorization was not granted"
			}
			h.oauthError(w, r, p, msg)
			return
		}

		accessToken, err := h.exchangeCode(r.Context(), p, code, verifier)
		if err != nil {
			log.Warn().Err(err).Str("provider", p.Name).Msg("oauth: code exchange failed")
			h.oauthError(w, r, p, "could not exchange code")
			return
		}

		ident, err := h.fetchIdentity(r.Context(), p, accessToken)
		if err != nil {
			log.Warn().Err(err).Str("provider", p.Name).Msg("oauth: fetching identity failed")
			h.oauthError(w, r, p, "could not fetch user info")
			return
		}

		entity, dbc, err := h.resolveEntity()
		if err != nil {
			h.oauthError(w, r, p, "configuration error")
			return
		}

		user, err := h.oauthResolveUser(r.Context(), dbc, entity, p, ident)
		if err != nil {
			if errors.Is(err, errEmailUnverified) {
				h.oauthError(w, r, p, err.Error())
				return
			}
			log.Error().Err(err).Str("provider", p.Name).Msg("oauth: resolving user failed")
			h.oauthError(w, r, p, "could not sign in")
			return
		}

		resp, err := h.sessionPayload(w, r, entity, user)
		if err != nil {
			h.oauthError(w, r, p, "could not issue token")
			return
		}

		if p.SuccessRedirect == "" {
			writeJSON(w, http.StatusOK, resp)
			return
		}
		vals := url.Values{"token": {asString(resp["token"])}}
		if rt := asString(resp["refresh_token"]); rt != "" {
			vals.Set("refresh_token", rt)
		}
		if p.TokenDelivery == "query" {
			sep := "?"
			if strings.Contains(p.SuccessRedirect, "?") {
				sep = "&"
			}
			http.Redirect(w, r, p.SuccessRedirect+sep+vals.Encode(), http.StatusFound)
			return
		}
		// Fragment delivery: browsers never send the fragment to servers or in Referer.
		http.Redirect(w, r, p.SuccessRedirect+"#"+vals.Encode(), http.StatusFound)
	}
}

func (h *Handler) setCookieAt(w http.ResponseWriter, name, value string, maxAge int, sameSite http.SameSite) {
	c := &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     strings.TrimRight(h.mountPrefix, "/") + h.basePath(),
		HttpOnly: true,
		Secure:   h.cookieSecure(),
		SameSite: sameSite,
		MaxAge:   maxAge,
	}
	if h.cfg.Cookie != nil {
		c.Domain = h.cfg.Cookie.Domain
	}
	http.SetCookie(w, c)
}

func (h *Handler) oauthError(w http.ResponseWriter, r *http.Request, p config.OAuth2ProviderDef, msg string) {
	if p.ErrorRedirect != "" {
		sep := "?"
		if strings.Contains(p.ErrorRedirect, "?") {
			sep = "&"
		}
		http.Redirect(w, r, p.ErrorRedirect+sep+"error="+url.QueryEscape(msg), http.StatusFound)
		return
	}
	writeError(w, http.StatusBadRequest, msg)
}

// ── identity ──────────────────────────────────────────────────────────────────

// oauthIdentity is the normalised provider profile.
type oauthIdentity struct {
	ID            string
	Email         string
	EmailVerified bool
	Name          string
	Username      string
}

func (h *Handler) fetchIdentity(ctx context.Context, p config.OAuth2ProviderDef, accessToken string) (*oauthIdentity, error) {
	infoURL := resolveProviderURL(p, "userinfo")
	if infoURL == "" {
		return nil, fmt.Errorf("no userinfo URL for provider %q", p.Name)
	}
	var info map[string]any
	if err := h.providerGetJSON(ctx, infoURL, accessToken, &info); err != nil {
		return nil, err
	}

	id := &oauthIdentity{
		ID:       anyToString(info[orDefault(p.IDField, defaultIDField(p))]),
		Email:    normalizeCredential(asString(info[orDefault(p.EmailField, "email")])),
		Name:     asString(info[orDefault(p.NameField, "name")]),
		Username: asString(info[orDefault(p.UsernameField, "login")]),
	}
	id.EmailVerified = p.TrustEmail || truthy(info[orDefault(p.EmailVerifiedField, defaultVerifiedField(p))])

	// GitHub's /user omits private emails and never says whether one is verified;
	// /user/emails is authoritative.
	if isBuiltinGitHub(p) {
		var emails []struct {
			Email    string `json:"email"`
			Primary  bool   `json:"primary"`
			Verified bool   `json:"verified"`
		}
		if err := h.providerGetJSON(ctx, "https://api.github.com/user/emails", accessToken, &emails); err == nil {
			id.EmailVerified = p.TrustEmail
			for _, e := range emails {
				if e.Primary {
					id.Email = normalizeCredential(e.Email)
					id.EmailVerified = id.EmailVerified || e.Verified
				}
			}
		}
	}
	if id.ID == "" {
		return nil, errors.New("provider returned no user id")
	}
	if id.Email == "" {
		return nil, errors.New("provider returned no email")
	}
	return id, nil
}

func (h *Handler) providerGetJSON(ctx context.Context, u, accessToken string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "yayPi/1") // GitHub requires a User-Agent
	resp, err := h.oauthClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("%s returned HTTP %d", u, resp.StatusCode)
	}
	dec := json.NewDecoder(io.LimitReader(resp.Body, maxProviderBody))
	dec.UseNumber() // keep numeric ids exact (GitHub ids exceed float precision in %v)
	return dec.Decode(out)
}

func (h *Handler) exchangeCode(ctx context.Context, p config.OAuth2ProviderDef, code, verifier string) (string, error) {
	tokenURL := resolveProviderURL(p, "token")
	if tokenURL == "" {
		return "", fmt.Errorf("no token URL for provider %q", p.Name)
	}
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {p.RedirectURI},
		"client_id":     {p.ClientID},
		"client_secret": {p.ClientSecret},
	}
	if verifier != "" {
		form.Set("code_verifier", verifier)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := h.oauthClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxProviderBody))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", fmt.Errorf("token endpoint returned HTTP %d", resp.StatusCode)
	}

	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		// GitHub returns form-encoded on non-JSON Accept
		if vals, parseErr := url.ParseQuery(string(body)); parseErr == nil {
			if t := vals.Get("access_token"); t != "" {
				return t, nil
			}
		}
		return "", fmt.Errorf("unexpected token response")
	}
	if t, ok := result["access_token"].(string); ok && t != "" {
		return t, nil
	}
	return "", fmt.Errorf("no access_token in response")
}

// oauthResolveUser finds or creates the local user for a provider identity:
//  1. an account already linked to (provider, provider user id);
//  2. otherwise an account with the same email — linked only if the provider verified
//     the email (prevents takeover by registering someone else's address upstream);
//  3. otherwise a new account, again only with a verified email.
func (h *Handler) oauthResolveUser(ctx context.Context, dbc *db.DB, entity *schema.Entity, p config.OAuth2ProviderDef, id *oauthIdentity) (map[string]any, error) {
	d := dbc.Dialect
	canLink := entity.HasColumn("oauth_provider") && entity.HasColumn("oauth_id")

	if canLink {
		q := fmt.Sprintf(`SELECT * FROM %s WHERE %s = $1 AND %s = $2`,
			d.QuoteIdent(entity.Table), d.QuoteIdent("oauth_provider"), d.QuoteIdent("oauth_id"))
		if entity.SoftDelete {
			q += " AND " + d.QuoteIdent("deleted_at") + " IS NULL"
		}
		rows, err := dbc.SQL.QueryContext(ctx, d.Rebind(q+" LIMIT 1"), p.Name, id.ID)
		if err != nil {
			return nil, err
		}
		user, err := scanRow(rows)
		rows.Close()
		if err != nil || user != nil {
			return user, err
		}
	}

	if !id.EmailVerified {
		return nil, errEmailUnverified
	}

	emailCol := fieldToColumn(entity, "email")
	user, err := h.findUserBy(ctx, dbc, entity, emailCol, id.Email)
	if err != nil {
		return nil, err
	}
	if user != nil {
		if canLink && user["oauth_id"] == nil {
			q := d.Rebind(fmt.Sprintf(`UPDATE %s SET %s = $1, %s = $2 WHERE %s = $3`,
				d.QuoteIdent(entity.Table), d.QuoteIdent("oauth_provider"), d.QuoteIdent("oauth_id"), d.QuoteIdent(entity.PKColumn())))
			if _, err := dbc.SQL.ExecContext(ctx, q, p.Name, id.ID, user[entity.PKColumn()]); err != nil {
				return nil, err
			}
		}
		return user, nil
	}

	defaultRole := "member"
	if h.cfg.Register != nil && h.cfg.Register.DefaultRole != "" {
		defaultRole = h.cfg.Register.DefaultRole
	}
	local := strings.Split(id.Email, "@")[0]
	newUser := map[string]any{
		emailCol:       id.Email,
		"username":     orDefault(id.Username, local),
		"display_name": orDefault(id.Name, local),
		"role":         defaultRole,
	}
	if canLink {
		newUser["oauth_provider"] = p.Name
		newUser["oauth_id"] = id.ID
	}
	if entity.HasColumn("email_verified_at") {
		newUser["email_verified_at"] = time.Now().UTC()
	}
	// No password hash: OAuth-only accounts cannot use password login (until a reset).
	created, err := h.insertUser(ctx, dbc, entity, newUser, emailCol)
	if err != nil && d.IsUniqueViolation(err) {
		// Lost a race with a concurrent sign-in for the same email.
		return h.findUserBy(ctx, dbc, entity, emailCol, id.Email)
	}
	return created, err
}

func truthy(v any) bool {
	switch b := v.(type) {
	case bool:
		return b
	case string:
		return strings.EqualFold(b, "true")
	}
	return false
}

func isBuiltinGitHub(p config.OAuth2ProviderDef) bool {
	return strings.EqualFold(p.Name, "github") && p.UserInfoURL == ""
}

func defaultIDField(p config.OAuth2ProviderDef) string {
	if _, builtin := builtinProviderURLs[strings.ToLower(p.Name)]; builtin {
		return "id"
	}
	return "sub" // OIDC standard claim
}

func defaultVerifiedField(p config.OAuth2ProviderDef) string {
	if strings.EqualFold(p.Name, "google") && p.UserInfoURL == "" {
		return "verified_email" // Google oauth2/v2/userinfo
	}
	return "email_verified" // OIDC standard claim
}

// ── Provider URL registry ─────────────────────────────────────────────────────

var builtinProviderURLs = map[string]map[string]string{
	"google": {
		"auth":     "https://accounts.google.com/o/oauth2/v2/auth",
		"token":    "https://oauth2.googleapis.com/token",
		"userinfo": "https://www.googleapis.com/oauth2/v2/userinfo",
	},
	"github": {
		"auth":     "https://github.com/login/oauth/authorize",
		"token":    "https://github.com/login/oauth/access_token",
		"userinfo": "https://api.github.com/user",
	},
}

func resolveProviderURL(p config.OAuth2ProviderDef, which string) string {
	switch which {
	case "auth":
		if p.AuthURL != "" {
			return p.AuthURL
		}
	case "token":
		if p.TokenURL != "" {
			return p.TokenURL
		}
	case "userinfo":
		if p.UserInfoURL != "" {
			return p.UserInfoURL
		}
	}
	if urls, ok := builtinProviderURLs[strings.ToLower(p.Name)]; ok {
		return urls[which]
	}
	return ""
}

func defaultScopes(provider string) []string {
	switch strings.ToLower(provider) {
	case "google":
		return []string{"openid", "email", "profile"}
	case "github":
		return []string{"read:user", "user:email"}
	}
	return []string{"openid", "email", "profile"}
}
