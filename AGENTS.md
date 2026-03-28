# yayPi — AI Agent Context

yayPi is a **YAML-driven REST API framework** written in Go. YAML config files define entities (database tables), endpoints (REST routes), auth, jobs, and policies — the framework compiles them into a running HTTP server with no hand-written Go code required.

## AI context directory

Full structured context for AI assistants lives in [`.ai/`](.ai/):

| File | Contents |
|---|---|
| [`.ai/overview.md`](.ai/overview.md) | Architecture, request flow, package map, design decisions |
| [`.ai/config-reference.md`](.ai/config-reference.md) | Every YAML field for all file kinds (`entity`, `endpoints`, `auth`, `jobs`) |
| [`.ai/patterns.md`](.ai/patterns.md) | Copy-paste YAML patterns for common use cases |

**Start here** when working in this repo — read `.ai/overview.md` first, then the config reference for the specific area you're touching.

## Common tasks → key files

| Task | Files to read first |
|---|---|
| Add a new YAML config field | `internal/config/types.go`, `internal/schema/registry.go` |
| Change how routes are registered | `internal/router/builder.go` |
| Change CRUD handler behavior | `internal/handler/` |
| Change SQL generation | `internal/query/builder.go`, `internal/dialect/` |
| Add a new database driver | `internal/dialect/dialect.go`, `internal/db/manager.go` |
| Change auth logic | `internal/auth/handler.go` |
| Change migration behavior | `internal/migration/engine.go`, `internal/migration/runner.go` |
| Change OpenAPI generation | `internal/openapi/builder.go` |
| Wire a new feature end-to-end | `cmd/yaypi/main.go`, `internal/router/builder.go` |

## Build & run

```bash
go build ./...                              # build everything
go build -o yaypi ./cmd/yaypi              # build the binary

cd examples/blog && ../yaypi run           # run the blog example
cd examples/community-blog && ../yaypi run # run the community blog example

yaypi validate --config yaypi.yaml         # validate config
yaypi migrate generate --name init         # generate initial migration
yaypi migrate up                           # apply migrations
yaypi spec generate --name api             # generate OpenAPI spec
```

## Key conventions

- **Entity names** are PascalCase (`Post`, `User`); table names are snake_case (`posts`, `users`)
- **All DB access** goes through `*db.DB` (a `{SQL *sql.DB, Dialect dialect.Dialect}` struct) — never use `*pgxpool.Pool` or driver-specific types directly
- **Placeholders** — use `dialect.Rebind(query)` to convert `$1,$2` to `?` for MySQL/SQLite
- **Identifier quoting** — use `dialect.QuoteIdent(name)`; never interpolate raw names into SQL
- **No new `go.mod` dependencies** without discussion — keep the dependency surface small
- **`go build ./...` must stay clean** — run before committing
