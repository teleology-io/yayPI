package sdk

import (
	"context"
	"net/http"
)

// PluginInfo contains metadata about a plugin.
type PluginInfo struct {
	Name        string
	Version     string
	Description string
}

// Logger is the interface plugins use for structured logging.
type Logger interface {
	Info(msg string, fields ...any)
	Error(msg string, err error, fields ...any)
}

// InitContext is passed to Plugin.Init.
type InitContext struct {
	Config map[string]any
	Logger Logger
}

// Subject holds the authenticated caller's identity, mirroring middleware.Subject
// but defined here so plugins don't import internal packages.
type Subject struct {
	ID    string
	Role  string
	Email string
}

// HookContext is passed to each entity hook call.
type HookContext struct {
	Ctx       context.Context
	RequestID string
	Subject   *Subject // nil when the request is unauthenticated
}

// Plugin is the base interface all yaypi plugins must implement.
type Plugin interface {
	Info() PluginInfo
	Init(ctx InitContext) error
	Shutdown(ctx context.Context) error
}

// EntityHookPlugin extends Plugin with entity lifecycle hooks.
type EntityHookPlugin interface {
	Plugin
	BeforeCreate(ctx HookContext, entity string, data map[string]any) (map[string]any, error)
	AfterCreate(ctx HookContext, entity string, record map[string]any) error
	BeforeUpdate(ctx HookContext, entity string, id string, data map[string]any) (map[string]any, error)
	AfterUpdate(ctx HookContext, entity string, record map[string]any) error
	BeforeDelete(ctx HookContext, entity string, id string) error
	AfterDelete(ctx HookContext, entity string, id string) error
}

// RouteContext is passed to a custom route handler. Unlike HookContext, it carries the raw
// http.Request/ResponseWriter — a custom route isn't wrapping a generated CRUD operation, so
// there's no envelope format to hand back instead.
type RouteContext struct {
	Ctx      context.Context
	Subject  *Subject // nil when the request is unauthenticated
	Request  *http.Request
	Response http.ResponseWriter
}

// RouteHandlerFunc handles one custom HTTP route registered by a RouteHandlerPlugin.
type RouteHandlerFunc func(RouteContext)

// RouteHandlerPlugin lets a plugin expose custom HTTP routes that yayPI's declarative CRUD
// endpoints can't express (bulk operations, non-entity-shaped responses, file uploads/downloads,
// etc). Each entry in Handlers() is referenced from endpoints YAML as
// `handler: <PluginInfo.Name>.<key>` — e.g. a plugin named "reports" exposing a "Generate"
// handler is wired up with `handler: reports.Generate`. The endpoint still declares `path`,
// `method`, `entity` (for RBAC — the action is derived from the HTTP method), and `auth` exactly
// like a CRUD endpoint, so a custom route gets the same auth/rate-limit/RBAC middleware chain for
// free instead of reimplementing it.
type RouteHandlerPlugin interface {
	Plugin
	Handlers() map[string]RouteHandlerFunc
}
