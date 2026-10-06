// Package apikey resolves API keys to authenticated subjects, from either a static list in
// config or a database table.
//
// Keys are compared by SHA-256 digest. For DB-backed keys the table stores only the hex
// digest (key_hash: sha256, the default), so a database leak does not expose usable keys;
// generate a key and its digest with `yaypi apikey generate`.
package apikey

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/teleology-io/yayPI/internal/config"
	"github.com/teleology-io/yayPI/internal/db"
	"github.com/teleology-io/yayPI/internal/middleware"
	"github.com/teleology-io/yayPI/internal/schema"
)

// Hash returns the hex SHA-256 digest stored for a key.
func Hash(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// Generate returns a new random key ("yk_" + 32 random bytes, base64url) and its digest.
func Generate() (key, hash string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	key = "yk_" + base64.RawURLEncoding.EncodeToString(b)
	return key, Hash(key), nil
}

// StaticLookup resolves keys from the config list. Keys are indexed by digest, so the map
// lookup never compares secret bytes directly (no timing side channel on the key).
func StaticLookup(keys []config.StaticAPIKey) middleware.APIKeyLookup {
	byHash := make(map[string]*middleware.Subject, len(keys))
	for i, k := range keys {
		if k.Key == "" {
			continue
		}
		id := k.Name
		if id == "" {
			id = fmt.Sprintf("apikey:%d", i)
		}
		byHash[Hash(k.Key)] = &middleware.Subject{ID: id, Role: k.Role}
	}
	return func(_ context.Context, key string) *middleware.Subject {
		if s, ok := byHash[Hash(key)]; ok {
			cp := *s
			return &cp
		}
		return nil
	}
}

// DBLookup resolves keys from the configured entity's table. Returned subjects carry an
// ID (the owner column when present, else the key row's primary key) so row_access
// filters on :subject.id work for API-key callers too.
func DBLookup(cfg *config.APIKeyConfig, reg *schema.Registry, dbm *db.Manager) (middleware.APIKeyLookup, error) {
	entity, ok := reg.GetEntity(cfg.Entity)
	if !ok {
		return nil, fmt.Errorf("api_keys.entity %q not found", cfg.Entity)
	}
	dbc := dbm.Default()
	if entity.Database != "" {
		d, err := dbm.Get(entity.Database)
		if err != nil {
			return nil, err
		}
		dbc = d
	}
	col := func(name, def string) (string, error) {
		if name == "" {
			name = def
		}
		for _, f := range entity.Fields {
			if strings.EqualFold(f.Name, name) || strings.EqualFold(f.ColumnName, name) {
				return f.ColumnName, nil
			}
		}
		return "", fmt.Errorf("api_keys: entity %q has no field %q", entity.Name, name)
	}
	keyCol, err := col(cfg.KeyField, "token")
	if err != nil {
		return nil, err
	}
	roleCol, err := col(cfg.RoleField, "role")
	if err != nil {
		return nil, err
	}
	subjectCol := entity.PKColumn()
	if cfg.SubjectField != "" {
		if subjectCol, err = col(cfg.SubjectField, ""); err != nil {
			return nil, err
		}
	} else if entity.HasColumn("user_id") {
		subjectCol = "user_id"
	}

	hashed := cfg.KeyHash == "" || cfg.KeyHash == "sha256"
	if !hashed && cfg.KeyHash != "plain" {
		return nil, fmt.Errorf("api_keys.key_hash %q must be sha256 or plain", cfg.KeyHash)
	}
	if !hashed {
		log.Warn().Msg("api_keys.key_hash is plain: keys are stored in cleartext; migrate to sha256")
	}

	d := dbc.Dialect
	q := fmt.Sprintf(`SELECT %s, %s FROM %s WHERE %s = $1`,
		d.QuoteIdent(roleCol), d.QuoteIdent(subjectCol), d.QuoteIdent(entity.Table), d.QuoteIdent(keyCol))
	hasExpiry := entity.HasColumn("expires_at")
	if hasExpiry {
		q += fmt.Sprintf(" AND (%s IS NULL OR %s > $2)", d.QuoteIdent("expires_at"), d.QuoteIdent("expires_at"))
	}
	if entity.SoftDelete {
		q += " AND " + d.QuoteIdent("deleted_at") + " IS NULL"
	}
	if entity.HasColumn("revoked_at") {
		q += " AND " + d.QuoteIdent("revoked_at") + " IS NULL"
	}
	q = d.Rebind(q + " LIMIT 1")

	return func(ctx context.Context, key string) *middleware.Subject {
		lookup := key
		if hashed {
			lookup = Hash(key)
		}
		args := []any{lookup}
		if hasExpiry {
			args = append(args, time.Now().UTC())
		}
		var role, subject any
		if err := dbc.SQL.QueryRowContext(ctx, q, args...).Scan(&role, &subject); err != nil {
			return nil
		}
		return &middleware.Subject{ID: str(subject), Role: str(role)}
	}, nil
}

func str(v any) string {
	switch s := v.(type) {
	case nil:
		return ""
	case string:
		return s
	case []byte:
		return string(s)
	}
	return fmt.Sprint(v)
}
