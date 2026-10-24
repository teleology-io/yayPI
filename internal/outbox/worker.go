package outbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/teleology-io/yayPI/internal/config"
	"github.com/teleology-io/yayPI/internal/db"
	"github.com/teleology-io/yayPI/internal/netsafe"
	"github.com/teleology-io/yayPI/internal/queue"
	"github.com/teleology-io/yayPI/internal/schema"
	"github.com/teleology-io/yayPI/internal/signature"
)

// Sender delivers one email (satisfied by *mailer.Mailer).
type Sender interface {
	Send(ctx context.Context, to, subject, htmlBody string) error
}

const (
	defaultWebhookAttempts = 8
	defaultEmailAttempts   = 5
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
	q          *queue.Queue
	webhooks   map[string]config.WebhookDef
	schemes    map[string]signedBy
	emails     map[string]config.EmailDef
	mailer     Sender
	public     *http.Client
	private    *http.Client
	interval   time.Duration
	retention  time.Duration
	emailRetry *config.RetryConfig

	// OnDelivery, when set, observes every attempt (metrics). name is the webhook or
	// email name.
	OnDelivery func(kind, name string, ok bool)

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
		q:          &queue.Queue{DB: d, Table: schema.OutboxTable, Columns: []string{"kind", "name", "payload"}},
		webhooks:   map[string]config.WebhookDef{},
		schemes:    map[string]signedBy{},
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
		scheme, err := signature.Resolve(wd.Signature, wd.Secret)
		w.schemes[wd.Name] = signedBy{scheme, err}
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

// signedBy is a webhook's resolved signing scheme (nil = unsigned). A resolution error
// (already reported at boot by NewObserver) fails each delivery rather than sending it
// unsigned.
type signedBy struct {
	scheme *signature.Scheme
	err    error
}

type row struct {
	queue.Item
	kind, name, payload string
}

// RunOnce claims and delivers one batch of due messages.
func (w *Worker) RunOnce(ctx context.Context) {
	due, err := w.q.Due(ctx, batchSize)
	if err != nil {
		log.Error().Err(err).Msg("outbox: polling failed")
		return
	}
	for _, it := range due {
		// Claim with a lease; another replica may have taken it since the SELECT.
		if !w.q.Claim(ctx, it.ID) {
			continue
		}
		w.inflight.Add(1)
		w.deliverAndRecord(row{Item: it, kind: it.Fields[0], name: it.Fields[1], payload: it.Fields[2]})
		w.inflight.Done()
	}

	if time.Since(w.lastGC) > time.Hour {
		w.lastGC = time.Now()
		w.q.Purge(ctx, time.Now().Add(-w.retention))
	}
}

func (w *Worker) deliverAndRecord(r row) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	err := w.deliver(ctx, r)
	if w.OnDelivery != nil {
		w.OnDelivery(r.kind, r.name, err == nil)
	}
	if err == nil {
		w.q.Done(ctx, r.ID)
		return
	}
	dead, retryIn := w.q.Fail(ctx, r.Item, err, w.retryPolicy(r), false)
	if dead {
		log.Error().Str("kind", r.kind).Str("name", r.name).Str("id", r.ID).Err(err).Msg("outbox: giving up after max attempts (status=dead)")
		return
	}
	log.Warn().Str("kind", r.kind).Str("name", r.name).Int("attempt", r.Attempts+1).Dur("retry_in", retryIn).Err(err).Msg("outbox: delivery failed")
}

func (w *Worker) retryPolicy(r row) queue.Policy {
	def := queue.Policy{MaxAttempts: defaultEmailAttempts, Initial: 10 * time.Second, Max: time.Hour}
	if r.kind == KindWebhook {
		def.MaxAttempts = defaultWebhookAttempts
		return queue.PolicyFrom(w.webhooks[r.name].Retry, def)
	}
	retry := w.emails[r.name].Retry
	if retry == nil {
		retry = w.emailRetry
	}
	return queue.PolicyFrom(retry, def)
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
		return w.sendWebhook(ctx, r.ID, r.name, p)
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
	now := time.Now()
	req.Header.Set("X-Yaypi-Event-Id", id) // stable across retries: receivers dedupe on it
	req.Header.Set("X-Yaypi-Timestamp", strconv.FormatInt(now.Unix(), 10))
	sig := w.schemes[name]
	if sig.err != nil {
		return sig.err
	}
	if sig.scheme != nil {
		for k, v := range sig.scheme.Sign(def.Secret, []byte(p.Body), now) {
			req.Header.Set(k, v)
		}
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
// verify X-Yaypi-Signature (and reject stale timestamps to block replays). It is the
// yaypi_v1 scheme; see internal/signature for the others.
func Sign(secret, timestamp, body string) string {
	ts, _ := strconv.ParseInt(timestamp, 10, 64)
	s, _ := signature.Resolve(nil, secret)
	return s.Sign(secret, []byte(body), time.Unix(ts, 0))["X-Yaypi-Signature"][len("v1="):]
}
