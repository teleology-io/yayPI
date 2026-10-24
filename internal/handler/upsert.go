package handler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/teleology-io/yayPI/internal/db"
	"github.com/teleology-io/yayPI/internal/middleware"
	"github.com/teleology-io/yayPI/internal/query"
	"github.com/teleology-io/yayPI/internal/schema"
)

// Write outcomes, reported to clients (X-Write-Action, bulk "action") and plugins.
const (
	ActionCreated   = "created"
	ActionUpdated   = "updated"
	ActionUnchanged = "unchanged" // upsert matched but stale_guard (or no changes) skipped it
	ActionIgnored   = "ignored"   // on_conflict: ignore kept the stored row
	ActionDeleted   = "deleted"
	ActionNotFound  = "not_found" // delete of a row that does not exist (idempotent)
)

// Conflict modes (create.on_conflict).
const (
	ConflictError  = "error"
	ConflictUpdate = "update"
	ConflictIgnore = "ignore"
)

// errKeyHidden: a row with the natural key exists, but row_access / tenant scope hides
// it from this caller, so it may neither be overwritten nor recreated.
var errKeyHidden = errors.New("a record with this key exists but is not accessible")

// writeOutcome is the result of one create/upsert inside a transaction.
type writeOutcome struct {
	Action string
	Record map[string]any
	Before map[string]any
}

// isUpsert reports whether create input must carry the natural key.
func isUpsert(mode string) bool { return mode == ConflictUpdate || mode == ConflictIgnore }

// missingNaturalKey returns the first natural key field absent (or null) in data.
func missingNaturalKey(entity *schema.Entity, data map[string]any) string {
	for _, col := range entity.NaturalKey {
		if v, ok := data[col]; !ok || v == nil {
			f, _ := entity.FieldByName(col)
			return f.Name
		}
	}
	return ""
}

// insert writes one record inside tx and notifies observers.
func (f *Factory) insert(ctx context.Context, tx *sql.Tx, dbc *db.DB, entity *schema.Entity, data map[string]any, sub *middleware.Subject, requestID string) (map[string]any, error) {
	record, err := query.NewBuilder(entity, tx, dbc.Dialect).Create(ctx, data)
	if err != nil {
		return nil, err
	}
	err = f.observe(ctx, tx, dbc.Dialect, WriteEvent{
		Entity: entity, Action: "create", ID: idString(record[entity.PKColumn()]), After: record,
		Subject: sub, RequestID: requestID,
	})
	return record, err
}

// createOrUpsert writes validated create input according to the conflict mode.
func (f *Factory) createOrUpsert(ctx context.Context, tx *sql.Tx, dbc *db.DB, entity *schema.Entity, data map[string]any, mode string, row query.RowFilter, sub *middleware.Subject, requestID string) (writeOutcome, error) {
	if !isUpsert(mode) {
		rec, err := f.insert(ctx, tx, dbc, entity, data, sub, requestID)
		return writeOutcome{Action: ActionCreated, Record: rec}, err
	}
	return f.upsert(ctx, tx, dbc, entity, data, mode, row, sub, requestID)
}

// upsert inserts data, or — when a row with the same natural key exists — updates it
// (mode update) or leaves it (mode ignore). row (row_access + tenant scope) limits which
// existing rows may be touched; a hidden match is errKeyHidden. A soft-deleted match is
// revived. With stale_guard, an incoming row that is not newer is skipped (unchanged).
//
// The lookup locks the row (Postgres/MySQL); two concurrent inserts of a new key can
// still race, and the loser gets a unique violation (409) — callers retry.
func (f *Factory) upsert(ctx context.Context, tx *sql.Tx, dbc *db.DB, entity *schema.Entity, data map[string]any, mode string, row query.RowFilter, sub *middleware.Subject, requestID string) (writeOutcome, error) {
	key := make(map[string]any, len(entity.NaturalKey))
	for _, col := range entity.NaturalKey {
		key[col] = data[col]
	}
	b := query.NewBuilder(entity, tx, dbc.Dialect)
	existing, err := b.FindByKey(ctx, key, query.RowFilter{}, true, true)
	if err != nil {
		return writeOutcome{}, err
	}
	if existing == nil {
		rec, err := f.insert(ctx, tx, dbc, entity, data, sub, requestID)
		return writeOutcome{Action: ActionCreated, Record: rec}, err
	}
	if row.SQL != "" {
		visible, err := b.FindByKey(ctx, key, row, true, false)
		if err != nil {
			return writeOutcome{}, err
		}
		if visible == nil {
			return writeOutcome{}, errKeyHidden
		}
	}
	if mode == ConflictIgnore {
		return writeOutcome{Action: ActionIgnored, Record: existing}, nil
	}

	upd := copyMap(data)
	stripServerManaged(entity, upd, true)
	for _, col := range entity.NaturalKey {
		delete(upd, col)
	}
	guard := query.Guard{Revive: entity.SoftDelete && existing["deleted_at"] != nil}
	if entity.StaleGuard != "" {
		guard.Column, guard.Value = entity.StaleGuard, data[entity.StaleGuard]
	}
	id := existing[entity.PKColumn()]
	rec, err := b.UpdateGuarded(ctx, id, upd, query.RowFilter{}, guard)
	switch {
	case errors.Is(err, query.ErrStale), errors.Is(err, query.ErrNoFields):
		return writeOutcome{Action: ActionUnchanged, Record: existing}, nil
	case err != nil:
		return writeOutcome{}, err
	}
	err = f.observe(ctx, tx, dbc.Dialect, WriteEvent{
		Entity: entity, Action: "update", ID: idString(id), Before: existing, After: rec,
		Subject: sub, RequestID: requestID,
	})
	return writeOutcome{Action: ActionUpdated, Record: rec, Before: existing}, err
}

// afterWrite runs the After* hook matching what a create/upsert did. Hook errors are
// logged by the caller-supplied logger; the change is already committed.
func (f *Factory) afterWrite(ctx context.Context, entity *schema.Entity, out writeOutcome) error {
	if f.plugins == nil {
		return nil
	}
	switch out.Action {
	case ActionCreated:
		return f.plugins.AfterCreate(ctx, entity.Name, copyMap(out.Record))
	case ActionUpdated:
		return f.plugins.AfterUpdate(ctx, entity.Name, copyMap(out.Record))
	}
	return nil
}

func conflictMode(opts *schema.CreateOpts) string {
	if opts == nil || opts.OnConflict == "" {
		return ConflictError
	}
	return opts.OnConflict
}

func naturalKeyError(entity *schema.Entity, field string) ValidationErrors {
	return ValidationErrors{field: fmt.Sprintf("%s is required (natural key of %s)", field, entity.Name)}
}
