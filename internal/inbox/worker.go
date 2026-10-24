package inbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/teleology-io/yayPI/internal/config"
	"github.com/teleology-io/yayPI/internal/db"
	"github.com/teleology-io/yayPI/internal/handler"
	"github.com/teleology-io/yayPI/internal/middleware"
	"github.com/teleology-io/yayPI/internal/query"
	"github.com/teleology-io/yayPI/internal/queue"
	"github.com/teleology-io/yayPI/internal/schema"
	"github.com/teleology-io/yayPI/pkg/sdk"
)

const (
	defaultAttempts       = 8
	defaultProcessTimeout = 30 * time.Second
	batchSize             = 25
)

// Writer applies entity writes (satisfied by *handler.Factory).
type Writer interface {
	Write(ctx context.Context, reqs ...handler.WriteRequest) ([]handler.WriteResult, error)
}

// Enforcer checks Casbin policies for as_role (satisfied by *policy.Engine).
type Enforcer interface {
	Enforce(role, resource, action string) (bool, error)
}

// Options wires a Worker.
type Options struct {
	Writer    Writer
	Registry  *schema.Registry
	Enforcer  Enforcer // optional; checked against as_role
	Handlers  func(name string) (sdk.WebhookHandlerFunc, bool)
	Store     sdk.Store
	HTTP      func(sdk.HTTPOptions) *http.Client
	Logger    func(name string) sdk.Logger
	Poll      time.Duration // default 1s
	Retention time.Duration // processed rows are deleted after this (default 7d)
}

type route struct {
	def     config.WebhookRouteDef
	entity  *schema.Entity
	handler sdk.WebhookHandlerFunc
}

type compiled struct {
	def     config.WebhookDef
	routes  map[string]route
	policy  queue.Policy
	timeout time.Duration
}

// Worker processes stored inbound events.
type Worker struct {
	q        *queue.Queue
	defs     map[string]*compiled
	opts     Options
	stop     chan struct{}
	wg       sync.WaitGroup
	inflight sync.WaitGroup
	lastGC   time.Time

	// OnProcess, when set, observes every processing attempt (metrics).
	OnProcess func(webhook string, ok bool)
}

// NewWorker compiles the inbound webhooks' routes. Unknown entities or plugin handlers
// fail here, at boot.
func NewWorker(d *db.DB, defs []config.WebhookDef, opts Options) (*Worker, error) {
	if opts.Poll <= 0 {
		opts.Poll = time.Second
	}
	if opts.Retention <= 0 {
		opts.Retention = 7 * 24 * time.Hour
	}
	w := &Worker{
		q:    &queue.Queue{DB: d, Table: schema.InboxTable, Columns: []string{"webhook", "event", "payload", "created_at"}},
		defs: map[string]*compiled{},
		opts: opts,
		stop: make(chan struct{}),
	}
	for _, def := range defs {
		if !def.IsInbound() {
			continue
		}
		c := &compiled{
			def:     def,
			routes:  map[string]route{},
			policy:  queue.PolicyFrom(def.Retry, queue.Policy{MaxAttempts: defaultAttempts, Initial: 10 * time.Second, Max: time.Hour}),
			timeout: defaultProcessTimeout,
		}
		if t, err := config.ParseDuration(def.Timeout); err == nil && t > 0 {
			c.timeout = t
		}
		routes := def.Routes
		if def.Handler != "" {
			routes = map[string]config.WebhookRouteDef{"*": {Handler: def.Handler}}
		}
		for ev, rd := range routes {
			rt := route{def: rd}
			if rd.Handler != "" {
				fn, ok := lookupHandler(opts.Handlers, rd.Handler)
				if !ok {
					return nil, fmt.Errorf("webhook %q route %q: no webhook handler %q is registered (Server.RegisterWebhooks)", def.Name, ev, rd.Handler)
				}
				rt.handler = fn
			} else {
				e, ok := opts.Registry.GetEntity(rd.Entity)
				if !ok || e.Internal {
					return nil, fmt.Errorf("webhook %q route %q: unknown entity %q", def.Name, ev, rd.Entity)
				}
				rt.entity = e
			}
			c.routes[ev] = rt
		}
		w.defs[def.Name] = c
	}
	return w, nil
}

func lookupHandler(fn func(string) (sdk.WebhookHandlerFunc, bool), name string) (sdk.WebhookHandlerFunc, bool) {
	if fn == nil {
		return nil, false
	}
	return fn(name)
}

// Start begins polling in the background.
func (w *Worker) Start() {
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		t := time.NewTicker(w.opts.Poll)
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

// Stop stops polling and waits for in-flight events (bounded by ctx). Unprocessed events
// stay in the table for the next start.
func (w *Worker) Stop(ctx context.Context) {
	close(w.stop)
	done := make(chan struct{})
	go func() { w.wg.Wait(); w.inflight.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		log.Warn().Msg("inbox: shutdown timeout; pending events resume on next start")
	}
}

// RunOnce claims and processes one batch of due events.
func (w *Worker) RunOnce(ctx context.Context) {
	due, err := w.q.Due(ctx, batchSize)
	if err != nil {
		log.Error().Err(err).Msg("inbox: polling failed")
		return
	}
	for _, it := range due {
		if !w.q.Claim(ctx, it.ID) {
			continue
		}
		w.inflight.Add(1)
		w.processAndRecord(it)
		w.inflight.Done()
	}
	if time.Since(w.lastGC) > time.Hour {
		w.lastGC = time.Now()
		w.q.Purge(ctx, time.Now().Add(-w.opts.Retention))
	}
}

func (w *Worker) processAndRecord(it queue.Item) {
	name, event := it.Fields[0], it.Fields[1]
	c := w.defs[name]
	if c == nil {
		// The webhook was removed from config while events were queued.
		w.q.Fail(context.Background(), it, fmt.Errorf("webhook %q is no longer configured", name), queue.Policy{}, true)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	err := w.process(ctx, c, it, event)
	if w.OnProcess != nil {
		w.OnProcess(name, err == nil)
	}
	if err == nil {
		w.q.Done(ctx, it.ID)
		return
	}
	permanent := sdk.IsPermanent(err) ||
		(errors.Is(err, handler.ErrInvalidWrite) && !errors.Is(err, query.ErrNotFound)) // update before create: retry
	dead, retryIn := w.q.Fail(ctx, it, err, c.policy, permanent)
	if dead {
		log.Error().Str("webhook", name).Str("event", event).Str("id", it.ID).Err(err).Msg("inbox: giving up (status=dead)")
		return
	}
	log.Warn().Str("webhook", name).Str("event", event).Int("attempt", it.Attempts+1).Dur("retry_in", retryIn).Err(err).Msg("inbox: processing failed")
}

func (w *Worker) process(ctx context.Context, c *compiled, it queue.Item, event string) error {
	rt, ok := c.routes[event]
	if !ok {
		if rt, ok = c.routes["*"]; !ok {
			log.Debug().Str("webhook", c.def.Name).Str("event", event).Msg("inbox: no route for event; ignored")
			return nil
		}
	}
	var req storedRequest
	if err := json.Unmarshal([]byte(it.Fields[2]), &req); err != nil {
		return sdk.Permanent(fmt.Errorf("corrupt inbox payload: %w", err))
	}

	if rt.handler != nil {
		wc := sdk.WebhookContext{
			Ctx: ctx, Store: w.opts.Store, HTTP: w.opts.HTTP,
			Event: sdk.WebhookEvent{
				Webhook: c.def.Name, Event: event, Body: []byte(req.Body),
				Headers: http.Header(req.Headers), Query: req.Query, Attempt: it.Attempts + 1,
				ReceivedAt: receivedAt(it.Fields[3]),
			},
		}
		if w.opts.Logger != nil {
			wc.Logger = w.opts.Logger(c.def.Name)
		}
		return rt.handler(wc)
	}

	sc := newScope([]byte(req.Body), req.ContentType, http.Header(req.Headers), req.Query)
	sub := &middleware.Subject{ID: "webhook:" + c.def.Name, Role: c.def.AsRole}
	if err := w.authorize(c, rt); err != nil {
		return err
	}
	wr := handler.WriteRequest{
		Entity: rt.entity.Name, Action: rt.def.Op, Subject: sub, RequestID: "inbox:" + it.ID,
		Data: sc.renderFields(rt.entity, rt.def.Map), Key: sc.renderFields(rt.entity, rt.def.Key),
	}
	_, err := w.opts.Writer.Write(ctx, wr)
	return err
}

func receivedAt(unix string) time.Time {
	sec, err := strconv.ParseInt(unix, 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(sec, 0).UTC()
}

// authorize checks as_role against Casbin for the route's operation (an upsert needs
// create and update). Without as_role the write is a trusted system write.
func (w *Worker) authorize(c *compiled, rt route) error {
	if c.def.AsRole == "" || w.opts.Enforcer == nil {
		return nil
	}
	actions := []string{rt.def.Op}
	if rt.def.Op == "upsert" {
		actions = []string{"create", "update"}
	}
	for _, a := range actions {
		ok, err := w.opts.Enforcer.Enforce(c.def.AsRole, rt.entity.Name, a)
		if err != nil {
			return err
		}
		if !ok {
			return sdk.Permanent(fmt.Errorf("as_role %q may not %s %s", c.def.AsRole, strings.ToLower(a), rt.entity.Name))
		}
	}
	return nil
}
