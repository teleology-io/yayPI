# yayPi

**yayPi** (Yay-PI, like API but YAML-based) is a Go framework that turns YAML configuration files into a fully functional REST API backend — no code generation, no templates, no boilerplate.

```bash
curl -fsSL https://raw.githubusercontent.com/teleology-io/yayPI/master/install.sh | sh
yaypi init my-api && cd my-api
yaypi run
```

## What you get

- **CRUD endpoints** — operator filters, search, sorting, keyset/offset pagination, sparse fields, embedded relations, PATCH and PUT
- **Safe writes** — strict type checking, transactions, ETag/If-Match concurrency control, Idempotency-Key retries, all-or-nothing bulk create
- **Field validation** — required, min/max length, min/max value, regex pattern, format checks (email, url, uuid, slug)
- **Auth endpoints** — register, login, `/me`, rotating refresh tokens, logout(-all), password reset, email verification, OAuth2 with PKCE (Google, GitHub, custom)
- **Tokens** — HS/RS/ES signing, issuer/audience, JWKS, optional instant revocation
- **API key auth** — static or DB-backed (hashed) keys; works alongside JWT
- **RBAC + ABAC** — roles, per-subject conditions, row-level and field-level filtering, owner fields, multi-tenancy
- **Rate limiting** — global and per-endpoint, trusted-proxy aware; login brute-force lockout
- **Migrations** — per-database, transactional, locked, drift-checked
- **Background jobs** — cron-scheduled SQL or HTTP jobs with retry, executed once across replicas
- **Email & webhooks** — transactional outbox with retries, HMAC-signed webhooks, SSRF protection
- **Audit log** — `audit: true` records who changed what
- **Operations** — JSON logs, Prometheus metrics, trace-context, health/readiness with graceful draining
- **Seed data** — idempotent rows via `yaypi seed`
- **Plugins** — hook into any lifecycle event; write Go plugins for custom logic
- **OpenAPI 3.1** — auto-generated spec, served live, exportable

## Quick example

```yaml
# auth.yaml — register, login, /me, OAuth2 out of the box
# The built-in User (email, password_hash, role, oauth_provider, …) is always present.
# Add custom fields here; no entities/user.yaml needed.
version: "1"
kind: auth
auth:
  base_path: /auth
  user:
    fields:
      - name: display_name
        type: string
        length: 128
        nullable: true
  register:
    enabled: true
  login:
    enabled: true
  me:
    enabled: true
```

```yaml
# endpoints/users.yaml
version: "1"
kind: endpoints

endpoints:
  - path: /users
    entity: User
    crud: [list, get, create, update, delete]
    auth:
      require: true
      roles: [admin]
    list:
      allow_filter_by: [role]
      allow_sort_by: [email, created_at]
      default_sort: "-created_at"
      pagination:
        style: offset
        default_limit: 20
        max_limit: 100
        include_total: true
```

```yaml
# yaypi.yaml
version: "1"

project:
  name: my-api
  base_url: https://api.example.com

server:
  port: 8080
  health:
    enabled: true
  rate_limit:
    requests_per_minute: 120
    burst: 20

databases:
  - name: primary
    driver: postgres
    dsn: "${DATABASE_URL}"
    default: true

auth:
  secret: "${JWT_SECRET}"
  expiry: 15m
  algorithm: HS256

auto_migrate: true   # development only; use `yaypi migrate up` in production

include:
  - entities/**/*.yaml
  - endpoints/**/*.yaml
```

## CLI

```bash
yaypi init <name>    # scaffold a new project
yaypi run            # start the server
yaypi migrate        # generate and apply schema migrations
yaypi seed           # run seed files
yaypi spec           # export OpenAPI spec to JSON
yaypi apikey generate  # create an API key and its storable digest
```

## IDE autocomplete

yayPi ships JSON Schema files for every YAML kind. Open the project in **VS Code** or **Cursor** and you get autocomplete, hover docs, and red squiggles for typos out of the box.

VS Code: install the [Red Hat YAML extension](vscode:extension/redhat.vscode-yaml). Cursor: no extension needed.

See [docs/intellisense.md](docs/intellisense.md) for details and manual setup instructions.

## Documentation

| Topic | File |
|-------|------|
| Getting Started | [docs/getting-started.md](docs/getting-started.md) |
| Core Concepts | [docs/concepts.md](docs/concepts.md) |
| Project Config (`yaypi.yaml`) | [docs/project-config.md](docs/project-config.md) |
| Entity Definitions | [docs/entities.md](docs/entities.md) |
| Endpoint Configuration | [docs/endpoints.md](docs/endpoints.md) |
| Auth Endpoints (login, register, OAuth2, refresh) | [docs/auth-endpoints.md](docs/auth-endpoints.md) |
| Authorization (JWT, API keys, RBAC, ABAC) | [docs/authorization.md](docs/authorization.md) |
| Migrations | [docs/migrations.md](docs/migrations.md) |
| Background Jobs | [docs/jobs.md](docs/jobs.md) |
| Plugins | [docs/plugins.md](docs/plugins.md) |
| OpenAPI | [docs/openapi.md](docs/openapi.md) |
| CLI Reference | [docs/cli.md](docs/cli.md) |
| Running in Production | [docs/production.md](docs/production.md) |
| Patterns Cookbook | [docs/patterns.md](docs/patterns.md) |
| YAML IntelliSense | [docs/intellisense.md](docs/intellisense.md) |

## Examples

- [`examples/blog/`](examples/blog/) — single-author blog (users, posts, tags)
- [`examples/community-blog/`](examples/community-blog/) — multi-author community blog (users, posts, tags, threaded comments, roles)

## License

MIT
