package outbox

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/teleology-io/yayPI/internal/config"
	"github.com/teleology-io/yayPI/internal/db"
	"github.com/teleology-io/yayPI/internal/netsafe"
	"github.com/teleology-io/yayPI/internal/schema"
)

// Sender delivers one email (satisfied by *mailer.Mailer).
type Sender interface {
	Send(ctx context.Context, to, subject, htmlBody string) error
}

const (
	defaultWebhookAttempts = 8
	defaultEmailAttempts   = 5
	claimLease             = 2 * time.Minute
	batchSize              = 25
	defaultRetention       = 7 * 24 * time.Hour
	defaultPollInterval    = time.Second
)

// WorkerOptions tunes a Worker; zero values use the defaults.
type WorkerOptions struct {
	PollInterval time.Duration       // default 1s
	Retention    time.Duration       // delivered rows are deleted after this (default 7d)
	EmailRetry   *config.RetryConfig // default retry for emails without their own retry:
}

// Worker polls the outbox and delivers due messages.
type Worker struct {
	db         *db.DB
	webhooks   map[string]config.WebhookDef
	emails     map[string]config.EmailDef
	mailer     Sender
	public     *http.Client
	private    *http.Client
	interval   time.Duration
	retention  time.Duration
	emailRetry *config.RetryConfig

	// OnDelivery, when set, observes every attempt (metrics).
	OnDelivery func(kind string, ok bool)

	stop     chan struct{}
	wg       sync.WaitGroup
	inflight sync.WaitGroup
	lastGC   time.Time
}

// NewWorker creates a Worker. mailer may be nil when no email triggers exist.
func NewWorker(d *db.DB, webhooks []config.WebhookDef, emails []config.EmailDef, mailer Sender, opts WorkerOptions) *Worker {
	if opts.PollInterval <= 0 {
		opts.PollInterval = defaultPollInterval
	}
	if opts.Retention <= 0 {
		opts.Retention = defaultRetention
	}
	w := &Worker{
		db:         d,
		webhooks:   map[string]config.WebhookDef{},
		emails:     map[string]config.EmailDef{},
		mailer:     mailer,
		public:     netsafe.NewClient(netsafe.Options{Timeout: 30 * time.Second}),
		private:    netsafe.NewClient(netsafe.Options{Timeout: 30 * time.Second, AllowPrivate: true}),
		interval:   opts.PollInterval,
		retention:  opts.Retention,
		emailRetry: opts.EmailRetry,
		stop:       make(chan struct{}),
	}
	for _, wd := range webhooks {
		w.webhooks[wd.Name] = wd
	}
	for _, ed := range emails {
		w.emails[ed.Name] = ed
	}
	return w
}

// Start begins polling in the background.
func (w *Worker) Start() {
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		t := time.NewTicker(w.interval)
		defer t.Stop()
		for {
			select {
			case <-w.stop:
				return
			case <-t.C:
				w.RunOnce(context.Background())
			}
		}
	}()
}

// Stop stops polling and waits for in-flight deliveries (bounded by ctx). Undelivered
// messages stay in the table for the next start.
func (w *Worker) Stop(ctx context.Context) {
	close(w.stop)
	done := make(chan struct{})
	go func() { w.wg.Wait(); w.inflight.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		log.Warn().Msg("outbox: shutdown timeout; pending deliveries resume on next start")
	}
}

type row struct {
	id, kind, name, payload string
	attempts                int
}

// RunOnce claims and delivers one batch of due messages.
func (w *Worker) RunOnce(ctx context.Context) {
	d := w.db.Dialect
	table := d.QuoteIdent(schema.OutboxTable)
	now := time.Now().Unix()

	rows, err := w.db.SQL.QueryContext(ctx, d.Rebind(fmt.Sprintf(
		`SELECT id, kind, name, payload, attempts FROM %s WHERE status = 'pending' AND next_attempt_at <= $1 AND locked_until <= $2 ORDER BY next_attempt_at LIMIT %d`,
		table, batchSize)), now, now)
	if err != nil {
		log.Error().Err(err).Msg("outbox: polling failed")
		return
	}
	var due []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.kind, &r.name, &r.payload, &r.attempts); err == nil {
			due = append(due, r)
		}
	}
	rows.Close()

	for _, r := range due {
		// Claim with a lease; another replica may have taken it since the SELECT.
		res, err := w.db.SQL.ExecContext(ctx, d.Rebind(fmt.Sprintf(
			`UPDATE %s SET locked_until = $1 WHERE id = $2 AND status = 'pending' AND locked_until <= $3`, table)),
			time.Now().Add(claimLease).Unix(), r.id, now)
		if err != nil {
			continue
		}
		if n, _ := res.RowsAffected(); n != 1 {
			continue
		}
		w.inflight.Add(1)
		w.deliverAndRecord(r)
		w.inflight.Done()
	}

	if time.Since(w.lastGC) > time.Hour {
		w.lastGC = time.Now()
		_, _ = w.db.SQL.ExecContext(ctx, d.Rebind(fmt.Sprintf(
			`DELETE FROM %s WHERE status = 'done' AND created_at < $1`, table)), time.Now().Add(-w.retention).Unix())
	}
}

func (w *Worker) deliverAndRecord(r row) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	err := w.deliver(ctx, r)
	if w.OnDelivery != nil {
		w.OnDelivery(r.kind, err == nil)
	}

	d := w.db.Dialect
	table := d.QuoteIdent(schema.OutboxTable)
	if err == nil {
		_, _ = w.db.SQL.ExecContext(ctx, d.Rebind(fmt.Sprintf(
			`UPDATE %s SET status = 'done', attempts = attempts + 1, locked_until = 0, last_error = NULL WHERE id = $1`, table)), r.id)
		return
	}

	attempts := r.attempts + 1
	maxAttempts, initial, maxDelay := w.retryPolicy(r)
	errText := err.Error()
	if len(errText) > 1000 {
		errText = errText[:1000]
	}
	if attempts >= maxAttempts {
		log.Error().Str("kind", r.kind).Str("name", r.name).Str("id", r.id).Err(err).Msg("outbox: giving up after max attempts (status=dead)")
		_, _ = w.db.SQL.ExecContext(ctx, d.Rebind(fmt.Sprintf(
			`UPDATE %s SET status = 'dead', attempts = $1, locked_until = 0, last_error = $2 WHERE id = $3`, table)), attempts, errText, r.id)
		return
	}
	backoff := min(initial*time.Duration(1<<min(attempts-1, 20)), maxDelay)
	backoff = time.Duration(float64(backoff) * (0.8 + 0.4*rand.Float64())) // ±20% jitter
	log.Warn().Str("kind", r.kind).Str("name", r.name).Int("attempt", attempts).Dur("retry_in", backoff).Err(err).Msg("outbox: delivery failed")
	_, _ = w.db.SQL.ExecContext(ctx, d.Rebind(fmt.Sprintf(
		`UPDATE %s SET attempts = $1, next_attempt_at = $2, locked_until = 0, last_error = $3 WHERE id = $4`, table)),
		attempts, time.Now().Add(backoff).Unix(), errText, r.id)
}

func (w *Worker) retryPolicy(r row) (int, time.Duration, time.Duration) {
	attempts, initial, maxDelay := defaultEmailAttempts, 10*time.Second, time.Hour
	var retry *config.RetryConfig
	if r.kind == KindWebhook {
		attempts = defaultWebhookAttempts
		retry = w.webhooks[r.name].Retry
	} else {
		retry = w.emails[r.name].Retry
		if retry == nil {
			retry = w.emailRetry
		}
	}
	if retry != nil {
		if retry.MaxAttempts > 0 {
			attempts = retry.MaxAttempts
		}
		if d, err := time.ParseDuration(retry.InitialDelay); err == nil && d > 0 {
			initial = d
		}
		if d, err := time.ParseDuration(retry.MaxDelay); err == nil && d > 0 {
			maxDelay = d
		}
	}
	return attempts, initial, maxDelay
}

func (w *Worker) deliver(ctx context.Context, r row) error {
	switch r.kind {
	case KindEmail:
		if w.mailer == nil {
			return fmt.Errorf("SMTP is not configured")
		}
		var p EmailPayload
		if err := json.Unmarshal([]byte(r.payload), &p); err != nil {
			return err
		}
		return w.mailer.Send(ctx, p.To, p.Subject, p.HTML)
	case KindWebhook:
		var p WebhookPayload
		if err := json.Unmarshal([]byte(r.payload), &p); err != nil {
			return err
		}
		return w.sendWebhook(ctx, r.id, r.name, p)
	}
	return fmt.Errorf("unknown outbox kind %q", r.kind)
}

func (w *Worker) sendWebhook(ctx context.Context, id, name string, p WebhookPayload) error {
	def := w.webhooks[name]
	if t, err := time.ParseDuration(def.Timeout); def.Timeout != "" && err == nil && t > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, t)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(ctx, p.Method, p.URL, bytes.NewBufferString(p.Body))
	if err != nil {
		return err
	}
	for k, v := range p.Headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "yayPi-webhook/1")
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	req.Header.Set("X-Yaypi-Event-Id", id) // stable across retries: receivers dedupe on it
	req.Header.Set("X-Yaypi-Timestamp", ts)
	if def.Secret != "" {
		req.Header.Set("X-Yaypi-Signature", "v1="+Sign(def.Secret, ts, p.Body))
	}

	client := w.public
	if def.AllowPrivateNetwork {
		client = w.private
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

// Sign returns the hex HMAC-SHA256 of "<timestamp>.<body>" — what receivers recompute to
// verify X-Yaypi-Signature (and reject stale timestamps to block replays).
func Sign(secret, timestamp, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp + "." + body))
	return hex.EncodeToString(mac.Sum(nil))
}
