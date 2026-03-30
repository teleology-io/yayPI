package auth

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	refreshTokenCookie  = "refresh_token"
	refreshTokenType    = "refresh"
	defaultRefreshExpiry = 30 * 24 * time.Hour
)

// issueRefreshToken creates a long-lived refresh JWT for the given user ID.
func (h *Handler) issueRefreshToken(userID string, ttl time.Duration) (string, error) {
	claims := jwt.MapClaims{
		"sub":  userID,
		"type": refreshTokenType,
		"iat":  time.Now().Unix(),
		"exp":  time.Now().Add(ttl).Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(h.secret)
}

// refresh handles POST /auth/refresh.
// Accepts a refresh token from cookie or JSON body, validates it, and returns a new access token.
func (h *Handler) refresh(w http.ResponseWriter, r *http.Request) {
	ref := h.cfg.Refresh
	if ref == nil || !ref.Enabled {
		writeError(w, http.StatusNotFound, "refresh not enabled")
		return
	}

	// Resolve TTL
	ttl := defaultRefreshExpiry
	if ref.Expiry != "" {
		if d, err := parseDuration(ref.Expiry); err == nil {
			ttl = d
		}
	}

	store := ref.Store
	if store == "" {
		store = "cookie"
	}

	// Extract refresh token
	var tokenStr string
	if store == "cookie" {
		c, err := r.Cookie(refreshTokenCookie)
		if err != nil || c.Value == "" {
			writeError(w, http.StatusUnauthorized, "refresh token missing")
			return
		}
		tokenStr = c.Value
	} else {
		// body: {"refresh_token": "..."}
		var body struct {
			RefreshToken string `json:"refresh_token"`
		}
		if err := decodeJSON(r, &body); err != nil || body.RefreshToken == "" {
			writeError(w, http.StatusUnauthorized, "refresh token missing")
			return
		}
		tokenStr = body.RefreshToken
	}

	// Parse and validate
	alg := h.algorithm
	if alg == "" {
		alg = "HS256"
	}
	parser := jwt.NewParser(
		jwt.WithValidMethods([]string{alg}),
		jwt.WithExpirationRequired(),
	)
	token, err := parser.ParseWithClaims(tokenStr, &jwt.MapClaims{}, func(_ *jwt.Token) (any, error) {
		return h.secret, nil
	})
	if err != nil || !token.Valid {
		writeError(w, http.StatusUnauthorized, "invalid or expired refresh token")
		return
	}

	claims, ok := token.Claims.(*jwt.MapClaims)
	if !ok {
		writeError(w, http.StatusUnauthorized, "invalid token claims")
		return
	}
	if typ, _ := (*claims)["type"].(string); typ != refreshTokenType {
		writeError(w, http.StatusUnauthorized, "not a refresh token")
		return
	}

	userID, _ := (*claims)["sub"].(string)
	if userID == "" {
		writeError(w, http.StatusUnauthorized, "invalid token subject")
		return
	}

	// Look up the user to get current role/email for the new access token
	entity, dbc, err := h.resolveEntity()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "configuration error")
		return
	}
	d := dbc.Dialect
	q := d.Rebind(fmt.Sprintf(`SELECT * FROM %s WHERE id = $1 AND deleted_at IS NULL LIMIT 1`,
		d.QuoteIdent(entity.Table)))
	rows, err := dbc.SQL.QueryContext(r.Context(), q, userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer rows.Close()
	user, err := scanRow(rows)
	if err != nil || user == nil {
		writeError(w, http.StatusUnauthorized, "user not found")
		return
	}

	// Issue a new access token
	accessToken, err := h.issueToken(user)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not issue token")
		return
	}

	// Issue a new refresh token and rotate it
	newRefresh, err := h.issueRefreshToken(userID, ttl)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not issue refresh token")
		return
	}

	if store == "cookie" {
		http.SetCookie(w, &http.Cookie{
			Name:     refreshTokenCookie,
			Value:    newRefresh,
			HttpOnly: true,
			Secure:   r.TLS != nil,
			SameSite: http.SameSiteLaxMode,
			MaxAge:   int(ttl.Seconds()),
			Path:     "/",
		})
		writeJSON(w, http.StatusOK, map[string]any{"token": accessToken})
	} else {
		writeJSON(w, http.StatusOK, map[string]any{
			"token":         accessToken,
			"refresh_token": newRefresh,
		})
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

// decodeJSON decodes a JSON request body into v.
func decodeJSON(r *http.Request, v any) error {
	return json.NewDecoder(r.Body).Decode(v)
}
