// Package server exposes yaypi as an embeddable library.
// Use it when you need to wire custom plugins before starting the server.
package server

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/teleology-io/yayPI/internal/apikey"
	"github.com/teleology-io/yayPI/internal/audit"
	"github.com/teleology-io/yayPI/internal/auth"
	"github.com/teleology-io/yayPI/internal/config"
	"github.com/teleology-io/yayPI/internal/cron"
	"github.com/teleology-io/yayPI/internal/db"
	"github.com/teleology-io/yayPI/internal/handler"
	"github.com/teleology-io/yayPI/internal/health"
	"github.com/teleology-io/yayPI/internal/logging"
	"github.com/teleology-io/yayPI/internal/mailer"
	"github.com/teleology-io/yayPI/internal/metrics"
	"github.com/teleology-io/yayPI/internal/middleware"
	"github.com/teleology-io/yayPI/internal/openapi"
	"github.com/teleology-io/yayPI/internal/outbox"
	"github.com/teleology-io/yayPI/internal/plugin"
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

// RegisterRoutes registers a plugin's custom route handlers, referenceable from endpoints
// YAML as `handler: <p.Info().Name>.<key>`. Must be called before Run.
func (s *Server) RegisterRoutes(p sdk.RouteHandlerPlugin) {
	s.dispatcher.RegisterRoutePlugin(p)
}

// Run loads configuration, starts the HTTP server, and blocks until SIGINT/SIGTERM, then
// shuts down gracefully.
//
// Startup is fail-fast: anything that would leave the server insecure (auth, policy,
// migrations) is always fatal, and degraded subsystems (cron, OpenAPI, email without
// SMTP) are fatal unless server.strict_startup is false.
func (s *Server) Run() error {
	cfg, err := config.Load(s.configFile)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	if err := logging.Setup(cfg.Log); err != nil {
		return err
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

	strict := cfg.Server.StrictStartup == nil || *cfg.Server.StrictStartup
	degraded := func(msg string, err error) error {
		if strict {
			if err != nil {
				return fmt.Errorf("%s: %w (set server.strict_startup: false to start anyway)", msg, err)
			}
			return fmt.Errorf("%s (set server.strict_startup: false to start anyway)", msg)
		}
		log.Warn().Err(err).Msg(msg)
		return nil
	}

	// ── databases ────────────────────────────────────────────────────────────
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
			if err := autoMigrate(dbManager, reg); err != nil {
				return fmt.Errorf("auto_migrate: %w", err)
			}
		}
	}

	// A configured policy engine that fails to initialise must stop the boot: running
	// without it would silently skip every Casbin check (fail open).
	policyEngine, err := buildPolicyEngine(cfg.Policy, reg, filepath.Dir(s.configFile))
	if err != nil {
		return fmt.Errorf("initializing policy engine: %w", err)
	}

	// ── metrics ──────────────────────────────────────────────────────────────
	var reqObserver middleware.RequestObserver
	var metricsReg *metrics.Registry
	var cronRuns, outboxDeliveries *metrics.Counter
	if cfg.Metrics != nil && cfg.Metrics.Enabled {
		metricsReg = metrics.NewRegistry()
		reqTotal := metricsReg.NewCounter("yaypi_http_requests_total", "HTTP requests by method, route and status.", "method", "route", "status")
		reqDur := metricsReg.NewHistogram("yaypi_http_request_duration_seconds", "HTTP request latency.", nil, "method", "route")
		reqObserver = func(method, route string, status int, d time.Duration) {
			reqTotal.Inc(method, route, strconv.Itoa(status))
			reqDur.Observe(d.Seconds(), method, route)
		}
		cronRuns = metricsReg.NewCounter("yaypi_cron_runs_total", "Cron job runs by job and result.", "job", "result")
		outboxDeliveries = metricsReg.NewCounter("yaypi_outbox_deliveries_total", "Webhook/email delivery attempts by kind and result.", "kind", "result")
		if dbManager != nil {
			metricsReg.NewGaugeFunc("yaypi_db_connections", "Database pool connections by state.", func() map[string]float64 {
				out := map[string]float64{}
				for _, name := range dbManager.Names() {
					d, _ := dbManager.Get(name)
					st := d.SQL.Stats()
					out[metrics.LabelValues(name, "open")] = float64(st.OpenConnections)
					out[metrics.LabelValues(name, "in_use")] = float64(st.InUse)
					out[metrics.LabelValues(name, "idle")] = float64(st.Idle)
				}
				return out
			}, "database", "state")
			metricsReg.NewGaugeFunc("yaypi_db_wait_seconds_total", "Total time blocked waiting for a pooled connection.", func() map[string]float64 {
				out := map[string]float64{}
				for _, name := range dbManager.Names() {
					d, _ := dbManager.Get(name)
					out[metrics.LabelValues(name)] = d.SQL.Stats().WaitDuration.Seconds()
				}
				return out
			}, "database")
		}
	}

	// ── handlers ─────────────────────────────────────────────────────────────
	// Pagination cursors are HMAC-signed with the auth secret. Without one (no auth in
	// use), sign with a random per-process key rather than an empty, forgeable one.
	cursorSecret := []byte(cfg.Auth.Secret)
	if len(cursorSecret) == 0 {
		cursorSecret = make([]byte, 32)
		if _, err := rand.Read(cursorSecret); err != nil {
			return fmt.Errorf("generating cursor key: %w", err)
		}
		log.Info().Msg("auth.secret not set; pagination cursors use a per-process key and will not survive restarts or span replicas")
	}
	factory := handler.NewFactory(reg, dbManager, policyEngine, s.dispatcher, cursorSecret)
	idemTTL, _ := config.ParseDuration(cfg.Server.IdempotencyTTL) // validated above
	factory.SetIdempotencyTTL(idemTTL)

	smtp := mailer.New(cfg.SMTP)
	var smtpSender outbox.Sender
	if smtp != nil {
		smtpSender = smtp
	}

	// Webhooks and emails go through the transactional outbox: rendered and stored in
	// the same transaction as the change, then delivered with retries by the worker.
	var outboxWorker *outbox.Worker
	webhookDefs, emailDefs := cfg.AllWebhookDefs(), cfg.AllEmailDefs()
	authMail := schema.AuthSendsEmail(cfg)
	if len(webhookDefs) > 0 || len(emailDefs) > 0 || authMail {
		if dbManager == nil {
			return fmt.Errorf("webhooks and email triggers require a database (they are delivered via an outbox table)")
		}
		if len(emailDefs) > 0 && smtp == nil {
			if err := degraded("email triggers are configured but no SMTP host is set (smtp.host or SMTP_HOST); emails will queue and fail", nil); err != nil {
				return err
			}
		}
		obs, err := outbox.NewObserver(webhookDefs, emailDefs)
		if err != nil {
			return err
		}
		obs.DefaultDBName, obs.DefaultDB, obs.DefaultDialect = dbManager.DefaultName(), dbManager.Default().SQL, dbManager.Default().Dialect
		factory.AddObserver(obs)
		poll, _ := config.ParseDuration(cfg.Outbox.PollInterval) // validated above
		retention, _ := config.ParseDuration(cfg.Outbox.Retention)
		outboxWorker = outbox.NewWorker(dbManager.Default(), webhookDefs, emailDefs, smtpSender,
			outbox.WorkerOptions{PollInterval: poll, Retention: retention, EmailRetry: smtpRetry(cfg.SMTP)})
		if outboxDeliveries != nil {
			outboxWorker.OnDelivery = func(kind string, ok bool) { outboxDeliveries.Inc(kind, result(ok)) }
		}
	}
	var stopAuditRetention func()
	if _, ok := reg.GetEntity(schema.AuditEntityName); ok && dbManager != nil {
		factory.AddObserver(&audit.Observer{
			DefaultDBName: dbManager.DefaultName(), DefaultDB: dbManager.Default().SQL, DefaultDialect: dbManager.Default().Dialect,
		})
		if keep, _ := config.ParseDuration(cfg.Audit.Retention); keep > 0 {
			stopAuditRetention = audit.StartRetention(dbManager.Default(), keep)
		}
	}

	tokens, err := buildTokenKeys(cfg)
	if err != nil {
		return fmt.Errorf("auth: %w", err)
	}

	var authHandler *auth.Handler
	var validateSubject middleware.SubjectValidator
	if cfg.AuthEndpoint != nil {
		ae := cfg.AuthEndpoint.Auth
		if dbManager == nil {
			return fmt.Errorf("auth: the auth endpoints require a database")
		}
		opts := auth.Options{
			Config:          ae,
			Registry:        reg,
			DB:              dbManager,
			Tokens:          tokens,
			RevocationCheck: cfg.Auth.RevocationCheck,
			MountPrefix:     cfg.Project.BaseURL,
		}
		if smtp != nil {
			opts.Mailer = smtp
		}
		needsMail := (ae.PasswordReset != nil && ae.PasswordReset.Enabled) ||
			(ae.EmailVerification != nil && ae.EmailVerification.Enabled)
		if needsMail && smtp == nil {
			return fmt.Errorf("auth: password_reset / email_verification need SMTP (set smtp.host in yaypi.yaml or SMTP_HOST)")
		}
		authHandler = auth.New(opts)
		validateSubject = authHandler.SubjectValidator()
	} else if cfg.Auth.RevocationCheck {
		log.Warn().Msg("auth.revocation_check needs the built-in auth endpoints (kind: auth); ignoring")
	}

	var openapiHandler *openapi.Handler
	if len(cfg.Specs) > 0 {
		specs := openapi.Build(reg, cfg.Project.Name, tokens != nil)
		if openapiHandler, err = openapi.NewHandler(specs); err != nil {
			if err := degraded("building OpenAPI handler failed", err); err != nil {
				return err
			}
		}
	}

	var healthHandler *health.Handler
	if cfg.Server.Health != nil && cfg.Server.Health.Enabled {
		// dbManager satisfies health.Checker via its HealthCheck method; nil is safe.
		var checker health.Checker
		if dbManager != nil {
			checker = dbManager
		}
		healthHandler = health.New(checker, cfg.Server.Health.Path, cfg.Server.Health.ReadinessPath)
	}

	var rateLimiter *middleware.RateLimiter
	if rl := cfg.Server.RateLimit; rl != nil && rl.RequestsPerMinute > 0 {
		burst := rl.Burst
		if burst <= 0 {
			burst = rl.RequestsPerMinute
		}
		rateLimiter = middleware.NewRateLimiter(burst, float64(rl.RequestsPerMinute)/60.0, rl.KeyBy)
	}

	var apiKeyLookup middleware.APIKeyLookup
	var apiKeyHeader, apiKeyParam string
	if ak := cfg.Auth.APIKeys; ak != nil {
		apiKeyHeader, apiKeyParam = ak.Header, ak.QueryParam
		if apiKeyParam != "" {
			log.Warn().Msg("auth.api_keys.query_param is enabled: keys in URLs leak into proxy/access logs and browser history; prefer the header")
		}
		if len(ak.Keys) > 0 {
			apiKeyLookup = apikey.StaticLookup(ak.Keys)
		} else if ak.Entity != "" {
			if dbManager == nil {
				return fmt.Errorf("auth.api_keys.entity requires a database")
			}
			if apiKeyLookup, err = apikey.DBLookup(ak, reg, dbManager); err != nil {
				return err
			}
		}
	}

	trustedProxies, err := middleware.ParseTrustedProxies(cfg.Server.TrustedProxies)
	if err != nil {
		return fmt.Errorf("server.trusted_proxies: %w", err)
	}
	maxBody, _ := config.ParseByteSize(cfg.Server.MaxRequestBodySize) // validated above
	if maxBody == 0 {
		maxBody = config.DefaultMaxRequestBodyBytes
	}
	maxHeader, _ := config.ParseByteSize(cfg.Server.MaxHeaderBytes)

	// ── plugins ──────────────────────────────────────────────────────────────
	pluginConfigs := map[string]map[string]any{}
	for _, pc := range cfg.Plugins {
		pluginConfigs[pc.Name] = pc.Config
	}
	if err := s.dispatcher.InitAll(pluginConfigs); err != nil {
		return err
	}

	routerCfg := router.Config{
		BaseURL:         cfg.Project.BaseURL,
		Tokens:          tokens,
		ValidateSubject: validateSubject,
		Enforcer:        policyEngine,
		AuthHandler:     authHandler,
		OpenAPIHandler:  openapiHandler,
		HealthHandler:   healthHandler,
		CORS:            corsOptions(cfg.Server),
		MaxBodyBytes:    maxBody,
		RequestTimeout:  cfg.Server.RequestTimeout,
		TrustedProxies:  trustedProxies,
		RateLimit:       rateLimiter,
		APIKeyHeader:    apiKeyHeader,
		APIKeyParam:     apiKeyParam,
		APIKeyLookup:    apiKeyLookup,
		Dispatcher:      s.dispatcher,
		RequestObserver: reqObserver,
		SecurityHeaders: cfg.Server.SecurityHeaders == nil || *cfg.Server.SecurityHeaders,
		HSTSMaxAge:      cfg.Server.HSTSMaxAge,
	}
	if metricsReg != nil {
		routerCfg.MetricsHandler = metricsReg.Handler()
		routerCfg.MetricsPath = cfg.Metrics.Path
		routerCfg.MetricsToken = cfg.Metrics.Token
	}
	httpHandler := router.Build(reg, factory, routerCfg)

	// ── background work ──────────────────────────────────────────────────────
	var sched *cron.Scheduler
	if jobDefs := cfg.AllJobDefs(); len(jobDefs) > 0 {
		distributed := cfg.Cron.Distributed == nil || *cfg.Cron.Distributed
		sched, err = cron.New(jobDefs, dbManager, distributed)
		if err != nil {
			if err := degraded("initializing cron scheduler failed", err); err != nil {
				return err
			}
			sched = nil
		} else {
			if cronRuns != nil {
				sched.OnRun = func(job string, _ time.Duration, err error) { cronRuns.Inc(job, result(err == nil)) }
			}
			sched.Start()
		}
	}
	if outboxWorker != nil {
		outboxWorker.Start()
	}

	// ── serve ────────────────────────────────────────────────────────────────
	addr := fmt.Sprintf(":%d", cfg.Server.Port)
	srv := &http.Server{
		Addr:              addr,
		Handler:           httpHandler,
		ReadTimeout:       cfg.Server.ReadTimeout,
		ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout,
		WriteTimeout:      cfg.Server.WriteTimeout,
		IdleTimeout:       cfg.Server.IdleTimeout,
		MaxHeaderBytes:    int(maxHeader), // 0 = net/http default (1MB)
	}

	shutdownSignal := make(chan os.Signal, 1)
	signal.Notify(shutdownSignal, os.Interrupt, syscall.SIGTERM)

	serverErr := make(chan error, 1)
	go func() {
		log.Info().Str("addr", addr).Str("base_url", cfg.Project.BaseURL).Msg("server starting")
		if cfg.Server.TLS != nil {
			serverErr <- srv.ListenAndServeTLS(cfg.Server.TLS.CertFile, cfg.Server.TLS.KeyFile)
		} else {
			serverErr <- srv.ListenAndServe()
		}
	}()

	var runErr error
	select {
	case err := <-serverErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			runErr = fmt.Errorf("server error: %w", err)
		}
	case sig := <-shutdownSignal:
		log.Info().Str("signal", sig.String()).Msg("shutting down")
	}

	// Graceful shutdown, all within shutdown_timeout:
	//   1. report not-ready and keep serving for drain_delay so load balancers move off;
	//   2. stop accepting connections and finish in-flight requests;
	//   3. stop cron and the outbox worker (pending messages stay queued for next start);
	//   4. shut plugins down; databases close via defer.
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout+cfg.Server.DrainDelay)
	defer cancel()
	if healthHandler != nil {
		healthHandler.SetDraining()
	}
	if d := cfg.Server.DrainDelay; d > 0 && runErr == nil {
		log.Info().Dur("drain_delay", d).Msg("draining: readiness now failing")
		time.Sleep(d)
	}
	if err := srv.Shutdown(ctx); err != nil && runErr == nil {
		runErr = fmt.Errorf("shutdown error: %w", err)
	}
	if sched != nil {
		sched.Stop()
	}
	if outboxWorker != nil {
		outboxWorker.Stop(ctx)
	}
	if stopAuditRetention != nil {
		stopAuditRetention()
	}
	s.dispatcher.ShutdownAll(ctx)
	log.Info().Msg("shutdown complete")
	return runErr
}

func smtpRetry(c *config.SMTPConfig) *config.RetryConfig {
	if c == nil {
		return nil
	}
	return c.Retry
}

func result(ok bool) string {
	if ok {
		return "success"
	}
	return "error"
}

// ensure db.Manager satisfies health.Checker at compile time.
var _ health.Checker = (*db.Manager)(nil)
