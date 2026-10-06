package auth

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"golang.org/x/crypto/bcrypt"

	"github.com/teleology-io/yayPI/internal/db"
	"github.com/teleology-io/yayPI/internal/middleware"
	"github.com/teleology-io/yayPI/internal/outbox"
	"github.com/teleology-io/yayPI/internal/schema"
)

const (
	purposeReset  = "reset"
	purposeVerify = "verify"

	defaultResetExpiry  = time.Hour
	defaultVerifyExpiry = 24 * time.Hour
	resendCooldown      = time.Minute
)

var errTokenInvalid = errors.New("invalid or expired token")

// resendGuard rate-limits verification resends per user.
var resendGuard sync.Map // userID → time.Time

// issueOneTimeToken stores a single-use token for purpose, invalidating any earlier
// unused token of the same purpose for the user, and returns the plaintext.
func issueOneTimeToken(ctx context.Context, dbc *db.DB, userID, purpose string, ttl time.Duration) (string, error) {
	plain, hash, err := newOpaqueToken()
	if err != nil {
		return "", err
	}
	d := dbc.Dialect
	table := d.QuoteIdent(schema.AuthTokenTable)
	now := time.Now().Unix()
	if _, err := dbc.SQL.ExecContext(ctx, d.Rebind(fmt.Sprintf(
		`UPDATE %s SET used_at = $1 WHERE user_id = $2 AND purpose = $3 AND used_at IS NULL`, table)),
		now, userID, purpose); err != nil {
		return "", err
	}
	if _, err := dbc.SQL.ExecContext(ctx, d.Rebind(fmt.Sprintf(
		`INSERT INTO %s (id, user_id, purpose, token_hash, expires_at, created_at) VALUES ($1, $2, $3, $4, $5, $6)`, table)),
		uuid.NewString(), userID, purpose, hash, time.Now().Add(ttl).Unix(), now); err != nil {
		return "", err
	}
	return plain, nil
}

// consumeOneTimeToken atomically marks the token used and returns its user ID.
func consumeOneTimeToken(ctx context.Context, dbc *db.DB, plain, purpose string) (string, error) {
	if plain == "" {
		return "", errTokenInvalid
	}
	d := dbc.Dialect
	table := d.QuoteIdent(schema.AuthTokenTable)
	hash := hashToken(plain)
	now := time.Now().Unix()

	var userID string
	err := dbc.SQL.QueryRowContext(ctx, d.Rebind(fmt.Sprintf(
		`SELECT user_id FROM %s WHERE token_hash = $1 AND purpose = $2 AND used_at IS NULL AND expires_at > $3`, table)),
		hash, purpose, now).Scan(&userID)
	if err != nil {
		return "", errTokenInvalid
	}
	res, err := dbc.SQL.ExecContext(ctx, d.Rebind(fmt.Sprintf(
		`UPDATE %s SET used_at = $1 WHERE token_hash = $2 AND used_at IS NULL`, table)), now, hash)
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return "", errTokenInvalid // consumed concurrently
	}
	return userID, nil
}

// renderLinkEmail substitutes {{token}} into urlTmpl and {{link}} into bodyTmpl.
func renderLinkEmail(urlTmpl, bodyTmpl, defaultBody, plain string) string {
	link := strings.ReplaceAll(urlTmpl, "{{token}}", plain)
	if bodyTmpl == "" {
		bodyTmpl = defaultBody
	}
	return strings.ReplaceAll(bodyTmpl, "{{link}}", html.EscapeString(link))
}

// queueEmail stores an email in the outbox; the outbox worker delivers it with the
// smtp.retry policy, so a slow or briefly unavailable mail server neither delays the
// request nor loses the message. Request latency also stays independent of whether the
// account exists.
func (h *Handler) queueEmail(ctx context.Context, to, subject, body, what string) {
	if h.dbManager == nil {
		return
	}
	d := h.dbManager.Default()
	err := outbox.Enqueue(ctx, d.SQL, d.Dialect, outbox.KindEmail, what,
		outbox.EmailPayload{To: to, Subject: subject, HTML: body})
	if err != nil {
		log.Error().Err(err).Str("email", what).Msg("auth: queueing email failed")
	}
}

// ── password reset ────────────────────────────────────────────────────────────

// forgotPassword handles POST /auth/password/forgot {email}. It always answers 202 —
// whether the account exists is never revealed — and does the work in the background.
func (h *Handler) forgotPassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email string `json:"email"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	email := normalizeCredential(body.Email)
	if email == "" {
		writeError(w, http.StatusBadRequest, "email is required")
		return
	}
	go h.startPasswordReset(email)
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "if that account exists, a reset email has been sent"})
}

func (h *Handler) startPasswordReset(email string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pr := h.cfg.PasswordReset
	entity, dbc, err := h.resolveEntity()
	if err != nil {
		return
	}
	user, err := h.findUserBy(ctx, dbc, entity, fieldToColumn(entity, "email"), email)
	if err != nil || user == nil {
		return
	}
	ttl := defaultResetExpiry
	if d, err := parseDuration(pr.Expiry); pr.Expiry != "" && err == nil && d > 0 {
		ttl = d
	}
	plain, err := issueOneTimeToken(ctx, dbc, anyToString(user[entity.PKColumn()]), purposeReset, ttl)
	if err != nil {
		log.Error().Err(err).Msg("auth: issuing reset token failed")
		return
	}
	subject := orDefault(pr.Subject, "Reset your password")
	htmlBody := renderLinkEmail(pr.ResetURL, pr.Body,
		`<p>We received a request to reset your password.</p><p><a href="{{link}}">Reset your password</a></p><p>If you did not ask for this, you can ignore this email.</p>`,
		plain)
	h.queueEmail(ctx, email, subject, htmlBody, "password_reset")
}

// resetPassword handles POST /auth/password/reset {token, password}. On success it
// revokes every session (refresh tokens + token version) for the account.
func (h *Handler) resetPassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	minLen := 0
	if h.cfg.Register != nil {
		minLen = h.cfg.Register.MinPasswordLength
	}
	if msg := checkPassword(body.Password, minLen); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	entity, dbc, err := h.resolveEntity()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "configuration error")
		return
	}
	userID, err := consumeOneTimeToken(r.Context(), dbc, body.Token, purposeReset)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid or expired token")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(body.Password), bcryptCost)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	hashField := "password_hash"
	if h.cfg.Register != nil && h.cfg.Register.HashField != "" {
		hashField = h.cfg.Register.HashField
	}
	d := dbc.Dialect
	set := []string{d.QuoteIdent(fieldToColumn(entity, hashField)) + " = $1"}
	args := []any{string(hash)}
	// Completing a reset proves control of the inbox.
	if entity.HasColumn("email_verified_at") {
		set = append(set, fmt.Sprintf("%s = COALESCE(%s, $2)", d.QuoteIdent("email_verified_at"), d.QuoteIdent("email_verified_at")))
		args = append(args, time.Now().UTC())
	}
	args = append(args, userID)
	q := d.Rebind(fmt.Sprintf(`UPDATE %s SET %s WHERE %s = $%d`,
		d.QuoteIdent(entity.Table), strings.Join(set, ", "), d.QuoteIdent(entity.PKColumn()), len(args)))
	if _, err := dbc.SQL.ExecContext(r.Context(), q, args...); err != nil {
		writeError(w, http.StatusInternalServerError, "could not update password")
		return
	}
	_ = h.revokeAllForUser(r.Context(), dbc, userID)
	_ = h.bumpTokenVersion(r.Context(), dbc, entity, userID)
	h.clearRefreshCookie(w)
	writeJSON(w, http.StatusOK, map[string]string{"status": "password updated"})
}

// ── email verification ────────────────────────────────────────────────────────

// sendVerificationEmail issues a verification token for user and emails it.
func (h *Handler) sendVerificationEmail(ctx context.Context, dbc *db.DB, entity *schema.Entity, user map[string]any) {
	ev := h.cfg.EmailVerification
	email := asString(user["email"])
	if email == "" {
		return
	}
	ttl := defaultVerifyExpiry
	if d, err := parseDuration(ev.Expiry); ev.Expiry != "" && err == nil && d > 0 {
		ttl = d
	}
	plain, err := issueOneTimeToken(ctx, dbc, anyToString(user[entity.PKColumn()]), purposeVerify, ttl)
	if err != nil {
		log.Error().Err(err).Msg("auth: issuing verification token failed")
		return
	}
	subject := orDefault(ev.Subject, "Verify your email address")
	htmlBody := renderLinkEmail(ev.VerifyURL, ev.Body,
		`<p>Please confirm your email address.</p><p><a href="{{link}}">Verify email</a></p>`, plain)
	h.queueEmail(ctx, email, subject, htmlBody, "email_verification")
}

// verifyEmail handles POST /auth/verify-email {token}.
func (h *Handler) verifyEmail(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	entity, dbc, err := h.resolveEntity()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "configuration error")
		return
	}
	userID, err := consumeOneTimeToken(r.Context(), dbc, body.Token, purposeVerify)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid or expired token")
		return
	}
	d := dbc.Dialect
	q := d.Rebind(fmt.Sprintf(`UPDATE %s SET %s = $1 WHERE %s = $2 AND %s IS NULL`,
		d.QuoteIdent(entity.Table), d.QuoteIdent("email_verified_at"), d.QuoteIdent(entity.PKColumn()), d.QuoteIdent("email_verified_at")))
	if _, err := dbc.SQL.ExecContext(r.Context(), q, time.Now().UTC(), userID); err != nil {
		writeError(w, http.StatusInternalServerError, "could not verify email")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "email verified"})
}

// resendVerification handles POST /auth/verify-email/resend (authenticated).
func (h *Handler) resendVerification(w http.ResponseWriter, r *http.Request) {
	sub := middleware.GetSubject(r)
	if sub == nil || sub.ID == "" {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	if last, ok := resendGuard.Load(sub.ID); ok && time.Since(last.(time.Time)) < resendCooldown {
		writeError(w, http.StatusTooManyRequests, "please wait before requesting another email")
		return
	}
	entity, dbc, err := h.resolveEntity()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "configuration error")
		return
	}
	user, err := h.findUserBy(r.Context(), dbc, entity, entity.PKColumn(), sub.ID)
	if err != nil || user == nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	if user["email_verified_at"] != nil {
		writeJSON(w, http.StatusOK, map[string]string{"status": "already verified"})
		return
	}
	resendGuard.Store(sub.ID, time.Now())
	h.sendVerificationEmail(r.Context(), dbc, entity, user)
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "verification email sent"})
}
