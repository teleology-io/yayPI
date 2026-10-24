// Package rawsql runs plugin SQL ($1, $2 placeholders on every dialect, rows
// as column-keyed maps) on a database handle or a transaction. Shared by
// sdk.Store and the transaction that entity hooks receive.
package rawsql

import (
	"context"
	"database/sql"
	"time"

	"github.com/teleology-io/yayPI/internal/dialect"
	"github.com/teleology-io/yayPI/internal/timefmt"
	"github.com/teleology-io/yayPI/pkg/sdk"
)

// Querier is a *sql.DB or *sql.Tx.
type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Query runs q and returns its rows ([]byte columns as strings).
func Query(ctx context.Context, d dialect.Dialect, x Querier, q string, args []any) ([]map[string]any, error) {
	rows, err := x.QueryContext(ctx, d.Rebind(q), bind(d, args)...)
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

// Exec runs q and returns the number of affected rows.
func Exec(ctx context.Context, d dialect.Dialect, x Querier, q string, args []any) (int64, error) {
	res, err := x.ExecContext(ctx, d.Rebind(q), bind(d, args)...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Tx wraps a transaction as an sdk.Tx.
func Tx(d dialect.Dialect, tx *sql.Tx) sdk.Tx { return txWrap{d: d, tx: tx} }

type txWrap struct {
	d  dialect.Dialect
	tx *sql.Tx
}

func (t txWrap) Query(ctx context.Context, q string, args ...any) ([]map[string]any, error) {
	return Query(ctx, t.d, t.tx, q, args)
}

func (t txWrap) Exec(ctx context.Context, q string, args ...any) (int64, error) {
	return Exec(ctx, t.d, t.tx, q, args)
}

// bind stores times on SQLite in the same sortable text form the query builder uses.
func bind(d dialect.Dialect, args []any) []any {
	if d.Name() != "sqlite" {
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
