package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/rs/zerolog/log"
	"golang.org/x/crypto/bcrypt"

	"github.com/teleology-io/yayPI/internal/apierr"
	"github.com/teleology-io/yayPI/internal/config"
	"github.com/teleology-io/yayPI/internal/db"
	"github.com/teleology-io/yayPI/internal/dialect"
	"github.com/teleology-io/yayPI/internal/middleware"
	"github.com/teleology-io/yayPI/internal/schema"
	"github.com/teleology-io/yayPI/internal/token"
)

// bcryptCost is the password hashing work factor (a var so tests can lower it).
var bcryptCost = 12

// bcrypt only hashes the first 72 bytes; longer passwords would be silently truncated.
const maxPasswordBytes = 72

// dummyHash is a real bcrypt hash (cost 12). Comparing against it when the user does not
// exist makes unknown-user and wrong-password logins take the same time.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("yaypi-timing-equaliser"), bcryptCost)

// Sender delivers a single HTML email. *mailer.Mailer satisfies it.
type Sender interface {
	Send(ctx context.Context, to, subject, htmlBody string) error
}

// Options configures a Handler.
type Options struct {
	Config          config.AuthEndpointDef
	Registry        *schema.Registry
	DB              *db.Manager
	Tokens          *token.Keys
	Mailer          Sender // optional; required for password reset / email verification
	RevocationCheck bool   // see config.AuthConfig.RevocationCheck
	MountPrefix     string // API base_url the auth routes are mounted under (cookie path)
}

// Handler provides the built-in auth routes: register, login, me, refresh, logout,
// OAuth2, password reset and email verification.
type Handler struct {
	cfg             config.AuthEndpointDef
	registry        *schema.Registry
	dbManager       *db.Manager
	tokens          *token.Keys
	mailer          Sender
	revocationCheck bool
	mountPrefix     string
	throttle        *loginThrottle
	oauthClient     *http.Client
}

// New creates a Handler.
func New(opts Options) *Handler {
	return &Handler{
		cfg:             opts.Config,
		registry:        opts.Registry,
		dbManager:       opts.DB,
		tokens:          opts.Tokens,
		mailer:          opts.Mailer,
		revocationCheck: opts.RevocationCheck,
		mountPrefix:     opts.MountPrefix,
		throttle:        newLoginThrottle(opts.Config.Login),
		oauthClient:     newOAuthClient(),
	}
}

func (h *Handler) basePath() string {
	if h.cfg.BasePath == "" {
		return "/auth"
	}
	return h.cfg.BasePath
}

// Mount registers all enabled auth routes under the configured base path.
func (h *Handler) Mount(r chi.Router) {
	requireAuth := middleware.RequireAuth(h.tokens, true, h.SubjectValidator())

	r.Route(h.basePath(), func(r chi.Router) {
		// Responses here carry credentials; never let a cache keep them.
		r.Use(noStore)
		if h.cfg.Register != nil && h.cfg.Register.Enabled {
			r.Post("/register", h.register)
		}
		if h.cfg.Login != nil && h.cfg.Login.Enabled {
			r.Post("/login", h.login)
		}
		if h.cfg.Me != nil && h.cfg.Me.Enabled {
			r.With(requireAuth).Get("/me", h.me)
		}
		if h.refreshEnabled() {
			r.Post("/refresh", h.refresh)
			r.Post("/logout", h.logout)
			r.With(requireAuth).Post("/logout-all", h.logoutAll)
		}
		if h.cfg.PasswordReset != nil && h.cfg.PasswordReset.Enabled {
			r.Post("/password/forgot", h.forgotPassword)
			r.Post("/password/reset", h.resetPassword)
		}
		if h.cfg.EmailVerification != nil && h.cfg.EmailVerification.Enabled {
			r.Post("/verify-email", h.verifyEmail)
			r.With(requireAuth).Post("/verify-email/resend", h.resendVerification)
		}
		if h.cfg.OAuth2 != nil {
			for _, p := range h.cfg.OAuth2.Providers {
				p := p
				r.Get("/"+p.Name, h.oauthInitiate(p))
				r.Get("/callback/"+p.Name, h.oauthCallback(p))
			}
		}
		if jwks := h.jwks(); jwks != nil {
			body, _ := json.Marshal(jwks)
			r.Get("/.well-known/jwks.json", func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Cache-Control", "public, max-age=300")
				_, _ = w.Write(body)
			})
		}
	})
}

func (h *Handler) jwks() any {
	if h.tokens == nil {
		return nil
	}
	if set := h.tokens.JWKS(); set != nil {
		return set
	}
	return nil
}

func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Pragma", "no-cache")
		next.ServeHTTP(w, r)
	})
}

// ── register ──────────────────────────────────────────────────────────────────

func (h *Handler) register(w http.ResponseWriter, r *http.Request) {
	reg := h.cfg.Register
	credField := orDefault(reg.CredentialField, "email")
	passField := orDefault(reg.PasswordField, "password")
	hashField := orDefault(reg.HashField, "password_hash")
	defaultRole := orDefault(reg.DefaultRole, "member")

	entity, dbc, err := h.resolveEntity()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "configuration error")
		return
	}

	var body map[string]any
	if !decodeBody(w, r, &body) {
		return
	}

	// Extract password — never stored directly
	password, _ := body[passField].(string)
	delete(body, passField)
	if msg := checkPassword(password, reg.MinPasswordLength); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}

	credential, _ := body[credField].(string)
	credential = normalizeCredential(credential)
	if credential == "" {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("%s is required", credField))
		return
	}
	if isEmailField(entity, credField) && !looksLikeEmail(credential) {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("%s must be a valid email address", credField))
		return
	}
	body[credField] = credential

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Never let the caller choose server-controlled columns (role, id, oauth linkage,
	// timestamps) or any field gated by write_roles — registration is unauthenticated.
	stripServerControlled(entity, body, hashField)
	body[hashField] = string(hash)
	body["role"] = defaultRole

	user, err := h.insertUser(r.Context(), dbc, entity, body, credField)
	if err != nil {
		if dbc.Dialect.IsUniqueViolation(err) {
			writeError(w, http.StatusConflict, "a user with that credential already exists")
			return
		}
		log.Error().Err(err).Msg("auth: register insert failed")
		writeError(w, http.StatusInternalServerError, "could not create user")
		return
	}

	if ev := h.cfg.EmailVerification; ev != nil && ev.Enabled {
		h.sendVerificationEmail(r.Context(), dbc, entity, user)
	}

	h.respondWithSession(w, r, http.StatusCreated, entity, user)
}

// insertUser inserts a user row and returns it as stored. Dialects without RETURNING
// re-read the row by its (unique) credential.
func (h *Handler) insertUser(ctx context.Context, dbc *db.DB, entity *schema.Entity, data map[string]any, credField string) (map[string]any, error) {
	d := dbc.Dialect
	cols, placeholders, vals := buildInsert(entity, data, d)
	if len(cols) == 0 {
		return nil, errors.New("no valid fields provided")
	}
	table := d.QuoteIdent(entity.Table)
	if d.SupportsReturning() {
		q := d.Rebind(fmt.Sprintf(`INSERT INTO %s (%s) VALUES (%s) RETURNING *`,
			table, strings.Join(cols, ", "), strings.Join(placeholders, ", ")))
		rows, err := dbc.SQL.QueryContext(ctx, q, vals...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		user, err := scanRow(rows)
		if err == nil && user == nil {
			err = errors.New("insert returned no row")
		}
		return user, err
	}
	q := d.Rebind(fmt.Sprintf(`INSERT INTO %s (%s) VALUES (%s)`,
		table, strings.Join(cols, ", "), strings.Join(placeholders, ", ")))
	if _, err := dbc.SQL.ExecContext(ctx, q, vals...); err != nil {
		return nil, err
	}
	user, err := h.findUserBy(ctx, dbc, entity, fieldToColumn(entity, credField), data[credField])
	if err == nil && user == nil {
		err = errors.New("could not read created user")
	}
	return user, err
}

// ── login ─────────────────────────────────────────────────────────────────────

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	login := h.cfg.Login
	credField := orDefault(login.CredentialField, "email")
	passField := orDefault(login.PasswordField, "password")
	hashField := orDefault(login.HashField, "password_hash")

	entity, dbc, err := h.resolveEntity()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "configuration error")
		return
	}

	var body map[string]any
	if !decodeBody(w, r, &body) {
		return
	}

	credential, _ := body[credField].(string)
	credential = normalizeCredential(credential)
	password, _ := body[passField].(string)
	if credential == "" || password == "" {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("%s and %s are required", credField, passField))
		return
	}

	ip := middleware.GetClientIP(r)
	if wait, blocked := h.throttle.blocked(credential, ip); blocked {
		w.Header().Set("Retry-After", fmt.Sprintf("%d", int(wait.Seconds())+1))
		writeError(w, http.StatusTooManyRequests, "too many failed login attempts; try again later")
		return
	}

	user, err := h.findUserBy(r.Context(), dbc, entity, fieldToColumn(entity, credField), credential)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	storedHash := dummyHash
	if user != nil {
		if s := asString(user[fieldToColumn(entity, hashField)]); s != "" {
			storedHash = []byte(s)
		}
	}
	// Always run bcrypt so unknown users and wrong passwords cost the same time.
	cmpErr := bcrypt.CompareHashAndPassword(storedHash, []byte(password))
	if user == nil || cmpErr != nil || len(password) > maxPasswordBytes {
		h.throttle.fail(credential, ip)
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	h.throttle.reset(credential)

	if ev := h.cfg.EmailVerification; ev != nil && ev.Enabled && ev.Required && user["email_verified_at"] == nil {
		writeError(w, http.StatusForbidden, "email address not verified")
		return
	}

	h.respondWithSession(w, r, http.StatusOK, entity, user)
}

// ── me ────────────────────────────────────────────────────────────────────────

func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	entity, dbc, err := h.resolveEntity()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "configuration error")
		return
	}
	sub := middleware.GetSubject(r)
	if sub == nil || sub.ID == "" {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	user, err := h.findUserBy(r.Context(), dbc, entity, entity.PKColumn(), sub.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if user == nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	stripSensitive(entity, user)
	writeJSON(w, http.StatusOK, map[string]any{"user": user})
}

// ── sessions ──────────────────────────────────────────────────────────────────

// respondWithSession issues tokens for user and writes the standard
// {token, expires_in, user[, refresh_token]} response.
func (h *Handler) respondWithSession(w http.ResponseWriter, r *http.Request, status int, entity *schema.Entity, user map[string]any) {
	resp, err := h.sessionPayload(w, r, entity, user)
	if err != nil {
		log.Error().Err(err).Msg("auth: issuing session failed")
		writeError(w, http.StatusInternalServerError, "could not issue token")
		return
	}
	writeJSON(w, status, resp)
}

// sessionPayload issues an access token and, when refresh is enabled, starts a new
// refresh-token family delivered via cookie or body according to refresh.store.
func (h *Handler) sessionPayload(w http.ResponseWriter, r *http.Request, entity *schema.Entity, user map[string]any) (map[string]any, error) {
	access, err := h.issueAccessToken(entity, user)
	if err != nil {
		return nil, err
	}
	userID := anyToString(user[entity.PKColumn()])
	stripSensitive(entity, user)
	resp := map[string]any{"token": access, "user": user, "expires_in": int(h.tokens.TTL().Seconds())}
	if h.refreshEnabled() {
		_, dbc, err := h.resolveEntity()
		if err != nil {
			return nil, err
		}
		refresh, err := h.issueRefreshToken(r.Context(), dbc, userID, "")
		if err != nil {
			return nil, err
		}
		h.deliverRefreshToken(w, resp, refresh)
	}
	return resp, nil
}

func (h *Handler) issueAccessToken(entity *schema.Entity, user map[string]any) (string, error) {
	claims := jwt.MapClaims{
		"sub":   anyToString(user[entity.PKColumn()]),
		"role":  asString(user["role"]),
		"email": asString(user["email"]),
	}
	if tv, ok := toInt64(user["token_version"]); ok {
		claims["tv"] = tv
	}
	if t := anyToString(user["tenant_id"]); t != "" {
		claims["tenant"] = t
	}
	return h.tokens.Sign(claims)
}

// SubjectValidator returns the per-request revocation check, or nil when
// auth.revocation_check is off. It reloads the user so deleted users, role changes and
// bumped token versions take effect immediately.
func (h *Handler) SubjectValidator() middleware.SubjectValidator {
	if !h.revocationCheck {
		return nil
	}
	return func(ctx context.Context, sub *middleware.Subject, tv int64) (*middleware.Subject, error) {
		entity, dbc, err := h.resolveEntity()
		if err != nil {
			return nil, err
		}
		user, err := h.findUserBy(ctx, dbc, entity, entity.PKColumn(), sub.ID)
		if err != nil {
			return nil, err
		}
		if user == nil {
			return nil, errors.New("user not found")
		}
		if cur, ok := toInt64(user["token_version"]); ok && cur != tv {
			return nil, errors.New("token version revoked")
		}
		return &middleware.Subject{ID: sub.ID, Role: asString(user["role"]), Email: asString(user["email"]), Tenant: anyToString(user["tenant_id"])}, nil
	}
}

// bumpTokenVersion invalidates every outstanding access token for userID (immediately
// with revocation_check, otherwise at token expiry).
func (h *Handler) bumpTokenVersion(ctx context.Context, dbc *db.DB, entity *schema.Entity, userID string) error {
	if !entity.HasColumn("token_version") {
		return nil
	}
	d := dbc.Dialect
	col := d.QuoteIdent("token_version")
	q := d.Rebind(fmt.Sprintf(`UPDATE %s SET %s = %s + 1 WHERE %s = $1`,
		d.QuoteIdent(entity.Table), col, col, d.QuoteIdent(entity.PKColumn())))
	_, err := dbc.SQL.ExecContext(ctx, q, userID)
	return err
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
	if h.dbManager == nil {
		return nil, nil, errors.New("no database configured")
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

// findUserBy loads one live (not soft-deleted) user where column = value; nil if none.
func (h *Handler) findUserBy(ctx context.Context, dbc *db.DB, entity *schema.Entity, column string, value any) (map[string]any, error) {
	d := dbc.Dialect
	q := fmt.Sprintf(`SELECT * FROM %s WHERE %s = $1`, d.QuoteIdent(entity.Table), d.QuoteIdent(column))
	if entity.SoftDelete {
		q += " AND " + d.QuoteIdent("deleted_at") + " IS NULL"
	}
	rows, err := dbc.SQL.QueryContext(ctx, d.Rebind(q+" LIMIT 1"), value)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRow(rows)
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

func isEmailField(entity *schema.Entity, fieldName string) bool {
	col := fieldToColumn(entity, fieldName)
	for _, f := range entity.Fields {
		if f.ColumnName == col {
			return col == "email" || (f.Validate != nil && f.Validate.Format == "email")
		}
	}
	return false
}

// buildInsert produces quoted column names, $N placeholders, and values for an INSERT.
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
		placeholders = append(placeholders, fmt.Sprintf("$%d", n))
		vals = append(vals, val)
		n++
	}
	return
}

// registerReservedColumns are user columns a self-registering caller may never set.
var registerReservedColumns = map[string]bool{
	"role": true, "oauth_provider": true, "oauth_id": true,
	"created_at": true, "updated_at": true, "deleted_at": true,
	"email_verified_at": true, "token_version": true,
}

// stripServerControlled removes keys from an unauthenticated register body that the
// caller must not control: the primary key, reserved columns, and any field with
// write_roles (an anonymous caller holds no role). hashField is kept — the handler
// sets it from the bcrypt hash after this runs.
func stripServerControlled(entity *schema.Entity, body map[string]any, hashField string) {
	for _, f := range entity.Fields {
		if f.ColumnName == hashField || f.Name == hashField {
			continue
		}
		if f.PrimaryKey || registerReservedColumns[f.ColumnName] || len(f.WriteRoles) > 0 {
			delete(body, f.Name)
			delete(body, f.ColumnName)
		}
	}
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

// scanRow reads the first row from *sql.Rows into a map[string]any; nil when empty.
func scanRow(rows *sql.Rows) (map[string]any, error) {
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	if !rows.Next() {
		return nil, rows.Err()
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

// ── value helpers ─────────────────────────────────────────────────────────────

// checkPassword returns an error message, or "" when the password is acceptable.
func checkPassword(password string, minLen int) string {
	if minLen <= 0 {
		minLen = 8
	}
	switch {
	case password == "":
		return "password is required"
	case len(password) < minLen:
		return fmt.Sprintf("password must be at least %d characters", minLen)
	case len(password) > maxPasswordBytes:
		return fmt.Sprintf("password must be at most %d bytes", maxPasswordBytes)
	}
	return ""
}

// normalizeCredential trims and lowercases a login identifier so register, login, and
// OAuth all agree on the stored form.
func normalizeCredential(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

func looksLikeEmail(s string) bool {
	at := strings.LastIndex(s, "@")
	return at > 0 && at < len(s)-1 && strings.Contains(s[at+1:], ".") && !strings.ContainsAny(s, " \t\r\n")
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func asString(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case []byte:
		return string(s)
	}
	return ""
}

func toInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case int64:
		return n, true
	case int32:
		return int64(n), true
	case int:
		return int64(n), true
	case float64:
		return int64(n), true
	case []byte:
		var x int64
		_, err := fmt.Sscan(string(n), &x)
		return x, err == nil
	case string:
		var x int64
		_, err := fmt.Sscan(n, &x)
		return x, err == nil
	}
	return 0, false
}

// anyToString converts an id value to its string representation.
// database/sql may scan UUID columns as []byte (16 raw bytes); format those as UUID strings.
func anyToString(v any) string {
	switch id := v.(type) {
	case nil:
		return ""
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

// ── HTTP helpers ──────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	apierr.Write(w, w.Header().Get(middleware.RequestIDHeader), status, msg)
}

// decodeBody decodes the JSON request body into v. On failure it writes a 413 (body over
// the size cap) or 400 and returns false.
func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		if middleware.IsBodyTooLarge(err) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
		} else {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
		}
		return false
	}
	return true
}

// ── cookies ───────────────────────────────────────────────────────────────────

func (h *Handler) cookieSecure() bool {
	if c := h.cfg.Cookie; c != nil && c.Secure != nil {
		return *c.Secure
	}
	return true
}

func (h *Handler) cookieSameSite() http.SameSite {
	if c := h.cfg.Cookie; c != nil {
		switch strings.ToLower(c.SameSite) {
		case "strict":
			return http.SameSiteStrictMode
		case "none":
			return http.SameSiteNoneMode
		}
	}
	return http.SameSiteLaxMode
}

// setCookie writes an HttpOnly auth cookie scoped to the auth routes, so it is not sent
// with every API request. maxAge < 0 deletes it.
func (h *Handler) setCookie(w http.ResponseWriter, name, value string, maxAge int) {
	h.setCookieAt(w, name, value, maxAge, h.cookieSameSite())
}
