package handler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/teleology-io/yayPI/internal/db"
	"github.com/teleology-io/yayPI/internal/middleware"
	"github.com/teleology-io/yayPI/internal/query"
	"github.com/teleology-io/yayPI/internal/schema"
)

// ErrInvalidWrite wraps every WriteRequest problem that retrying cannot fix (unknown
// entity, bad action, validation failure, missing key, update of a missing row).
var ErrInvalidWrite = errors.New("invalid write")

// WriteRequest is one entity write made outside an HTTP request — by an inbound webhook
// route or a plugin. It gets the same coercion, validation, plugin hooks, transaction and
// TxObservers (audit log, outbound webhooks, emails) as the CRUD handlers.
type WriteRequest struct {
	Entity string
	Action string         // create | update | upsert | delete
	Data   map[string]any // field or column name → value (create, update, upsert)
	// Key identifies the row for update/delete: the primary key field, or exactly the
	// natural_key fields.
	Key map[string]any
	// Subject is the audit actor. With a Role, field write_roles apply to it; without
	// one the write is a trusted system write (no role checks, default_from and tenant
	// columns are taken from Data as given).
	Subject   *middleware.Subject
	RequestID string
}

// WriteResult is what one WriteRequest did.
type WriteResult struct {
	Action string         // created | updated | unchanged | deleted | not_found
	Record map[string]any // the stored row (nil for delete)
}

type preparedWrite struct {
	req    WriteRequest
	entity *schema.Entity
	data   map[string]any
	key    map[string]any
}

// Write applies reqs in one transaction: all succeed or none do. Every entity must live
// in the same database.
func (f *Factory) Write(ctx context.Context, reqs ...WriteRequest) ([]WriteResult, error) {
	if len(reqs) == 0 {
		return nil, nil
	}
	var dbc *db.DB
	prepared := make([]preparedWrite, len(reqs))
	for i, req := range reqs {
		entity, ok := f.registry.GetEntity(req.Entity)
		if !ok || entity.Internal {
			return nil, fmt.Errorf("%w: unknown entity %q", ErrInvalidWrite, req.Entity)
		}
		d, err := f.db.ForEntity(entity.Name)
		if err != nil {
			return nil, err
		}
		if dbc == nil {
			dbc = d
		} else if d != dbc {
			return nil, fmt.Errorf("%w: one Write call cannot span databases (%s)", ErrInvalidWrite, entity.Name)
		}
		p, err := f.prepareWrite(writeCtx(ctx, req), entity, req, dbc.Dialect.Name())
		if err != nil {
			return nil, fmt.Errorf("write %d (%s %s): %w", i, req.Action, req.Entity, err)
		}
		prepared[i] = p
	}

	results := make([]WriteResult, len(prepared))
	outs := make([]writeOutcome, len(prepared))
	err := withTx(ctx, dbc.SQL, func(tx *sql.Tx) error {
		for i, p := range prepared {
			out, err := f.applyWrite(writeCtx(ctx, p.req), tx, dbc, p)
			if err != nil {
				return fmt.Errorf("write %d (%s %s): %w", i, p.req.Action, p.req.Entity, err)
			}
			outs[i] = out
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for i, p := range prepared {
		hctx := writeCtx(ctx, p.req)
		var hookErr error
		switch outs[i].Action {
		case ActionCreated, ActionUpdated:
			hookErr = f.afterWrite(hctx, p.entity, outs[i])
		case ActionDeleted:
			if f.plugins != nil {
				hookErr = f.plugins.AfterDelete(hctx, p.entity.Name, idString(outs[i].Before[p.entity.PKColumn()]))
			}
		}
		if hookErr != nil {
			logHookErrorCtx(hctx, p.req.RequestID, "after-"+p.req.Action, hookErr)
		}
		rec := outs[i].Record
		if rec != nil {
			rec = copyMap(rec)
			p.entity.RenderTimes(rec)
		}
		results[i] = WriteResult{Action: outs[i].Action, Record: rec}
	}
	return results, nil
}

// writeCtx carries the request's subject so hooks see the same caller as HTTP writes.
func writeCtx(ctx context.Context, req WriteRequest) context.Context {
	if req.Subject != nil {
		return middleware.WithSubject(ctx, req.Subject)
	}
	return ctx
}

func (f *Factory) prepareWrite(ctx context.Context, entity *schema.Entity, req WriteRequest, dialectName string) (preparedWrite, error) {
	p := preparedWrite{req: req, entity: entity}
	system := req.Subject == nil || req.Subject.Role == ""
	raw := copyMap(req.Data)
	if !system {
		applyWriteRoles(entity, raw, req.Subject)
	}

	switch req.Action {
	case "create", "upsert":
		stripServerManaged(entity, raw, false)
		data, verrs := prepareInput(entity, raw, false, false, dialectName)
		if verrs != nil {
			return p, fmt.Errorf("%w: %w", ErrInvalidWrite, verrs)
		}
		if !system {
			if missing := applySubjectDefaults(entity, data, req.Subject); missing != "" {
				return p, fmt.Errorf("%w: no subject value for %s", ErrInvalidWrite, missing)
			}
		}
		if req.Action == "upsert" {
			if len(entity.NaturalKey) == 0 {
				return p, fmt.Errorf("%w: entity %s has no natural_key", ErrInvalidWrite, entity.Name)
			}
			if field := missingNaturalKey(entity, data); field != "" {
				return p, fmt.Errorf("%w: %w", ErrInvalidWrite, naturalKeyError(entity, field))
			}
		}
		if f.plugins != nil {
			var err error
			if data, err = f.plugins.BeforeCreate(ctx, entity.Name, data); err != nil {
				return p, err
			}
		}
		p.data = data
	case "update":
		stripServerManaged(entity, raw, true)
		data, verrs := prepareInput(entity, raw, true, false, dialectName)
		if verrs != nil {
			return p, fmt.Errorf("%w: %w", ErrInvalidWrite, verrs)
		}
		p.data = data
		fallthrough
	case "delete":
		key, err := resolveKey(entity, req.Key, dialectName)
		if err != nil {
			return p, err
		}
		p.key = key
	default:
		return p, fmt.Errorf("%w: action %q must be create, update, upsert or delete", ErrInvalidWrite, req.Action)
	}
	return p, nil
}

func (f *Factory) applyWrite(ctx context.Context, tx *sql.Tx, dbc *db.DB, p preparedWrite) (writeOutcome, error) {
	sub := p.req.Subject
	switch p.req.Action {
	case "create":
		return f.createOrUpsert(ctx, tx, dbc, p.entity, p.data, ConflictError, query.RowFilter{}, sub, p.req.RequestID)
	case "upsert":
		return f.createOrUpsert(ctx, tx, dbc, p.entity, p.data, ConflictUpdate, query.RowFilter{}, sub, p.req.RequestID)
	}

	b := query.NewBuilder(p.entity, tx, dbc.Dialect)
	before, err := b.FindByKey(ctx, p.key, query.RowFilter{}, false, true)
	if err != nil {
		return writeOutcome{}, err
	}
	if before == nil {
		if p.req.Action == "delete" {
			return writeOutcome{Action: ActionNotFound}, nil
		}
		return writeOutcome{}, fmt.Errorf("%w: %w", ErrInvalidWrite, query.ErrNotFound)
	}
	id := before[p.entity.PKColumn()]

	if p.req.Action == "delete" {
		if f.plugins != nil {
			if err := f.plugins.BeforeDelete(ctx, p.entity.Name, idString(id)); err != nil {
				return writeOutcome{}, err
			}
		}
		if err := b.Delete(ctx, id, p.entity.SoftDelete, query.RowFilter{}); err != nil {
			return writeOutcome{}, err
		}
		err := f.observe(ctx, tx, dbc.Dialect, WriteEvent{
			Entity: p.entity, Action: "delete", ID: idString(id), Before: before, Subject: sub, RequestID: p.req.RequestID,
		})
		return writeOutcome{Action: ActionDeleted, Before: before}, err
	}

	data := p.data
	if f.plugins != nil {
		if data, err = f.plugins.BeforeUpdate(ctx, p.entity.Name, idString(id), data); err != nil {
			return writeOutcome{}, err
		}
	}
	rec, err := b.Update(ctx, id, data, query.RowFilter{})
	if errors.Is(err, query.ErrNoFields) {
		return writeOutcome{Action: ActionUnchanged, Record: before}, nil
	}
	if err != nil {
		return writeOutcome{}, err
	}
	err = f.observe(ctx, tx, dbc.Dialect, WriteEvent{
		Entity: p.entity, Action: "update", ID: idString(id), Before: before, After: rec, Subject: sub, RequestID: p.req.RequestID,
	})
	return writeOutcome{Action: ActionUpdated, Record: rec, Before: before}, err
}

// resolveKey coerces a row key, which must be the primary key alone or exactly the
// natural key, so it can match at most one row.
func resolveKey(entity *schema.Entity, key map[string]any, dialectName string) (map[string]any, error) {
	if len(key) == 0 {
		return nil, fmt.Errorf("%w: key is required", ErrInvalidWrite)
	}
	out := make(map[string]any, len(key))
	var cols []string
	for name, v := range key {
		f, ok := entity.FieldByName(name)
		if !ok {
			return nil, fmt.Errorf("%w: key: unknown field %q", ErrInvalidWrite, name)
		}
		if v == nil {
			return nil, fmt.Errorf("%w: key %s is null", ErrInvalidWrite, f.Name)
		}
		cv, msg := coerceValue(f, v, dialectName)
		if msg != "" {
			return nil, fmt.Errorf("%w: key: %s", ErrInvalidWrite, msg)
		}
		out[f.ColumnName] = cv
		cols = append(cols, f.ColumnName)
	}
	if !KeyIsUnique(entity, cols) {
		return nil, fmt.Errorf("%w: key must be the primary key or the natural key (%s)", ErrInvalidWrite, strings.Join(entity.NaturalKey, ", "))
	}
	return out, nil
}

// KeyIsUnique reports whether cols (field or column names) are the primary key alone or
// exactly the natural key.
func KeyIsUnique(entity *schema.Entity, cols []string) bool {
	set := map[string]bool{}
	for _, c := range cols {
		if f, ok := entity.FieldByName(c); ok {
			set[f.ColumnName] = true
		} else {
			return false
		}
	}
	if len(set) == 1 && set[entity.PKColumn()] {
		return true
	}
	if len(set) != len(entity.NaturalKey) || len(set) == 0 {
		return false
	}
	for _, c := range entity.NaturalKey {
		if !set[c] {
			return false
		}
	}
	return true
}
