# Project Config (`yaypi.yaml`)

`yaypi.yaml` is the root configuration file. All other YAML files are discovered through the `include:` globs it defines.

## Full annotated example

```yaml
version: "1"

# ── Project metadata ──────────────────────────────────────────────────────────
project:
  name: my-api          # used for logging and scaffolding; no spaces
  base_url: /api/v1     # prefix for all routes (e.g. /api/v1/users)

# ── HTTP server ───────────────────────────────────────────────────────────────
server:
  port: 8080
  read_timeout: 30s     # max time to read request body
  write_timeout: 30s    # max time to write response
  shutdown_timeout: 10s # graceful shutdown window

  # Optional size limits
  max_request_body_size: 4MB
  max_header_bytes: 1MB

  # Optional TLS (omit to use plain HTTP)
  tls:
    cert_file: /etc/ssl/certs/server.crt
    key_file:  /etc/ssl/private/server.key

# ── Databases ─────────────────────────────────────────────────────────────────
databases:
  - name: primary
    driver: postgres
    dsn: ${DATABASE_URL:-postgres://localhost/myapp}
    max_open_conns: 25
    max_idle_conns: 5
    conn_max_lifetime: 1h
    default: true         # used when an entity has no explicit database:

  # Optional second database (entities opt-in with `database: analytics`)
  - name: analytics
    driver: postgres
    dsn: ${ANALYTICS_DATABASE_URL}
    max_open_conns: 10
    read_only: true       # disables write operations on this pool

# ── Authentication ────────────────────────────────────────────────────────────
auth:
  provider: jwt
  secret: ${JWT_SECRET}           # HMAC secret for HS256/HS512
  algorithm: HS256                # HS256, HS384, or HS512
  reject_algorithms: [none]       # always reject unsigned tokens
  expiry: 24h                     # informational only — yayPi validates `exp` claim

# ── Authorization (RBAC) ──────────────────────────────────────────────────────
policy:
  engine: casbin
  model: ./policies/model.conf    # Casbin model file path
  adapter: file                   # only "file" is supported in v1

# ── Auto-migrate ──────────────────────────────────────────────────────────────
# When true, yayPi applies any pending schema diff on startup.
# Use only in development or CI — never in production.
auto_migrate: false

# ── Plugins ───────────────────────────────────────────────────────────────────
plugins:
  - name: hash-password
    path: ./plugins/hashpassword   # path to compiled plugin binary (future)
    config:
      bcrypt_cost: 12

# ── Include patterns ──────────────────────────────────────────────────────────
# Glob patterns relative to yaypi.yaml. All matched YAML files are loaded
# and dispatched by their `kind` field.
include:
  - entities/**/*.yaml
  - endpoints/**/*.yaml
  - policies/**/*.yaml
  - jobs/**/*.yaml
```

## Minimal `yaypi.yaml`

The only required fields are those without defaults:

```yaml
version: "1"
project:
  name: my-api
  base_url: /api/v1
server:
  port: 8080
databases:
  - name: primary
    driver: postgres
    dsn: ${DATABASE_URL}
    default: true
auth:
  provider: jwt
  secret: ${JWT_SECRET}
  algorithm: HS256
  reject_algorithms: [none]
include:
  - entities/**/*.yaml
  - endpoints/**/*.yaml
```

## Field reference

### `project`

| Field | Type | Description |
|---|---|---|
| `name` | string | Project name used in logs |
| `base_url` | string | URL prefix for all routes (e.g. `/api/v1`) |

### `server`

| Field | Type | Default | Description |
|---|---|---|---|
| `port` | integer | — | Port to listen on |
| `read_timeout` | duration | — | Max time to read a request |
| `write_timeout` | duration | — | Max time to write a response |
| `shutdown_timeout` | duration | `10s` | Graceful shutdown window |
| `max_request_body_size` | string | — | e.g. `4MB` |
| `max_header_bytes` | string | — | e.g. `1MB` |
| `tls.cert_file` | string | — | TLS certificate path |
| `tls.key_file` | string | — | TLS private key path |

### `databases[]`

| Field | Type | Default | Description |
|---|---|---|---|
| `name` | string | — | Logical name referenced by entities |
| `driver` | string | — | Only `postgres` is supported |
| `dsn` | string | — | Connection string (URL or key=value) |
| `max_open_conns` | integer | `0` (unlimited) | Max open connections in pool |
| `max_idle_conns` | integer | `0` | Max idle connections |
| `conn_max_lifetime` | duration | `0` (no limit) | Max connection lifetime |
| `default` | boolean | `false` | Mark as the default database |
| `read_only` | boolean | `false` | Disallow writes on this pool |
| `schema` | string | `public` | PostgreSQL schema name |

### `auth`

| Field | Type | Description |
|---|---|---|
| `provider` | string | Only `jwt` is supported |
| `secret` | string | HMAC signing secret — **use env var** |
| `algorithm` | string | `HS256`, `HS384`, or `HS512` |
| `reject_algorithms` | list | Always include `[none]` |
| `expiry` | duration | Informational; `exp` claim is always validated |

### `policy`

| Field | Type | Description |
|---|---|---|
| `engine` | string | Only `casbin` is supported |
| `model` | string | Path to `model.conf` |
| `adapter` | string | Only `file` is supported in v1 |

### Top-level fields

| Field | Type | Default | Description |
|---|---|---|---|
| `auto_migrate` | boolean | `false` | Apply schema diff on startup |
| `plugins` | list | `[]` | Plugins to load (see [Plugins](plugins.md)) |
| `include` | list | `[]` | Glob patterns for entity/endpoint/job/policy files |

## Secret hygiene

At startup (and during `yaypi validate`), yayPi scans all config fields whose names contain `secret`, `password`, `token`, `key`, or `dsn`. If a matching field contains a plain-text value instead of a `${VAR}` reference, it logs a warning:

```
WRN auth.secret contains a plain-text value; use ${ENV_VAR} instead
```

This is a warning, not an error — the server still starts. But you should always store sensitive values in environment variables.
