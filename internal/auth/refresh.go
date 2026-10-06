package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/teleology-io/yayPI/internal/db"
	"github.com/teleology-io/yayPI/internal/middleware"
	"github.com/teleology-io/yayPI/internal/schema"
)

const (
	refreshTokenCookie   = "refresh_token"
	defaultRefreshExpiry = 30 * 24 * time.Hour
)

var (
	errRefreshInvalid = errors.New("invalid or expired refresh token")
	errRefreshReused  = errors.New("refresh token reuse detected")
)

func (h *Handler) refreshEnabled() bool {
	return h.cfg.Refresh != nil && h.cfg.Refresh.Enabled
}

func (h *Handler) refreshTTL() time.Duration {
	if h.cfg.Refresh != nil && h.cfg.Refresh.Expiry != "" {
		if d, err := parseDuration(h.cfg.Refresh.Expiry); err == nil && d > 0 {
			return d
		}
	}
	return defaultRefreshExpiry
}

func (h *Handler) refreshInCookie() bool {
	return h.cfg.Refresh == nil || h.cfg.Refresh.Store == "" || h.cfg.Refresh.Store == "cookie"
}

// newOpaqueToken returns a random URL-safe token and its SHA-256 hex digest. Only the
// digest is stored, so a database leak does not yield usable tokens.
func newOpaqueToken() (plain, hash string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	plain = base64.RawURLEncoding.EncodeToString(b)
	return plain, hashToken(plain), nil
}

func hashToken(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}

// issueRefreshToken stores a new refresh token for userID. familyID groups the rotation
// chain of one login session; "" starts a new family.
func (h *Handler) issueRefreshToken(ctx context.Context, dbc *db.DB, userID, familyID string) (string, error) {
	plain, hash, err := newOpaqueToken()
	if err != nil {
		return "", err
	}
	if familyID == "" {
		familyID = uuid.NewString()
	}
	now := time.Now()
	d := dbc.Dialect
	q := d.Rebind(fmt.Sprintf(
		`INSERT INTO %s (id, user_id, family_id, token_hash, expires_at, created_at) VALUES ($1, $2, $3, $4, $5, $6)`,
		d.QuoteIdent(schema.RefreshTokenTable)))
	_, err = dbc.SQL.ExecContext(ctx, q, uuid.NewString(), userID, familyID, hash, now.Add(h.refreshTTL()).Unix(), now.Unix())
	if err != nil {
		return "", fmt.Errorf("storing refresh token: %w", err)
	}
	return plain, nil
}

// deliverRefreshToken sends the token as an HttpOnly cookie (store: cookie) or in the
// response body (store: body).
func (h *Handler) deliverRefreshToken(w http.ResponseWriter, resp map[string]any, plain string) {
	if h.refreshInCookie() {
		h.setCookie(w, refreshTokenCookie, plain, int(h.refreshTTL().Seconds()))
		return
	}
	resp["refresh_token"] = plain
}

// readRefreshToken extracts the presented refresh token from cookie or JSON body.
// ok=false means the response has already been written.
func (h *Handler) readRefreshToken(w http.ResponseWriter, r *http.Request) (string, bool) {
	if h.refreshInCookie() {
		c, err := r.Cookie(refreshTokenCookie)
		if err != nil || c.Value == "" {
			writeError(w, http.StatusUnauthorized, "refresh token missing")
			return "", false
		}
		return c.Value, true
	}
	var body struct {
		RefreshToken string `json:"refresh_token"`
	}
	if !decodeBody(w, r, &body) {
		return "", false
	}
	if body.RefreshToken == "" {
		writeError(w, http.StatusUnauthorized, "refresh token missing")
		return "", false
	}
	return body.RefreshToken, true
}

// rotateRefreshToken consumes plain and returns (userID, familyID). A token presented a
// second time means it was stolen (either the attacker or the user already rotated it):
// the whole family is revoked so neither party keeps a valid session.
func (h *Handler) rotateRefreshToken(ctx context.Context, dbc *db.DB, plain string) (string, string, error) {
	d := dbc.Dialect
	table := d.QuoteIdent(schema.RefreshTokenTable)
	hash := hashToken(plain)

	rows, err := dbc.SQL.QueryContext(ctx, d.Rebind(fmt.Sprintf(
		`SELECT id, user_id, family_id, expires_at, used_at, revoked_at FROM %s WHERE token_hash = $1`, table)), hash)
	if err != nil {
		return "", "", err
	}
	row, err := scanRow(rows)
	rows.Close()
	if err != nil {
		return "", "", err
	}
	if row == nil {
		return "", "", errRefreshInvalid
	}
	id, userID, familyID := asString(row["id"]), asString(row["user_id"]), asString(row["family_id"])
	now := time.Now().Unix()

	if row["revoked_at"] != nil {
		return "", "", errRefreshInvalid
	}
	if row["used_at"] != nil {
		_ = h.revokeFamily(ctx, dbc, familyID)
		return "", "", errRefreshReused
	}
	if exp, _ := toInt64(row["expires_at"]); exp <= now {
		return "", "", errRefreshInvalid
	}

	// Conditional update: of two concurrent refreshes with the same token only one wins;
	// the loser is treated as reuse.
	res, err := dbc.SQL.ExecContext(ctx, d.Rebind(fmt.Sprintf(
		`UPDATE %s SET used_at = $1 WHERE id = $2 AND used_at IS NULL AND revoked_at IS NULL`, table)), now, id)
	if err != nil {
		return "", "", err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		_ = h.revokeFamily(ctx, dbc, familyID)
		return "", "", errRefreshReused
	}
	return userID, familyID, nil
}

func (h *Handler) revokeFamily(ctx context.Context, dbc *db.DB, familyID string) error {
	d := dbc.Dialect
	_, err := dbc.SQL.ExecContext(ctx, d.Rebind(fmt.Sprintf(
		`UPDATE %s SET revoked_at = $1 WHERE family_id = $2 AND revoked_at IS NULL`,
		d.QuoteIdent(schema.RefreshTokenTable))), time.Now().Unix(), familyID)
	return err
}

// revokeAllForUser revokes every refresh token belonging to userID.
func (h *Handler) revokeAllForUser(ctx context.Context, dbc *db.DB, userID string) error {
	d := dbc.Dialect
	_, err := dbc.SQL.ExecContext(ctx, d.Rebind(fmt.Sprintf(
		`UPDATE %s SET revoked_at = $1 WHERE user_id = $2 AND revoked_at IS NULL`,
		d.QuoteIdent(schema.RefreshTokenTable))), time.Now().Unix(), userID)
	return err
}

// refresh handles POST /auth/refresh: it consumes the presented refresh token, issues a
// new one in the same family, and returns a fresh access token.
func (h *Handler) refresh(w http.ResponseWriter, r *http.Request) {
	plain, ok := h.readRefreshToken(w, r)
	if !ok {
		return
	}
	entity, dbc, err := h.resolveEntity()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "configuration error")
		return
	}

	userID, familyID, err := h.rotateRefreshToken(r.Context(), dbc, plain)
	if err != nil {
		if errors.Is(err, errRefreshReused) {
			log.Warn().Str("ip", middleware.GetClientIP(r)).Msg("auth: refresh token reuse detected; session family revoked")
		}
		if errors.Is(err, errRefreshInvalid) || errors.Is(err, errRefreshReused) {
			h.clearRefreshCookie(w)
			writeError(w, http.StatusUnauthorized, "invalid or expired refresh token")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Reload the user so the new access token carries the current role / token version.
	user, err := h.findUserBy(r.Context(), dbc, entity, entity.PKColumn(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if user == nil {
		_ = h.revokeFamily(r.Context(), dbc, familyID)
		h.clearRefreshCookie(w)
		writeError(w, http.StatusUnauthorized, "user not found")
		return
	}

	access, err := h.issueAccessToken(entity, user)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not issue token")
		return
	}
	newRefresh, err := h.issueRefreshToken(r.Context(), dbc, userID, familyID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not issue refresh token")
		return
	}
	resp := map[string]any{"token": access, "expires_in": int(h.tokens.TTL().Seconds())}
	h.deliverRefreshToken(w, resp, newRefresh)
	writeJSON(w, http.StatusOK, resp)
}

// logout handles POST /auth/logout: revokes the presented refresh token's session family
// and clears the cookie. Always 204 so it is safe to call with a stale token.
func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	var plain string
	if h.refreshInCookie() {
		if c, err := r.Cookie(refreshTokenCookie); err == nil {
			plain = c.Value
		}
	} else {
		var body struct {
			RefreshToken string `json:"refresh_token"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		plain = body.RefreshToken
	}
	if plain != "" {
		if _, dbc, err := h.resolveEntity(); err == nil {
			d := dbc.Dialect
			var familyID string
			err := dbc.SQL.QueryRowContext(r.Context(), d.Rebind(fmt.Sprintf(
				`SELECT family_id FROM %s WHERE token_hash = $1`, d.QuoteIdent(schema.RefreshTokenTable))),
				hashToken(plain)).Scan(&familyID)
			if err == nil {
				_ = h.revokeFamily(r.Context(), dbc, familyID)
			}
		}
	}
	h.clearRefreshCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

// logoutAll handles POST /auth/logout-all (authenticated): revokes every refresh token
// for the caller and bumps their token version so outstanding access tokens die too.
func (h *Handler) logoutAll(w http.ResponseWriter, r *http.Request) {
	sub := middleware.GetSubject(r)
	if sub == nil || sub.ID == "" {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	entity, dbc, err := h.resolveEntity()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "configuration error")
		return
	}
	if err := h.revokeAllForUser(r.Context(), dbc, sub.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if err := h.bumpTokenVersion(r.Context(), dbc, entity, sub.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	h.clearRefreshCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) clearRefreshCookie(w http.ResponseWriter) {
	if h.refreshInCookie() {
		h.setCookie(w, refreshTokenCookie, "", -1)
	}
}

// parseDuration extends time.ParseDuration with day support ("30d").
func parseDuration(s string) (time.Duration, error) {
	if strings.HasSuffix(s, "d") {
		var days int
		if _, err := fmt.Sscanf(s, "%dd", &days); err == nil {
			return time.Duration(days) * 24 * time.Hour, nil
		}
	}
	return time.ParseDuration(s)
}
