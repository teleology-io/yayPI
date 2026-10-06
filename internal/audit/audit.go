// Package audit records entity changes for entities with `audit: true`.
package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/teleology-io/yayPI/internal/db"
	"github.com/teleology-io/yayPI/internal/dialect"
	"github.com/teleology-io/yayPI/internal/handler"
	"github.com/teleology-io/yayPI/internal/schema"
)

// Observer writes one yaypi_audit_log row per change, inside the write transaction.
type Observer struct {
	// The audit table lives in the default database; changes to entities stored in
	// another database are logged there directly (not atomically with the change).
	DefaultDBName  string
	DefaultDB      *sql.DB
	DefaultDialect dialect.Dialect
}

const redacted = "[redacted]"

// OnWrite implements handler.TxObserver.
func (o *Observer) OnWrite(ctx context.Context, tx *sql.Tx, d dialect.Dialect, ev handler.WriteEvent) error {
	if !ev.Entity.Audit {
		return nil
	}
	changes, err := json.Marshal(Diff(ev.Entity, ev.Before, ev.After))
	if err != nil {
		return err
	}
	actorID, actorRole := "", ""
	if ev.Subject != nil {
		actorID, actorRole = ev.Subject.ID, ev.Subject.Role
	}
	type execer interface {
		ExecContext(context.Context, string, ...any) (sql.Result, error)
	}
	var target execer = tx
	if db := ev.Entity.Database; db != "" && db != o.DefaultDBName && o.DefaultDB != nil {
		target, d = o.DefaultDB, o.DefaultDialect
	}
	_, err = target.ExecContext(ctx, d.Rebind(fmt.Sprintf(
		`INSERT INTO %s (id, entity, record_id, action, actor_id, actor_role, request_id, changes, created_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		d.QuoteIdent(schema.AuditTable))),
		uuid.NewString(), ev.Entity.Name, ev.ID, ev.Action, nullable(actorID), nullable(actorRole), nullable(ev.RequestID),
		string(changes), time.Now().Unix())
	return err
}

// Diff returns {field: [old, new]} for changed fields (create: old is null; delete: new
// is null). updated_at is skipped; omit_log fields show as "[redacted]".
func Diff(entity *schema.Entity, before, after map[string]any) map[string][2]any {
	out := map[string][2]any{}
	for _, f := range entity.Fields {
		if f.ColumnName == "updated_at" {
			continue
		}
		var oldV, newV any
		var hasOld, hasNew bool
		if before != nil {
			oldV, hasOld = before[f.ColumnName]
		}
		if after != nil {
			newV, hasNew = after[f.ColumnName]
		}
		if !hasOld && !hasNew {
			continue
		}
		if before != nil && after != nil && reflect.DeepEqual(oldV, newV) {
			continue
		}
		if f.OmitLog {
			if oldV != nil {
				oldV = redacted
			}
			if newV != nil {
				newV = redacted
			}
		}
		out[f.ColumnName] = [2]any{oldV, newV}
	}
	return out
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// StartRetention deletes audit rows older than keep, once at start and then hourly, until
// the returned stop function is called. Safe to run on every replica (the delete is
// idempotent).
func StartRetention(d *db.DB, keep time.Duration) func() {
	stop := make(chan struct{})
	purge := func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_, err := d.SQL.ExecContext(ctx, d.Dialect.Rebind(fmt.Sprintf(
			`DELETE FROM %s WHERE created_at < $1`, d.Dialect.QuoteIdent(schema.AuditTable))),
			time.Now().Add(-keep).Unix())
		if err != nil {
			log.Warn().Err(err).Msg("audit: retention purge failed")
		}
	}
	go func() {
		purge()
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				purge()
			}
		}
	}()
	return func() { close(stop) }
}
