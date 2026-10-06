package handler

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"time"

	"github.com/teleology-io/yayPI/internal/db"
	"github.com/teleology-io/yayPI/internal/middleware"
	"github.com/teleology-io/yayPI/internal/schema"
)

const (
	idempotencyHeader     = "Idempotency-Key"
	defaultIdempotencyTTL = 24 * time.Hour
	maxIdempotencyKey     = 255
)

// captureWriter tees the response so it can be stored for replay.
type captureWriter struct {
	http.ResponseWriter
	status int
	buf    bytes.Buffer
}

func (c *captureWriter) WriteHeader(status int) {
	c.status = status
	c.ResponseWriter.WriteHeader(status)
}

func (c *captureWriter) Write(b []byte) (int, error) {
	if c.status == 0 {
		c.status = http.StatusOK
	}
	c.buf.Write(b)
	return c.ResponseWriter.Write(b)
}

// withIdempotency makes a POST safe to retry. With an Idempotency-Key header, the first
// request runs normally and its response is stored; a retry with the same key
// and identical body replays that response for the configured window (server.idempotency_ttl,
// default 24h) instead of creating a duplicate. Keys are
// scoped to the caller, method and path. Without the header, next runs unchanged.
func (f *Factory) withIdempotency(w http.ResponseWriter, r *http.Request, next func(http.ResponseWriter, *http.Request)) {
	key := r.Header.Get(idempotencyHeader)
	if key == "" || f.db == nil {
		next(w, r)
		return
	}
	if len(key) > maxIdempotencyKey {
		writeError(w, r, http.StatusBadRequest, "Idempotency-Key is too long")
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		if middleware.IsBodyTooLarge(err) {
			writeError(w, r, http.StatusRequestEntityTooLarge, "request body too large")
		} else {
			writeError(w, r, http.StatusBadRequest, "could not read request body")
		}
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))

	subjectID := ""
	if sub := middleware.GetSubject(r); sub != nil {
		subjectID = sub.ID
	}
	keyHash := sha256Hex(subjectID + "\x00" + r.Method + "\x00" + r.URL.Path + "\x00" + key)
	fingerprint := sha256Hex(string(body))
	dbc := f.db.Default()

	ttl := f.idempotencyTTL
	if ttl <= 0 {
		ttl = defaultIdempotencyTTL
	}
	claimed, stored, err := claimIdempotencyKey(r, dbc, keyHash, fingerprint, ttl)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "idempotency check failed")
		return
	}
	if !claimed {
		switch {
		case stored.fingerprint != fingerprint:
			writeError(w, r, http.StatusUnprocessableEntity, "Idempotency-Key was already used with a different request body")
		case stored.status == 0:
			writeError(w, r, http.StatusConflict, "a request with this Idempotency-Key is still in progress")
		default:
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Idempotent-Replayed", "true")
			w.WriteHeader(stored.status)
			_, _ = io.WriteString(w, stored.body)
		}
		return
	}

	cw := &captureWriter{ResponseWriter: w}
	next(cw, r)

	d := dbc.Dialect
	table := d.QuoteIdent(schema.IdempotencyTable)
	ctx := r.Context()
	if cw.status >= 500 || cw.status == 0 {
		// Server failure: release the key so the client can retry.
		_, _ = dbc.SQL.ExecContext(ctx, d.Rebind(fmt.Sprintf(`DELETE FROM %s WHERE key_hash = $1`, table)), keyHash)
		return
	}
	_, _ = dbc.SQL.ExecContext(ctx, d.Rebind(fmt.Sprintf(`UPDATE %s SET status = $1, body = $2 WHERE key_hash = $3`, table)),
		cw.status, cw.buf.String(), keyHash)
}

type storedIdempotency struct {
	fingerprint string
	status      int
	body        string
}

// claimIdempotencyKey inserts the key; on conflict it returns the stored entry (expired
// entries are replaced).
func claimIdempotencyKey(r *http.Request, dbc *db.DB, keyHash, fingerprint string, ttl time.Duration) (bool, storedIdempotency, error) {
	ctx := r.Context()
	d := dbc.Dialect
	table := d.QuoteIdent(schema.IdempotencyTable)
	now := time.Now()

	if rand.IntN(100) == 0 { // opportunistic cleanup
		_, _ = dbc.SQL.ExecContext(ctx, d.Rebind(fmt.Sprintf(`DELETE FROM %s WHERE created_at < $1`, table)),
			now.Add(-ttl).Unix())
	}

	for attempt := 0; attempt < 2; attempt++ {
		_, err := dbc.SQL.ExecContext(ctx, d.Rebind(fmt.Sprintf(
			`INSERT INTO %s (key_hash, fingerprint, status, created_at) VALUES ($1, $2, 0, $3)`, table)),
			keyHash, fingerprint, now.Unix())
		if err == nil {
			return true, storedIdempotency{}, nil
		}
		if !d.IsUniqueViolation(err) {
			return false, storedIdempotency{}, err
		}
		var s storedIdempotency
		var body sql.NullString
		var created int64
		err = dbc.SQL.QueryRowContext(ctx, d.Rebind(fmt.Sprintf(
			`SELECT fingerprint, status, body, created_at FROM %s WHERE key_hash = $1`, table)), keyHash).
			Scan(&s.fingerprint, &s.status, &body, &created)
		if err != nil {
			return false, storedIdempotency{}, err
		}
		if now.Sub(time.Unix(created, 0)) < ttl {
			s.body = body.String
			return false, s, nil
		}
		_, _ = dbc.SQL.ExecContext(ctx, d.Rebind(fmt.Sprintf(`DELETE FROM %s WHERE key_hash = $1`, table)), keyHash)
	}
	return false, storedIdempotency{}, fmt.Errorf("could not claim idempotency key")
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
