# Plugins

Plugins let you add custom logic that runs during entity lifecycle events (before/after create, update, delete). When you need plugins, you use yaypi as a library inside your own `main.go` rather than the standalone `yaypi` binary.

## When to use plugins

- Hash passwords before saving a User
- Send a welcome email after creating a User
- Write an audit log entry after any create/update/delete
- Validate business rules that can't be expressed in field constraints

## SDK import path

```go
import "github.com/csullivan/yaypi/pkg/sdk"
```

## Interfaces

### `Plugin`

Every plugin must implement the base `Plugin` interface:

```go
type Plugin interface {
    Info() PluginInfo
    Init(ctx InitContext) error
    Shutdown(ctx context.Context) error
}
```

### `EntityHookPlugin`

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

## Hook behavior

| Hook | Runs | Can modify data | Error effect |
|---|---|---|---|
| `BeforeCreate` | Before INSERT | Yes — return modified map | Aborts the operation (returns 400) |
| `AfterCreate` | After INSERT | No | Logged only — operation is not rolled back |
| `BeforeUpdate` | Before UPDATE | Yes — return modified map | Aborts the operation (returns 400) |
| `AfterUpdate` | After UPDATE | No | Logged only |
| `BeforeDelete` | Before DELETE | No | Aborts the operation (returns 400) |
| `AfterDelete` | After DELETE | No | Logged only |

**Before hooks** that return a non-nil error cancel the operation and return an HTTP error to the caller. Use this for validation and data transformation.

**After hooks** that return a non-nil error are logged but do not affect the response. The database operation is not rolled back. Use this for side effects like emails or audit logs.

## Project layout

When you need plugins, your project has its own `go.mod` and a `main.go` that uses yaypi as a library. Everything else — entities, endpoints, `yaypi.yaml` — stays the same.

```
my-api/
├── go.mod
├── go.sum
├── main.go              ← your entry point; registers plugins and starts yaypi
├── yaypi.yaml
├── entities/
├── endpoints/
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
    "github.com/csullivan/yaypi/pkg/sdk"
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

Import `github.com/csullivan/yaypi/pkg/server`, create a `Server`, call `RegisterHook` for each entity that should receive the plugin's hooks, then call `Run`.

**`main.go`:**

```go
package main

import (
    "log"

    "github.com/csullivan/yaypi/pkg/server"
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

require github.com/csullivan/yaypi v0.0.0
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

## Plugin config

Pass plugin configuration through `InitContext.Config`. Call `Init` yourself before `RegisterHook` if you need config values from `yaypi.yaml`, or pass them directly when constructing the plugin:

```go
import (
    "github.com/csullivan/yaypi/pkg/sdk"
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
