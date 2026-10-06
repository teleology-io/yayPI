// Package seed applies seed data to the database idempotently.
package seed

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/bcrypt"

	"github.com/rs/zerolog/log"
	"github.com/teleology-io/yayPI/internal/config"
	"github.com/teleology-io/yayPI/internal/db"
	"github.com/teleology-io/yayPI/internal/schema"
)

// Run inserts seed rows for all SeedDef entries. Each row is only inserted if
// no existing row matches the key_field value, making it safe to run repeatedly.
func Run(ctx context.Context, seeds []config.SeedDef, reg *schema.Registry, dbMgr *db.Manager) error {
	for _, sd := range seeds {
		if err := applySeed(ctx, sd, reg, dbMgr); err != nil {
			return fmt.Errorf("seed %q: %w", sd.Entity, err)
		}
	}
	return nil
}

func applySeed(ctx context.Context, sd config.SeedDef, reg *schema.Registry, dbMgr *db.Manager) error {
	entity, ok := reg.GetEntity(sd.Entity)
	if !ok {
		return fmt.Errorf("entity %q not found in registry", sd.Entity)
	}
	if sd.KeyField == "" {
		return fmt.Errorf("key_field is required")
	}

	dbc, err := dbMgr.ForEntity(sd.Entity)
	if err != nil {
		return fmt.Errorf("getting database connection: %w", err)
	}

	table := dbc.Dialect.QuoteIdent(entity.Table)
	keyCol := dbc.Dialect.QuoteIdent(toSnakeCase(sd.KeyField))
	inserted, skipped := 0, 0

	for _, row := range sd.Data {
		if err := prepareUserRow(entity, row); err != nil {
			return err
		}
		keyVal, ok := row[sd.KeyField]
		if !ok {
			return fmt.Errorf("row missing key_field %q", sd.KeyField)
		}

		exists, err := rowExists(ctx, dbc, table, keyCol, keyVal)
		if err != nil {
			return fmt.Errorf("checking existence: %w", err)
		}
		if exists {
			skipped++
			continue
		}

		if err := insertRow(ctx, dbc, table, row); err != nil {
			return fmt.Errorf("inserting row: %w", err)
		}
		inserted++
	}

	log.Info().Str("entity", sd.Entity).Int("inserted", inserted).Int("skipped", skipped).Msg("seed applied")
	return nil
}

func rowExists(ctx context.Context, dbc *db.DB, quotedTable, quotedKeyCol string, keyVal interface{}) (bool, error) {
	// Build query with $1 placeholder then rebind for the dialect.
	q := dbc.Dialect.Rebind(fmt.Sprintf("SELECT 1 FROM %s WHERE %s = $1 LIMIT 1", quotedTable, quotedKeyCol))
	row := dbc.SQL.QueryRowContext(ctx, q, keyVal)
	var n int
	if err := row.Scan(&n); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func insertRow(ctx context.Context, dbc *db.DB, quotedTable string, row map[string]interface{}) error {
	cols := make([]string, 0, len(row))
	vals := make([]interface{}, 0, len(row))
	placeholders := make([]string, 0, len(row))

	i := 1
	for k, v := range row {
		cols = append(cols, dbc.Dialect.QuoteIdent(toSnakeCase(k)))
		vals = append(vals, v)
		placeholders = append(placeholders, fmt.Sprintf("$%d", i))
		i++
	}

	q := dbc.Dialect.Rebind(fmt.Sprintf(
		"INSERT INTO %s (%s) VALUES (%s)",
		quotedTable,
		strings.Join(cols, ", "),
		strings.Join(placeholders, ", "),
	))

	_, err := dbc.SQL.ExecContext(ctx, q, vals...)
	return err
}

// prepareUserRow lets seeds for the built-in User entity specify a plain `password`
// (typically ${ENV_VAR}): it is bcrypt-hashed into password_hash and never stored as is.
// Emails are normalised the same way registration does.
func prepareUserRow(entity *schema.Entity, row map[string]interface{}) error {
	if entity.Name != schema.BuiltinUserEntityName {
		return nil
	}
	if email, ok := row["email"].(string); ok {
		row["email"] = strings.ToLower(strings.TrimSpace(email))
	}
	if pw, ok := row["password"].(string); ok {
		delete(row, "password")
		if pw == "" {
			return fmt.Errorf("seed user password is empty (is the env var set?)")
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(pw), 12)
		if err != nil {
			return err
		}
		row["password_hash"] = string(hash)
	}
	return nil
}

// toSnakeCase converts camelCase/PascalCase to snake_case.
func toSnakeCase(s string) string {
	var b strings.Builder
	for i, r := range s {
		if i > 0 && r >= 'A' && r <= 'Z' {
			b.WriteByte('_')
		}
		b.WriteRune(r)
	}
	return strings.ToLower(b.String())
}
