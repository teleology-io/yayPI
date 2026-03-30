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

  # CORS — allowed origins for cross-origin requests
  # Use ["*"] to allow all origins (not recommended in production)
  allowed_origins:
    - https://app.example.com
    - http://localhost:3000

  # Health/readiness endpoints (for Kubernetes probes)
  health:
    enabled: true
    path: /health          # liveness: always 200
    readiness_path: /ready # readiness: 200 if DB reachable, 503 if not

  # Global rate limiting (token bucket)
  rate_limit:
    requests_per_minute: 120
    burst: 30
    key_by: ip            # ip (default) | user (JWT sub claim)

# ── Databases ─────────────────────────────────────────────────────────────────
databases:
  - name: primary
    driver: postgres           # postgres/postgresql, mysql/mariadb, sqlite/sqlite3
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

  # Optional API key authentication (alongside JWT)
  api_keys:
    header: X-API-Key             # header to read the key from (default: X-API-Key)
    query_param: api_key          # optional: also accept ?api_key= query param
    # Static key list (use this OR entity below, not both):
    keys:
      - key: ${ADMIN_API_KEY}
        role: admin
      - key: ${SERVICE_API_KEY}
        role: service
    # DB-backed alternative:
    entity: ApiKey                # entity name containing key records
    key_field: token              # column holding the key value (default: token)
    role_field: role              # column holding the role (default: role)

# ── Authorization (RBAC) ──────────────────────────────────────────────────────
policy:
  engine: casbin
  model: ./policies/model.conf    # Casbin model file path
  adapter: file                   # "file" or "database"
  adapter_table: casbin_rules     # table name when adapter: database

# ── OpenAPI spec generation ───────────────────────────────────────────────────
# Define one or more named specs. Endpoints are included in all specs by default.
# See docs/openapi.md for the full guide.
spec:
  - name: api
    title: "My API"
    description: "Public REST API"
    version: "1.0.0"
    servers:
      - url: https://api.example.com
        description: Production
      - url: http://localhost:8080
        description: Local

# ── Auto-migrate ──────────────────────────────────────────────────────────────
# When true, yayPi applies any pending schema diff on startup.
# Use only in development or CI — never in production.
auto_migrate: false

# ── Plugins ───────────────────────────────────────────────────────────────────
plugins:
  - name: hash-password
    path: ./plugins/hashpassword
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
  - seeds/**/*.yaml       # kind: seed — idempotent startup data
  - emails/**/*.yaml      # kind: email — SMTP notification hooks
  - webhooks/**/*.yaml    # kind: webhooks — HTTP webhook hooks
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
| `allowed_origins` | list | `[]` | CORS allowed origins; `["*"]` allows all |
| `tls.cert_file` | string | — | TLS certificate path |
| `tls.key_file` | string | — | TLS private key path |
| `health.enabled` | boolean | `false` | Mount liveness + readiness endpoints |
| `health.path` | string | `/health` | Liveness path — always 200 |
| `health.readiness_path` | string | `/ready` | Readiness path — 503 if DB unreachable |
| `rate_limit.requests_per_minute` | integer | — | Token bucket fill rate |
| `rate_limit.burst` | integer | equal to rpm | Burst capacity |
| `rate_limit.key_by` | string | `ip` | `ip` or `user` (JWT sub) |

Health endpoints are mounted **outside** the `base_url` prefix so they are always reachable.

### `databases[]`

| Field | Type | Default | Description |
|---|---|---|---|
| `name` | string | — | Logical name referenced by entities |
| `driver` | string | — | `postgres`/`postgresql`, `mysql`/`mariadb`, `sqlite`/`sqlite3` |
| `dsn` | string | — | Connection string |
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

### `auth.api_keys`

| Field | Type | Default | Description |
|---|---|---|---|
| `header` | string | `X-API-Key` | Request header to read the key from |
| `query_param` | string | — | Also accept the key from this query param |
| `keys[].key` | string | — | Static key value (use env var) |
| `keys[].role` | string | — | Role granted to this key |
| `entity` | string | — | DB-backed: entity name holding key records |
| `key_field` | string | `token` | DB-backed: column with the key value |
| `role_field` | string | `role` | DB-backed: column with the role |

API keys and JWTs are OR-logic: a request authenticated by either is considered authenticated. If a key is present but not found, the request is rejected (401) regardless of whether a JWT is also present.

### `policy`

| Field | Type | Description |
|---|---|---|
| `engine` | string | Only `casbin` is supported |
| `model` | string | Path to `model.conf` |
| `adapter` | string | `file` or `database` |
| `adapter_table` | string | Table name when `adapter: database` (default: `casbin_rules`) |

### Top-level fields

| Field | Type | Default | Description |
|---|---|---|---|
| `auto_migrate` | boolean | `false` | Apply schema diff on startup |
| `plugins` | list | `[]` | Plugins to load (see [Plugins](plugins.md)) |
| `include` | list | `[]` | Glob patterns for all YAML kinds |
| `spec` | list | `[]` | Named OpenAPI specs to generate (see [OpenAPI](openapi.md)) |

### `spec[]`

| Field | Type | Description |
|---|---|---|
| `name` | string | Unique spec identifier used in the URL (`/openapi/{name}.json`) |
| `title` | string | API title in the spec `info` block |
| `description` | string | API description |
| `version` | string | API version string (e.g. `"1.0.0"`) |
| `servers[].url` | string | Server base URL |
| `servers[].description` | string | Server label (e.g. `"Production"`) |

## Secret hygiene

At startup (and during `yaypi validate`), yayPi scans all config fields whose names contain `secret`, `password`, `token`, `key`, or `dsn`. If a matching field contains a plain-text value instead of a `${VAR}` reference, it logs a warning:

```
WRN auth.secret contains a plain-text value; use ${ENV_VAR} instead
```

This is a warning, not an error — the server still starts. But you should always store sensitive values in environment variables.
