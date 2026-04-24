package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"

	"github.com/teleology-io/yayPI/internal/config"
	"github.com/teleology-io/yayPI/internal/db"
	"github.com/teleology-io/yayPI/internal/dialect"
	"github.com/teleology-io/yayPI/internal/schema"
)

const bcryptCost = 12

// Handler provides register, login, me, and OAuth2 routes.
type Handler struct {
	cfg       config.AuthEndpointDef
	registry  *schema.Registry
	dbManager *db.Manager
	secret    []byte
	algorithm string
}

// New creates a Handler. secret and algorithm must match the main JWT config.
func New(cfg config.AuthEndpointDef, reg *schema.Registry, dbm *db.Manager, secret []byte, alg string) *Handler {
	return &Handler{cfg: cfg, registry: reg, dbManager: dbm, secret: secret, algorithm: alg}
}

// Mount registers all enabled auth routes under the configured base path.
func (h *Handler) Mount(r chi.Router) {
	base := h.cfg.BasePath
	if base == "" {
		base = "/auth"
	}

	r.Route(base, func(r chi.Router) {
		if h.cfg.Register != nil && h.cfg.Register.Enabled {
			r.Post("/register", h.register)
		}
		if h.cfg.Login != nil && h.cfg.Login.Enabled {
			r.Post("/login", h.login)
		}
		if h.cfg.Me != nil && h.cfg.Me.Enabled {
			r.Get("/me", h.me)
		}
		if h.cfg.Refresh != nil && h.cfg.Refresh.Enabled {
			r.Post("/refresh", h.refresh)
		}
		if h.cfg.OAuth2 != nil {
			for _, p := range h.cfg.OAuth2.Providers {
				p := p
				r.Get("/"+p.Name, h.oauthInitiate(p))
				r.Get("/callback/"+p.Name, h.oauthCallback(p))
			}
		}
	})
}

// ── register ──────────────────────────────────────────────────────────────────

func (h *Handler) register(w http.ResponseWriter, r *http.Request) {
	reg := h.cfg.Register

	credField := reg.CredentialField
	if credField == "" {
		credField = "email"
	}
	passField := reg.PasswordField
	if passField == "" {
		passField = "password"
	}
	hashField := reg.HashField
	if hashField == "" {
		hashField = "password_hash"
	}
	defaultRole := reg.DefaultRole
	if defaultRole == "" {
		defaultRole = "member"
	}

	entity, dbc, err := h.resolveEntity()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "configuration error")
		return
	}
	d := dbc.Dialect

	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	// Extract password — never stored directly
	password, _ := body[passField].(string)
	if password == "" {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("%s is required", passField))
		return
	}
	if len(password) < 8 {
		writeError(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}

	// Replace raw password with bcrypt hash
	delete(body, passField)
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	body[hashField] = string(hash)

	// Apply default role if caller did not provide one
	if _, set := body["role"]; !set {
		body["role"] = defaultRole
	}

	cols, placeholders, vals := buildInsert(entity, body, d)
	if len(cols) == 0 {
		writeError(w, http.StatusBadRequest, "no valid fields provided")
		return
	}

	var user map[string]any
	if d.SupportsReturning() {
		query := d.Rebind(fmt.Sprintf(
			`INSERT INTO %s (%s) VALUES (%s) RETURNING *`,
			d.QuoteIdent(entity.Table), strings.Join(cols, ", "), strings.Join(placeholders, ", "),
		))
		rows, qerr := dbc.SQL.QueryContext(r.Context(), query, vals...)
		if qerr != nil {
			if d.IsUniqueViolation(qerr) {
				writeError(w, http.StatusConflict, "a user with that credential already exists")
				return
			}
			writeError(w, http.StatusInternalServerError, "could not create user")
			return
		}
		defer rows.Close()
		user, err = scanRow(rows)
	} else {
		query := d.Rebind(fmt.Sprintf(
			`INSERT INTO %s (%s) VALUES (%s)`,
			d.QuoteIdent(entity.Table), strings.Join(cols, ", "), strings.Join(placeholders, ", "),
		))
		res, qerr := dbc.SQL.ExecContext(r.Context(), query, vals...)
		if qerr != nil {
			if d.IsUniqueViolation(qerr) {
				writeError(w, http.StatusConflict, "a user with that credential already exists")
				return
			}
			writeError(w, http.StatusInternalServerError, "could not create user")
			return
		}
		lastID, _ := res.LastInsertId()
		credCol := fieldToColumn(entity, credField)
		fetchQ := d.Rebind(fmt.Sprintf(`SELECT * FROM %s WHERE %s = $1 LIMIT 1`,
			d.QuoteIdent(entity.Table), d.QuoteIdent(credCol)))
		rows, qerr := dbc.SQL.QueryContext(r.Context(), fetchQ, body[credField])
		if qerr != nil || lastID == 0 {
			writeError(w, http.StatusInternalServerError, "could not read created user")
			return
		}
		defer rows.Close()
		user, err = scanRow(rows)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not read created user")
		return
	}

	stripSensitive(entity, user)
	token, err := h.issueToken(user)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not issue token")
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{"token": token, "user": user})
}

// ── login ─────────────────────────────────────────────────────────────────────

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	login := h.cfg.Login

	credField := login.CredentialField
	if credField == "" {
		credField = "email"
	}
	passField := login.PasswordField
	if passField == "" {
		passField = "password"
	}
	hashField := login.HashField
	if hashField == "" {
		hashField = "password_hash"
	}

	entity, dbc, err := h.resolveEntity()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "configuration error")
		return
	}
	d := dbc.Dialect

	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	credential, _ := body[credField].(string)
	password, _ := body[passField].(string)
	if credential == "" || password == "" {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("%s and %s are required", credField, passField))
		return
	}

	credCol := fieldToColumn(entity, credField)
	hashCol := fieldToColumn(entity, hashField)

	query := d.Rebind(fmt.Sprintf(
		`SELECT * FROM %s WHERE %s = $1 AND deleted_at IS NULL LIMIT 1`,
		d.QuoteIdent(entity.Table), d.QuoteIdent(credCol),
	))
	rows, err := dbc.SQL.QueryContext(r.Context(), query, strings.ToLower(strings.TrimSpace(credential)))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer rows.Close()

	user, err := scanRow(rows)
	if err != nil || user == nil {
		// Burn time to prevent user enumeration via timing
		_ = bcrypt.CompareHashAndPassword([]byte("$2a$12$invalid.padding.to.burn.time000000000000000000000"), []byte(password))
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	storedHash, _ := user[hashCol].(string)
	if err := bcrypt.CompareHashAndPassword([]byte(storedHash), []byte(password)); err != nil {
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	stripSensitive(entity, user)
	token, err := h.issueToken(user)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not issue token")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"token": token, "user": user})
}

// ── me ────────────────────────────────────────────────────────────────────────

func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	entity, dbc, err := h.resolveEntity()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "configuration error")
		return
	}
	d := dbc.Dialect

	sub, err := h.extractSub(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	query := d.Rebind(fmt.Sprintf(
		`SELECT * FROM %s WHERE id = $1 AND deleted_at IS NULL LIMIT 1`, d.QuoteIdent(entity.Table),
	))
	rows, err := dbc.SQL.QueryContext(r.Context(), query, sub)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer rows.Close()

	user, err := scanRow(rows)
	if err != nil || user == nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}

	stripSensitive(entity, user)
	writeJSON(w, http.StatusOK, map[string]any{"user": user})
}

// ── OAuth2 ────────────────────────────────────────────────────────────────────

func (h *Handler) oauthInitiate(p config.OAuth2ProviderDef) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		authURL := resolveProviderURL(p, "auth")
		if authURL == "" {
			writeError(w, http.StatusInternalServerError, "provider not configured")
			return
		}

		state, err := generateState(h.secret)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not generate state")
			return
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
		http.Redirect(w, r, authURL+"?"+params.Encode(), http.StatusFound)
	}
}

func (h *Handler) oauthCallback(p config.OAuth2ProviderDef) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := verifyState(r.URL.Query().Get("state"), h.secret); err != nil {
			h.oauthError(w, r, p, "invalid state parameter")
			return
		}

		code := r.URL.Query().Get("code")
		if code == "" {
			h.oauthError(w, r, p, r.URL.Query().Get("error_description"))
			return
		}

		accessToken, err := exchangeCode(r.Context(), p, code)
		if err != nil {
			h.oauthError(w, r, p, "could not exchange code")
			return
		}

		info, err := fetchUserInfo(r.Context(), p, accessToken)
		if err != nil {
			h.oauthError(w, r, p, "could not fetch user info")
			return
		}

		entity, dbc, err := h.resolveEntity()
		if err != nil {
			h.oauthError(w, r, p, "configuration error")
			return
		}

		emailField := p.EmailField
		if emailField == "" {
			emailField = "email"
		}
		email, _ := info[emailField].(string)
		if email == "" {
			h.oauthError(w, r, p, "no email returned by provider")
			return
		}
		email = strings.ToLower(strings.TrimSpace(email))

		user, err := h.oauthUpsert(r.Context(), dbc, entity, p, email, info)
		if err != nil {
			h.oauthError(w, r, p, "could not sign in")
			return
		}

		stripSensitive(entity, user)
		token, err := h.issueToken(user)
		if err != nil {
			h.oauthError(w, r, p, "could not issue token")
			return
		}

		if p.SuccessRedirect != "" {
			http.Redirect(w, r, p.SuccessRedirect+"?token="+url.QueryEscape(token), http.StatusFound)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"token": token, "user": user})
	}
}

func (h *Handler) oauthError(w http.ResponseWriter, r *http.Request, p config.OAuth2ProviderDef, msg string) {
	if p.ErrorRedirect != "" {
		http.Redirect(w, r, p.ErrorRedirect+"?error="+url.QueryEscape(msg), http.StatusFound)
		return
	}
	writeError(w, http.StatusBadRequest, msg)
}

func (h *Handler) oauthUpsert(
	ctx context.Context,
	dbc *db.DB,
	entity *schema.Entity,
	p config.OAuth2ProviderDef,
	email string,
	info map[string]any,
) (map[string]any, error) {
	d := dbc.Dialect
	emailCol := fieldToColumn(entity, "email")

	// Try to find an existing user by email
	q := d.Rebind(fmt.Sprintf(`SELECT * FROM %s WHERE %s = $1 AND deleted_at IS NULL LIMIT 1`,
		d.QuoteIdent(entity.Table), d.QuoteIdent(emailCol)))
	rows, err := dbc.SQL.QueryContext(ctx, q, email)
	if err != nil {
		return nil, err
	}
	user, _ := scanRow(rows)
	rows.Close()
	if user != nil {
		return user, nil
	}

	nameField := p.NameField
	if nameField == "" {
		nameField = "name"
	}
	usernameField := p.UsernameField
	if usernameField == "" {
		usernameField = "login" // GitHub convention
	}

	displayName, _ := info[nameField].(string)
	if displayName == "" {
		displayName = strings.Split(email, "@")[0]
	}
	username, _ := info[usernameField].(string)
	if username == "" {
		username = strings.Split(email, "@")[0]
	}

	// OAuth2 users get an unusable password hash — they can only sign in via OAuth2
	randBytes := make([]byte, 32)
	_, _ = rand.Read(randBytes)
	unusableHash, _ := bcrypt.GenerateFromPassword(randBytes, 4)

	defaultRole := "member"
	hashField := "password_hash"
	if h.cfg.Register != nil {
		if h.cfg.Register.DefaultRole != "" {
			defaultRole = h.cfg.Register.DefaultRole
		}
		if h.cfg.Register.HashField != "" {
			hashField = h.cfg.Register.HashField
		}
	}

	newUser := map[string]any{
		"email":        email,
		"username":     username,
		"display_name": displayName,
		hashField:      string(unusableHash),
		"role":         defaultRole,
	}

	cols, placeholders, vals := buildInsert(entity, newUser, d)
	upsertSQL := d.Rebind(d.UpsertIgnore(d.QuoteIdent(entity.Table), cols, placeholders))

	if d.SupportsReturning() {
		rows, err = dbc.SQL.QueryContext(ctx, upsertSQL, vals...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		return scanRow(rows)
	}
	// MySQL: INSERT IGNORE then re-fetch by email
	if _, err = dbc.SQL.ExecContext(ctx, upsertSQL, vals...); err != nil {
		return nil, err
	}
	fetchQ := d.Rebind(fmt.Sprintf(`SELECT * FROM %s WHERE %s = $1 AND deleted_at IS NULL LIMIT 1`,
		d.QuoteIdent(entity.Table), d.QuoteIdent(emailCol)))
	rows2, err := dbc.SQL.QueryContext(ctx, fetchQ, email)
	if err != nil {
		return nil, err
	}
	defer rows2.Close()
	return scanRow(rows2)
}

// ── JWT helpers ───────────────────────────────────────────────────────────────

func (h *Handler) issueToken(user map[string]any) (string, error) {
	sub := anyToString(user["id"])
	role, _ := user["role"].(string)
	email, _ := user["email"].(string)

	claims := jwt.MapClaims{
		"sub":   sub,
		"role":  role,
		"email": email,
		"iat":   time.Now().Unix(),
		"exp":   time.Now().Add(24 * time.Hour).Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(h.secret)
}

func (h *Handler) extractSub(r *http.Request) (string, error) {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return "", fmt.Errorf("no bearer token")
	}
	tokenStr := strings.TrimPrefix(auth, "Bearer ")
	alg := h.algorithm
	if alg == "" {
		alg = "HS256"
	}
	parser := jwt.NewParser(jwt.WithValidMethods([]string{alg}))
	token, err := parser.ParseWithClaims(tokenStr, &jwt.MapClaims{}, func(_ *jwt.Token) (any, error) {
		return h.secret, nil
	})
	if err != nil || !token.Valid {
		return "", fmt.Errorf("invalid token")
	}
	claims, _ := token.Claims.(*jwt.MapClaims)
	sub, _ := (*claims)["sub"].(string)
	if sub == "" {
		return "", fmt.Errorf("missing sub claim")
	}
	return sub, nil
}

// ── OAuth2 state (HMAC-signed, stateless) ─────────────────────────────────────

func generateState(secret []byte) (string, error) {
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ts := fmt.Sprintf("%d", time.Now().Unix())
	payload := base64.RawURLEncoding.EncodeToString(nonce) + ":" + ts
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(payload))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return payload + ":" + sig, nil
}

func verifyState(state string, secret []byte) error {
	parts := strings.Split(state, ":")
	if len(parts) != 3 {
		return fmt.Errorf("malformed state")
	}
	payload := parts[0] + ":" + parts[1]
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return fmt.Errorf("invalid state encoding")
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(payload))
	if !hmac.Equal(mac.Sum(nil), sig) {
		return fmt.Errorf("invalid state signature")
	}
	var ts int64
	fmt.Sscanf(parts[1], "%d", &ts)
	if time.Now().Unix()-ts > 600 {
		return fmt.Errorf("state expired")
	}
	return nil
}

// ── OAuth2 HTTP helpers ───────────────────────────────────────────────────────

func exchangeCode(ctx context.Context, p config.OAuth2ProviderDef, code string) (string, error) {
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

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

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
	if t, ok := result["access_token"].(string); ok {
		return t, nil
	}
	return "", fmt.Errorf("no access_token in response")
}

func fetchUserInfo(ctx context.Context, p config.OAuth2ProviderDef, accessToken string) (map[string]any, error) {
	infoURL := resolveProviderURL(p, "userinfo")
	if infoURL == "" {
		return nil, fmt.Errorf("no userinfo URL for provider %q", p.Name)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, infoURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "yayPi/1") // GitHub requires User-Agent

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return result, nil
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
		return []string{"user:email"}
	}
	return []string{"email"}
}

// ── DB / schema helpers ───────────────────────────────────────────────────────

func (h *Handler) resolveEntity() (*schema.Entity, *db.DB, error) {
	name := h.cfg.UserEntity
	if name == "" {
		name = schema.BuiltinUserEntityName
	}
	entity, ok := h.registry.GetEntity(name)
	if !ok {
		return nil, nil, fmt.Errorf("entity %q not found in registry", name)
	}
	dbc := h.dbManager.Default()
	if entity.Database != "" {
		d, err := h.dbManager.Get(entity.Database)
		if err != nil {
			return nil, nil, err
		}
		dbc = d
	}
	return entity, dbc, nil
}

// fieldToColumn returns the snake_case column name for a given entity field name.
func fieldToColumn(entity *schema.Entity, fieldName string) string {
	for _, f := range entity.Fields {
		if strings.EqualFold(f.Name, fieldName) || strings.EqualFold(f.ColumnName, fieldName) {
			return f.ColumnName
		}
	}
	return fieldName
}

// buildInsert produces quoted column names, dialect placeholders, and values for an INSERT.
// Only columns that exist in the entity definition are included (protects against
// arbitrary key injection). Primary-key fields with a DB default are skipped.
func buildInsert(entity *schema.Entity, data map[string]any, d dialect.Dialect) (cols, placeholders []string, vals []any) {
	n := 1
	for _, f := range entity.Fields {
		if f.PrimaryKey && f.Default != "" {
			continue // let the DB generate it (e.g. gen_random_uuid())
		}
		// Skip timestamp/soft-delete columns — the DB defaults handle them
		switch f.ColumnName {
		case "created_at", "updated_at", "deleted_at":
			continue
		}
		val, ok := data[f.Name]
		if !ok {
			val, ok = data[f.ColumnName]
		}
		if !ok {
			continue
		}
		cols = append(cols, d.QuoteIdent(f.ColumnName))
		if d.Name() == "postgres" {
			placeholders = append(placeholders, fmt.Sprintf("$%d", n))
		} else {
			placeholders = append(placeholders, "?")
		}
		vals = append(vals, val)
		n++
	}
	return
}

// anyToString converts an id value to its string representation.
// database/sql may scan UUID columns as []byte (16 raw bytes); format those as UUID strings.
func anyToString(v any) string {
	switch id := v.(type) {
	case string:
		return id
	case []byte:
		if len(id) == 16 {
			return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
				id[0:4], id[4:6], id[6:8], id[8:10], id[10:16])
		}
		return string(id)
	default:
		return fmt.Sprintf("%v", v)
	}
}

// scanRow reads the first row from *sql.Rows into a map[string]any.
func scanRow(rows *sql.Rows) (map[string]any, error) {
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	if !rows.Next() {
		return nil, nil
	}
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return nil, err
	}
	result := make(map[string]any, len(cols))
	for i, col := range cols {
		result[col] = vals[i]
	}
	return result, nil
}

// stripSensitive removes omit_response fields from a user record before sending it.
func stripSensitive(entity *schema.Entity, user map[string]any) {
	for _, f := range entity.Fields {
		if f.OmitResponse {
			delete(user, f.ColumnName)
			delete(user, f.Name)
		}
	}
}


// ── HTTP helpers ──────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
