package plugin

import (
	"context"
	"database/sql"

	"github.com/teleology-io/yayPI/internal/dialect"
	"github.com/teleology-io/yayPI/internal/rawsql"
	"github.com/teleology-io/yayPI/pkg/sdk"
)

// writeScope is the entity write a Before* hook runs inside.
type writeScope struct {
	sqlTx  *sql.Tx
	tx     sdk.Tx
	before map[string]any
}

type writeScopeKey struct{}

// InWrite marks ctx as running inside an entity write's transaction, on the
// stored row before (nil for creates). Hooks dispatched with it get
// HookContext.Tx and Before; Store.Query/Exec with it join the transaction.
func InWrite(ctx context.Context, d dialect.Dialect, tx *sql.Tx, before map[string]any) context.Context {
	var b map[string]any
	if before != nil {
		b = make(map[string]any, len(before))
		for k, v := range before {
			b[k] = v
		}
	}
	return context.WithValue(ctx, writeScopeKey{}, &writeScope{sqlTx: tx, tx: rawsql.Tx(d, tx), before: b})
}

// WriteTx is the transaction of the write ctx runs inside, or nil.
func WriteTx(ctx context.Context) *sql.Tx {
	if s, ok := ctx.Value(writeScopeKey{}).(*writeScope); ok {
		return s.sqlTx
	}
	return nil
}

// hooksAdapter lets sdk.Hooks (plain functions) sit in the hook list.
type hooksAdapter struct {
	sdk.NoHooks
	h sdk.Hooks
}

func (a hooksAdapter) Info() sdk.PluginInfo           { return sdk.PluginInfo{Name: "hooks"} }
func (a hooksAdapter) Init(sdk.InitContext) error     { return nil }
func (a hooksAdapter) Shutdown(context.Context) error { return nil }
func (a hooksAdapter) BeforeCreate(c sdk.HookContext, _ string, d map[string]any) (map[string]any, error) {
	if a.h.BeforeCreate == nil {
		return d, nil
	}
	return a.h.BeforeCreate(c, d)
}
func (a hooksAdapter) AfterCreate(c sdk.HookContext, _ string, r map[string]any) error {
	if a.h.AfterCreate == nil {
		return nil
	}
	return a.h.AfterCreate(c, r)
}
func (a hooksAdapter) BeforeUpdate(c sdk.HookContext, _, id string, d map[string]any) (map[string]any, error) {
	if a.h.BeforeUpdate == nil {
		return d, nil
	}
	return a.h.BeforeUpdate(c, id, d)
}
func (a hooksAdapter) AfterUpdate(c sdk.HookContext, _ string, r map[string]any) error {
	if a.h.AfterUpdate == nil {
		return nil
	}
	return a.h.AfterUpdate(c, r)
}
func (a hooksAdapter) BeforeDelete(c sdk.HookContext, _, id string) error {
	if a.h.BeforeDelete == nil {
		return nil
	}
	return a.h.BeforeDelete(c, id)
}
func (a hooksAdapter) AfterDelete(c sdk.HookContext, _, id string) error {
	if a.h.AfterDelete == nil {
		return nil
	}
	return a.h.AfterDelete(c, id)
}

// RegisterHooks adds one entity's hooks given as plain functions.
func (d *Dispatcher) RegisterHooks(entity string, h sdk.Hooks) {
	d.hooks[entity] = append(d.hooks[entity], hooksAdapter{h: h})
}
