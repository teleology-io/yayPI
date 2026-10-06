package handler

import (
	"context"
	"database/sql"
	"time"

	"github.com/teleology-io/yayPI/internal/db"
	"github.com/teleology-io/yayPI/internal/dialect"
	"github.com/teleology-io/yayPI/internal/middleware"
	"github.com/teleology-io/yayPI/internal/plugin"
	"github.com/teleology-io/yayPI/internal/policy"
	"github.com/teleology-io/yayPI/internal/schema"
)

// WriteEvent describes one committed-together change, handed to TxObservers inside the
// write transaction.
type WriteEvent struct {
	Entity    *schema.Entity
	Action    string // create | update | delete
	ID        string
	Before    map[string]any // nil for create
	After     map[string]any // nil for delete
	Subject   *middleware.Subject
	RequestID string
}

// TxObserver records a change in the same transaction as the change itself, so the
// record exists if and only if the change does (audit log, webhook outbox).
type TxObserver interface {
	OnWrite(ctx context.Context, tx *sql.Tx, d dialect.Dialect, ev WriteEvent) error
}

// Factory creates HTTP handler functions from entity and endpoint configuration.
type Factory struct {
	registry  *schema.Registry
	db        *db.Manager
	policy    *policy.Engine
	plugins   *plugin.Dispatcher
	secret    []byte // for cursor signing
	observers []TxObserver

	idempotencyTTL time.Duration // 0 = default (24h)
}

// NewFactory creates a Factory.
func NewFactory(
	registry *schema.Registry,
	dbManager *db.Manager,
	policyEngine *policy.Engine,
	dispatcher *plugin.Dispatcher,
	secret []byte,
) *Factory {
	return &Factory{
		registry: registry,
		db:       dbManager,
		policy:   policyEngine,
		plugins:  dispatcher,
		secret:   secret,
	}
}

// SetIdempotencyTTL sets how long Idempotency-Key responses are replayed.
func (f *Factory) SetIdempotencyTTL(d time.Duration) { f.idempotencyTTL = d }

// AddObserver registers a TxObserver (audit log, outbox). Call before serving.
func (f *Factory) AddObserver(o TxObserver) {
	f.observers = append(f.observers, o)
}

// observe runs every observer for ev inside tx.
func (f *Factory) observe(ctx context.Context, tx *sql.Tx, d dialect.Dialect, ev WriteEvent) error {
	for _, o := range f.observers {
		if err := o.OnWrite(ctx, tx, d, ev); err != nil {
			return err
		}
	}
	return nil
}
