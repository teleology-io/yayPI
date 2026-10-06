package cron

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/go-co-op/gocron/v2"
	"github.com/google/uuid"

	"github.com/teleology-io/yayPI/internal/db"
	"github.com/teleology-io/yayPI/internal/schema"
)

// errLocked is returned when another instance holds the job lock for this tick.
var errLocked = errors.New("job is locked by another instance")

// dbLocker implements gocron.Locker on a database row per job, so with several replicas
// each scheduled run executes on exactly one of them.
//
// A lock is a lease: acquired when locked_until has passed, held for `lease` (so a
// crashed holder frees it), and on Unlock shortened to `minHold` after the run started
// rather than released outright — otherwise a replica whose clock fires a few hundred
// milliseconds later would see the lock free and run the same tick again.
type dbLocker struct {
	db      *db.DB
	owner   string
	lease   time.Duration
	minHold time.Duration
}

func newDBLocker(d *db.DB) *dbLocker {
	host, _ := os.Hostname()
	return &dbLocker{db: d, owner: host + "/" + uuid.NewString()[:8], lease: 30 * time.Minute, minHold: 15 * time.Second}
}

type dbLock struct {
	l       *dbLocker
	key     string
	started time.Time
}

// Lock tries to take the lease for key; errLocked if another instance holds it.
func (l *dbLocker) Lock(ctx context.Context, key string) (gocron.Lock, error) {
	d := l.db.Dialect
	table := d.QuoteIdent(schema.JobLockTable)
	now := time.Now()
	until := now.Add(l.lease).Unix()

	// Take over an expired lease…
	res, err := l.db.SQL.ExecContext(ctx, d.Rebind(fmt.Sprintf(
		`UPDATE %s SET owner = $1, locked_until = $2 WHERE job_name = $3 AND locked_until <= $4`, table)),
		l.owner, until, key, now.Unix())
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return &dbLock{l: l, key: key, started: now}, nil
	}
	// …or create the row on first run. A unique violation means someone else holds it.
	_, err = l.db.SQL.ExecContext(ctx, d.Rebind(fmt.Sprintf(
		`INSERT INTO %s (job_name, owner, locked_until) VALUES ($1, $2, $3)`, table)), key, l.owner, until)
	if err != nil {
		if d.IsUniqueViolation(err) {
			return nil, errLocked
		}
		return nil, err
	}
	return &dbLock{l: l, key: key, started: now}, nil
}

// Unlock shortens the lease to minHold after the run started.
func (k *dbLock) Unlock(ctx context.Context) error {
	d := k.l.db.Dialect
	until := k.started.Add(k.l.minHold).Unix()
	_, err := k.l.db.SQL.ExecContext(ctx, d.Rebind(fmt.Sprintf(
		`UPDATE %s SET locked_until = $1 WHERE job_name = $2 AND owner = $3`, d.QuoteIdent(schema.JobLockTable))),
		until, k.key, k.l.owner)
	return err
}
