# yayPi — Architecture Overview

## What it is

yayPi is a YAML-driven REST API framework written in Go. You describe your data model and endpoints in YAML files; yayPi compiles them into a running HTTP server with full CRUD, authentication, authorization, migrations, background jobs, and OpenAPI spec generation — all with no hand-written Go code required.

## The kind system

Every included YAML file has a `kind` field that tells yayPi what it contains:

| kind | What it defines |
|---|---|
| `entity` | A database table and its fields, relations, indexes, constraints |
| `endpoints` | REST routes for one or more entities |
| `auth` | Built-in register/login/me/OAuth2/refresh endpoints |
| `jobs` | Background cron jobs (SQL or HTTP) |
| `seed` | Idempotent seed data rows inserted at startup |
| `email` | Email notification hooks triggered by entity lifecycle events |
| `webhooks` | HTTP webhook hooks triggered by entity lifecycle events |
| `policy` | Casbin RBAC rules (skipped at load time; processed by policy engine) |

## Request flow

```
HTTP request
  → chi router (base_url prefix)
    → CORS middleware (allowed_origins)
    → RequestID + Logger + Recover middleware
    → RateLimit middleware (global token bucket, if configured)
    → APIKeyAuth middleware (X-API-Key header, if configured)
    → RequireAuth middleware (JWT validation)
    → RBAC middleware (Casbin enforcement)
    → handler.Factory (CRUD handler)
      → validateFields() (field validation rules)
      → query.Builder (SQL construction)
        → dialect (Postgres / MySQL / SQLite)
        → database/sql (*sql.DB)
      → plugin.Dispatcher (before/after hooks → email, webhook, custom plugins)
```

## Package map

| Package | Path | Role |
|---|---|---|
| `config` | `internal/config/` | Parse `yaypi.yaml` and all included YAML files into typed Go structs (`RootConfig`, `EndpointDef`, `EntityDef`, etc.) |
| `schema` | `internal/schema/` | Compile config structs into a runtime `Registry` of `Entity` and `Endpoint` objects; resolve foreign keys, field types, etc. |
| `router` | `internal/router/` | Build a `chi.Router` from the registry; register all CRUD routes with middleware chains |
| `handler` | `internal/handler/` | `Factory` that produces `http.HandlerFunc` values for list/get/create/update/delete; field validation and bulk create logic |
| `query` | `internal/query/` | Dialect-aware SQL query builder; constructs and executes SELECT/INSERT/UPDATE/DELETE; offset and cursor pagination |
| `dialect` | `internal/dialect/` | `Dialect` interface + implementations for Postgres, MySQL, SQLite; encapsulates placeholder syntax, identifier quoting, type mapping, RETURNING support, schema introspection |
| `db` | `internal/db/` | `Manager` holding `map[name]*DB`; `DB` is `{SQL *sql.DB, Dialect dialect.Dialect}`; satisfies `health.Checker` |
| `auth` | `internal/auth/` | Built-in auth handler: register, login, me, OAuth2 upsert, refresh token rotation; issues and validates JWT tokens |
| `middleware` | `internal/middleware/` | JWT `RequireAuth`, API key `APIKeyAuth`, Casbin `RBAC`, token-bucket `RateLimiter`, CORS, RequestID, Logger, Recover |
| `migration` | `internal/migration/` | Diff-based schema migration engine; `Engine` introspects live DB + desired schema; `Runner` applies/rolls back SQL files |
| `openapi` | `internal/openapi/` | Builds OpenAPI 3.1 `Spec` objects from the registry; HTTP handler serves `/openapi/{name}.json`; `Build()` generates all named specs |
| `cron` | `internal/cron/` | gocron-based scheduler; runs SQL and HTTP jobs defined in `jobs[]` |
| `plugin` | `internal/plugin/` | `Dispatcher` calls before/after hooks on create/update/delete via the plugin SDK |
| `policy` | `internal/policy/` | Casbin `Engine` wrapper; loads rules from YAML role files or a database adapter |
| `health` | `internal/health/` | Liveness (`/health`) and readiness (`/ready`) HTTP handlers; readiness pings all DB connections |
| `seed` | `internal/seed/` | Idempotent seed runner; checks row existence by key field before INSERT |
| `mailer` | `internal/mailer/` | Built-in email hook; implements `sdk.EntityHookPlugin`; sends via SMTP on entity lifecycle events |
| `webhook` | `internal/webhook/` | Built-in webhook hook; implements `sdk.EntityHookPlugin`; fires HTTP requests with SSRF protection |
| `types` | `pkg/types/` | Shared constants: `FieldType`, `RelationType`, `ReferentialAction` |
| `sdk` | `pkg/sdk/` | Plugin author SDK — `Plugin` interface, `BeforeCreate`/`AfterCreate`/etc. hooks |

## Data flow: YAML → running API

```
yaypi.yaml
  config.Load()              → RootConfig (+ all included files merged in)
  schema.Build()             → Registry{entities, endpoints, specs}
  db.NewManager()            → Manager{map[name]*DB}
  seed.Run()                 → idempotent seed rows inserted
  migration.NewEngine()      → Engine (introspects DB, diffs against registry)
  openapi.Build()            → map[specName]*Spec
  mailer.New() + webhook.New() → registered as entity hook plugins
  router.Build()             → http.Handler (chi router with all routes mounted)
  http.Server.ListenAndServe()
```

## Key design decisions

- **`database/sql` throughout** — pgx is used via its stdlib shim; all three drivers share the same interface
- **Dialect abstraction** — all database-specific behavior (placeholders, quoting, type names, RETURNING, schema introspection) is behind `dialect.Dialect`; the query builder and migration engine never touch driver-specific code directly
- **No code generation** — the running server IS the compiled yayPi binary; user YAML is interpreted at startup
- **Registry is immutable at runtime** — built once at startup; all handlers close over entity/endpoint pointers
- **OpenAPI specs are pre-marshaled** — `openapi.NewHandler` marshals all specs to JSON bytes at startup; serving is a map lookup + `w.Write`
- **Email/webhook as built-in plugins** — both implement `sdk.EntityHookPlugin` and are auto-registered by `server.go` based on config; no user code required
- **API key + JWT are OR-logic** — `APIKeyAuth` runs before `RequireAuth`; if the API key succeeds it sets the Subject in context and JWT parsing is skipped

## Adding a new feature

1. Add config fields to `internal/config/types.go`
2. Add runtime types to `internal/schema/registry.go`; populate in `buildEndpoint` / `Build`
3. Implement behavior in the relevant package
4. Wire through `internal/router/builder.go` and/or `pkg/server/server.go`
5. Run `go build ./...` to verify
