package sdk

import "context"

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
