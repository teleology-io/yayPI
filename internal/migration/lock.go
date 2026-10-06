package migration

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/teleology-io/yayPI/internal/dialect"
)

// lockKey identifies yaypi's migration lock (arbitrary, stable).
const (
	pgLockKey     int64 = 0x79617970692d6d67 // "yaypi-mg"
	mysqlLockName       = "yaypi_migrations"
	lockTimeout         = 5 * time.Minute
)

// AcquireLock takes a cross-process migration lock so concurrently booting replicas (or
// a deploy job racing a replica) apply migrations one at a time. The returned func
// releases it. SQLite needs no lock: writes are serialised by the file lock.
func AcquireLock(ctx context.Context, db *sql.DB, d dialect.Dialect) (func(), error) {
	switch d.Name() {
	case "postgres", "mysql":
	default:
		return func() {}, nil
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("migration lock: %w", err)
	}
	lctx, cancel := context.WithTimeout(ctx, lockTimeout)
	defer cancel()

	if d.Name() == "postgres" {
		if _, err := conn.ExecContext(lctx, "SELECT pg_advisory_lock($1)", pgLockKey); err != nil {
			conn.Close()
			return nil, fmt.Errorf("migration lock: %w", err)
		}
		return func() {
			_, _ = conn.ExecContext(context.Background(), "SELECT pg_advisory_unlock($1)", pgLockKey)
			conn.Close()
		}, nil
	}

	var got sql.NullInt64
	if err := conn.QueryRowContext(lctx, "SELECT GET_LOCK(?, ?)", mysqlLockName, int(lockTimeout.Seconds())).Scan(&got); err != nil {
		conn.Close()
		return nil, fmt.Errorf("migration lock: %w", err)
	}
	if !got.Valid || got.Int64 != 1 {
		conn.Close()
		return nil, fmt.Errorf("migration lock: timed out waiting for another migrator")
	}
	return func() {
		_, _ = conn.ExecContext(context.Background(), "SELECT RELEASE_LOCK(?)", mysqlLockName)
		conn.Close()
	}, nil
}
