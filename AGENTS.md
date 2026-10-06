# yayPi — AI Agent Context

yayPi is a **YAML-driven REST API framework** written in Go. YAML config files define entities (database tables), endpoints (REST routes), auth, jobs, seeds, emails, webhooks, and policies — the framework compiles them into a running HTTP server with no hand-written Go code required.

## AI context directory

Full structured context for AI assistants lives in [`.ai/`](.ai/):

| File | Contents |
|---|---|
| [`.ai/overview.md`](.ai/overview.md) | Architecture, request flow, package map, design decisions |
| [`.ai/config-reference.md`](.ai/config-reference.md) | Every YAML field for all file kinds (`entity`, `endpoints`, `auth`, `jobs`, `seed`, `email`, `webhooks`) |
| [`.ai/patterns.md`](.ai/patterns.md) | Copy-paste YAML patterns for common use cases |

**Start here** when working in this repo — read `.ai/overview.md` first, then the config reference for the specific area you're touching.

## Common tasks → key files

| Task | Files to read first |
|---|---|
| Add a new YAML config field | `internal/config/types.go`, `internal/schema/registry.go` |
| Change how routes are registered | `internal/router/builder.go` |
| Change CRUD handler behavior | `internal/handler/` |
| Change field validation logic | `internal/handler/validate.go` |
| Change SQL generation | `internal/query/builder.go`, `internal/dialect/` |
| Add a new database driver | `internal/dialect/dialect.go`, `internal/db/manager.go` |
| Change auth logic | `internal/auth/handler.go`, `refresh.go`, `oauth.go`, `recovery.go`, `throttle.go` |
| Change token signing/verification | `internal/token/token.go`, `internal/middleware/auth.go` |
| Change API key auth | `internal/apikey/apikey.go`, `internal/middleware/apikey.go` |
| Change rate limiting / client IP | `internal/middleware/ratelimit.go`, `internal/middleware/clientip.go`, `internal/router/builder.go` |
| Change error responses | `internal/apierr/apierr.go`, `internal/handler/respond.go` |
| Change migration behavior | `internal/migration/engine.go`, `internal/migration/runner.go`, `internal/migration/lock.go` |
| Change OpenAPI generation | `internal/openapi/builder.go` |
| Change email / webhook delivery | `internal/outbox/` (observer renders in-tx, worker delivers), `internal/mailer/mailer.go` (SMTP) |
| Change audit logging | `internal/audit/audit.go` |
| Change outbound HTTP safety | `internal/netsafe/netsafe.go` |
| Change metrics / logging | `internal/metrics/`, `internal/middleware/logger.go`, `internal/logging/` |
| Production readiness work | `.ai/production-manifest.md` |
| Change health endpoints | `internal/health/handler.go` |
| Change seed behavior | `internal/seed/runner.go` |
| Wire a new feature end-to-end | `pkg/server/server.go`, `internal/router/builder.go` |

## Build & run

```bash
go build ./...                              # build everything
go build -o yaypi ./cmd/yaypi              # build the binary

cd examples/blog && ../yaypi run           # run the blog example
cd examples/community-blog && ../yaypi run # run the community blog example

yaypi validate --config yaypi.yaml         # validate config
yaypi migrate generate --name init         # generate initial migration
yaypi migrate up                           # apply migrations
yaypi seed                                 # run seed files manually
yaypi spec generate --name api             # generate OpenAPI spec
```

## Key conventions

- **Entity names** are PascalCase (`Post`, `User`); table names are snake_case (`posts`, `users`)
- **All DB access** goes through `*db.DB` (a `{SQL *sql.DB, Dialect dialect.Dialect}` struct) — never use `*pgxpool.Pool` or driver-specific types directly
- **Placeholders** — use `dialect.Rebind(query)` to convert `$1,$2` to `?` for MySQL/SQLite
- **Identifier quoting** — use `dialect.QuoteIdent(name)`; never interpolate raw names into SQL
- **Email/webhooks** go through the transactional outbox (`internal/outbox`), registered as a `handler.TxObserver` by `pkg/server/server.go`; never send from a request goroutine
- **Writes run in a transaction** (`handler.withTx`); anything that must commit with the change implements `handler.TxObserver`
- **Errors** use `apierr` (`{"error","code","request_id","errors"}`); never leak driver error text to clients
- **Outbound HTTP to user-influenced URLs** must use `netsafe.NewClient`
- **New config fields** also go in `schemas/*.schema.json`
- **API key + JWT are OR-logic** — `APIKeyAuth` middleware runs first; if it sets a Subject, `RequireAuth` skips JWT parsing
- **No new `go.mod` dependencies** without discussion — keep the dependency surface small
- **`go build ./...`, `go vet ./...`, `go test ./...` and `gofmt -l` must stay clean** — CI (`.github/workflows/ci.yml`) also runs staticcheck, govulncheck and the Postgres/MySQL integration suite
