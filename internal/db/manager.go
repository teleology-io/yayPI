package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	// Postgres via pgx stdlib shim
	_ "github.com/jackc/pgx/v5/stdlib"
	// MySQL
	_ "github.com/go-sql-driver/mysql"
	// SQLite (pure Go)
	_ "modernc.org/sqlite"

	"github.com/csullivan/yaypi/internal/config"
	"github.com/csullivan/yaypi/internal/dialect"
)

// DB wraps a *sql.DB together with its dialect.
type DB struct {
	SQL     *sql.DB
	Dialect dialect.Dialect
}

// Manager manages multiple named database connections.
type Manager struct {
	dbs       map[string]*DB
	defaultDB string
	entityDB  map[string]string // entity name → db name
}

// NewManager creates a Manager from a list of DBConfig entries and connects to each.
func NewManager(cfg []config.DBConfig) (*Manager, error) {
	m := &Manager{
		dbs:      make(map[string]*DB),
		entityDB: make(map[string]string),
	}

	for _, dbCfg := range cfg {
		d, err := dialect.FromDriver(dbCfg.Driver)
		if err != nil {
			return nil, fmt.Errorf("database %q: %w", dbCfg.Name, err)
		}

		dsn := NewSecretString(dbCfg.DSN)

		// pgx uses its own driver name when registered via stdlib shim
		driverName := dbCfg.Driver
		if driverName == "" || driverName == "postgres" || driverName == "postgresql" {
			driverName = "pgx"
		}
		if driverName == "sqlite3" {
			driverName = "sqlite"
		}

		sqlDB, err := sql.Open(driverName, dsn.Value())
		if err != nil {
			return nil, fmt.Errorf("opening database %q (%s): %w", dbCfg.Name, dsn, err)
		}

		if dbCfg.MaxOpenConns > 0 {
			sqlDB.SetMaxOpenConns(dbCfg.MaxOpenConns)
		}
		if dbCfg.MaxIdleConns > 0 {
			sqlDB.SetMaxIdleConns(dbCfg.MaxIdleConns)
		}
		if dbCfg.ConnMaxLifetime > 0 {
			sqlDB.SetConnMaxLifetime(dbCfg.ConnMaxLifetime)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := sqlDB.PingContext(ctx); err != nil {
			cancel()
			return nil, fmt.Errorf("connecting to database %q (%s): %w", dbCfg.Name, dsn, err)
		}
		cancel()

		m.dbs[dbCfg.Name] = &DB{SQL: sqlDB, Dialect: d}

		if dbCfg.Default || m.defaultDB == "" {
			m.defaultDB = dbCfg.Name
		}
	}

	if len(cfg) > 0 && m.defaultDB == "" {
		return nil, fmt.Errorf("no default database configured")
	}

	return m, nil
}

// Get returns a DB by name.
func (m *Manager) Get(name string) (*DB, error) {
	db, ok := m.dbs[name]
	if !ok {
		return nil, fmt.Errorf("database %q not found", name)
	}
	return db, nil
}

// Default returns the default DB.
func (m *Manager) Default() *DB {
	return m.dbs[m.defaultDB]
}

// ForEntity returns the DB associated with a named entity.
// Falls back to the default DB if no specific database is configured.
func (m *Manager) ForEntity(entityName string) (*DB, error) {
	if dbName, ok := m.entityDB[entityName]; ok {
		return m.Get(dbName)
	}
	return m.Default(), nil
}

// RegisterEntityDB maps an entity name to a named database.
func (m *Manager) RegisterEntityDB(entityName, dbName string) {
	m.entityDB[entityName] = dbName
}

// HealthCheck pings all databases and returns any errors.
func (m *Manager) HealthCheck(ctx context.Context) map[string]error {
	results := make(map[string]error)
	for name, db := range m.dbs {
		results[name] = db.SQL.PingContext(ctx)
	}
	return results
}

// Close closes all database connections.
func (m *Manager) Close() {
	for _, db := range m.dbs {
		db.SQL.Close()
	}
}
