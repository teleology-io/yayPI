package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

// Subject holds the authenticated user's identity extracted from a JWT.
type Subject struct {
	ID    string
	Role  string
	Email string
}

// RequireAuth is a JWT authentication middleware.
// If requireAuth is false, it still parses the token if present but does not reject
// requests without tokens.
func RequireAuth(secret []byte, algorithm string, requireAuth bool) func(http.Handler) http.Handler {
	parser := jwt.NewParser(
		jwt.WithValidMethods([]string{algorithm}),
		jwt.WithExpirationRequired(),
	)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// If a prior middleware (e.g. APIKeyAuth) already authenticated this
			// request, honour that and skip JWT processing.
			if GetSubject(r) != nil {
				next.ServeHTTP(w, r)
				return
			}

			tokenStr := extractBearerToken(r)

			if tokenStr == "" {
				if requireAuth {
					writeJSONError(w, http.StatusUnauthorized, "authentication required")
					return
				}
				next.ServeHTTP(w, r)
				return
			}

			// Always reject "none" algorithm
			if isNoneAlgorithm(tokenStr) {
				writeJSONError(w, http.StatusUnauthorized, "invalid token algorithm")
				return
			}

			token, err := parser.ParseWithClaims(tokenStr, &jwt.MapClaims{}, func(t *jwt.Token) (interface{}, error) {
				return secret, nil
			})
			if err != nil || !token.Valid {
				writeJSONError(w, http.StatusUnauthorized, "invalid or expired token")
				return
			}

			claims, ok := token.Claims.(*jwt.MapClaims)
			if !ok {
				writeJSONError(w, http.StatusUnauthorized, "invalid token claims")
				return
			}

			sub := extractSubject(claims)
			ctx := context.WithValue(r.Context(), ctxKeySubject, sub)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
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

// isNoneAlgorithm checks the JWT header without verification to detect "none" alg.
func isNoneAlgorithm(tokenStr string) bool {
	parts := strings.SplitN(tokenStr, ".", 3)
	if len(parts) < 1 {
		return false
	}
	// The jwt library will reject "none" via WithValidMethods, but belt-and-suspenders:
	headerJSON, err := jwt.NewParser().DecodeSegment(parts[0])
	if err != nil {
		return false
	}
	var header struct {
		Alg string `json:"alg"`
	}
	_ = json.Unmarshal(headerJSON, &header)
	return strings.ToLower(header.Alg) == "none"
}

// extractSubject extracts identity fields from JWT claims.
func extractSubject(claims *jwt.MapClaims) *Subject {
	sub := &Subject{}
	if v, ok := (*claims)["sub"].(string); ok {
		sub.ID = v
	}
	if v, ok := (*claims)["role"].(string); ok {
		sub.Role = v
	}
	if v, ok := (*claims)["email"].(string); ok {
		sub.Email = v
	}
	return sub
}

// writeJSONError writes a JSON error response.
func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
