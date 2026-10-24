// Package store implements sdk.Store: plugin database access that goes through yayPi's
// dialect layer (placeholders, quoting) and entity write path (validation, hooks, audit
// log, outbound webhooks), plus incremental-sync cursors.
package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/teleology-io/yayPI/internal/db"
	"github.com/teleology-io/yayPI/internal/handler"
	"github.com/teleology-io/yayPI/internal/middleware"
	"github.com/teleology-io/yayPI/internal/plugin"
	"github.com/teleology-io/yayPI/internal/rawsql"
	"github.com/teleology-io/yayPI/internal/schema"
	"github.com/teleology-io/yayPI/pkg/sdk"
)

// Store is the sdk.Store implementation over the default database.
type Store struct {
	db      *db.DB
	writer  *handler.Factory
	cursors bool // yaypi_sync_cursors is registered (and so migrated)
}

var _ sdk.Store = (*Store)(nil)

// New returns a Store over the default database of dbm.
func New(dbm *db.Manager, writer *handler.Factory, reg *schema.Registry) *Store {
	_, cursors := reg.GetEntity(schema.SyncCursorEntityName)
	return &Store{db: dbm.Default(), writer: writer, cursors: cursors}
}

// Dialect implements sdk.Store.
func (s *Store) Dialect() string { return s.db.Dialect.Name() }

// QuoteIdent implements sdk.Store.
func (s *Store) QuoteIdent(name string) string { return s.db.Dialect.QuoteIdent(name) }

// on returns where plugin SQL runs: inside an entity hook, the write's own
// transaction (a second connection would deadlock a one-connection pool and
// wouldn't see the write); otherwise the database.
func (s *Store) on(ctx context.Context) rawsql.Querier {
	if tx := plugin.WriteTx(ctx); tx != nil {
		return tx
	}
	return s.db.SQL
}

// Query implements sdk.Store.
func (s *Store) Query(ctx context.Context, q string, args ...any) ([]map[string]any, error) {
	return rawsql.Query(ctx, s.db.Dialect, s.on(ctx), q, args)
}

// Exec implements sdk.Store.
func (s *Store) Exec(ctx context.Context, q string, args ...any) (int64, error) {
	return rawsql.Exec(ctx, s.db.Dialect, s.on(ctx), q, args)
}

// WithTx implements sdk.Store. Inside an entity hook fn runs in the write's
// transaction (committed with the write).
func (s *Store) WithTx(ctx context.Context, fn func(tx sdk.Tx) error) (err error) {
	if tx := plugin.WriteTx(ctx); tx != nil {
		return fn(rawsql.Tx(s.db.Dialect, tx))
	}
	tx, err := s.db.SQL.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if err = fn(rawsql.Tx(s.db.Dialect, tx)); err != nil {
		return err
	}
	return tx.Commit()
}

// Read implements sdk.Store. Invalid requests come back as sdk.Permanent.
func (s *Store) Read(ctx context.Context, req sdk.ReadRequest) (sdk.ReadPage, error) {
	p, err := s.writer.Read(ctx, readRequest(req))
	return sdk.ReadPage{Rows: p.Rows, Next: p.Next}, readErr(err)
}

// ReadAll implements sdk.Store.
func (s *Store) ReadAll(ctx context.Context, req sdk.ReadRequest) ([]map[string]any, error) {
	rows, err := s.writer.ReadAll(ctx, readRequest(req))
	return rows, readErr(err)
}

func readRequest(req sdk.ReadRequest) handler.ReadRequest {
	hr := handler.ReadRequest{Entity: req.Entity, Via: req.Via, Where: req.Where, Sort: req.Sort, Limit: req.Limit, Cursor: req.Cursor}
	if req.Subject != nil {
		hr.Subject = &middleware.Subject{ID: req.Subject.ID, Role: req.Subject.Role, Email: req.Subject.Email, Tenant: req.Subject.Tenant}
	}
	return hr
}

func readErr(err error) error {
	if errors.Is(err, handler.ErrInvalidRead) {
		return sdk.Permanent(err)
	}
	return err
}

// Write implements sdk.Store. Problems retrying cannot fix come back as sdk.Permanent.
func (s *Store) Write(ctx context.Context, reqs ...sdk.WriteRequest) ([]sdk.WriteResult, error) {
	hreqs := make([]handler.WriteRequest, len(reqs))
	for i, r := range reqs {
		hreqs[i] = handler.WriteRequest{Entity: r.Entity, Action: r.Action, Data: r.Data, Key: r.Key}
		if r.Subject != nil {
			hreqs[i].Subject = &middleware.Subject{ID: r.Subject.ID, Role: r.Subject.Role, Email: r.Subject.Email, Tenant: r.Subject.Tenant}
		}
	}
	res, err := s.writer.Write(ctx, hreqs...)
	if err != nil {
		if errors.Is(err, handler.ErrInvalidWrite) {
			return nil, sdk.Permanent(err)
		}
		return nil, err
	}
	out := make([]sdk.WriteResult, len(res))
	for i, r := range res {
		out[i] = sdk.WriteResult{Action: r.Action, Record: r.Record}
	}
	return out, nil
}

// Upsert implements sdk.Store.
func (s *Store) Upsert(ctx context.Context, entity string, rows ...map[string]any) ([]sdk.WriteResult, error) {
	reqs := make([]sdk.WriteRequest, len(rows))
	for i, r := range rows {
		reqs[i] = sdk.WriteRequest{Entity: entity, Action: "upsert", Data: r}
	}
	return s.Write(ctx, reqs...)
}

var errNoCursors = errors.New("sync cursors need the yaypi_sync_cursors table: list the plugin under plugins: in yaypi.yaml, then migrate")

func cursorID(source, key string) string {
	sum := sha256.Sum256([]byte(source + "\x00" + key))
	return hex.EncodeToString(sum[:])
}

// Cursor implements sdk.Store.
func (s *Store) Cursor(ctx context.Context, source, key string) (string, error) {
	if !s.cursors {
		return "", errNoCursors
	}
	d := s.db.Dialect
	rows, err := s.Query(ctx, fmt.Sprintf(`SELECT %s FROM %s WHERE id = $1`, d.QuoteIdent("value"), d.QuoteIdent(schema.SyncCursorTable)), cursorID(source, key))
	if err != nil || len(rows) == 0 {
		return "", err
	}
	v, _ := rows[0]["value"].(string)
	return v, nil
}

// SetCursor implements sdk.Store.
func (s *Store) SetCursor(ctx context.Context, source, key, value string) error {
	if !s.cursors {
		return errNoCursors
	}
	d := s.db.Dialect
	table, val := d.QuoteIdent(schema.SyncCursorTable), d.QuoteIdent("value")
	id, now := cursorID(source, key), time.Now().Unix()
	update := func() (int64, error) {
		return s.Exec(ctx, fmt.Sprintf(`UPDATE %s SET %s = $1, updated_at = $2 WHERE id = $3`, table, val), value, now, id)
	}
	n, err := update()
	if err != nil || n > 0 {
		return err
	}
	// No row yet (or MySQL reporting 0 changed rows for an identical value): insert, and
	// if a concurrent writer — or that unchanged row — got there first, update instead.
	_, err = s.Exec(ctx, fmt.Sprintf(`INSERT INTO %s (id, source, cursor_key, %s, updated_at) VALUES ($1, $2, $3, $4, $5)`, table, val),
		id, source, key, value, now)
	if err != nil && d.IsUniqueViolation(err) {
		_, err = update()
	}
	return err
}
