package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"

	"github.com/csullivan/yaypi/internal/auth"
	"github.com/csullivan/yaypi/internal/config"
	"github.com/csullivan/yaypi/internal/cron"
	"github.com/csullivan/yaypi/internal/db"
	"github.com/csullivan/yaypi/internal/handler"
	"github.com/csullivan/yaypi/internal/migration"
	"github.com/csullivan/yaypi/internal/plugin"
	"github.com/csullivan/yaypi/internal/policy"
	"github.com/csullivan/yaypi/internal/router"
	"github.com/csullivan/yaypi/internal/schema"
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
	)

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

// newRunCmd creates the `yaypi run` subcommand.
func newRunCmd(configFile *string) *cobra.Command {
	return &cobra.Command{
		Use:   "run",
		Short: "Start the API server",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServer(*configFile)
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
	upCmd := &cobra.Command{
		Use:   "up",
		Short: "Apply pending migrations",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMigrateUp(*configFile, steps)
		},
	}
	upCmd.Flags().IntVar(&steps, "steps", 0, "number of migrations to apply (0 = all)")

	downCmd := &cobra.Command{
		Use:   "down",
		Short: "Rollback migrations",
		RunE: func(cmd *cobra.Command, args []string) error {
			if steps <= 0 {
				return fmt.Errorf("--steps must be > 0")
			}
			return runMigrateDown(*configFile, steps)
		},
	}
	downCmd.Flags().IntVar(&steps, "steps", 1, "number of migrations to rollback")
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

// runServer loads config and starts the HTTP server.
func runServer(configFile string) error {
	cfg, err := config.Load(configFile)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	// Warn about sensitive plain-text values
	for _, w := range config.WarnSensitiveValues(cfg) {
		log.Warn().Msg(w)
	}

	// Validate config
	if errs := config.Validate(cfg); len(errs) > 0 {
		for _, e := range errs {
			log.Error().Str("file", e.File).Msg(e.Message)
		}
		return fmt.Errorf("configuration validation failed")
	}

	// Build schema registry
	reg, err := schema.Build(cfg)
	if err != nil {
		return fmt.Errorf("building schema registry: %w", err)
	}

	// Register entity→database mappings
	var dbManager *db.Manager
	if len(cfg.Databases) > 0 {
		dbManager, err = db.NewManager(cfg.Databases)
		if err != nil {
			return fmt.Errorf("initializing database connections: %w", err)
		}
		defer dbManager.Close()

		for _, entity := range reg.Entities() {
			if entity.Database != "" {
				dbManager.RegisterEntityDB(entity.Name, entity.Database)
			}
		}

		// Auto-migrate if configured
		if cfg.AutoMigrate {
			engine := migration.NewEngine(dbManager.Default().SQL, dbManager.Default().Dialect, reg)
			stmts, err := engine.Diff(context.Background())
			if err != nil {
				log.Warn().Err(err).Msg("schema diff failed; skipping auto-migrate")
			} else if len(stmts) > 0 {
				gen := migration.NewGenerator("migrations")
				m, err := gen.Generate("auto", stmts)
				if err != nil {
					log.Warn().Err(err).Msg("auto-migrate generation failed")
				} else {
					runner := migration.NewRunner(dbManager.Default().SQL, dbManager.Default().Dialect, "migrations")
					if err := runner.Up(context.Background(), 0); err != nil {
						log.Warn().Err(err).Str("up", m.UpPath).Msg("auto-migrate failed")
					}
				}
			}
		}
	}

	// Load policy engine
	var policyEngine *policy.Engine
	if cfg.Policy.Engine == "casbin" && cfg.Policy.Model != "" {
		roles, err := policy.LoadRolesDir("policies")
		if err != nil {
			log.Warn().Err(err).Msg("loading roles")
		}

		if cfg.Policy.Adapter == "file" {
			// Use in-memory policy from YAML roles
			pe, err := buildInMemoryPolicy(cfg.Policy.Model, roles)
			if err != nil {
				log.Warn().Err(err).Msg("initializing policy engine")
			} else {
				policyEngine = pe
			}
		}
	}

	// Initialize plugin dispatcher
	dispatcher := plugin.NewDispatcher()

	// Build handler factory
	secret := []byte(cfg.Auth.Secret)
	factory := handler.NewFactory(reg, dbManager, policyEngine, dispatcher, secret)

	// Build auth handler if a kind:auth file was loaded
	var authHandler *auth.Handler
	if cfg.AuthEndpoint != nil {
		authHandler = auth.New(cfg.AuthEndpoint.Auth, reg, dbManager, secret, cfg.Auth.Algorithm)
	}

	// Build router
	routerCfg := router.Config{
		BaseURL:        cfg.Project.BaseURL,
		AuthSecret:     secret,
		AuthAlg:        cfg.Auth.Algorithm,
		Enforcer:       policyEngine,
		AuthHandler:    authHandler,
		AllowedOrigins: cfg.Server.AllowedOrigins,
	}
	httpHandler := router.Build(reg, factory, routerCfg)

	// Start cron scheduler
	if jobDefs := cfg.AllJobDefs(); len(jobDefs) > 0 {
		var sched *cron.Scheduler
		if dbManager != nil {
			sched, err = cron.New(jobDefs, dbManager)
		} else {
			sched, err = cron.New(jobDefs, nil)
		}
		if err != nil {
			log.Warn().Err(err).Msg("initializing cron scheduler")
		} else {
			sched.Start()
			defer sched.Stop()
		}
	}

	// Configure HTTP server
	addr := fmt.Sprintf(":%d", cfg.Server.Port)
	srv := &http.Server{
		Addr:         addr,
		Handler:      httpHandler,
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
	}

	// Graceful shutdown
	shutdown := make(chan os.Signal, 1)
	signal.Notify(shutdown, os.Interrupt, syscall.SIGTERM)

	serverErr := make(chan error, 1)
	go func() {
		log.Info().Str("addr", addr).Str("base_url", cfg.Project.BaseURL).Msg("server starting")
		if cfg.Server.TLS != nil {
			serverErr <- srv.ListenAndServeTLS(cfg.Server.TLS.CertFile, cfg.Server.TLS.KeyFile)
		} else {
			serverErr <- srv.ListenAndServe()
		}
	}()

	select {
	case err := <-serverErr:
		if err != nil && err != http.ErrServerClosed {
			return fmt.Errorf("server error: %w", err)
		}
	case sig := <-shutdown:
		log.Info().Str("signal", sig.String()).Msg("shutting down")
		timeout := cfg.Server.ShutdownTimeout
		if timeout == 0 {
			timeout = 10 * time.Second
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			return fmt.Errorf("shutdown error: %w", err)
		}
	}

	return nil
}

// buildInMemoryPolicy loads a Casbin model from file and populates it from roles.
func buildInMemoryPolicy(modelPath string, roles []policy.RoleConfig) (*policy.Engine, error) {
	// Use a file-based policy adapter with a temp file, or use string adapter
	// For simplicity, write a temp CSV and load it
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

	if err := pe.LoadFromRolesConfig(roles); err != nil {
		return nil, err
	}

	return pe, nil
}

// runMigrateGenerate generates migration files from a schema diff.
func runMigrateGenerate(configFile, name string) error {
	cfg, err := config.Load(configFile)
	if err != nil {
		return err
	}

	reg, err := schema.Build(cfg)
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

	engine := migration.NewEngine(dbManager.Default().SQL, dbManager.Default().Dialect, reg)
	stmts, err := engine.Diff(context.Background())
	if err != nil {
		return fmt.Errorf("schema diff: %w", err)
	}

	gen := migration.NewGenerator("migrations")
	m, err := gen.Generate(name, stmts)
	if err != nil {
		return err
	}

	log.Info().Str("up", m.UpPath).Str("down", m.DownPath).Msg("migration files generated")
	return nil
}

// runMigrateUp applies pending migrations.
func runMigrateUp(configFile string, steps int) error {
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

	runner := migration.NewRunner(dbManager.Default().SQL, dbManager.Default().Dialect, "migrations")
	if err := runner.Up(context.Background(), steps); err != nil {
		return fmt.Errorf("migration up: %w", err)
	}

	log.Info().Msg("migrations applied")
	return nil
}

// runMigrateDown rolls back N migrations.
func runMigrateDown(configFile string, steps int) error {
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

	runner := migration.NewRunner(dbManager.Default().SQL, dbManager.Default().Dialect, "migrations")
	if err := runner.Down(context.Background(), steps); err != nil {
		return fmt.Errorf("migration down: %w", err)
	}

	log.Info().Int("steps", steps).Msg("migrations rolled back")
	return nil
}

// runMigrateStatus shows migration status.
func runMigrateStatus(configFile string) error {
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

	runner := migration.NewRunner(dbManager.Default().SQL, dbManager.Default().Dialect, "migrations")
	statuses, err := runner.Status(context.Background())
	if err != nil {
		return err
	}

	for _, s := range statuses {
		if s.Pending {
			fmt.Printf("PENDING  %s\n", s.Name)
		} else {
			fmt.Printf("APPLIED  %s  (at %s)\n", s.Name, s.AppliedAt)
		}
	}
	return nil
}

// runMigrateVerify verifies migration checksums.
func runMigrateVerify(configFile string) error {
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

	runner := migration.NewRunner(dbManager.Default().SQL, dbManager.Default().Dialect, "migrations")
	if err := runner.Verify(context.Background()); err != nil {
		return err
	}

	log.Info().Msg("all migration checksums verified")
	return nil
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
  secret: ${JWT_SECRET:-changeme-in-production}
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
`, name, name)

	if err := os.WriteFile(name+"/yaypi.yaml", []byte(yaypiYAML), 0644); err != nil {
		return fmt.Errorf("writing yaypi.yaml: %w", err)
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
