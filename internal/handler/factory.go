package handler

import (
	"github.com/teleology-io/yayPI/internal/db"
	"github.com/teleology-io/yayPI/internal/plugin"
	"github.com/teleology-io/yayPI/internal/policy"
	"github.com/teleology-io/yayPI/internal/schema"
)

// Factory creates HTTP handler functions from entity and endpoint configuration.
type Factory struct {
	registry *schema.Registry
	db       *db.Manager
	policy   *policy.Engine
	plugins  *plugin.Dispatcher
	secret   []byte // for cursor signing
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
