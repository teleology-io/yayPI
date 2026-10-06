package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"

	"github.com/teleology-io/yayPI/internal/apierr"
	"github.com/teleology-io/yayPI/internal/token"
)

// Subject holds the authenticated user's identity extracted from a JWT.
type Subject struct {
	ID     string
	Role   string
	Email  string
	Tenant string // from the "tenant" claim; scopes tenant_scoped entities
}

// SubjectValidator re-checks a verified token's subject against current state (e.g. the
// user still exists and the token version has not been bumped by logout-all or a password
// reset). It returns the subject to use — typically with the current role — or an error
// to reject the request. tokenVersion is the token's "tv" claim (0 when absent).
type SubjectValidator func(ctx context.Context, sub *Subject, tokenVersion int64) (*Subject, error)

// RequireAuth is a JWT authentication middleware. Tokens are verified with keys (which
// pins the algorithm, issuer, audience and expiry). If requireAuth is false it still
// parses a token when present but does not reject anonymous requests; a present but
// invalid token is always rejected. validate may be nil.
func RequireAuth(keys *token.Keys, requireAuth bool, validate SubjectValidator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// If a prior middleware (e.g. APIKeyAuth) already authenticated this
			// request, honour that and skip JWT processing.
			if GetSubject(r) != nil {
				next.ServeHTTP(w, r)
				return
			}

			tokenStr := extractBearerToken(r)
			if tokenStr == "" || keys == nil {
				if requireAuth {
					writeJSONError(w, http.StatusUnauthorized, "authentication required")
					return
				}
				next.ServeHTTP(w, r)
				return
			}

			claims, err := keys.Parse(tokenStr)
			if err != nil {
				writeJSONError(w, http.StatusUnauthorized, "invalid or expired token")
				return
			}

			sub := extractSubject(claims)
			if sub.ID == "" {
				writeJSONError(w, http.StatusUnauthorized, "invalid token claims")
				return
			}
			if validate != nil {
				tv, _ := claims["tv"].(float64)
				sub, err = validate(r.Context(), sub, int64(tv))
				if err != nil || sub == nil {
					writeJSONError(w, http.StatusUnauthorized, "token revoked")
					return
				}
			}
			next.ServeHTTP(w, r.WithContext(WithSubject(r.Context(), sub)))
		})
	}
}

// subjectHolder lets the outer logging middleware learn the subject that inner auth
// middleware resolved (contexts only flow inward).
type subjectHolder struct{ sub *Subject }

type subjectHolderKey struct{}

func withSubjectHolder(ctx context.Context, h *subjectHolder) context.Context {
	return context.WithValue(ctx, subjectHolderKey{}, h)
}

// WithSubject returns a copy of ctx carrying sub (for tests and custom auth flows).
func WithSubject(ctx context.Context, sub *Subject) context.Context {
	if h, ok := ctx.Value(subjectHolderKey{}).(*subjectHolder); ok {
		h.sub = sub
	}
	return context.WithValue(ctx, ctxKeySubject, sub)
}

// GetSubject retrieves the authenticated Subject from the request context.
func GetSubject(r *http.Request) *Subject {
	s, _ := r.Context().Value(ctxKeySubject).(*Subject)
	return s
}

// SubjectFromContext retrieves the authenticated Subject directly from a context.
func SubjectFromContext(ctx context.Context) *Subject {
	s, _ := ctx.Value(ctxKeySubject).(*Subject)
	return s
}

// SubjectAttr returns a named attribute of the subject.
// Supported keys: "id", "role", "email". Returns "" for nil subject or unknown key.
func SubjectAttr(s *Subject, key string) string {
	if s == nil {
		return ""
	}
	switch key {
	case "id":
		return s.ID
	case "role":
		return s.Role
	case "email":
		return s.Email
	case "tenant":
		return s.Tenant
	}
	return ""
}

// extractBearerToken extracts the token from the Authorization header.
func extractBearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(auth, "Bearer ")
}

// extractSubject extracts identity fields from JWT claims.
func extractSubject(claims jwt.MapClaims) *Subject {
	sub := &Subject{}
	if v, ok := claims["sub"].(string); ok {
		sub.ID = v
	}
	if v, ok := claims["role"].(string); ok {
		sub.Role = v
	}
	if v, ok := claims["email"].(string); ok {
		sub.Email = v
	}
	if v, ok := claims["tenant"].(string); ok {
		sub.Tenant = v
	}
	return sub
}

// writeJSONError writes the standard error envelope. The request ID header is set by the
// RequestID middleware before anything else runs, so it is read back from the response.
func writeJSONError(w http.ResponseWriter, status int, msg string) {
	apierr.Write(w, w.Header().Get(RequestIDHeader), status, msg)
}
