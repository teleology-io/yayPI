package db

import (
	"context"
	"fmt"

	"github.com/csullivan/yaypi/internal/config"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Manager manages multiple named database connection pools.
type Manager struct {
	pools     map[string]*pgxpool.Pool
	defaultDB string
	entityDB  map[string]string // entity name → db name
}

// NewManager creates a Manager from a list of DBConfig entries and connects to each.
func NewManager(cfg []config.DBConfig) (*Manager, error) {
	m := &Manager{
		pools:    make(map[string]*pgxpool.Pool),
		entityDB: make(map[string]string),
	}

	for _, dbCfg := range cfg {
		dsn := NewSecretString(dbCfg.DSN)

		poolCfg, err := pgxpool.ParseConfig(dsn.Value())
		if err != nil {
			return nil, fmt.Errorf("parsing DSN for database %q (%s): %w", dbCfg.Name, dsn, err)
		}

		if dbCfg.MaxOpenConns > 0 {
			poolCfg.MaxConns = int32(dbCfg.MaxOpenConns)
		}
		if dbCfg.ConnMaxLifetime > 0 {
			poolCfg.MaxConnLifetime = dbCfg.ConnMaxLifetime
		}

		pool, err := pgxpool.NewWithConfig(context.Background(), poolCfg)
		if err != nil {
			return nil, fmt.Errorf("connecting to database %q (%s): %w", dbCfg.Name, dsn, err)
		}

		m.pools[dbCfg.Name] = pool

		if dbCfg.Default || m.defaultDB == "" {
			m.defaultDB = dbCfg.Name
		}
	}

	if len(cfg) > 0 && m.defaultDB == "" {
		return nil, fmt.Errorf("no default database configured")
	}

	return m, nil
}

// Get returns a pool by name.
func (m *Manager) Get(name string) (*pgxpool.Pool, error) {
	pool, ok := m.pools[name]
	if !ok {
		return nil, fmt.Errorf("database %q not found", name)
	}
	return pool, nil
}

// Default returns the default connection pool.
func (m *Manager) Default() *pgxpool.Pool {
	return m.pools[m.defaultDB]
}

// ForEntity returns the pool associated with a named entity.
// Falls back to the default pool if no specific database is configured.
func (m *Manager) ForEntity(entityName string) (*pgxpool.Pool, error) {
	if dbName, ok := m.entityDB[entityName]; ok {
		return m.Get(dbName)
	}
	return m.Default(), nil
}

// RegisterEntityDB maps an entity name to a named database.
func (m *Manager) RegisterEntityDB(entityName, dbName string) {
	m.entityDB[entityName] = dbName
}

// HealthCheck pings all pools and returns any errors.
func (m *Manager) HealthCheck(ctx context.Context) map[string]error {
	results := make(map[string]error)
	for name, pool := range m.pools {
		results[name] = pool.Ping(ctx)
	}
	return results
}

// Close closes all connection pools.
func (m *Manager) Close() {
	for _, pool := range m.pools {
		pool.Close()
	}
}
