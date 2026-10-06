package router

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
	"github.com/teleology-io/yayPI/internal/apierr"
	"github.com/teleology-io/yayPI/internal/auth"
	"github.com/teleology-io/yayPI/internal/handler"
	"github.com/teleology-io/yayPI/internal/health"
	"github.com/teleology-io/yayPI/internal/middleware"
	"github.com/teleology-io/yayPI/internal/openapi"
	"github.com/teleology-io/yayPI/internal/plugin"
	"github.com/teleology-io/yayPI/internal/policy"
	"github.com/teleology-io/yayPI/internal/schema"
	"github.com/teleology-io/yayPI/internal/token"
	"github.com/teleology-io/yayPI/pkg/sdk"
)

// Config holds router-building configuration.
type Config struct {
	BaseURL         string
	Tokens          *token.Keys                 // access token verifier; nil = no JWT auth
	ValidateSubject middleware.SubjectValidator // optional per-request revocation / role refresh
	Enforcer        *policy.Engine
	AuthHandler     *auth.Handler              // optional; mounts register/login/me/oauth2 routes
	OpenAPIHandler  *openapi.Handler           // optional; serves /openapi/{name}.json
	HealthHandler   *health.Handler            // optional; mounts /health and /ready
	MaxBodyBytes    int64                      // request body cap; <= 0 disables
	RequestTimeout  time.Duration              // request context deadline; <= 0 disables
	CORS            middleware.CORSOptions     // CORS: AllowedOrigins empty disables; ["*"] allows any origin without credentials
	RateLimit       *middleware.RateLimiter    // optional; global rate limiter
	TrustedProxies  middleware.TrustedProxies  // peers whose X-Forwarded-For / X-Real-IP are believed
	APIKeyHeader    string                     // header name for API key (default: X-API-Key)
	APIKeyParam     string                     // optional query param for API key
	APIKeyLookup    middleware.APIKeyLookup    // optional; enables API key auth
	Dispatcher      *plugin.Dispatcher         // optional; resolves custom-route `handler:` names
	RequestObserver middleware.RequestObserver // optional; per-request metrics
	SecurityHeaders bool                       // add default security headers
	HSTSMaxAge      int                        // > 0 adds Strict-Transport-Security
	MetricsHandler  http.Handler               // optional; mounted at MetricsPath outside base_url
	MetricsPath     string
	MetricsToken    string // bearer token required to scrape metrics ("" = open)
}

// Build constructs a chi.Router from the schema registry and config.
func Build(
	reg *schema.Registry,
	factory *handler.Factory,
	cfg Config,
) http.Handler {
	r := chi.NewRouter()

	// Global middleware
	r.Use(middleware.ClientIP(cfg.TrustedProxies))
	r.Use(middleware.RequestID)
	r.Use(middleware.Trace)
	r.Use(middleware.Logger(log.Logger, cfg.RequestObserver))
	r.Use(middleware.Recover)
	if cfg.SecurityHeaders {
		r.Use(middleware.SecurityHeaders(cfg.HSTSMaxAge))
	}
	// The body limit is applied per route (see buildMiddlewareChain) rather than globally,
	// so an endpoint can raise it with max_body_size — a global MaxBytesReader could only
	// ever be tightened further down the chain.
	if cfg.RequestTimeout > 0 {
		r.Use(middleware.Timeout(cfg.RequestTimeout))
	}
	if len(cfg.CORS.AllowedOrigins) > 0 {
		r.Use(middleware.CORS(cfg.CORS))
	}
	// An IP-keyed global limiter runs for every request. A user-keyed one needs the
	// authenticated subject, so it is mounted per route after auth (buildMiddlewareChain).
	if cfg.RateLimit != nil && !cfg.RateLimit.KeyByUser() {
		r.Use(cfg.RateLimit.Handler)
	}

	// Health/readiness endpoints — mounted outside base URL so they are
	// always reachable regardless of base_url prefix.
	if cfg.HealthHandler != nil {
		cfg.HealthHandler.Mount(r)
	}
	if cfg.MetricsHandler != nil {
		path := cfg.MetricsPath
		if path == "" {
			path = "/metrics"
		}
		r.Get(path, middleware.BearerToken(cfg.MetricsToken, cfg.MetricsHandler).ServeHTTP)
	}

	// JSON 404/405 in the standard error envelope instead of chi's plain text.
	r.NotFound(func(w http.ResponseWriter, req *http.Request) {
		apierr.Write(w, middleware.GetRequestID(req), http.StatusNotFound, "route not found")
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, req *http.Request) {
		apierr.Write(w, middleware.GetRequestID(req), http.StatusMethodNotAllowed, "method not allowed")
	})

	// Global OPTIONS catch-all so chi never returns 405 for CORS preflight.
	// The CORS middleware above has already written the Allow-* headers before
	// this handler is reached.
	r.Options("/*", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "/"
	}

	r.Route(baseURL, func(r chi.Router) {
		if cfg.AuthHandler != nil {
			r.Group(func(r chi.Router) {
				if cfg.MaxBodyBytes > 0 {
					r.Use(middleware.BodyLimit(cfg.MaxBodyBytes))
				}
				cfg.AuthHandler.Mount(r)
			})
		}
		if cfg.OpenAPIHandler != nil {
			cfg.OpenAPIHandler.Mount(r)
		}
		registerEndpoints(r, reg, factory, cfg)
	})

	return r
}

// registerEndpoints iterates over registry endpoints and registers chi routes.
func registerEndpoints(r chi.Router, reg *schema.Registry, factory *handler.Factory, cfg Config) {
	for _, ep := range reg.Endpoints() {
		ep := ep // capture
		entity, ok := reg.GetEntity(ep.Entity)
		if !ok {
			continue
		}

		// Determine CRUD operations
		if len(ep.CRUD) > 0 {
			for _, op := range ep.CRUD {
				registerCRUDOp(r, op, ep, entity, factory, cfg)
			}
		} else if ep.Method != "" && ep.Handler != "" {
			registerCustomHandler(r, ep, entity, cfg)
		}
	}
}

// registerCRUDOp registers a single CRUD operation as a chi route.
func registerCRUDOp(
	r chi.Router,
	op string,
	ep *schema.Endpoint,
	entity *schema.Entity,
	factory *handler.Factory,
	cfg Config,
) {
	path := ep.Path
	if path == "" {
		return
	}

	// Build per-operation middleware chain
	mws := buildMiddlewareChain(ep, entity, cfg, op)

	// itemPath appends /{id} only when the path doesn't already contain a param.
	itemPath := path
	if !strings.Contains(path, "{") {
		itemPath = path + "/{id}"
	}

	switch op {
	case "list":
		opts := ep.List
		if opts == nil {
			opts = &schema.ListOpts{}
		}
		r.With(mws...).Get(path, factory.List(entity, opts))

	case "get":
		opts := ep.Get
		if opts == nil {
			opts = &schema.GetOpts{}
		}
		r.With(mws...).Get(itemPath, factory.Get(entity, opts))

	case "create":
		opts := ep.Create
		if opts == nil {
			opts = &schema.CreateOpts{}
		}
		r.With(mws...).Post(path, factory.Create(entity, opts))

	case "update":
		opts := ep.Update
		if opts == nil {
			opts = &schema.UpdateOpts{}
		}
		r.With(mws...).Patch(itemPath, factory.Update(entity, opts))

	case "replace":
		opts := ep.Update
		if opts == nil {
			opts = &schema.UpdateOpts{}
		}
		r.With(mws...).Put(itemPath, factory.Replace(entity, opts))

	case "delete":
		opts := ep.Delete
		if opts == nil {
			opts = &schema.DeleteOpts{}
		}
		r.With(mws...).Delete(itemPath, factory.Delete(entity, opts))
	}
}

// registerCustomHandler registers an endpoint's `method`/`handler` pair (a RouteHandlerPlugin
// entry, not a CRUD op) as a chi route. It gets the same auth/rate-limit/RBAC middleware chain
// as a CRUD route — buildMiddlewareChain is called with op="" so resolveOpAuth falls through to
// the endpoint-level `auth:` block, and RBAC derives the casbin action from the request's actual
// HTTP method rather than a fixed op string, so this needs no special-casing there.
func registerCustomHandler(r chi.Router, ep *schema.Endpoint, entity *schema.Entity, cfg Config) {
	if cfg.Dispatcher == nil {
		log.Warn().Str("handler", ep.Handler).Str("path", ep.Path).
			Msg("no plugin dispatcher configured; skipping custom-handler endpoint")
		return
	}
	fn, ok := cfg.Dispatcher.RouteHandler(ep.Handler)
	if !ok {
		log.Warn().Str("handler", ep.Handler).Str("path", ep.Path).
			Msg("no route handler registered for endpoint; skipping")
		return
	}
	mws := buildMiddlewareChain(ep, entity, cfg, "")
	r.With(mws...).Method(strings.ToUpper(ep.Method), ep.Path, wrapRouteHandler(fn))
}

// wrapRouteHandler adapts an sdk.RouteHandlerFunc to an http.HandlerFunc, attaching the
// authenticated Subject the same way internal/plugin's hook dispatch does.
func wrapRouteHandler(fn sdk.RouteHandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rc := sdk.RouteContext{Ctx: r.Context(), Request: r, Response: w}
		if sub := middleware.SubjectFromContext(r.Context()); sub != nil {
			rc.Subject = &sdk.Subject{ID: sub.ID, Role: sub.Role, Email: sub.Email, Tenant: sub.Tenant}
		}
		fn(rc)
	}
}

// resolveOpAuth returns the effective Auth for the given CRUD operation.
// Per-operation auth overrides the endpoint-level auth.
func resolveOpAuth(ep *schema.Endpoint, op string) *schema.Auth {
	var opAuth *schema.Auth
	switch op {
	case "list":
		if ep.List != nil {
			opAuth = ep.List.Auth
		}
	case "get":
		if ep.Get != nil {
			opAuth = ep.Get.Auth
		}
	case "create":
		if ep.Create != nil {
			opAuth = ep.Create.Auth
		}
	case "update", "replace":
		if ep.Update != nil {
			opAuth = ep.Update.Auth
		}
	case "delete":
		if ep.Delete != nil {
			opAuth = ep.Delete.Auth
		}
	}
	if opAuth != nil {
		return opAuth
	}
	return ep.Auth
}

// buildMiddlewareChain constructs the middleware chain for a route.
func buildMiddlewareChain(
	ep *schema.Endpoint,
	entity *schema.Entity,
	cfg Config,
	op string,
) []func(http.Handler) http.Handler {
	var mws []func(http.Handler) http.Handler

	if limit := cfg.MaxBodyBytes; limit > 0 || ep.MaxBodyBytes > 0 {
		if ep.MaxBodyBytes > 0 {
			limit = ep.MaxBodyBytes
		}
		mws = append(mws, middleware.BodyLimit(limit))
	}

	// Per-endpoint rate limiter. It applies in addition to the global limiter — a request
	// must pass both. IP-keyed limiters run first so they shed load before auth work;
	// user-keyed ones run after auth so the subject is known.
	var epLimiter *middleware.RateLimiter
	if ep.RateLimit != nil && ep.RateLimit.RequestsPerMinute > 0 {
		rps := float64(ep.RateLimit.RequestsPerMinute) / 60.0
		burst := ep.RateLimit.Burst
		if burst <= 0 {
			burst = ep.RateLimit.RequestsPerMinute
		}
		epLimiter = middleware.NewRateLimiter(burst, rps, ep.RateLimit.KeyBy)
	}
	if epLimiter != nil && !epLimiter.KeyByUser() {
		mws = append(mws, epLimiter.Handler)
	}

	auth := resolveOpAuth(ep, op)

	// roles/conditions only make sense for an authenticated caller, so declaring either
	// implies require: true — otherwise an anonymous request would skip the check.
	requireAuth := auth != nil && (auth.Require || len(auth.Roles) > 0 || len(auth.Conditions) > 0)

	// API key auth middleware (runs before JWT so either can satisfy auth).
	if cfg.APIKeyLookup != nil {
		mws = append(mws, middleware.APIKeyAuth(cfg.APIKeyHeader, cfg.APIKeyParam, cfg.APIKeyLookup))
	}

	// JWT auth middleware. Mounted whenever tokens are configured so optional-auth routes
	// still see the caller; with no token config, RBAC below fails closed for require: true.
	if cfg.Tokens != nil {
		mws = append(mws, middleware.RequireAuth(cfg.Tokens, requireAuth, cfg.ValidateSubject))
	}

	if cfg.RateLimit != nil && cfg.RateLimit.KeyByUser() {
		mws = append(mws, cfg.RateLimit.Handler)
	}
	if epLimiter != nil && epLimiter.KeyByUser() {
		mws = append(mws, epLimiter.Handler)
	}

	// RBAC + roles + conditions middleware. Mounted for every authenticated route: the
	// Casbin check runs only when a policy engine is configured, but roles/conditions are
	// always enforced — they must never silently disappear because Casbin is absent.
	if requireAuth {
		opts := middleware.AuthOpts{Require: true, Roles: auth.Roles, Conditions: auth.Conditions}
		var enforcer middleware.RBACEnforcer
		if cfg.Enforcer != nil {
			enforcer = cfg.Enforcer
		}
		mws = append(mws, middleware.RBAC(enforcer, entity.Name, opts, policy.EvalConditions))
	}

	return mws
}
