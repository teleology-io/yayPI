package router

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	chiMiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/csullivan/yaypi/internal/auth"
	"github.com/csullivan/yaypi/internal/handler"
	"github.com/csullivan/yaypi/internal/middleware"
	"github.com/csullivan/yaypi/internal/openapi"
	"github.com/csullivan/yaypi/internal/policy"
	"github.com/csullivan/yaypi/internal/schema"
)

// Config holds router-building configuration.
type Config struct {
	BaseURL        string
	AuthSecret     []byte
	AuthAlg        string
	Enforcer       *policy.Engine
	AuthHandler    *auth.Handler    // optional; mounts register/login/me/oauth2 routes
	OpenAPIHandler *openapi.Handler // optional; serves /openapi/{name}.json
	AllowedOrigins []string         // CORS: permitted origins; ["*"] allows all
}

// Build constructs a chi.Router from the schema registry and config.
func Build(
	reg *schema.Registry,
	factory *handler.Factory,
	cfg Config,
) http.Handler {
	r := chi.NewRouter()

	// Global middleware
	r.Use(chiMiddleware.RealIP)
	r.Use(middleware.RequestID)
	r.Use(middleware.DefaultLogger())
	r.Use(middleware.Recover)
	if len(cfg.AllowedOrigins) > 0 {
		r.Use(middleware.CORS(cfg.AllowedOrigins))
	}

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
			cfg.AuthHandler.Mount(r)
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

		requireAuth := false
		if ep.Auth != nil {
			requireAuth = ep.Auth.Require
		}

		// Determine CRUD operations
		if len(ep.CRUD) > 0 {
			for _, op := range ep.CRUD {
				registerCRUDOp(r, op, ep, entity, factory, cfg, requireAuth)
			}
		} else if ep.Method != "" && ep.Handler != "" {
			// Custom handler — not supported in v1 (YAML-driven only)
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
	requireAuth bool,
) {
	path := ep.Path
	if path == "" {
		return
	}

	// Build per-endpoint middleware chain
	mws := buildMiddlewareChain(ep, entity, cfg, requireAuth, op)

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

	case "delete":
		opts := ep.Delete
		if opts == nil {
			opts = &schema.DeleteOpts{}
		}
		r.With(mws...).Delete(itemPath, factory.Delete(entity, opts))
	}
}

// buildMiddlewareChain constructs the middleware chain for a route.
func buildMiddlewareChain(
	ep *schema.Endpoint,
	entity *schema.Entity,
	cfg Config,
	requireAuth bool,
	op string,
) []func(http.Handler) http.Handler {
	var mws []func(http.Handler) http.Handler

	// JWT auth middleware
	if cfg.AuthSecret != nil && cfg.AuthAlg != "" {
		mws = append(mws, middleware.RequireAuth(cfg.AuthSecret, cfg.AuthAlg, requireAuth))
	}

	// RBAC middleware
	if cfg.Enforcer != nil && requireAuth {
		mws = append(mws, middleware.RBAC(cfg.Enforcer, entity.Name, requireAuth))
	}

	return mws
}
