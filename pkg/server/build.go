package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/teleology-io/yayPI/internal/config"
	"github.com/teleology-io/yayPI/internal/db"
	"github.com/teleology-io/yayPI/internal/middleware"
	"github.com/teleology-io/yayPI/internal/migration"
	"github.com/teleology-io/yayPI/internal/policy"
	"github.com/teleology-io/yayPI/internal/schema"
	"github.com/teleology-io/yayPI/internal/token"
)

// buildTokenKeys returns the access-token signer/verifier, or nil when no auth is in use.
func buildTokenKeys(cfg *config.RootConfig) (*token.Keys, error) {
	if !config.AuthInUse(cfg) && cfg.Auth.Secret == "" && cfg.Auth.PublicKeyFile == "" && cfg.Auth.PrivateKeyFile == "" {
		return nil, nil
	}
	ttl, err := accessTTL(cfg)
	if err != nil {
		return nil, err
	}
	issuer := cfg.Auth.Issuer
	if issuer == "" {
		issuer = cfg.Project.Name
	}
	return token.New(token.Config{
		Algorithm:      cfg.Auth.Algorithm,
		Secret:         []byte(cfg.Auth.Secret),
		PrivateKeyFile: cfg.Auth.PrivateKeyFile,
		PublicKeyFile:  cfg.Auth.PublicKeyFile,
		KeyID:          cfg.Auth.KeyID,
		Issuer:         issuer,
		RequireIssuer:  cfg.Auth.Issuer != "", // only verify iss when explicitly configured
		Audience:       cfg.Auth.Audience,
		AccessTTL:      ttl,
	})
}

// accessTTL resolves auth.expiry; unset defaults to 15m when refresh tokens are enabled
// (short-lived access, long-lived refresh) and 1h otherwise.
func accessTTL(cfg *config.RootConfig) (time.Duration, error) {
	if cfg.Auth.Expiry != "" {
		d, err := time.ParseDuration(cfg.Auth.Expiry)
		if err != nil || d <= 0 {
			return 0, fmt.Errorf("auth.expiry %q is not a valid duration", cfg.Auth.Expiry)
		}
		return d, nil
	}
	if ae := cfg.AuthEndpoint; ae != nil && ae.Auth.Refresh != nil && ae.Auth.Refresh.Enabled {
		return 15 * time.Minute, nil
	}
	return time.Hour, nil
}

// autoMigrate applies the schema diff for every database directly (no files written),
// under the migration lock. Intended for development; production should run
// `yaypi migrate generate` + `yaypi migrate up` as a deploy step.
func autoMigrate(dbm *db.Manager, reg *schema.Registry) error {
	ctx := context.Background()
	for _, name := range dbm.Names() {
		dbc, _ := dbm.Get(name)
		engine := migration.NewEngine(dbc.SQL, dbc.Dialect, reg).ForDatabase(name, name == dbm.DefaultName())
		stmts, err := engine.Diff(ctx)
		if err != nil {
			return fmt.Errorf("database %q: schema diff: %w", name, err)
		}
		if len(stmts) == 0 {
			continue
		}
		for _, s := range stmts {
			log.Info().Str("database", name).Msg("auto_migrate: " + s.Description)
		}
		if err := migration.ApplyStatements(ctx, dbc.SQL, dbc.Dialect, stmts); err != nil {
			return fmt.Errorf("database %q: %w", name, err)
		}
	}
	return nil
}

// corsOptions maps server CORS config to middleware options.
func corsOptions(sc config.ServerConfig) middleware.CORSOptions {
	opts := middleware.CORSOptions{AllowedOrigins: sc.AllowedOrigins}
	if c := sc.CORS; c != nil {
		opts.AllowedHeaders = c.AllowedHeaders
		opts.AllowedMethods = c.AllowedMethods
		opts.ExposedHeaders = c.ExposedHeaders
		opts.MaxAge = c.MaxAge
	}
	return opts
}

// buildPolicyEngine returns nil when no policy engine is configured, and an error for any
// misconfiguration (unknown engine/adapter, missing model, unreadable roles).
// Relative paths resolve against the directory holding yaypi.yaml, not the process CWD.
func buildPolicyEngine(pc config.PolicyConfig, reg *schema.Registry, rootDir string) (*policy.Engine, error) {
	if pc.Engine == "" {
		return nil, nil
	}
	if pc.Engine != "casbin" {
		return nil, fmt.Errorf("unsupported policy engine %q (supported: casbin)", pc.Engine)
	}
	if pc.Model == "" {
		return nil, fmt.Errorf("policy.model is required when policy.engine is casbin")
	}
	adapter := pc.Adapter
	if adapter == "" {
		adapter = "file"
	}
	if adapter != "file" {
		return nil, fmt.Errorf("unsupported policy adapter %q (supported: file)", pc.Adapter)
	}
	roles, err := policy.LoadRolesDir(filepath.Join(rootDir, "policies"))
	if err != nil {
		return nil, fmt.Errorf("loading roles: %w", err)
	}
	model := pc.Model
	if !filepath.IsAbs(model) {
		model = filepath.Join(rootDir, model)
	}
	entityNames := make([]string, 0, len(reg.Entities()))
	for _, entity := range reg.Entities() {
		if !entity.Internal {
			entityNames = append(entityNames, entity.Name)
		}
	}
	return buildInMemoryPolicy(model, roles, entityNames)
}

func buildInMemoryPolicy(modelPath string, roles []policy.RoleConfig, allResources []string) (*policy.Engine, error) {
	tmpFile, err := os.CreateTemp("", "yaypi-policy-*.csv")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	pe, err := policy.NewEngine(modelPath, tmpFile.Name())
	if err != nil {
		return nil, err
	}

	if err := pe.LoadFromRolesConfig(roles, allResources); err != nil {
		return nil, err
	}

	return pe, nil
}
