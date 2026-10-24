package sdk

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/teleology-io/yayPI/internal/apierr"
)

// PluginInfo contains metadata about a plugin.
type PluginInfo struct {
	Name        string
	Version     string
	Description string
}

// Logger is the interface plugins use for structured logging.
type Logger interface {
	Info(msg string, fields ...any)
	Error(msg string, err error, fields ...any)
}

// InitContext is passed to Plugin.Init.
type InitContext struct {
	Config map[string]any
	Logger Logger
	// Store is database access through yayPi; nil when no database is configured.
	Store Store
	// HTTP returns an outbound client that blocks private/loopback/metadata addresses
	// (unless AllowPrivateNetwork) — use it for any user-influenced URL.
	HTTP func(HTTPOptions) *http.Client
}

// HTTPOptions configures an outbound client from InitContext.HTTP.
type HTTPOptions struct {
	Timeout             time.Duration // default 30s
	AllowPrivateNetwork bool
}

// Subject holds the authenticated caller's identity, mirroring middleware.Subject
// but defined here so plugins don't import internal packages.
type Subject struct {
	ID     string
	Role   string
	Email  string
	Tenant string
}

// HookContext is passed to each entity hook call.
type HookContext struct {
	Ctx       context.Context
	RequestID string
	Subject   *Subject // nil when the request is unauthenticated
	// Tx is the write's transaction in BeforeUpdate and BeforeDelete (nil in
	// BeforeCreate, which runs before it opens, and in After* hooks, which run
	// after it commits). Read related rows through it: they see the write's
	// view, and a pool with one connection (SQLite) can't deadlock. Store.Query
	// and Store.Exec called with Ctx join it automatically.
	Tx Tx
	// Before is the stored row (column names) in BeforeUpdate and BeforeDelete,
	// read inside the transaction; nil otherwise. Merge it with the update's
	// data to check rules across fields that a partial update didn't send.
	Before map[string]any
}

// Reject returns a hook error that rejects the request with status and a
// client-safe message (e.g. Reject(422, "End must be after start")).
func Reject(status int, msg string) error { return &HookError{Status: status, Message: msg} }

// HookError lets a Before* hook reject a request with a specific HTTP status and a
// client-safe message (e.g. 422 for a business-rule violation). Any other error returned
// from a hook becomes a generic 500 and its text is only logged.
type HookError struct {
	Status  int               // HTTP status, typically 400, 403, 409 or 422
	Message string            // returned to the client as "error"
	Code    string            // optional machine code (defaults from Status)
	Fields  map[string]string // optional per-field messages
}

func (e *HookError) Error() string { return e.Message }

// Plugin is the base interface all yaypi plugins must implement.
type Plugin interface {
	Info() PluginInfo
	Init(ctx InitContext) error
	Shutdown(ctx context.Context) error
}

// EntityHookPlugin extends Plugin with entity lifecycle hooks. Embed NoHooks to
// implement only the hooks you need, or register plain functions with Hooks.
type EntityHookPlugin interface {
	Plugin
	BeforeCreate(ctx HookContext, entity string, data map[string]any) (map[string]any, error)
	AfterCreate(ctx HookContext, entity string, record map[string]any) error
	BeforeUpdate(ctx HookContext, entity string, id string, data map[string]any) (map[string]any, error)
	AfterUpdate(ctx HookContext, entity string, record map[string]any) error
	BeforeDelete(ctx HookContext, entity string, id string) error
	AfterDelete(ctx HookContext, entity string, id string) error
}

// RouteContext is passed to a custom route handler. It carries the raw
// http.Request/ResponseWriter, the route's declared input already checked,
// and helpers that answer in yayPi's envelopes.
type RouteContext struct {
	Ctx       context.Context
	Subject   *Subject // nil when the request is unauthenticated
	Request   *http.Request
	Response  http.ResponseWriter
	RequestID string
	// Params and Body are the endpoint's declared `params` (query) and `body`
	// fields, type-checked and validated before the handler runs: integers
	// int64, floats float64, decimals their exact string, booleans bool,
	// timestamptz time.Time (UTC), date "YYYY-MM-DD", jsonb/arrays decoded
	// JSON. A field the caller didn't send is absent. Empty maps when the
	// endpoint declares none (read Request or use Decode then).
	Params map[string]any
	Body   map[string]any
}

// JSON writes v as a JSON response with status.
func (c RouteContext) JSON(status int, v any) {
	c.Response.Header().Set("Content-Type", "application/json")
	c.Response.WriteHeader(status)
	_ = json.NewEncoder(c.Response).Encode(v)
}

// Data writes 200 {"data": v}, the envelope CRUD responses use.
func (c RouteContext) Data(v any) { c.JSON(http.StatusOK, map[string]any{"data": v}) }

// Created writes 201 {"data": v}.
func (c RouteContext) Created(v any) { c.JSON(http.StatusCreated, map[string]any{"data": v}) }

// Error writes yayPi's error envelope {"error", "code", "request_id"}; code
// follows status (e.g. 404 not_found, 422 validation_failed).
func (c RouteContext) Error(status int, msg string) {
	apierr.Write(c.Response, c.RequestID, status, msg)
}

// Decode reads an undeclared JSON body into v (declared bodies are in Body).
func (c RouteContext) Decode(v any) error { return json.NewDecoder(c.Request.Body).Decode(v) }

// NoHooks implements every EntityHookPlugin hook as a no-op; embed it and
// define only the hooks you need.
type NoHooks struct{}

func (NoHooks) BeforeCreate(_ HookContext, _ string, d map[string]any) (map[string]any, error) {
	return d, nil
}
func (NoHooks) AfterCreate(HookContext, string, map[string]any) error { return nil }
func (NoHooks) BeforeUpdate(_ HookContext, _, _ string, d map[string]any) (map[string]any, error) {
	return d, nil
}
func (NoHooks) AfterUpdate(HookContext, string, map[string]any) error { return nil }
func (NoHooks) BeforeDelete(HookContext, string, string) error        { return nil }
func (NoHooks) AfterDelete(HookContext, string, string) error         { return nil }

// Hooks are one entity's lifecycle hooks as plain functions (nil = none),
// registered with server.RegisterHooks(entity, Hooks{...}).
type Hooks struct {
	BeforeCreate func(c HookContext, data map[string]any) (map[string]any, error)
	AfterCreate  func(c HookContext, record map[string]any) error
	BeforeUpdate func(c HookContext, id string, data map[string]any) (map[string]any, error)
	AfterUpdate  func(c HookContext, record map[string]any) error
	BeforeDelete func(c HookContext, id string) error
	AfterDelete  func(c HookContext, id string) error
}

// RouteHandlerFunc handles one custom HTTP route registered by a RouteHandlerPlugin.
type RouteHandlerFunc func(RouteContext)

// RouteHandlerPlugin lets a plugin expose custom HTTP routes that yayPI's declarative CRUD
// endpoints can't express (bulk operations, non-entity-shaped responses, file uploads/downloads,
// etc). Each entry in Handlers() is referenced from endpoints YAML as
// `handler: <PluginInfo.Name>.<key>` — e.g. a plugin named "reports" exposing a "Generate"
// handler is wired up with `handler: reports.Generate`. The endpoint still declares `path`,
// `method`, `entity` (for RBAC — the action is derived from the HTTP method), and `auth` exactly
// like a CRUD endpoint, so a custom route gets the same auth/rate-limit/RBAC middleware chain for
// free instead of reimplementing it.
type RouteHandlerPlugin interface {
	Plugin
	Handlers() map[string]RouteHandlerFunc
}

// ── data access ──────────────────────────────────────────────────────────────

// Store is a plugin's database access. SQL uses $1, $2 placeholders on every dialect
// (rewritten for MySQL/SQLite); quote identifiers with QuoteIdent. Entity writes go
// through Write/Upsert so validation, hooks, the audit log and outbound webhooks apply.
type Store interface {
	Dialect() string // postgres | mysql | sqlite
	QuoteIdent(name string) string
	// Query runs raw SQL: no row_access, tenant scope or field rules apply.
	// Reading on a caller's behalf? Use Read.
	Query(ctx context.Context, query string, args ...any) ([]map[string]any, error)
	Exec(ctx context.Context, query string, args ...any) (rowsAffected int64, err error)
	// Read returns an entity's rows as one of its list endpoints serves them
	// to req.Subject: that endpoint's row_access, the tenant scope and field
	// visibility (omit_response, read_roles) apply, so a plugin can't leak
	// another caller's rows by forgetting a WHERE clause. One page of at
	// most Limit (≤ 1000) rows; pass page.Next as Cursor for the next.
	Read(ctx context.Context, req ReadRequest) (ReadPage, error)
	// ReadAll is Read following every page: all matching rows.
	ReadAll(ctx context.Context, req ReadRequest) ([]map[string]any, error)
	// WithTx runs fn in a transaction on the default database (commit on nil error).
	WithTx(ctx context.Context, fn func(tx Tx) error) error
	// Write applies entity writes in one transaction: all or nothing.
	Write(ctx context.Context, reqs ...WriteRequest) ([]WriteResult, error)
	// Upsert creates or updates rows of entity by its natural_key (one transaction).
	Upsert(ctx context.Context, entity string, rows ...map[string]any) ([]WriteResult, error)
	// Cursor returns where an incremental sync left off ("" if never set).
	Cursor(ctx context.Context, source, key string) (string, error)
	SetCursor(ctx context.Context, source, key, value string) error
}

// Tx is raw SQL access inside Store.WithTx.
type Tx interface {
	Query(ctx context.Context, query string, args ...any) ([]map[string]any, error)
	Exec(ctx context.Context, query string, args ...any) (int64, error)
}

// WriteRequest is one entity write. Action: create | update | upsert | delete.
type WriteRequest struct {
	Entity string
	Action string
	Data   map[string]any // field → value (create, update, upsert)
	// Key identifies the row for update/delete: the primary key field or exactly the
	// entity's natural_key fields.
	Key map[string]any
	// Subject is the audit actor; with a Role, field write_roles apply. nil = system.
	Subject *Subject
}

// ReadRequest is a Store.Read.
type ReadRequest struct {
	Entity  string
	Subject *Subject // required: whose rules apply
	// Via names the list endpoint whose rules apply, e.g. "GET /cycles";
	// empty picks the entity's only list endpoint.
	Via string
	// Where filters as list query parameters do: "field" is equality ([]any
	// is IN, nil IS NULL); "field[op]" takes gt gte lt lte ne in nin contains
	// starts_with is_null, e.g. {"starts_at[gte]": from, "starts_at[lt]": to}.
	Where  map[string]any
	Sort   string // "field" or "-field" (descending); default primary key
	Limit  int    // page size; default and maximum 1000
	Cursor string // ReadPage.Next of the previous page
}

// ReadPage is one page of a Store.Read. Next is "" on the last page.
type ReadPage struct {
	Rows []map[string]any
	Next string
}

// WriteResult reports what a write did: created | updated | unchanged | deleted |
// not_found. Record is the stored row (nil after delete).
type WriteResult struct {
	Action string
	Record map[string]any
}

// ── errors ───────────────────────────────────────────────────────────────────

// PermanentError marks a failure that retrying cannot fix: a job or webhook handler
// returning one is not retried (the inbox row goes straight to dead).
type PermanentError struct{ Err error }

func (e *PermanentError) Error() string { return e.Err.Error() }
func (e *PermanentError) Unwrap() error { return e.Err }

// Permanent wraps err as a PermanentError.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return &PermanentError{Err: err}
}

// IsPermanent reports whether err (or anything it wraps) is a PermanentError.
func IsPermanent(err error) bool {
	var p *PermanentError
	return errors.As(err, &p)
}

// ── jobs ─────────────────────────────────────────────────────────────────────

// JobContext is passed to a plugin job. A run holds the job's cluster-wide lock and is
// retried per the job's retry: block.
type JobContext struct {
	Ctx     context.Context // carries the job timeout
	Job     string          // job name from jobs YAML
	Config  map[string]any  // the job's config: block
	Attempt int             // 1-based
	Store   Store           // nil without a database
	Logger  Logger
	HTTP    func(HTTPOptions) *http.Client
	// Failure is set when this function runs as another job's on_failure handler.
	Failure *JobFailure
}

// JobFailure describes the job run that failed (on_failure handlers).
type JobFailure struct {
	Job      string
	Err      error
	Attempts int
}

// JobFunc is one plugin job, referenced from jobs YAML as `plugin: <name>.<key>`.
type JobFunc func(ctx JobContext) error

// JobHandlerPlugin exposes scheduled jobs (`handler: plugin`) and on_failure handlers.
type JobHandlerPlugin interface {
	Plugin
	Jobs() map[string]JobFunc
}

// ── inbound webhooks ─────────────────────────────────────────────────────────

// WebhookEvent is one verified inbound webhook request, delivered from the inbox.
type WebhookEvent struct {
	Webhook    string // webhook name
	Event      string // value of the webhook's event: path ("" when not set)
	DedupeKey  string
	Body       []byte
	Headers    http.Header // without credentials (Authorization, Cookie, token header)
	Query      url.Values
	ReceivedAt time.Time
	Attempt    int // 1-based
}

// JSON decodes the body into v.
func (e WebhookEvent) JSON(v any) error { return json.Unmarshal(e.Body, v) }

// WebhookContext is passed to an inbound webhook handler.
type WebhookContext struct {
	Ctx    context.Context
	Event  WebhookEvent
	Store  Store
	Logger Logger
	HTTP   func(HTTPOptions) *http.Client
}

// WebhookHandlerFunc processes one inbound event. An error retries it with backoff
// (sdk.Permanent: no retry); the request was already acknowledged.
type WebhookHandlerFunc func(ctx WebhookContext) error

// WebhookVerifyFunc checks a request's signature for `signature: {scheme: plugin}`.
// Return an error to reject it with 401. body is the raw request body.
type WebhookVerifyFunc func(r *http.Request, body []byte, secret string) error

// WebhookPlugin exposes inbound webhook handlers, referenced as `handler: <name>.<key>`.
type WebhookPlugin interface {
	Plugin
	WebhookHandlers() map[string]WebhookHandlerFunc
}

// WebhookVerifierPlugin exposes custom signature verifiers (`signature.handler`).
type WebhookVerifierPlugin interface {
	Plugin
	WebhookVerifiers() map[string]WebhookVerifyFunc
}
