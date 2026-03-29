# Plugins

Plugins let you add custom logic that runs during entity lifecycle events (before/after create, update, delete). They are compiled into your binary and registered via `yaypi.yaml` in yayPi.

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

## Example: HashPasswordPlugin

This plugin hashes the `password` field before creating or updating a User.

```go
package main

import (
    "context"
    "fmt"

    "golang.org/x/crypto/bcrypt"

    "github.com/csullivan/yaypi/pkg/sdk"
)

type HashPasswordPlugin struct {
    cost int
}

func (p *HashPasswordPlugin) Info() sdk.PluginInfo {
    return sdk.PluginInfo{
        Name:        "hash-password",
        Version:     "1.0.0",
        Description: "Hashes the password field before saving",
    }
}

func (p *HashPasswordPlugin) Init(ctx sdk.InitContext) error {
    cost := bcrypt.DefaultCost
    if v, ok := ctx.Config["bcrypt_cost"].(int); ok {
        cost = v
    }
    p.cost = cost
    ctx.Logger.Info("hash-password plugin initialized", "cost", cost)
    return nil
}

func (p *HashPasswordPlugin) Shutdown(_ context.Context) error {
    return nil
}

func (p *HashPasswordPlugin) BeforeCreate(ctx sdk.HookContext, entity string, data map[string]any) (map[string]any, error) {
    return p.hashPasswordField(data)
}

func (p *HashPasswordPlugin) AfterCreate(_ sdk.HookContext, _ string, _ map[string]any) error {
    return nil
}

func (p *HashPasswordPlugin) BeforeUpdate(ctx sdk.HookContext, entity string, id string, data map[string]any) (map[string]any, error) {
    return p.hashPasswordField(data)
}

func (p *HashPasswordPlugin) AfterUpdate(_ sdk.HookContext, _ string, _ map[string]any) error {
    return nil
}

func (p *HashPasswordPlugin) BeforeDelete(_ sdk.HookContext, _ string, _ string) error {
    return nil
}

func (p *HashPasswordPlugin) AfterDelete(_ sdk.HookContext, _ string, _ string) error {
    return nil
}

func (p *HashPasswordPlugin) hashPasswordField(data map[string]any) (map[string]any, error) {
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

## Writing a plugin

Each plugin lives in its own directory as a standard Go package. The only requirement is that it exports a `New` function:

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

## Registering a plugin

**Step 1 — put the plugin in its own directory:**

```
your-project/
├── yaypi.yaml
├── entities/
├── endpoints/
└── plugins/
    └── hashpassword/
        └── plugin.go   ← package hashpassword; func New(...) sdk.EntityHookPlugin
```

**Step 2 — declare in `yaypi.yaml` with `path:` pointing at the directory:**

```yaml
plugins:
  - name: hash-password
    path: ./plugins/hashpassword   # relative to yaypi.yaml
    config:
      bcrypt_cost: 12
```

**Step 3 — wire to entity hooks in the entity file:**

```yaml
entity:
  name: User
  hooks:
    before_create: [hash-password]
    before_update: [hash-password]
```

**Step 4 — build and run:**

```bash
# Compile a standalone binary with the plugin baked in:
yaypi build --output ./my-api-server

# Run it:
./my-api-server run
```

Or just use `yaypi run` — when `path:` entries are detected, it automatically builds a plugin binary and re-execs into it before starting:

```bash
yaypi run   # detects plugin paths, builds, then starts
```

> **How auto-run works:** `yaypi run` checks whether any `plugins[].path` is set. If so, it builds a temporary binary (same as `yaypi build`) and replaces itself via `exec()`. The new binary starts with the plugins wired in. The build happens in a temporary directory and is cleaned up automatically.

> **Package convention:** The directory name becomes the Go package import alias. Use a short, alphanumeric name — `hashpassword`, `auditlog`, `ratelimit`. Hyphens and dots are replaced with underscores.

## Plugin config

Values in the `config:` map of the plugin declaration in `yaypi.yaml` are available in `InitContext.Config`:

```yaml
plugins:
  - name: hash-password
    config:
      bcrypt_cost: 12
      algorithm: argon2id
```

```go
func (p *MyPlugin) Init(ctx sdk.InitContext) error {
    if cost, ok := ctx.Config["bcrypt_cost"].(int); ok {
        p.cost = cost
    }
    return nil
}
```
