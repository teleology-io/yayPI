# Plugins

Plugins let you add custom logic that runs during entity lifecycle events (before/after create, update, delete). When you need plugins, you use yaypi as a library inside your own `main.go` rather than the standalone `yaypi` binary.

> **For email and webhooks, you don't need a plugin.** yayPi has built-in email and webhook hooks driven by YAML files (`kind: email`, `kind: webhooks`). See the sections below for when to use each approach.

## When to use plugins vs. built-in hooks

| Need | Solution |
|---|---|
| Send email when a record is created/updated/deleted | `kind: email` YAML file — no code needed |
| Fire an HTTP webhook on lifecycle events | `kind: webhooks` YAML file — no code needed |
| Hash passwords before saving | Custom plugin (or use the built-in auth `/register` endpoint) |
| Write an audit log to a database table | Custom plugin |
| Validate business rules that can't be expressed in field constraints | Custom plugin |
| Complex data transformation before a write | Custom plugin |

## Built-in email hooks (`kind: email`)

Create a YAML file with `kind: email` to send transactional email on entity lifecycle events. yayPi reads SMTP configuration from environment variables.

**Required env vars:** `SMTP_HOST`, `SMTP_PORT`, `SMTP_USER`, `SMTP_PASS`, `SENDER_EMAIL`, `SENDER_NAME`

```yaml
# emails/welcome.yaml
version: "1"
kind: email

emails:
  - entity: User
    trigger: after_create
    to: "{{record.email}}"
    subject: "Welcome to our platform!"
    body: |
      Hi {{record.name}},

      Thanks for signing up. Your account is ready.
    html: |
      <p>Hi {{record.name}},</p>
      <p>Thanks for signing up. Your account is ready.</p>

  - entity: Order
    trigger: after_create
    condition: "record.email != \"\""
    to: "{{record.email}}"
    subject: "Order confirmation #{{record.id}}"
    body: "Your order has been received. Total: {{record.total}}"
```

**Template syntax:** `{{record.FIELD}}` where `FIELD` is any column name in the entity.

**Condition syntax:** `record.FIELD != ""` or `record.FIELD == "value"` (simple equality/inequality only). Omit to always fire.

**Triggers:** `before_create` | `after_create` | `before_update` | `after_update` | `before_delete` | `after_delete`

Add the file to your `include:` globs:
```yaml
include:
  - emails/**/*.yaml
```

Emails are sent in a goroutine (non-blocking) so they do not slow down the HTTP response.

## Built-in webhook hooks (`kind: webhooks`)

Create a YAML file with `kind: webhooks` to fire HTTP webhooks on entity lifecycle events.

```yaml
# webhooks/orders.yaml
version: "1"
kind: webhooks

webhooks:
  - entity: Order
    trigger: after_create
    url: "https://fulfillment.example.com/hooks/new-order"
    method: POST
    headers:
      Authorization: "Bearer ${FULFILLMENT_SECRET}"
      Content-Type: "application/json"
    payload: |
      {
        "event": "order.created",
        "order_id": "{{record.id}}",
        "customer_id": "{{record.customer_id}}"
      }
    timeout: 10s
    retry:
      max_attempts: 3
      backoff: 5s
```

**Template syntax:** same `{{record.FIELD}}` as email hooks.

**SSRF protection:** webhook targets are blocked if they resolve to RFC-1918 (private), loopback, or link-local addresses.

**Non-blocking:** webhooks fire in a goroutine; failures don't affect the HTTP response.

Add the file to your `include:` globs:
```yaml
include:
  - webhooks/**/*.yaml
```

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
    ID    string
    Role  string
    Email string
}

type Logger interface {
    Info(msg string, fields ...any)
    Error(msg string, err error, fields ...any)
}
```

### Hook behavior

| Hook | Runs | Can modify data | Error effect |
|---|---|---|---|
| `BeforeCreate` | Before INSERT | Yes — return modified map | Aborts the operation (returns 400) |
| `AfterCreate` | After INSERT | No | Logged only — operation is not rolled back |
| `BeforeUpdate` | Before UPDATE | Yes — return modified map | Aborts the operation (returns 400) |
| `AfterUpdate` | After UPDATE | No | Logged only |
| `BeforeDelete` | Before DELETE | No | Aborts the operation (returns 400) |
| `AfterDelete` | After DELETE | No | Logged only |

**Before hooks** that return a non-nil error cancel the operation and return an HTTP error to the caller. Use this for validation and data transformation.

**After hooks** that return a non-nil error are logged but do not affect the response. The database operation is not rolled back. Use this for side effects like audit logs.

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
