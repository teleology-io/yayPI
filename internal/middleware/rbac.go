package middleware

import (
	"net/http"
	"strings"
)

// RBACEnforcer is the interface satisfied by policy.Engine.
type RBACEnforcer interface {
	Enforce(role, resource, action string) (bool, error)
}

// AuthOpts holds the auth requirements for a single endpoint operation.
// It mirrors schema.Auth but lives in middleware to avoid import cycles.
type AuthOpts struct {
	Require    bool
	Roles      []string
	Conditions []string
}

// ConditionChecker evaluates a list of condition expressions against a subject.
// Implementations must return (true, nil) when all conditions pass.
// To avoid an import cycle (middleware→policy→middleware), this is injected
// from the router layer (which imports both packages).
type ConditionChecker func(conditions []string, sub *Subject) (bool, error)

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

// RBAC returns an authorization middleware that enforces:
//  1. Casbin RBAC policy (role, resource, action)
//  2. auth.roles — subject's role must be in the allowed list (if non-empty)
//  3. auth.conditions — all CEL-lite expressions must pass (if non-empty)
//
// checkConditions may be nil if no conditions are expected (skips condition evaluation).
func RBAC(enforcer RBACEnforcer, resource string, auth AuthOpts, checkConditions ConditionChecker) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !auth.Require {
				next.ServeHTTP(w, r)
				return
			}

			sub := GetSubject(r)
			if sub == nil {
				writeJSONError(w, http.StatusUnauthorized, "authentication required")
				return
			}

			// Casbin policy check.
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

			// Role allowlist check (auth.roles).
			if len(auth.Roles) > 0 && !sliceContains(auth.Roles, sub.Role) {
				writeJSONError(w, http.StatusForbidden, "insufficient role")
				return
			}

			// Attribute condition check (auth.conditions).
			if len(auth.Conditions) > 0 && checkConditions != nil {
				ok, err := checkConditions(auth.Conditions, sub)
				if err != nil {
					writeJSONError(w, http.StatusInternalServerError, "condition evaluation error")
					return
				}
				if !ok {
					writeJSONError(w, http.StatusForbidden, "access denied")
					return
				}
			}

			next.ServeHTTP(w, r)
		})
	}
}

// sliceContains reports whether s is in the slice.
func sliceContains(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}
