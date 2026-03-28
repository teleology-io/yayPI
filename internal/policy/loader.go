package policy

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// RoleConfig describes a role and its permissions.
type RoleConfig struct {
	Name        string             `yaml:"name"`
	Inherits    []string           `yaml:"inherits"`
	Permissions []PermissionConfig `yaml:"permissions"`
}

// PermissionConfig describes a permission entry.
type PermissionConfig struct {
	Resource string   `yaml:"resource"`
	Actions  []string `yaml:"actions"`
}

// PolicyFile is the top-level structure for a roles.yaml file.
type PolicyFile struct {
	Version string       `yaml:"version"`
	Kind    string       `yaml:"kind"`
	Roles   []RoleConfig `yaml:"roles"`
}

// LoadRolesFile reads a roles.yaml file and returns the roles.
func LoadRolesFile(path string) ([]RoleConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading roles file %s: %w", path, err)
	}

	var pf PolicyFile
	if err := yaml.Unmarshal(data, &pf); err != nil {
		return nil, fmt.Errorf("parsing roles file %s: %w", path, err)
	}

	return pf.Roles, nil
}

// LoadRolesDir reads all *.yaml files in a directory that have kind: policy.
func LoadRolesDir(dir string) ([]RoleConfig, error) {
	entries, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return nil, fmt.Errorf("globbing policy dir %s: %w", dir, err)
	}

	var allRoles []RoleConfig
	for _, path := range entries {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}
		var pf PolicyFile
		if err := yaml.Unmarshal(data, &pf); err != nil {
			continue // skip non-policy files
		}
		if pf.Kind != "policy" {
			continue
		}
		allRoles = append(allRoles, pf.Roles...)
	}
	return allRoles, nil
}
