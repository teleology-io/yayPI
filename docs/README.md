# yayPi Documentation

**yayPi** (Yay-PI, like API but yaml based) is a Go framework that turns YAML configuration files into a fully functional REST API backend — no code generation, no templates, no boilerplate.

## Quick Start

```bash
# 1. Scaffold a new project
yaypi init my-api
cd my-api

# 2. Set your database URL
export DATABASE_URL=postgres://localhost/my_api
export JWT_SECRET=$(openssl rand -hex 32)   # at least 32 bytes

# 3. Start the server
yaypi run
```

## What yayPi gives you

- **CRUD endpoints** with operator filters, search, sorting, keyset (cursor) and offset pagination, sparse fields, embedded relations, PUT/PATCH
- **Field validation** — strict type checking plus required, min/max length, min/max value, regex pattern, built-in format checks
- **Safe writes** — every write is a transaction; ETag / If-Match optimistic concurrency; Idempotency-Key retries; consistent error envelope
- **Immutable fields** — set on create, silently ignored on update
- **Bulk create** — post an array; all-or-nothing transaction or partial success (207)
- **Auth endpoints** — register, login, /me, logout, password reset, email verification, OAuth2 with PKCE (Google, GitHub, custom)
- **Token refresh** — stored, rotating refresh tokens with reuse detection; HS/RS/ES signing with JWKS
- **API key auth** — static or DB-backed (hashed) keys; works alongside JWT
- **RBAC + ABAC** — role-based access, conditions on subject attributes, row-level and field-level filtering
- **Multi-tenancy, owner fields, audit log** — `tenant_scoped`, `default_from`, `audit: true`
- **Rate limiting & brute-force protection** — global and per-endpoint limits, trusted-proxy aware, login lockout
- **Health/readiness endpoints** — for Kubernetes liveness and readiness probes
- **Diff-based migrations** — per-database, transactional, locked, drift-checked
- **Background jobs** — cron-scheduled SQL or HTTP jobs, run once across replicas
- **Seed data** — idempotent rows via `yaypi seed`
- **Email & webhooks** — transactional outbox with retries, signed webhooks, SSRF protection
- **Operations** — JSON logs, Prometheus metrics, trace-context, graceful draining shutdown
- **Custom plugins** — hook into any lifecycle event (before/after create/update/delete)
- **OpenAPI 3.1** — auto-generated specs, served live, exportable to JSON

## Documentation

| I want to… | Go to… |
|---|---|
| Build my first API in 10 minutes | [Getting Started](getting-started.md) |
| Understand how yayPi works | [Concepts](concepts.md) |
| Configure `yaypi.yaml` | [Project Config](project-config.md) |
| Define entities and fields | [Entities](entities.md) |
| Configure REST endpoints | [Endpoints](endpoints.md) |
| Add login, register, OAuth2, and token refresh | [Auth Endpoints](auth-endpoints.md) |
| Set up JWT auth, API keys, and roles | [Authorization](authorization.md) |
| Generate and run migrations | [Migrations](migrations.md) |
| Schedule background jobs | [Jobs](jobs.md) |
| Write a plugin (hooks) | [Plugins](plugins.md) |
| Deploy and operate in production | [Production](production.md) |
| See all CLI commands | [CLI Reference](cli.md) |
| See common patterns | [Patterns Cookbook](patterns.md) |
| Get autocomplete in VS Code / Cursor | [YAML IntelliSense](intellisense.md) |

## Examples

- [`examples/blog/`](../examples/blog/) — A simple single-author blog (users, posts, tags)
- [`examples/community-blog/`](../examples/community-blog/) — A multi-author community blog (users, posts, tags, threaded comments, roles)
