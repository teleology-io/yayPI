// Package plugin provides an in-process plugin hook registry.
// The interface is designed to be compatible with hashicorp/go-plugin subprocess
// support in a future version — for v1, all plugins run in-process.
package plugin

import (
	"context"
	"fmt"

	"github.com/rs/zerolog/log"

	"github.com/teleology-io/yayPI/internal/middleware"
	"github.com/teleology-io/yayPI/pkg/sdk"
)

// Dispatcher manages entity lifecycle hooks and custom route handlers, dispatching them to
// registered plugins.
type Dispatcher struct {
	hooks         map[string][]sdk.EntityHookPlugin // keyed by entity name
	routeHandlers map[string]sdk.RouteHandlerFunc   // keyed by "<PluginInfo.Name>.<handlerKey>"
	plugins       []sdk.Plugin                      // distinct plugins, registration order
}

func (d *Dispatcher) track(p sdk.Plugin) {
	for _, existing := range d.plugins {
		if existing == p {
			return
		}
	}
	d.plugins = append(d.plugins, p)
}

// Plugins returns every distinct registered plugin.
func (d *Dispatcher) Plugins() []sdk.Plugin { return d.plugins }

// InitAll calls Init on every plugin with its `plugins:` config block (matched by
// PluginInfo.Name). The first error aborts startup.
func (d *Dispatcher) InitAll(configs map[string]map[string]any) error {
	for _, p := range d.plugins {
		name := p.Info().Name
		if err := p.Init(sdk.InitContext{Config: configs[name], Logger: pluginLogger{name: name}}); err != nil {
			return fmt.Errorf("plugin %q init: %w", name, err)
		}
	}
	return nil
}

// ShutdownAll calls Shutdown on every plugin (reverse order), logging failures.
func (d *Dispatcher) ShutdownAll(ctx context.Context) {
	for i := len(d.plugins) - 1; i >= 0; i-- {
		p := d.plugins[i]
		if err := p.Shutdown(ctx); err != nil {
			log.Warn().Err(err).Str("plugin", p.Info().Name).Msg("plugin shutdown failed")
		}
	}
}

// pluginLogger adapts zerolog to sdk.Logger.
type pluginLogger struct{ name string }

func (l pluginLogger) Info(msg string, fields ...any) {
	log.Info().Str("plugin", l.name).Fields(fields).Msg(msg)
}

func (l pluginLogger) Error(msg string, err error, fields ...any) {
	log.Error().Str("plugin", l.name).Err(err).Fields(fields).Msg(msg)
}

// NewDispatcher creates an empty Dispatcher.
func NewDispatcher() *Dispatcher {
	return &Dispatcher{
		hooks:         make(map[string][]sdk.EntityHookPlugin),
		routeHandlers: make(map[string]sdk.RouteHandlerFunc),
	}
}

// RegisterHook registers an EntityHookPlugin for a given entity.
func (d *Dispatcher) RegisterHook(entityName string, plugin sdk.EntityHookPlugin) {
	d.hooks[entityName] = append(d.hooks[entityName], plugin)
	d.track(plugin)
}

// RegisterRoutePlugin registers all of p's named handlers, prefixed with its own
// PluginInfo.Name, matching the "handler: <name>.<key>" string endpoints YAML uses
// to reference it.
func (d *Dispatcher) RegisterRoutePlugin(p sdk.RouteHandlerPlugin) {
	d.track(p)
	prefix := p.Info().Name
	for name, fn := range p.Handlers() {
		d.routeHandlers[prefix+"."+name] = fn
	}
}

// RouteHandler looks up a registered custom-route handler by its full "plugin.key" name.
func (d *Dispatcher) RouteHandler(name string) (sdk.RouteHandlerFunc, bool) {
	fn, ok := d.routeHandlers[name]
	return fn, ok
}

// buildHookContext constructs a HookContext from a request context,
// extracting the authenticated subject if present.
func buildHookContext(ctx context.Context) sdk.HookContext {
	hCtx := sdk.HookContext{Ctx: ctx, RequestID: middleware.RequestIDFromContext(ctx)}
	if sub := middleware.SubjectFromContext(ctx); sub != nil {
		hCtx.Subject = &sdk.Subject{
			ID:     sub.ID,
			Role:   sub.Role,
			Email:  sub.Email,
			Tenant: sub.Tenant,
		}
	}
	return hCtx
}

// BeforeCreate dispatches the BeforeCreate hook for an entity.
func (d *Dispatcher) BeforeCreate(ctx context.Context, entity string, data map[string]any) (map[string]any, error) {
	hCtx := buildHookContext(ctx)
	var err error
	for _, p := range d.hooks[entity] {
		data, err = p.BeforeCreate(hCtx, entity, data)
		if err != nil {
			return nil, err
		}
	}
	return data, nil
}

// AfterCreate dispatches the AfterCreate hook for an entity.
func (d *Dispatcher) AfterCreate(ctx context.Context, entity string, record map[string]any) error {
	hCtx := buildHookContext(ctx)
	for _, p := range d.hooks[entity] {
		if err := p.AfterCreate(hCtx, entity, record); err != nil {
			return err
		}
	}
	return nil
}

// BeforeUpdate dispatches the BeforeUpdate hook for an entity.
func (d *Dispatcher) BeforeUpdate(ctx context.Context, entity string, id string, data map[string]any) (map[string]any, error) {
	hCtx := buildHookContext(ctx)
	var err error
	for _, p := range d.hooks[entity] {
		data, err = p.BeforeUpdate(hCtx, entity, id, data)
		if err != nil {
			return nil, err
		}
	}
	return data, nil
}

// AfterUpdate dispatches the AfterUpdate hook for an entity.
func (d *Dispatcher) AfterUpdate(ctx context.Context, entity string, record map[string]any) error {
	hCtx := buildHookContext(ctx)
	for _, p := range d.hooks[entity] {
		if err := p.AfterUpdate(hCtx, entity, record); err != nil {
			return err
		}
	}
	return nil
}

// BeforeDelete dispatches the BeforeDelete hook for an entity.
func (d *Dispatcher) BeforeDelete(ctx context.Context, entity string, id string) error {
	hCtx := buildHookContext(ctx)
	for _, p := range d.hooks[entity] {
		if err := p.BeforeDelete(hCtx, entity, id); err != nil {
			return err
		}
	}
	return nil
}

// AfterDelete dispatches the AfterDelete hook for an entity.
func (d *Dispatcher) AfterDelete(ctx context.Context, entity string, id string) error {
	hCtx := buildHookContext(ctx)
	for _, p := range d.hooks[entity] {
		if err := p.AfterDelete(hCtx, entity, id); err != nil {
			return err
		}
	}
	return nil
}
