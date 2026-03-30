package config

import (
	"fmt"
	"io/fs"
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
		matches, err := globDoubleStar(pattern)
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

	case "auth":
		var ac AuthEndpointFileConfig
		if err := yaml.Unmarshal(raw, &ac); err != nil {
			return fmt.Errorf("parsing auth file %s: %w", path, err)
		}
		ac.FilePath = path
		cfg.AuthEndpoint = &ac

	case "seed":
		var sf SeedFileConfig
		if err := yaml.Unmarshal(raw, &sf); err != nil {
			return fmt.Errorf("parsing seed file %s: %w", path, err)
		}
		sf.FilePath = path
		cfg.SeedFiles = append(cfg.SeedFiles, &sf)

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

// globDoubleStar expands a glob pattern that may contain ** (double-star).
// Go's filepath.Glob does not support **, so we walk the directory tree when
// ** is present and match each file against the pattern segments manually.
func globDoubleStar(pattern string) ([]string, error) {
	if !strings.Contains(pattern, "**") {
		return filepath.Glob(pattern)
	}

	// Split pattern at the first ** segment to get a root dir to walk.
	parts := strings.SplitN(pattern, "**", 2)
	root := filepath.Clean(parts[0])
	suffix := strings.TrimPrefix(parts[1], string(filepath.Separator))

	var matches []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		if d.IsDir() {
			return nil
		}
		if suffix == "" {
			matches = append(matches, path)
			return nil
		}
		// Match only the filename (or relative tail) against the suffix pattern.
		ok, merr := filepath.Match(suffix, filepath.Base(path))
		if merr != nil {
			return merr
		}
		if ok {
			matches = append(matches, path)
		}
		return nil
	})
	return matches, err
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

// AllSeedDefs returns a flat list of all seed definitions from all loaded seed files.
func (cfg *RootConfig) AllSeedDefs() []SeedDef {
	var seeds []SeedDef
	for _, sf := range cfg.SeedFiles {
		seeds = append(seeds, sf.Seeds...)
	}
	return seeds
}
