// Package plugin provides an in-process plugin hook registry.
// The interface is designed to be compatible with hashicorp/go-plugin subprocess
// support in a future version — for v1, all plugins run in-process.
package plugin

import (
	"context"

	"github.com/csullivan/yaypi/internal/middleware"
	"github.com/csullivan/yaypi/pkg/sdk"
)

// Dispatcher manages entity lifecycle hooks and dispatches them to registered plugins.
type Dispatcher struct {
	hooks map[string][]sdk.EntityHookPlugin // keyed by entity name
}

// NewDispatcher creates an empty Dispatcher.
func NewDispatcher() *Dispatcher {
	return &Dispatcher{
		hooks: make(map[string][]sdk.EntityHookPlugin),
	}
}

// RegisterHook registers an EntityHookPlugin for a given entity.
func (d *Dispatcher) RegisterHook(entityName string, plugin sdk.EntityHookPlugin) {
	d.hooks[entityName] = append(d.hooks[entityName], plugin)
}

// buildHookContext constructs a HookContext from a request context,
// extracting the authenticated subject if present.
func buildHookContext(ctx context.Context) sdk.HookContext {
	hCtx := sdk.HookContext{Ctx: ctx}
	if sub := middleware.SubjectFromContext(ctx); sub != nil {
		hCtx.Subject = &sdk.Subject{
			ID:    sub.ID,
			Role:  sub.Role,
			Email: sub.Email,
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
