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

	// Validate endpoint references
	for _, ef := range cfg.Endpoints {
		for _, ep := range ef.Endpoints {
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
