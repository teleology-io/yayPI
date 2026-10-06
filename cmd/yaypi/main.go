package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"

	"github.com/teleology-io/yayPI/internal/apikey"
	"github.com/teleology-io/yayPI/internal/config"
	"github.com/teleology-io/yayPI/internal/db"
	"github.com/teleology-io/yayPI/internal/migration"
	"github.com/teleology-io/yayPI/internal/openapi"
	"github.com/teleology-io/yayPI/internal/schema"
	"github.com/teleology-io/yayPI/internal/seed"
	"github.com/teleology-io/yayPI/pkg/server"
)

func main() {
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})

	rootCmd := &cobra.Command{
		Use:   "yaypi",
		Short: "yaypi — YAML-Powered API framework",
		Long:  "yaypi generates a fully functional REST API backend from YAML configuration files.",
	}

	var configFile string
	rootCmd.PersistentFlags().StringVarP(&configFile, "config", "c", "yaypi.yaml", "path to yaypi.yaml config file")

	rootCmd.AddCommand(
		newRunCmd(&configFile),
		newValidateCmd(&configFile),
		newMigrateCmd(&configFile),
		newInitCmd(),
		newSpecCmd(&configFile),
		newSeedCmd(&configFile),
		newAPIKeyCmd(),
	)

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

// newAPIKeyCmd creates `yaypi apikey generate`, which prints a new API key and the
// SHA-256 digest to store in the api_keys table (key_hash: sha256).
func newAPIKeyCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "apikey", Short: "API key utilities"}
	cmd.AddCommand(&cobra.Command{
		Use:   "generate",
		Short: "Generate an API key and the digest to store in the database",
		RunE: func(cmd *cobra.Command, args []string) error {
			key, hash, err := apikey.Generate()
			if err != nil {
				return err
			}
			fmt.Printf("key:    %s   (give this to the client; it is not stored)\n", key)
			fmt.Printf("digest: %s   (store this in the key column)\n", hash)
			return nil
		},
	})
	return cmd
}

// newSeedCmd creates the `yaypi seed` subcommand.
func newSeedCmd(configFile *string) *cobra.Command {
	return &cobra.Command{
		Use:   "seed",
		Short: "Insert seed data (idempotent)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSeed(*configFile)
		},
	}
}

// runSeed loads config and applies all seed definitions.
func runSeed(configFile string) error {
	cfg, err := config.Load(configFile)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	seeds := cfg.AllSeedDefs()
	if len(seeds) == 0 {
		log.Info().Msg("no seed definitions found")
		return nil
	}

	reg, err := schema.Build(cfg)
	if err != nil {
		return fmt.Errorf("building schema registry: %w", err)
	}

	if len(cfg.Databases) == 0 {
		return fmt.Errorf("no databases configured")
	}
	dbManager, err := db.NewManager(cfg.Databases)
	if err != nil {
		return fmt.Errorf("connecting to database: %w", err)
	}
	defer dbManager.Close()

	if err := seed.Run(context.Background(), seeds, reg, dbManager); err != nil {
		return fmt.Errorf("seeding: %w", err)
	}

	log.Info().Msg("seed complete")
	return nil
}

// newRunCmd creates the `yaypi run` subcommand.
func newRunCmd(configFile *string) *cobra.Command {
	return &cobra.Command{
		Use:   "run",
		Short: "Start the API server",
		RunE: func(cmd *cobra.Command, args []string) error {
			return server.New(*configFile).Run()
		},
	}
}

// newValidateCmd creates the `yaypi validate` subcommand.
func newValidateCmd(configFile *string) *cobra.Command {
	return &cobra.Command{
		Use:   "validate",
		Short: "Validate configuration files",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(*configFile)
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}

			errs := config.Validate(cfg)
			warnings := config.WarnSensitiveValues(cfg)

			for _, w := range warnings {
				log.Warn().Msg(w)
			}

			if len(errs) == 0 {
				log.Info().Msg("configuration is valid")
				return nil
			}

			for _, e := range errs {
				log.Error().Str("file", e.File).Msg(e.Message)
			}
			return fmt.Errorf("%d validation error(s)", len(errs))
		},
	}
}

// newMigrateCmd creates the `yaypi migrate` subcommand with sub-subcommands.
func newMigrateCmd(configFile *string) *cobra.Command {
	migrateCmd := &cobra.Command{
		Use:   "migrate",
		Short: "Database migration commands",
	}

	var migrateName string
	generateCmd := &cobra.Command{
		Use:   "generate",
		Short: "Generate migration files from schema diff",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMigrateGenerate(*configFile, migrateName)
		},
	}
	generateCmd.Flags().StringVar(&migrateName, "name", "migration", "name for the migration")

	var steps int
	var allowDrift bool
	var database string
	upCmd := &cobra.Command{
		Use:   "up",
		Short: "Apply pending migrations (all databases)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMigrateUp(*configFile, steps, allowDrift)
		},
	}
	upCmd.Flags().IntVar(&steps, "steps", 0, "number of migrations to apply per database (0 = all)")
	upCmd.Flags().BoolVar(&allowDrift, "allow-drift", false, "apply even if an applied migration file's checksum changed")

	downCmd := &cobra.Command{
		Use:   "down",
		Short: "Rollback migrations",
		RunE: func(cmd *cobra.Command, args []string) error {
			if steps <= 0 {
				return fmt.Errorf("--steps must be > 0")
			}
			return runMigrateDown(*configFile, steps, database)
		},
	}
	downCmd.Flags().IntVar(&steps, "steps", 1, "number of migrations to rollback")
	downCmd.Flags().StringVar(&database, "database", "", "database to roll back (default: the default database)")
	_ = downCmd.MarkFlagRequired("steps")

	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "Show migration status",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMigrateStatus(*configFile)
		},
	}

	verifyCmd := &cobra.Command{
		Use:   "verify",
		Short: "Verify migration checksums",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMigrateVerify(*configFile)
		},
	}

	migrateCmd.AddCommand(generateCmd, upCmd, downCmd, statusCmd, verifyCmd)
	return migrateCmd
}

// newInitCmd creates the `yaypi init` subcommand.
func newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init <name>",
		Short: "Scaffold a new yaypi project",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return scaffoldProject(args[0])
		},
	}
}

// newSpecCmd creates the `yaypi spec` subcommand.
func newSpecCmd(configFile *string) *cobra.Command {
	specCmd := &cobra.Command{
		Use:   "spec",
		Short: "OpenAPI spec commands",
	}

	var (
		specName   string
		outputPath string
	)

	generateCmd := &cobra.Command{
		Use:   "generate",
		Short: "Generate an OpenAPI 3.1 spec file",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSpecGenerate(*configFile, specName, outputPath)
		},
	}
	generateCmd.Flags().StringVar(&specName, "name", "", "spec name to generate (required)")
	generateCmd.Flags().StringVar(&outputPath, "output", "openapi.json", "output file path")
	_ = generateCmd.MarkFlagRequired("name")

	specCmd.AddCommand(generateCmd)
	return specCmd
}

// runSpecGenerate generates a named OpenAPI spec to a file.
func runSpecGenerate(configFile, specName, outputPath string) error {
	cfg, err := config.Load(configFile)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	reg, err := schema.Build(cfg)
	if err != nil {
		return fmt.Errorf("building schema registry: %w", err)
	}

	specs := openapi.Build(reg, cfg.Project.Name, cfg.Auth.Secret != "")
	spec, ok := specs[specName]
	if !ok {
		available := make([]string, 0, len(specs))
		for k := range specs {
			available = append(available, k)
		}
		return fmt.Errorf("spec %q not found; available: %v", specName, available)
	}

	b, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling spec: %w", err)
	}

	if err := os.WriteFile(outputPath, b, 0644); err != nil {
		return fmt.Errorf("writing %s: %w", outputPath, err)
	}

	log.Info().Str("spec", specName).Str("output", outputPath).Msg("spec generated")
	return nil
}

// migrationTarget is one configured database with its migrations directory.
type migrationTarget struct {
	name      string
	isDefault bool
	db        *db.DB
	dir       string
}

// withMigrationTargets loads config, connects every database and calls fn for each.
// Migration files live next to yaypi.yaml: migrations/ for the default database and
// migrations/<name>/ for any other.
func withMigrationTargets(configFile string, fn func(cfg *config.RootConfig, t migrationTarget) error) error {
	cfg, err := config.Load(configFile)
	if err != nil {
		return err
	}
	if len(cfg.Databases) == 0 {
		return fmt.Errorf("no databases configured")
	}
	dbManager, err := db.NewManager(cfg.Databases)
	if err != nil {
		return err
	}
	defer dbManager.Close()

	base := filepath.Join(filepath.Dir(configFile), "migrations")
	for _, name := range dbManager.Names() {
		dbc, _ := dbManager.Get(name)
		t := migrationTarget{name: name, isDefault: name == dbManager.DefaultName(), db: dbc, dir: base}
		if !t.isDefault {
			t.dir = filepath.Join(base, name)
		}
		if err := fn(cfg, t); err != nil {
			return fmt.Errorf("database %q: %w", name, err)
		}
	}
	return nil
}

// runMigrateGenerate generates migration files from a schema diff, per database.
func runMigrateGenerate(configFile, name string) error {
	var reg *schema.Registry
	generated := 0
	err := withMigrationTargets(configFile, func(cfg *config.RootConfig, t migrationTarget) error {
		if reg == nil {
			var err error
			if reg, err = schema.Build(cfg); err != nil {
				return fmt.Errorf("building schema registry: %w", err)
			}
		}
		stmts, err := migration.NewEngine(t.db.SQL, t.db.Dialect, reg).ForDatabase(t.name, t.isDefault).Diff(context.Background())
		if err != nil {
			return fmt.Errorf("schema diff: %w", err)
		}
		if len(stmts) == 0 {
			log.Info().Str("database", t.name).Msg("no schema changes")
			return nil
		}
		m, err := migration.NewGenerator(t.dir).Generate(name, stmts)
		if err != nil {
			return err
		}
		generated++
		log.Info().Str("database", t.name).Str("up", m.UpPath).Str("down", m.DownPath).Msg("migration files generated")
		return nil
	})
	if err == nil && generated == 0 {
		log.Info().Msg("schema is up to date; nothing generated")
	}
	return err
}

// runMigrateUp applies pending migrations on every database.
func runMigrateUp(configFile string, steps int, allowDrift bool) error {
	return withMigrationTargets(configFile, func(_ *config.RootConfig, t migrationTarget) error {
		if err := migration.NewRunner(t.db.SQL, t.db.Dialect, t.dir).Up(context.Background(), steps, allowDrift); err != nil {
			return fmt.Errorf("migration up: %w", err)
		}
		log.Info().Str("database", t.name).Msg("migrations applied")
		return nil
	})
}

// runMigrateDown rolls back N migrations on the selected database.
func runMigrateDown(configFile string, steps int, database string) error {
	return withMigrationTargets(configFile, func(_ *config.RootConfig, t migrationTarget) error {
		if (database == "" && !t.isDefault) || (database != "" && database != t.name) {
			return nil
		}
		if err := migration.NewRunner(t.db.SQL, t.db.Dialect, t.dir).Down(context.Background(), steps); err != nil {
			return fmt.Errorf("migration down: %w", err)
		}
		log.Info().Str("database", t.name).Int("steps", steps).Msg("migrations rolled back")
		return nil
	})
}

// runMigrateStatus shows migration status for every database.
func runMigrateStatus(configFile string) error {
	return withMigrationTargets(configFile, func(_ *config.RootConfig, t migrationTarget) error {
		statuses, err := migration.NewRunner(t.db.SQL, t.db.Dialect, t.dir).Status(context.Background())
		if err != nil {
			return err
		}
		fmt.Printf("database %s (%s):\n", t.name, t.dir)
		for _, s := range statuses {
			if s.Pending {
				fmt.Printf("  PENDING  %s\n", s.Name)
			} else {
				fmt.Printf("  APPLIED  %s\n", s.Name)
			}
		}
		return nil
	})
}

// runMigrateVerify verifies migration checksums on every database.
func runMigrateVerify(configFile string) error {
	err := withMigrationTargets(configFile, func(_ *config.RootConfig, t migrationTarget) error {
		return migration.NewRunner(t.db.SQL, t.db.Dialect, t.dir).Verify(context.Background())
	})
	if err == nil {
		log.Info().Msg("all migration checksums verified")
	}
	return err
}

// scaffoldProject creates a minimal yaypi project structure.
func scaffoldProject(name string) error {
	dirs := []string{
		name + "/entities",
		name + "/endpoints",
		name + "/policies",
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("creating directory %s: %w", dir, err)
		}
	}

	yaypiYAML := fmt.Sprintf(`version: "1"
project:
  name: %s
  base_url: /api/v1
server:
  port: 8080
  read_timeout: 30s
  write_timeout: 30s
  shutdown_timeout: 10s
databases:
  - name: primary
    driver: postgres
    dsn: ${DATABASE_URL:-postgres://localhost/%s}
    max_open_conns: 25
    default: true
auth:
  provider: jwt
  secret: ${JWT_SECRET:-dev-only-insecure-jwt-key-replace-me-0123456789}
  expiry: 24h
  algorithm: HS256
  reject_algorithms: [none]
policy:
  engine: casbin
  model: ./policies/model.conf
  adapter: file
auto_migrate: false
include:
  - entities/**/*.yaml
  - endpoints/**/*.yaml
  - policies/**/*.yaml
  - auth.yaml
`, name, name)

	if err := os.WriteFile(name+"/yaypi.yaml", []byte(yaypiYAML), 0644); err != nil {
		return fmt.Errorf("writing yaypi.yaml: %w", err)
	}

	authYAML := `version: "1"
kind: auth

auth:
  base_path: /auth

  # Uncomment to extend the built-in User with custom fields.
  # Built-in fields (id, email, password_hash, role, oauth_provider, oauth_id,
  # created_at, updated_at, deleted_at) are always present.
  #
  # user:
  #   fields:
  #     - name: display_name
  #       type: string
  #       length: 128
  #       nullable: true

  register:
    enabled: true

  login:
    enabled: true

  me:
    enabled: true
`
	if err := os.WriteFile(name+"/auth.yaml", []byte(authYAML), 0644); err != nil {
		return fmt.Errorf("writing auth.yaml: %w", err)
	}

	modelConf := `[request_definition]
r = sub, obj, act

[policy_definition]
p = sub, obj, act

[role_definition]
g = _, _

[policy_effect]
e = some(where (p.eft == allow))

[matchers]
m = g(r.sub, p.sub) && r.obj == p.obj && r.act == p.act
`
	if err := os.WriteFile(name+"/policies/model.conf", []byte(modelConf), 0644); err != nil {
		return fmt.Errorf("writing model.conf: %w", err)
	}

	log.Info().Str("name", name).Msg("project scaffolded")
	fmt.Printf("\nProject %q created. Next steps:\n", name)
	fmt.Printf("  cd %s\n", name)
	fmt.Printf("  export DATABASE_URL=postgres://localhost/%s\n", name)
	fmt.Printf("  yaypi run\n")
	return nil
}
