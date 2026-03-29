// Package server exposes yaypi as an embeddable library.
// Use it when you need to wire custom plugins before starting the server.
package server

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/teleology-io/yayPI/internal/auth"
	"github.com/teleology-io/yayPI/internal/config"
	"github.com/teleology-io/yayPI/internal/cron"
	"github.com/teleology-io/yayPI/internal/db"
	"github.com/teleology-io/yayPI/internal/handler"
	"github.com/teleology-io/yayPI/internal/migration"
	"github.com/teleology-io/yayPI/internal/openapi"
	"github.com/teleology-io/yayPI/internal/plugin"
	"github.com/teleology-io/yayPI/internal/policy"
	"github.com/teleology-io/yayPI/internal/router"
	"github.com/teleology-io/yayPI/internal/schema"
	"github.com/teleology-io/yayPI/pkg/sdk"
)

// Server is a yaypi API server instance. Create one with New, optionally register
// plugins with RegisterHook, then call Run.
type Server struct {
	configFile string
	dispatcher *plugin.Dispatcher
}

// New creates a Server that loads configuration from configFile.
func New(configFile string) *Server {
	return &Server{
		configFile: configFile,
		dispatcher: plugin.NewDispatcher(),
	}
}

// RegisterHook registers a plugin to handle lifecycle events for the named entity.
// Must be called before Run.
func (s *Server) RegisterHook(entity string, p sdk.EntityHookPlugin) {
	s.dispatcher.RegisterHook(entity, p)
}

// Run loads configuration, starts the HTTP server, and blocks until interrupted.
func (s *Server) Run() error {
	cfg, err := config.Load(s.configFile)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	for _, w := range config.WarnSensitiveValues(cfg) {
		log.Warn().Msg(w)
	}

	if errs := config.Validate(cfg); len(errs) > 0 {
		for _, e := range errs {
			log.Error().Str("file", e.File).Msg(e.Message)
		}
		return fmt.Errorf("configuration validation failed")
	}

	reg, err := schema.Build(cfg)
	if err != nil {
		return fmt.Errorf("building schema registry: %w", err)
	}

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

	var policyEngine *policy.Engine
	if cfg.Policy.Engine == "casbin" && cfg.Policy.Model != "" {
		roles, err := policy.LoadRolesDir("policies")
		if err != nil {
			log.Warn().Err(err).Msg("loading roles")
		}
		if cfg.Policy.Adapter == "file" {
			pe, err := buildInMemoryPolicy(cfg.Policy.Model, roles)
			if err != nil {
				log.Warn().Err(err).Msg("initializing policy engine")
			} else {
				policyEngine = pe
			}
		}
	}

	secret := []byte(cfg.Auth.Secret)
	factory := handler.NewFactory(reg, dbManager, policyEngine, s.dispatcher, secret)

	var authHandler *auth.Handler
	if cfg.AuthEndpoint != nil {
		authHandler = auth.New(cfg.AuthEndpoint.Auth, reg, dbManager, secret, cfg.Auth.Algorithm)
	}

	var openapiHandler *openapi.Handler
	if len(cfg.Specs) > 0 {
		specs := openapi.Build(reg, cfg.Project.Name, cfg.Auth.Secret != "")
		openapiHandler, err = openapi.NewHandler(specs)
		if err != nil {
			log.Warn().Err(err).Msg("building OpenAPI handler")
		}
	}

	routerCfg := router.Config{
		BaseURL:        cfg.Project.BaseURL,
		AuthSecret:     secret,
		AuthAlg:        cfg.Auth.Algorithm,
		Enforcer:       policyEngine,
		AuthHandler:    authHandler,
		OpenAPIHandler: openapiHandler,
		AllowedOrigins: cfg.Server.AllowedOrigins,
	}
	httpHandler := router.Build(reg, factory, routerCfg)

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

	addr := fmt.Sprintf(":%d", cfg.Server.Port)
	srv := &http.Server{
		Addr:         addr,
		Handler:      httpHandler,
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
	}

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

func buildInMemoryPolicy(modelPath string, roles []policy.RoleConfig) (*policy.Engine, error) {
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
