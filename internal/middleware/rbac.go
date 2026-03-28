package middleware

import (
	"net/http"
	"strings"
)

// RBACEnforcer is the interface satisfied by policy.Engine.
type RBACEnforcer interface {
	Enforce(role, resource, action string) (bool, error)
}

// methodToAction maps HTTP methods to RBAC action names.
func methodToAction(method string) string {
	switch strings.ToUpper(method) {
	case http.MethodGet:
		return "get"
	case http.MethodPost:
		return "create"
	case http.MethodPatch, http.MethodPut:
		return "update"
	case http.MethodDelete:
		return "delete"
	default:
		return strings.ToLower(method)
	}
}

// RBAC returns an authorization middleware that enforces Casbin policies.
// resource is the entity name; requireAuth indicates whether auth is mandatory.
func RBAC(enforcer RBACEnforcer, resource string, requireAuth bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !requireAuth {
				next.ServeHTTP(w, r)
				return
			}

			sub := GetSubject(r)
			if sub == nil {
				writeJSONError(w, http.StatusUnauthorized, "authentication required")
				return
			}

			action := methodToAction(r.Method)
			allowed, err := enforcer.Enforce(sub.Role, resource, action)
			if err != nil {
				writeJSONError(w, http.StatusInternalServerError, "authorization error")
				return
			}
			if !allowed {
				writeJSONError(w, http.StatusForbidden, "forbidden")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
