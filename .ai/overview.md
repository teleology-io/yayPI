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
  → chi router
    → ClientIP (trusted_proxies-aware) → RequestID → Trace (traceparent)
    → Logger (route pattern, subject, metrics observer) → Recover
    → SecurityHeaders → BodyLimit (413) → Timeout (request ctx deadline)
    → CORS → RateLimit (global, IP-keyed)
    → /health, /ready, /metrics (outside base_url)
    → base_url routes:
      → /auth/* (auth.Handler; no-store)
      → per-route chain: [endpoint RateLimit (ip)] → APIKeyAuth → RequireAuth (token.Keys + optional
        revocation check) → [RateLimit (user-keyed)] → RBAC (roles/conditions always; Casbin if configured)
      → handler.Factory (CRUD)
        → prepareInput (type coercion + validation) / write roles / default_from / tenant scope
        → transaction: query.Builder (+ row filter) → TxObservers (audit log, outbox) → commit
        → After* plugin hooks (post-commit)
  → outbox.Worker (background) delivers webhooks/emails with retries
```

## Package map

| Package | Path | Role |
|---|---|---|
| `config` | `internal/config/` | Parse `yaypi.yaml` and included files into typed structs; `Validate()` (secrets, DSNs, crud ops, sizes) |
| `schema` | `internal/schema/` | Compile config into a runtime `Registry`; built-in `User` plus internal tables (refresh/auth tokens, idempotency, outbox, audit, job locks) marked `Internal` |
| `router` | `internal/router/` | Build the chi router: global middleware, health/metrics, auth, per-route auth/RBAC chains |
| `handler` | `internal/handler/` | CRUD handlers: coercion/validation, transactions, ETag/If-Match, Idempotency-Key, includes, sparse fields, tenant scope, DB-error mapping; `TxObserver` hook point |
| `query` | `internal/query/` | Dialect-aware SQL builder over an `Executor` (*sql.DB or *sql.Tx): typed filters, search, keyset cursors, batched relation includes |
| `dialect` | `internal/dialect/` | Postgres / MySQL / SQLite: placeholders, quoting, types, default translation, constraint-error detection |
| `db` | `internal/db/` | `Manager` of named `*DB` pools with bounded defaults |
| `token` | `internal/token/` | Access token signing/verification (HS/RS/ES, iss/aud, TTL), JWKS |
| `auth` | `internal/auth/` | Register/login/me, stored rotating refresh tokens, logout(-all), password reset, email verification, OAuth2 (PKCE, verified email), login throttle |
| `apikey` | `internal/apikey/` | Static and DB-backed API key lookup by SHA-256 digest |
| `middleware` | `internal/middleware/` | Auth, API key, RBAC, rate limit, client IP, CORS, body limit, timeout, trace, security headers, logger, recover |
| `apierr` | `internal/apierr/` | The single JSON error envelope |
| `netsafe` | `internal/netsafe/` | SSRF-safe HTTP client (dial-time IP checks) |
| `outbox` | `internal/outbox/` | Transactional outbox: render webhooks/emails in-tx, deliver with retries/signatures |
| `audit` | `internal/audit/` | Audit log observer (`audit: true`) |
| `metrics` | `internal/metrics/` | Dependency-free Prometheus exposition |
| `logging` | `internal/logging/` | zerolog setup (level, json/console) |
| `migration` | `internal/migration/` | Diff engine (per database), transactional + locked runner, statement splitter |
| `openapi` | `internal/openapi/` | OpenAPI 3.1 generation |
| `cron` | `internal/cron/` | gocron scheduler with DB-leased distributed lock, retries, timezones |
| `plugin` | `internal/plugin/` | Hook/route dispatcher; plugin Init/Shutdown lifecycle |
| `policy` | `internal/policy/` | Casbin engine, conditions, row filters |
| `health` | `internal/health/` | Liveness/readiness (draining-aware) |
| `seed` | `internal/seed/` | Idempotent seeding (hashes User passwords) |
| `mailer` | `internal/mailer/` | SMTP sender |
| `sdk` | `pkg/sdk/` | Plugin SDK — hooks, route plugins, `HookError` |

## Data flow: YAML → running API

```
yaypi.yaml
  config.Load() + Validate()     → RootConfig (fail fast on bad secrets/DSNs/conditions)
  logging.Setup()
  schema.Build()                 → Registry (user + internal entities)
  db.NewManager()                → bounded pools per database
  auto_migrate (optional)        → per-database diff applied under lock
  policy engine                  → fatal on any error
  handler.Factory + observers    → outbox (webhooks/emails), audit
  token.Keys, auth.Handler, API keys, OpenAPI, health, metrics
  plugins.InitAll()
  router.Build()
  cron + outbox worker start
  http.Server (all timeouts) → SIGTERM → drain → Shutdown → stop workers → plugins.ShutdownAll
```

## Key design decisions

- **`database/sql` throughout** — pgx is used via its stdlib shim; all three drivers share the same interface
- **Dialect abstraction** — all database-specific behavior (placeholders, quoting, type names, RETURNING, schema introspection) is behind `dialect.Dialect`; the query builder and migration engine never touch driver-specific code directly
- **No code generation** — the running server IS the compiled yayPi binary; user YAML is interpreted at startup
- **Registry is immutable at runtime** — built once at startup; all handlers close over entity/endpoint pointers
- **OpenAPI specs are pre-marshaled** — `openapi.NewHandler` marshals all specs to JSON bytes at startup; serving is a map lookup + `w.Write`
- **Email/webhook via transactional outbox** — rendered inside the write transaction by `outbox.Observer` (a `handler.TxObserver`) and delivered by `outbox.Worker`; never fire-and-forget
- **Fail closed** — misconfigured security (policy, secrets, auth without DB) stops the boot; `roles`/`conditions` are enforced with or without Casbin
- **API key + JWT are OR-logic** — `APIKeyAuth` runs before `RequireAuth`; if the API key succeeds it sets the Subject in context and JWT parsing is skipped

## Adding a new feature

1. Add config fields to `internal/config/types.go`
2. Add runtime types to `internal/schema/registry.go`; populate in `buildEndpoint` / `Build`
3. Implement behavior in the relevant package
4. Wire through `internal/router/builder.go` and/or `pkg/server/server.go`
5. Add the field to the matching `schemas/*.schema.json` (they use `additionalProperties: false`, so VS Code flags unknown keys)
6. Run `go build ./... && go vet ./... && go test ./...` (`internal/integration` exercises the full stack on SQLite, and on Postgres/MySQL when `YAYPI_TEST_POSTGRES_DSN` / `YAYPI_TEST_MYSQL_DSN` are set)
