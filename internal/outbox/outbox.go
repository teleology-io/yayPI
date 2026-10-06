// Package outbox delivers webhooks and emails reliably.
//
// Messages are rendered and written to the yaypi_outbox table inside the same database
// transaction as the entity change that triggered them (see Observer), so a message
// exists if and only if the change committed. A Worker then delivers pending messages
// with exponential backoff, marks them done, or "dead" after the configured attempts.
// Several replicas can run workers concurrently: each message is claimed with a
// conditional lease update before delivery.
package outbox

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/teleology-io/yayPI/internal/dialect"
	"github.com/teleology-io/yayPI/internal/schema"
)

// Message kinds.
const (
	KindWebhook = "webhook"
	KindEmail   = "email"
)

// WebhookPayload is a rendered webhook request.
type WebhookPayload struct {
	URL     string            `json:"url"`
	Method  string            `json:"method"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body"`
}

// EmailPayload is a rendered email.
type EmailPayload struct {
	To      string `json:"to"`
	Subject string `json:"subject"`
	HTML    string `json:"html"`
}

// execer is satisfied by *sql.Tx and *sql.DB.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Enqueue writes one message (inside tx when given a *sql.Tx).
func Enqueue(ctx context.Context, tx execer, d dialect.Dialect, kind, name string, payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	now := time.Now().Unix()
	_, err = tx.ExecContext(ctx, d.Rebind(fmt.Sprintf(
		`INSERT INTO %s (id, kind, name, payload, status, attempts, next_attempt_at, locked_until, created_at) VALUES ($1, $2, $3, $4, 'pending', 0, $5, 0, $6)`,
		d.QuoteIdent(schema.OutboxTable))),
		uuid.NewString(), kind, name, string(b), now, now)
	return err
}
