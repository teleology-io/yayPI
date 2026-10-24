// Package queue is the claim/retry machinery shared by the outbox (outbound webhooks,
// emails) and the inbox (inbound webhooks). Rows carry status, attempts, next_attempt_at,
// locked_until, last_error and created_at; due rows are claimed with a lease so several
// replicas can poll one table, then marked done, rescheduled with jittered exponential
// backoff, or marked dead.
package queue

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/teleology-io/yayPI/internal/config"
	"github.com/teleology-io/yayPI/internal/db"
)

// ClaimLease is how long a claimed row stays invisible to other workers.
const ClaimLease = 2 * time.Minute

// Queue is one queue table.
type Queue struct {
	DB      *db.DB
	Table   string
	Columns []string // payload columns read by Due, in order
}

// Item is a due row.
type Item struct {
	ID       string
	Attempts int
	Fields   []string // Columns, in order ("" for NULL)
}

// Policy is a retry policy.
type Policy struct {
	MaxAttempts int
	Initial     time.Duration
	Max         time.Duration
}

// PolicyFrom applies a retry: block over defaults.
func PolicyFrom(r *config.RetryConfig, def Policy) Policy {
	if r == nil {
		return def
	}
	if r.MaxAttempts > 0 {
		def.MaxAttempts = r.MaxAttempts
	}
	if d, err := time.ParseDuration(r.InitialDelay); err == nil && d > 0 {
		def.Initial = d
	}
	if d, err := time.ParseDuration(r.MaxDelay); err == nil && d > 0 {
		def.Max = d
	}
	return def
}

func (q *Queue) table() string { return q.DB.Dialect.QuoteIdent(q.Table) }

func (q *Queue) exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return q.DB.SQL.ExecContext(ctx, q.DB.Dialect.Rebind(fmt.Sprintf(query, q.table())), args...)
}

// Due returns up to limit pending rows whose next attempt is due and that no worker holds.
func (q *Queue) Due(ctx context.Context, limit int) ([]Item, error) {
	d := q.DB.Dialect
	cols := make([]string, len(q.Columns))
	for i, c := range q.Columns {
		cols[i] = d.QuoteIdent(c)
	}
	now := time.Now().Unix()
	rows, err := q.DB.SQL.QueryContext(ctx, d.Rebind(fmt.Sprintf(
		`SELECT id, attempts, %s FROM %s WHERE status = 'pending' AND next_attempt_at <= $1 AND locked_until <= $2 ORDER BY next_attempt_at LIMIT %d`,
		strings.Join(cols, ", "), q.table(), limit)), now, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Item
	for rows.Next() {
		it := Item{Fields: make([]string, len(q.Columns))}
		vals := make([]sql.NullString, len(q.Columns))
		dest := []any{&it.ID, &it.Attempts}
		for i := range vals {
			dest = append(dest, &vals[i])
		}
		if err := rows.Scan(dest...); err != nil {
			continue
		}
		for i, v := range vals {
			it.Fields[i] = v.String
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// Claim leases a row; false when another worker took it since Due.
func (q *Queue) Claim(ctx context.Context, id string) bool {
	now := time.Now()
	res, err := q.exec(ctx, `UPDATE %s SET locked_until = $1 WHERE id = $2 AND status = 'pending' AND locked_until <= $3`,
		now.Add(ClaimLease).Unix(), id, now.Unix())
	if err != nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n == 1
}

// Done marks a row delivered/processed.
func (q *Queue) Done(ctx context.Context, id string) {
	_, _ = q.exec(ctx, `UPDATE %s SET status = 'done', attempts = attempts + 1, locked_until = 0, last_error = NULL WHERE id = $1`, id)
}

// Fail records a failed attempt: dead after the policy's attempts (or at once when
// permanent), otherwise rescheduled. It reports whether the row is dead and, if not,
// when it is retried.
func (q *Queue) Fail(ctx context.Context, it Item, cause error, p Policy, permanent bool) (dead bool, retryIn time.Duration) {
	attempts := it.Attempts + 1
	errText := cause.Error()
	if len(errText) > 1000 {
		errText = errText[:1000]
	}
	if permanent || attempts >= p.MaxAttempts {
		_, _ = q.exec(ctx, `UPDATE %s SET status = 'dead', attempts = $1, locked_until = 0, last_error = $2 WHERE id = $3`, attempts, errText, it.ID)
		return true, 0
	}
	backoff := min(p.Initial*time.Duration(1<<min(attempts-1, 20)), p.Max)
	backoff = time.Duration(float64(backoff) * (0.8 + 0.4*rand.Float64())) // ±20% jitter
	_, _ = q.exec(ctx, `UPDATE %s SET attempts = $1, next_attempt_at = $2, locked_until = 0, last_error = $3 WHERE id = $4`,
		attempts, time.Now().Add(backoff).Unix(), errText, it.ID)
	return false, backoff
}

// Purge deletes done rows created before cutoff.
func (q *Queue) Purge(ctx context.Context, cutoff time.Time) {
	_, _ = q.exec(ctx, `DELETE FROM %s WHERE status = 'done' AND created_at < $1`, cutoff.Unix())
}
