# yayPi — Architecture Overview

## What it is

yayPi is a YAML-driven REST API framework written in Go. You describe your data model and endpoints in YAML files; yayPi compiles them into a running HTTP server with full CRUD, authentication, authorization, migrations, background jobs, and OpenAPI spec generation — all with no hand-written Go code required.

## The kind system

Every included YAML file has a `kind` field that tells yayPi what it contains:

| kind | What it defines |
|---|---|
| `entity` | A database table and its fields, relations, indexes, constraints |
| `endpoints` | REST routes for one or more entities |
| `auth` | Built-in register/login/me/OAuth2 endpoints |
| `jobs` | Background cron jobs (SQL or HTTP) |
| `policy` | Casbin RBAC rules (skipped at load time; processed by policy engine) |

## Request flow

```
HTTP request
  → chi router (base_url prefix)
    → CORS middleware (allowed_origins)
    → RequestID + Logger + Recover middleware
    → RequireAuth middleware (JWT validation)
    → RBAC middleware (Casbin enforcement)
    → handler.Factory (CRUD handler)
      → query.Builder (SQL construction)
        → dialect (Postgres / MySQL / SQLite)
        → database/sql (*sql.DB)
```

## Package map

| Package | Path | Role |
|---|---|---|
| `config` | `internal/config/` | Parse `yaypi.yaml` and all included YAML files into typed Go structs (`RootConfig`, `EndpointDef`, `EntityDef`, etc.) |
| `schema` | `internal/schema/` | Compile config structs into a runtime `Registry` of `Entity` and `Endpoint` objects; resolve foreign keys, field types, etc. |
| `router` | `internal/router/` | Build a `chi.Router` from the registry; register all CRUD routes with middleware chains |
| `handler` | `internal/handler/` | `Factory` that produces `http.HandlerFunc` values for list/get/create/update/delete |
| `query` | `internal/query/` | Dialect-aware SQL query builder; constructs and executes SELECT/INSERT/UPDATE/DELETE |
| `dialect` | `internal/dialect/` | `Dialect` interface + implementations for Postgres, MySQL, SQLite; encapsulates placeholder syntax, identifier quoting, type mapping, RETURNING support, schema introspection |
| `db` | `internal/db/` | `Manager` holding `map[name]*DB`; `DB` is `{SQL *sql.DB, Dialect dialect.Dialect}` |
| `auth` | `internal/auth/` | Built-in auth handler: register, login, me, OAuth2 upsert; issues and validates JWT tokens |
| `middleware` | `internal/middleware/` | JWT `RequireAuth`, Casbin `RBAC`, CORS, RequestID, Logger, Recover |
| `migration` | `internal/migration/` | Diff-based schema migration engine; `Engine` introspects live DB + desired schema; `Runner` applies/rolls back SQL files |
| `openapi` | `internal/openapi/` | Builds OpenAPI 3.1 `Spec` objects from the registry; HTTP handler serves `/openapi/{name}.json`; `Build()` generates all named specs |
| `cron` | `internal/cron/` | gocron-based scheduler; runs SQL and HTTP jobs defined in `jobs[]` |
| `plugin` | `internal/plugin/` | `Dispatcher` calls before/after hooks on create/update/delete via the plugin SDK |
| `policy` | `internal/policy/` | Casbin `Engine` wrapper; loads rules from YAML role files or a database adapter |
| `types` | `pkg/types/` | Shared constants: `FieldType`, `RelationType`, `ReferentialAction` |
| `sdk` | `pkg/sdk/` | Plugin author SDK — `Plugin` interface, `BeforeCreate`/`AfterCreate`/etc. hooks |

## Data flow: YAML → running API

```
yaypi.yaml
  config.Load()              → RootConfig (+ all included files merged in)
  schema.Build()             → Registry{entities, endpoints, specs}
  db.NewManager()            → Manager{map[name]*DB}
  migration.NewEngine()      → Engine (introspects DB, diffs against registry)
  openapi.Build()            → map[specName]*Spec
  router.Build()             → http.Handler (chi router with all routes mounted)
  http.Server.ListenAndServe()
```

## Key design decisions

- **`database/sql` throughout** — pgx is used via its stdlib shim; all three drivers share the same interface
- **Dialect abstraction** — all database-specific behavior (placeholders, quoting, type names, RETURNING, schema introspection) is behind `dialect.Dialect`; the query builder and migration engine never touch driver-specific code directly
- **No code generation** — the running server IS the compiled yayPi binary; user YAML is interpreted at startup
- **Registry is immutable at runtime** — built once at startup; all handlers close over entity/endpoint pointers
- **OpenAPI specs are pre-marshaled** — `openapi.NewHandler` marshals all specs to JSON bytes at startup; serving is a map lookup + `w.Write`

## Adding a new feature

1. Add config fields to `internal/config/types.go`
2. Add runtime types to `internal/schema/registry.go`; populate in `buildEndpoint` / `Build`
3. Implement behavior in the relevant package
4. Wire through `internal/router/builder.go` and/or `cmd/yaypi/main.go`
5. Run `go build ./...` to verify
