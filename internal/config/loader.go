package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// sensitiveKeys are field key substrings that should not have plain-text values.
var sensitiveKeys = []string{"secret", "password", "token", "key", "dsn"}

// envVarRe matches ${VAR} and ${VAR:-default}.
var envVarRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(?::-([^}]*))?\}`)

// interpolateEnvVars replaces ${VAR} and ${VAR:-default} patterns in raw YAML bytes.
func interpolateEnvVars(data []byte) []byte {
	return envVarRe.ReplaceAllFunc(data, func(match []byte) []byte {
		s := string(match)
		// Extract var name and optional default
		sub := envVarRe.FindStringSubmatch(s)
		if sub == nil {
			return match
		}
		varName := sub[1]
		defVal := sub[2]
		val, ok := os.LookupEnv(varName)
		if !ok || val == "" {
			return []byte(defVal)
		}
		return []byte(val)
	})
}

// Load reads the root yaypi.yaml file and all included files, returning a merged RootConfig.
func Load(rootFile string) (*RootConfig, error) {
	rootDir := filepath.Dir(rootFile)

	rawRoot, err := os.ReadFile(rootFile)
	if err != nil {
		return nil, fmt.Errorf("reading root config %s: %w", rootFile, err)
	}
	rawRoot = interpolateEnvVars(rawRoot)

	var cfg RootConfig
	if err := yaml.Unmarshal(rawRoot, &cfg); err != nil {
		return nil, fmt.Errorf("parsing root config %s: %w", rootFile, err)
	}

	// Apply defaults
	applyServerDefaults(&cfg.Server)

	// Expand includes
	for _, pattern := range cfg.Include {
		if !filepath.IsAbs(pattern) {
			pattern = filepath.Join(rootDir, pattern)
		}
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return nil, fmt.Errorf("expanding include pattern %q: %w", pattern, err)
		}
		for _, match := range matches {
			if err := loadInclude(&cfg, match); err != nil {
				return nil, err
			}
		}
	}

	return &cfg, nil
}

// applyServerDefaults fills in zero-value server settings with sensible defaults.
func applyServerDefaults(s *ServerConfig) {
	if s.Port == 0 {
		s.Port = 8080
	}
	if s.ReadTimeout == 0 {
		s.ReadTimeout = 30e9 // 30s
	}
	if s.WriteTimeout == 0 {
		s.WriteTimeout = 30e9 // 30s
	}
	if s.ShutdownTimeout == 0 {
		s.ShutdownTimeout = 10e9 // 10s
	}
}

// loadInclude reads an included YAML file and appends its contents to cfg.
func loadInclude(cfg *RootConfig, path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading included file %s: %w", path, err)
	}
	raw = interpolateEnvVars(raw)

	// Peek at the kind field
	var meta struct {
		Kind string `yaml:"kind"`
	}
	if err := yaml.Unmarshal(raw, &meta); err != nil {
		return fmt.Errorf("parsing kind in %s: %w", path, err)
	}

	switch strings.ToLower(meta.Kind) {
	case "entity":
		var ec EntityConfig
		if err := yaml.Unmarshal(raw, &ec); err != nil {
			return fmt.Errorf("parsing entity file %s: %w", path, err)
		}
		ec.FilePath = path
		cfg.Entities = append(cfg.Entities, &ec)

	case "endpoints":
		var ef EndpointFileConfig
		if err := yaml.Unmarshal(raw, &ef); err != nil {
			return fmt.Errorf("parsing endpoints file %s: %w", path, err)
		}
		ef.FilePath = path
		cfg.Endpoints = append(cfg.Endpoints, &ef)

	case "jobs":
		var jc JobConfig
		if err := yaml.Unmarshal(raw, &jc); err != nil {
			return fmt.Errorf("parsing jobs file %s: %w", path, err)
		}
		cfg.Jobs = append(cfg.Jobs, &jc)

	case "policy":
		// Policies are handled by the policy package; skip here.

	default:
		// Unknown kind — ignore silently to support future extensions.
	}

	return nil
}

// WarnSensitiveValues checks for sensitive fields that are not env-var-referenced.
// Returns a list of warning messages.
func WarnSensitiveValues(cfg *RootConfig) []string {
	var warnings []string
	for _, db := range cfg.Databases {
		if isSensitivePlain(db.DSN) {
			warnings = append(warnings, fmt.Sprintf("database %q: dsn contains a plain-text value; use ${ENV_VAR} instead", db.Name))
		}
	}
	if isSensitivePlain(cfg.Auth.Secret) {
		warnings = append(warnings, "auth.secret contains a plain-text value; use ${ENV_VAR} instead")
	}
	return warnings
}

// isSensitivePlain returns true if a value looks like a plain credential (non-empty, not already resolved from env).
func isSensitivePlain(v string) bool {
	if v == "" {
		return false
	}
	// If after interpolation it still contains ${, it wasn't resolved (env var missing)
	if strings.Contains(v, "${") {
		return false
	}
	// Heuristic: if the value is one of a handful of safe placeholder strings, skip
	lower := strings.ToLower(v)
	for _, unsafe := range []string{"changeme", "secret", "password", "example"} {
		if strings.Contains(lower, unsafe) {
			// It's a known-bad placeholder — warn
			return true
		}
	}
	return false
}

// hasSensitiveKey checks if a key name contains a sensitive substring.
func hasSensitiveKey(key string) bool {
	lower := strings.ToLower(key)
	for _, s := range sensitiveKeys {
		if strings.Contains(lower, s) {
			return true
		}
	}
	return false
}

// AllJobDefs returns a flat list of all job definitions from all loaded job files.
func (cfg *RootConfig) AllJobDefs() []JobDef {
	var jobs []JobDef
	for _, jf := range cfg.Jobs {
		jobs = append(jobs, jf.Jobs...)
	}
	return jobs
}
