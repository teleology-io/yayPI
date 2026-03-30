# yayPi Documentation

**yayPi** (Yay-PI, like API but yaml based) is a Go framework that turns YAML configuration files into a fully functional REST API backend — no code generation, no templates, no boilerplate.

## Quick Start

```bash
# 1. Scaffold a new project
yaypi init my-api
cd my-api

# 2. Set your database URL
export DATABASE_URL=postgres://localhost/my_api
export JWT_SECRET=your-secret-here

# 3. Start the server
yaypi run
```

## What yayPi gives you

- **CRUD endpoints** with filtering, sorting, cursor and offset pagination, eager-loading relations
- **Field validation** — required, min/max length, min/max value, regex pattern, built-in format checks
- **Immutable fields** — set on create, silently ignored on update
- **Bulk create** — post an array; abort on first error or continue with partial success (207)
- **Auth endpoints** — register, login, /me, OAuth2 (Google, GitHub, and custom providers)
- **Token refresh** — long-lived refresh tokens with rotation (cookie or body store)
- **API key auth** — static keys in YAML or DB-backed; works alongside JWT
- **RBAC + ABAC** — role-based access, conditions on subject attributes, row-level and field-level filtering
- **Rate limiting** — global token bucket or per-endpoint
- **Health/readiness endpoints** — for Kubernetes liveness and readiness probes
- **Diff-based migrations** — auto-detect schema changes, generate SQL, apply or roll back
- **Background jobs** — cron-scheduled SQL or HTTP jobs
- **Seed data** — idempotent rows inserted at startup
- **Email hooks** — send transactional email on entity lifecycle events (SMTP)
- **Webhook hooks** — fire HTTP webhooks on entity lifecycle events, with SSRF protection and retry
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
| See all CLI commands | [CLI Reference](cli.md) |
| See common patterns | [Patterns Cookbook](patterns.md) |
| Get autocomplete in VS Code / Cursor | [YAML IntelliSense](intellisense.md) |

## Examples

- [`examples/blog/`](../examples/blog/) — A simple single-author blog (users, posts, tags)
- [`examples/community-blog/`](../examples/community-blog/) — A multi-author community blog (users, posts, tags, threaded comments, roles)
