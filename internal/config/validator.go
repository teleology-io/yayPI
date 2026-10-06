package config

import (
	"fmt"
	"strings"
)

// ValidationError represents a single validation problem.
type ValidationError struct {
	File    string
	Message string
}

func (e ValidationError) Error() string {
	if e.File != "" {
		return fmt.Sprintf("%s: %s", e.File, e.Message)
	}
	return e.Message
}

// Validate performs semantic validation on a loaded RootConfig.
// It returns a slice of ValidationErrors (may be empty).
func Validate(cfg *RootConfig) []ValidationError {
	var errs []ValidationError

	// Build entity name set; always include the built-in User so FK refs to "User" resolve.
	entityNames := make(map[string]struct{})
	entityNames[BuiltinUserEntityName] = struct{}{}
	for _, ec := range cfg.Entities {
		entityNames[ec.Entity.Name] = struct{}{}
	}

	// Build role name set
	roleNames := make(map[string]struct{})
	for _, ep := range cfg.Endpoints {
		_ = ep // roles come from policy files
	}

	// Validate each entity
	for _, ec := range cfg.Entities {
		errs = append(errs, validateEntity(ec, entityNames)...)
	}

	softDelete := map[string]bool{BuiltinUserEntityName: true}
	for _, ec := range cfg.Entities {
		softDelete[ec.Entity.Name] = ec.Entity.SoftDelete
	}

	// Validate endpoint references
	for _, ef := range cfg.Endpoints {
		for _, ep := range ef.Endpoints {
			for _, op := range ep.CRUD {
				switch op {
				case "list", "get", "create", "update", "replace", "delete":
				default:
					errs = append(errs, ValidationError{File: ef.FilePath,
						Message: fmt.Sprintf("endpoint %q: unknown crud operation %q (list, get, create, update, replace, delete)", ep.Path, op)})
				}
			}
			if _, err := ParseByteSize(ep.MaxBodySize); err != nil {
				errs = append(errs, ValidationError{File: ef.FilePath, Message: fmt.Sprintf("endpoint %q: max_body_size: %v", ep.Path, err)})
			}
			if ep.Delete != nil && ep.Delete.SoftDelete && !softDelete[ep.Entity] {
				errs = append(errs, ValidationError{
					File:    ef.FilePath,
					Message: fmt.Sprintf("endpoint %q: delete.soft_delete requires entity %q to set soft_delete: true (it would hard-delete)", ep.Path, ep.Entity),
				})
			}
			if ep.Entity != "" {
				if _, ok := entityNames[ep.Entity]; !ok {
					errs = append(errs, ValidationError{
						File:    ef.FilePath,
						Message: fmt.Sprintf("endpoint %q references unknown entity %q", ep.Path, ep.Entity),
					})
				}
			}
			_ = roleNames
		}
	}

	// Detect circular entity references
	errs = append(errs, detectCircularRefs(cfg.Entities)...)

	errs = append(errs, validateSecrets(cfg)...)

	if _, err := ParseByteSize(cfg.Server.MaxRequestBodySize); err != nil {
		errs = append(errs, ValidationError{Message: "server.max_request_body_size: " + err.Error()})
	}
	for _, rl := range []*RateLimitConfig{cfg.Server.RateLimit} {
		if rl != nil && rl.KeyBy != "" && rl.KeyBy != "ip" && rl.KeyBy != "user" {
			errs = append(errs, ValidationError{Message: fmt.Sprintf("rate_limit.key_by %q must be ip or user", rl.KeyBy)})
		}
	}
	for name, v := range map[string]string{
		"outbox.poll_interval":   cfg.Outbox.PollInterval,
		"outbox.retention":       cfg.Outbox.Retention,
		"audit.retention":        cfg.Audit.Retention,
		"server.idempotency_ttl": cfg.Server.IdempotencyTTL,
	} {
		if _, err := ParseDuration(v); err != nil {
			errs = append(errs, ValidationError{Message: name + ": " + err.Error()})
		}
	}
	if _, err := ParseByteSize(cfg.Server.MaxHeaderBytes); err != nil {
		errs = append(errs, ValidationError{Message: "server.max_header_bytes: " + err.Error()})
	}

	return errs
}

// MinSecretBytes is the minimum length of auth.secret when any feature that signs with it
// (JWTs, refresh tokens, OAuth state) is in use. 32 bytes = 256 bits, the HS256 key size.
const MinSecretBytes = 32

// AuthInUse reports whether any configured feature relies on auth.secret for signing.
func AuthInUse(cfg *RootConfig) bool {
	if cfg.AuthEndpoint != nil {
		return true
	}
	for _, ef := range cfg.Endpoints {
		for _, ep := range ef.Endpoints {
			if authDeclared(ep.Auth) {
				return true
			}
			for _, a := range []*AuthRequirement{opAuth(ep.List), opAuth(ep.Get), opAuth(ep.Create), opAuth(ep.Update), opAuth(ep.Delete)} {
				if authDeclared(a) {
					return true
				}
			}
		}
	}
	return false
}

func authDeclared(a *AuthRequirement) bool {
	return a != nil && (a.Require || len(a.Roles) > 0 || len(a.Conditions) > 0)
}

// opAuth extracts the auth block from any per-operation config (nil-safe).
func opAuth(v any) *AuthRequirement {
	switch c := v.(type) {
	case *ListConfig:
		if c != nil {
			return c.Auth
		}
	case *GetConfig:
		if c != nil {
			return c.Auth
		}
	case *CreateConfig:
		if c != nil {
			return c.Auth
		}
	case *UpdateConfig:
		if c != nil {
			return c.Auth
		}
	case *DeleteConfig:
		if c != nil {
			return c.Auth
		}
	}
	return nil
}

// validateSecrets rejects missing or weak signing secrets and empty DSNs. A missing
// ${ENV_VAR} interpolates to "", so this is also what catches an unset variable.
func validateSecrets(cfg *RootConfig) []ValidationError {
	var errs []ValidationError
	if AuthInUse(cfg) {
		switch alg := cfg.Auth.Algorithm; {
		case strings.HasPrefix(alg, "RS"), strings.HasPrefix(alg, "ES"):
			if cfg.Auth.PrivateKeyFile == "" && cfg.Auth.PublicKeyFile == "" {
				errs = append(errs, ValidationError{Message: fmt.Sprintf("auth.algorithm %s requires auth.private_key_file (and/or public_key_file)", alg)})
			}
			if cfg.AuthEndpoint != nil && cfg.Auth.PrivateKeyFile == "" {
				errs = append(errs, ValidationError{Message: "auth endpoints issue tokens and need auth.private_key_file"})
			}
		case strings.HasPrefix(alg, "HS"):
			if len(cfg.Auth.Secret) == 0 {
				errs = append(errs, ValidationError{Message: "auth.secret is required when authentication is used (is the env var set?)"})
			} else if len(cfg.Auth.Secret) < MinSecretBytes {
				errs = append(errs, ValidationError{Message: fmt.Sprintf("auth.secret must be at least %d bytes (got %d)", MinSecretBytes, len(cfg.Auth.Secret))})
			}
		}
	}
	switch cfg.Auth.Algorithm {
	case "", "HS256", "HS384", "HS512", "RS256", "RS384", "RS512", "ES256", "ES384", "ES512":
	default:
		errs = append(errs, ValidationError{Message: fmt.Sprintf("auth.algorithm %q is not supported", cfg.Auth.Algorithm)})
	}
	for _, d := range cfg.Databases {
		if strings.TrimSpace(d.DSN) == "" {
			errs = append(errs, ValidationError{Message: fmt.Sprintf("database %q: dsn is empty (is the env var set?)", d.Name)})
		}
	}
	return errs
}

func validateEntity(ec *EntityConfig, entityNames map[string]struct{}) []ValidationError {
	var errs []ValidationError
	for _, f := range ec.Entity.Fields {
		if f.References != nil && f.References.Entity != "" {
			if _, ok := entityNames[f.References.Entity]; !ok {
				errs = append(errs, ValidationError{
					File:    ec.FilePath,
					Message: fmt.Sprintf("entity %q field %q references unknown entity %q", ec.Entity.Name, f.Name, f.References.Entity),
				})
			}
		}
	}
	for _, r := range ec.Entity.Relations {
		if r.Entity != "" {
			if _, ok := entityNames[r.Entity]; !ok {
				errs = append(errs, ValidationError{
					File:    ec.FilePath,
					Message: fmt.Sprintf("entity %q relation %q references unknown entity %q", ec.Entity.Name, r.Name, r.Entity),
				})
			}
		}
	}
	return errs
}

// detectCircularRefs checks for circular relationships in entity references.
func detectCircularRefs(entities []*EntityConfig) []ValidationError {
	var errs []ValidationError

	// Build adjacency map: entity name → set of referenced entity names (via FK fields)
	adj := make(map[string][]string)
	for _, ec := range entities {
		name := ec.Entity.Name
		for _, f := range ec.Entity.Fields {
			if f.References != nil && f.References.Entity != "" && f.References.Entity != name {
				adj[name] = append(adj[name], f.References.Entity)
			}
		}
	}

	// DFS-based cycle detection
	visited := make(map[string]int) // 0=unvisited, 1=in-progress, 2=done
	var dfs func(node string, path []string) bool
	dfs = func(node string, path []string) bool {
		if visited[node] == 2 {
			return false
		}
		if visited[node] == 1 {
			cycle := append(path, node)
			errs = append(errs, ValidationError{
				Message: fmt.Sprintf("circular entity reference detected: %s", strings.Join(cycle, " → ")),
			})
			return true
		}
		visited[node] = 1
		for _, neighbor := range adj[node] {
			dfs(neighbor, append(path, node))
		}
		visited[node] = 2
		return false
	}

	for _, ec := range entities {
		if visited[ec.Entity.Name] == 0 {
			dfs(ec.Entity.Name, nil)
		}
	}

	return errs
}
