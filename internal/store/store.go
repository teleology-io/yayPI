// Package store implements sdk.Store: plugin database access that goes through yayPi's
// dialect layer (placeholders, quoting) and entity write path (validation, hooks, audit
// log, outbound webhooks), plus incremental-sync cursors.
package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/teleology-io/yayPI/internal/db"
	"github.com/teleology-io/yayPI/internal/handler"
	"github.com/teleology-io/yayPI/internal/middleware"
	"github.com/teleology-io/yayPI/internal/schema"
	"github.com/teleology-io/yayPI/internal/timefmt"
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

type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Query implements sdk.Store.
func (s *Store) Query(ctx context.Context, q string, args ...any) ([]map[string]any, error) {
	return s.query(ctx, s.db.SQL, q, args)
}

// Exec implements sdk.Store.
func (s *Store) Exec(ctx context.Context, q string, args ...any) (int64, error) {
	return s.exec(ctx, s.db.SQL, q, args)
}

// WithTx implements sdk.Store.
func (s *Store) WithTx(ctx context.Context, fn func(tx sdk.Tx) error) (err error) {
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
	if err = fn(txWrap{s: s, tx: tx}); err != nil {
		return err
	}
	return tx.Commit()
}

type txWrap struct {
	s  *Store
	tx *sql.Tx
}

func (t txWrap) Query(ctx context.Context, q string, args ...any) ([]map[string]any, error) {
	return t.s.query(ctx, t.tx, q, args)
}

func (t txWrap) Exec(ctx context.Context, q string, args ...any) (int64, error) {
	return t.s.exec(ctx, t.tx, q, args)
}

func (s *Store) query(ctx context.Context, x querier, q string, args []any) ([]map[string]any, error) {
	rows, err := x.QueryContext(ctx, s.db.Dialect.Rebind(q), s.bind(args)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := make(map[string]any, len(cols))
		for i, c := range cols {
			if b, ok := vals[i].([]byte); ok {
				row[c] = string(b)
			} else {
				row[c] = vals[i]
			}
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (s *Store) exec(ctx context.Context, x querier, q string, args []any) (int64, error) {
	res, err := x.ExecContext(ctx, s.db.Dialect.Rebind(q), s.bind(args)...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// bind stores times on SQLite in the same sortable text form the query builder uses.
func (s *Store) bind(args []any) []any {
	if s.db.Dialect.Name() != "sqlite" {
		return args
	}
	var out []any // copied on the first time value; the caller keeps its slice
	for i, v := range args {
		if t, ok := v.(time.Time); ok {
			if out == nil {
				out = append([]any(nil), args...)
			}
			out[i] = t.UTC().Format(timefmt.SQLiteLayout)
		}
	}
	if out == nil {
		return args
	}
	return out
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
