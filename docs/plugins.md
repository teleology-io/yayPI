# Plugins

Plugins let you add custom logic that runs during entity lifecycle events (before/after create, update, delete). When you need plugins, you use yaypi as a library inside your own `main.go` rather than the standalone `yaypi` binary.

> **For email and webhooks, you don't need a plugin.** yayPi has built-in email and webhook hooks driven by YAML files (`kind: email`, `kind: webhooks`). See the sections below for when to use each approach.

## When to use plugins vs. built-in hooks

| Need | Solution |
|---|---|
| Send email when a record is created/updated/deleted | `kind: email` YAML file — no code needed |
| Fire an HTTP webhook on lifecycle events | `kind: webhooks` YAML file — no code needed |
| Hash passwords before saving | Custom plugin (or use the built-in auth `/register` endpoint) |
| Write an audit log | `audit: true` on the entity — no code needed |
| Validate business rules that can't be expressed in field constraints | Custom plugin |
| Bulk export/import, a report, a file upload — anything that isn't one CRUD row | Custom route plugin ([below](#custom-route-plugins)) |
| Complex data transformation before a write | Custom plugin |

## Delivery model for email and webhooks

Built-in emails and webhooks use a **transactional outbox** (tune with `outbox.poll_interval` and `outbox.retention`). When a write commits, matching messages are rendered and stored in the `yaypi_outbox` table *in the same transaction*. A background worker then delivers them with exponential backoff (default: 8 attempts for webhooks, 5 for emails); undeliverable messages end up `status = 'dead'` with `last_error`. Delivery survives restarts and is coordinated across replicas. It is at-least-once, so receivers should dedupe on `X-Yaypi-Event-Id`. A failing receiver never affects the API response.

Triggers are `after_create`, `after_update` and `after_delete`. The record passed to templates is the row after the change (before it, for deletes).

## Built-in email hooks (`kind: email`)

**SMTP** is configured in `yaypi.yaml`. Any empty field falls back to its env var (`SMTP_HOST`, `SMTP_PORT`, `SMTP_USER`, `SMTP_PASS`, `SMTP_SENDER_NAME`, `SMTP_SENDER_EMAIL`). Without a host the server refuses to start (unless `server.strict_startup: false`).

```yaml
# yaypi.yaml
smtp:
  host: smtp.example.com
  port: 587
  username: ${SMTP_USER}
  password: ${SMTP_PASS}
  from_name: My App
  from_email: no-reply@example.com
  retry:                 # default for every email; an email's own retry: overrides it
    max_attempts: 5
    initial_delay: 10s
    max_delay: 1h
```

```yaml
# emails/welcome.yaml
version: "1"
kind: email

emails:
  - name: welcome
    entity: User
    trigger: after_create
    to: "{{record.email}}"
    subject: "Welcome to our platform!"
    body: |
      <p>Hi {{record.display_name}},</p>
      <p>Thanks for signing up. Your account is ready.</p>

  - name: order-confirmation
    entity: Order
    trigger: after_create
    condition: record.email != ""
    to: "{{record.email}}"
    subject: "Order confirmation #{{record.id}}"
    body: "<p>Your order has been received. Total: {{record.total}}</p>"
```

`body` is HTML. Record values are HTML-escaped, and CR/LF are stripped from `to` and `subject`, so record data can't inject markup or headers.

## Built-in webhook hooks (`kind: webhooks`)

```yaml
# webhooks/orders.yaml
version: "1"
kind: webhooks

webhooks:
  - name: new-order
    entity: Order
    trigger: after_create
    condition: record.total >= 100 and record.status == "paid"
    url: "https://fulfillment.example.com/hooks/orders/{{record.id}}"
    method: POST
    secret: ${FULFILLMENT_WEBHOOK_SECRET}
    payload: |
      { "event": "order.created", "order_id": "{{record.id}}", "total": {{record.total}} }
    timeout: 10s
    retry:
      max_attempts: 8
      initial_delay: 10s
      max_delay: 1h
```

- **Payload.** Values substituted into `payload` are JSON-escaped, so record data can't break out of a string or add keys. The template must be valid JSON (checked at startup). Without `payload`, the body is `{"event": "Order.created", "entity": "Order", "id": "…", "data": {…record…}}`.
- **URL.** Substituted values are path-escaped.
- **Signature.** With `secret`, each delivery carries `X-Yaypi-Timestamp` and `X-Yaypi-Signature: v1=<hex HMAC-SHA256(secret, timestamp + "." + body)>`. Verify it, and reject timestamps older than a few minutes.
- **SSRF protection.** Connections to private, loopback, link-local (including cloud metadata), CGNAT and unspecified addresses are refused after DNS resolution, including on redirects. Set `allow_private_network: true` for internal receivers.
- **Success.** Any 2xx response; anything else is retried.

**Conditions** (emails and webhooks): `record.<field> <op> <value>` with `==`, `!=`, `>`, `>=`, `<`, `<=`; the value is a quoted string, number, `true`/`false` or `null`; combine with `and`. An invalid condition fails startup.

## Custom plugins (code)

When built-in hooks aren't enough, write a plugin in Go.

### SDK import path

```go
import "github.com/teleology-io/yayPI/pkg/sdk"
```

### Interfaces

#### `Plugin`

Every plugin must implement the base `Plugin` interface:

```go
type Plugin interface {
    Info() PluginInfo
    Init(ctx InitContext) error
    Shutdown(ctx context.Context) error
}
```

#### `EntityHookPlugin`

To handle entity lifecycle events, also implement `EntityHookPlugin`:

```go
type EntityHookPlugin interface {
    Plugin

    BeforeCreate(ctx HookContext, entity string, data map[string]any) (map[string]any, error)
    AfterCreate(ctx HookContext, entity string, record map[string]any) error

    BeforeUpdate(ctx HookContext, entity string, id string, data map[string]any) (map[string]any, error)
    AfterUpdate(ctx HookContext, entity string, record map[string]any) error

    BeforeDelete(ctx HookContext, entity string, id string) error
    AfterDelete(ctx HookContext, entity string, id string) error
}
```

#### `RouteHandlerPlugin`

To expose a custom HTTP route — something CRUD can't express, like a bulk export/import,
a file upload, or a non-entity-shaped response — implement `RouteHandlerPlugin` instead:

```go
type RouteHandlerPlugin interface {
    Plugin

    Handlers() map[string]RouteHandlerFunc
}
```

Each key in the returned map is referenced from endpoints YAML as
`handler: <PluginInfo.Name>.<key>`. See [Custom route plugins](#custom-route-plugins) below.

### Supporting types

```go
type PluginInfo struct {
    Name        string
    Version     string
    Description string
}

type InitContext struct {
    Config map[string]any   // values from `config:` in yaypi.yaml
    Logger Logger
}

type HookContext struct {
    Ctx       context.Context
    RequestID string
    Subject   *Subject        // authenticated user, nil if unauthenticated
}

type Subject struct {
    ID     string
    Role   string
    Email  string
    Tenant string
}

// Return from a Before* hook to reject the request with a specific status.
type HookError struct {
    Status  int               // e.g. 422
    Message string            // returned as "error"
    Code    string            // optional machine code
    Fields  map[string]string // optional per-field messages
}

type Logger interface {
    Info(msg string, fields ...any)
    Error(msg string, err error, fields ...any)
}

type RouteContext struct {
    Ctx      context.Context
    Subject  *Subject          // authenticated user, nil if unauthenticated
    Request  *http.Request
    Response http.ResponseWriter
}

type RouteHandlerFunc func(RouteContext)
```

### Hook behavior

| Hook | Runs | Can modify data | Error effect |
|---|---|---|---|
| `BeforeCreate` | After validation, before INSERT | Yes — return modified map | Aborts the operation |
| `AfterCreate` | After the transaction commits | No | Logged only |
| `BeforeUpdate` | Inside the transaction, after the row is found, locked and access-checked | Yes — return modified map | Aborts and rolls back |
| `AfterUpdate` | After commit | No | Logged only |
| `BeforeDelete` | Inside the transaction, after the row is found and access-checked | No | Aborts and rolls back |
| `AfterDelete` | After commit | No | Logged only |

**Before hooks** that return an error cancel the operation. Return `&sdk.HookError{Status: 422, Message: "…", Code: "…"}` to send that status and message to the client. Any other error becomes a generic 500, and its text is only logged. `BeforeUpdate`/`BeforeDelete` never run for rows that don't exist or that the caller can't access.

**After hooks** run once the change is committed. Errors are logged and don't affect the response. For side effects that must not be lost, prefer the built-in outbox (webhooks/emails) over a best-effort after-hook.

**Lifecycle.** `Init` is called once at startup with the plugin's `config:` block from `yaypi.yaml` (matched by `PluginInfo.Name`), and an error stops startup. `Shutdown` is called during graceful shutdown. `HookContext.RequestID` carries the request id for log correlation.

## Project layout

When you need custom plugins, your project has its own `go.mod` and a `main.go` that uses yaypi as a library. Everything else — entities, endpoints, `yaypi.yaml` — stays the same.

```
my-api/
├── go.mod
├── go.sum
├── main.go              ← your entry point; registers plugins and starts yaypi
├── yaypi.yaml
├── entities/
├── endpoints/
├── emails/              ← kind: email YAML files (no code needed)
├── webhooks/            ← kind: webhooks YAML files (no code needed)
└── plugins/
    └── hashpassword/
        └── plugin.go
```

## Writing a plugin

Each plugin is a regular Go package. The only convention is to export a `New` constructor:

```go
func New(cfg map[string]any) sdk.EntityHookPlugin
```

**`plugins/hashpassword/plugin.go`:**

```go
package hashpassword

import (
    "context"
    "fmt"

    "golang.org/x/crypto/bcrypt"
    "github.com/teleology-io/yayPI/pkg/sdk"
)

type plugin struct{ cost int }

func New(_ map[string]any) sdk.EntityHookPlugin { return &plugin{} }

func (p *plugin) Info() sdk.PluginInfo {
    return sdk.PluginInfo{Name: "hash-password", Version: "1.0.0", Description: "Hashes the password field before saving"}
}

func (p *plugin) Init(ctx sdk.InitContext) error {
    p.cost = bcrypt.DefaultCost
    if v, ok := ctx.Config["bcrypt_cost"].(int); ok {
        p.cost = v
    }
    return nil
}

func (p *plugin) Shutdown(_ context.Context) error { return nil }

func (p *plugin) BeforeCreate(_ sdk.HookContext, _ string, data map[string]any) (map[string]any, error) {
    return p.hashPasswordField(data)
}

func (p *plugin) AfterCreate(_ sdk.HookContext, _ string, _ map[string]any) error  { return nil }

func (p *plugin) BeforeUpdate(_ sdk.HookContext, _ string, _ string, data map[string]any) (map[string]any, error) {
    return p.hashPasswordField(data)
}

func (p *plugin) AfterUpdate(_ sdk.HookContext, _ string, _ map[string]any) error  { return nil }
func (p *plugin) BeforeDelete(_ sdk.HookContext, _ string, _ string) error          { return nil }
func (p *plugin) AfterDelete(_ sdk.HookContext, _ string, _ string) error           { return nil }

func (p *plugin) hashPasswordField(data map[string]any) (map[string]any, error) {
    raw, ok := data["password"].(string)
    if !ok || raw == "" {
        return data, nil
    }
    hashed, err := bcrypt.GenerateFromPassword([]byte(raw), p.cost)
    if err != nil {
        return nil, fmt.Errorf("hashing password: %w", err)
    }
    data["password_hash"] = string(hashed)
    delete(data, "password")
    return data, nil
}
```

## Wiring plugins in main.go

Import `github.com/teleology-io/yayPI/pkg/server`, create a `Server`, call `RegisterHook` for each entity that should receive the plugin's hooks, then call `Run`.

**`main.go`:**

```go
package main

import (
    "log"

    "github.com/teleology-io/yayPI/pkg/server"
    "myproject/plugins/hashpassword"
)

func main() {
    srv := server.New("yaypi.yaml")

    // Register the hash-password plugin for the User entity.
    srv.RegisterHook("User", hashpassword.New(nil))

    if err := srv.Run(); err != nil {
        log.Fatal(err)
    }
}
```

`server.New` loads `yaypi.yaml`, `RegisterHook` wires the plugin to the named entity, and `Run` starts the HTTP server and blocks until interrupted.

**`go.mod`:**

```
module myproject

go 1.22

require github.com/teleology-io/yayPI v0.0.0
```

## Wiring hooks to entities in YAML

Declare which hooks fire for each entity in the entity file. yaypi uses these to filter and dispatch — only hooks registered for the entity in `main.go` are called:

```yaml
entity:
  name: User
  hooks:
    before_create: [hash-password]
    before_update: [hash-password]
```

The hook names in YAML are informational labels. What matters for dispatch is which entity name you pass to `RegisterHook`.

> Email and webhook hooks do **not** need to be listed here — they are auto-registered by yayPi based on their YAML files.

## Custom route plugins

A `RouteHandlerPlugin` gets a real HTTP route — its own path, method, request, and response —
instead of a before/after hook wrapped around a generated CRUD operation. Use this when the
operation doesn't map to a single entity row: bulk export/import, a report that joins several
entities, a file upload, a webhook receiver with a bespoke payload shape, and so on.

**`plugins/report/plugin.go`:**

```go
package report

import (
    "context"
    "encoding/csv"

    "github.com/teleology-io/yayPI/pkg/sdk"
)

type plugin struct{}

func New() sdk.RouteHandlerPlugin { return &plugin{} }

func (p *plugin) Info() sdk.PluginInfo {
    return sdk.PluginInfo{Name: "report", Version: "1.0.0", Description: "CSV export endpoint"}
}

func (p *plugin) Init(_ sdk.InitContext) error      { return nil }
func (p *plugin) Shutdown(_ context.Context) error  { return nil }

func (p *plugin) Handlers() map[string]sdk.RouteHandlerFunc {
    return map[string]sdk.RouteHandlerFunc{
        "Generate": p.handleGenerate,
    }
}

func (p *plugin) handleGenerate(rc sdk.RouteContext) {
    rc.Response.Header().Set("Content-Type", "text/csv")
    rc.Response.Header().Set("Content-Disposition", `attachment; filename="report.csv"`)
    w := csv.NewWriter(rc.Response)
    w.Write([]string{"user_id", "role"})
    if rc.Subject != nil {
        w.Write([]string{rc.Subject.ID, rc.Subject.Role})
    }
    w.Flush()
}
```

**Endpoint YAML** (`endpoints/report.yaml`) — `entity:` is still required (it's what RBAC
checks against; the action is derived from the HTTP method, so a `GET` here needs `get` on
that entity, same as a CRUD list/get route would):

```yaml
version: "1"
kind: endpoints

endpoints:
  - path: /reports/users
    entity: User
    method: GET
    handler: report.Generate
    auth:
      require: true
```

**Wiring in `main.go`** — `RegisterRoutes` instead of `RegisterHook`:

```go
srv := server.New("yaypi.yaml")
srv.RegisterRoutes(report.New())

if err := srv.Run(); err != nil {
    log.Fatal(err)
}
```

A plugin can implement both `EntityHookPlugin` and `RouteHandlerPlugin` at once — register it
with both `RegisterHook` and `RegisterRoutes` if it needs to react to lifecycle events *and*
expose its own route.

## Plugin config

Pass plugin configuration through `InitContext.Config`. Call `Init` yourself before `RegisterHook` if you need config values from `yaypi.yaml`, or pass them directly when constructing the plugin:

```go
import (
    "github.com/teleology-io/yayPI/pkg/sdk"
    "myproject/plugins/hashpassword"
)

p := hashpassword.New(map[string]any{"bcrypt_cost": 12})
_ = p.Init(sdk.InitContext{Config: map[string]any{"bcrypt_cost": 12}})
srv.RegisterHook("User", p)
```

## Building and running

Because your project is a standard Go program, build and run it like any other Go binary:

```bash
go build -o ./my-api-server .
./my-api-server

# Or just:
go run .
```

Use `yaypi migrate`, `yaypi validate`, and `yaypi spec` commands as usual — those don't need plugins.
