package policy

import (
	"fmt"
	"slices"

	"github.com/casbin/casbin/v2"
	"github.com/casbin/casbin/v2/model"
	"github.com/casbin/casbin/v2/persist"
)

// AllActions is the canonical set of CRUD actions a permission's `actions: ["*"]` wildcard
// expands to.
var AllActions = []string{"list", "get", "create", "update", "delete"}

// Engine wraps a Casbin enforcer.
type Engine struct {
	enforcer *casbin.Enforcer
}

// NewEngine creates a policy Engine from model and policy files.
func NewEngine(modelPath string, policyPath string) (*Engine, error) {
	enforcer, err := casbin.NewEnforcer(modelPath, policyPath)
	if err != nil {
		return nil, fmt.Errorf("creating casbin enforcer: %w", err)
	}
	return &Engine{enforcer: enforcer}, nil
}

// NewEngineFromModel creates a policy Engine from an in-memory model and adapter.
func NewEngineFromModel(m model.Model, adapter persist.Adapter) (*Engine, error) {
	enforcer, err := casbin.NewEnforcer(m, adapter)
	if err != nil {
		return nil, fmt.Errorf("creating casbin enforcer from model: %w", err)
	}
	return &Engine{enforcer: enforcer}, nil
}

// Enforce checks whether role is allowed to perform action on resource.
func (e *Engine) Enforce(role, resource, action string) (bool, error) {
	return e.enforcer.Enforce(role, resource, action)
}

// LoadFromRolesConfig populates the Casbin enforcer from a slice of RoleConfig entries.
// allResources is the full set of known entity names, used to expand a `resource: "*"`
// permission into one policy line per entity — Casbin's default matcher does literal string
// equality on obj, so a literal "*" policy row would never match a real resource on its own.
func (e *Engine) LoadFromRolesConfig(roles []RoleConfig, allResources []string) error {
	// Clear existing policies
	e.enforcer.ClearPolicy()

	// Add permissions
	for _, role := range roles {
		for _, perm := range role.Permissions {
			resources := []string{perm.Resource}
			if perm.Resource == "*" {
				resources = allResources
			}

			actions := perm.Actions
			if slices.Contains(actions, "*") {
				actions = AllActions
			}

			for _, resource := range resources {
				for _, action := range actions {
					if _, err := e.enforcer.AddPolicy(role.Name, resource, action); err != nil {
						return fmt.Errorf("adding policy for role %q: %w", role.Name, err)
					}
				}
			}
		}
	}

	// Add inheritance (g entries)
	for _, role := range roles {
		for _, parent := range role.Inherits {
			if _, err := e.enforcer.AddRoleForUser(role.Name, parent); err != nil {
				return fmt.Errorf("adding role inheritance %q → %q: %w", role.Name, parent, err)
			}
		}
	}

	return nil
}
