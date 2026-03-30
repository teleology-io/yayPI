# yayPi

**yayPi** (Yay-PI, like API but YAML-based) is a Go framework that turns YAML configuration files into a fully functional REST API backend — no code generation, no templates, no boilerplate.

```bash
go install github.com/teleology-io/yayPI/cmd/yaypi@latest
yaypi init my-api && cd my-api
yaypi run
```

## What you get

- **CRUD endpoints** with filtering, sorting, cursor and offset pagination, eager-loaded relations
- **Field validation** — required, min/max length, min/max value, regex pattern, format checks (email, url, uuid, slug)
- **Immutable fields** — accepted on create, silently dropped on update
- **Bulk create** — POST an array; abort on first error or continue with partial success (207)
- **Auth endpoints** — register, login, `/me`, token refresh, OAuth2 (Google, GitHub, custom)
- **API key auth** — static keys in YAML or DB-backed lookup; works alongside JWT (either is sufficient)
- **RBAC + ABAC** — role-based access, per-subject conditions, row-level and field-level filtering
- **Rate limiting** — global token bucket or per-endpoint overrides
- **Health/readiness** — liveness and readiness probes for Kubernetes
- **Migrations** — diff-based schema migrations; auto-apply or generate SQL to review
- **Background jobs** — cron-scheduled SQL, HTTP, or plugin handlers with retry
- **Seed data** — idempotent rows inserted at startup or via `yaypi seed`
- **Email hooks** — transactional email on entity lifecycle events (SMTP)
- **Webhook hooks** — outbound HTTP on entity lifecycle events, with SSRF protection and retry
- **Plugins** — hook into any lifecycle event; write Go plugins for custom logic
- **OpenAPI 3.1** — auto-generated spec, served live, exportable

## Quick example

```yaml
# entities/user.yaml
version: "1"
kind: entity

entity:
  name: User
  timestamps: true
  soft_delete: true
  fields:
    - name: email
      type: string
      unique: true
      immutable: true
      validate:
        required: true
        format: email
    - name: password_hash
      type: string
      serialization:
        omit_response: true
        omit_log: true
    - name: role
      type: string
      default: "'member'"
      validate:
        required: true
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

auto_migrate: true

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
yaypi build          # compile registered plugins
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
| Patterns Cookbook | [docs/patterns.md](docs/patterns.md) |
| YAML IntelliSense | [docs/intellisense.md](docs/intellisense.md) |

## Examples

- [`examples/blog/`](examples/blog/) — single-author blog (users, posts, tags)
- [`examples/community-blog/`](examples/community-blog/) — multi-author community blog (users, posts, tags, threaded comments, roles)

## License

MIT
