# Project Config (`yaypi.yaml`)

`yaypi.yaml` is the root configuration file. All other YAML files are discovered through the `include:` globs it defines. `${VAR}` and `${VAR:-default}` are replaced from the environment; an unset variable without a default becomes an empty string, which validation rejects for secrets and DSNs.

## Full annotated example

```yaml
version: "1"

# ── Project metadata ──────────────────────────────────────────────────────────
project:
  name: my-api          # used in logs, as the default JWT issuer and OpenAPI tag
  base_url: /api/v1     # prefix for all routes (e.g. /api/v1/users)

# ── HTTP server ───────────────────────────────────────────────────────────────
server:
  port: 8080
  read_timeout: 30s
  read_header_timeout: 10s   # default 10s — protects against slow-header attacks
  write_timeout: 30s
  idle_timeout: 120s         # keep-alive idle timeout
  request_timeout: 30s       # deadline for each request and its queries (default: write_timeout) → 504
  shutdown_timeout: 10s      # graceful shutdown window
  drain_delay: 5s            # after SIGTERM, /ready fails and serving continues this long

  max_request_body_size: 1MB # default 1MB; larger bodies get 413
  max_header_bytes: 1MB

  tls:
    cert_file: /etc/ssl/certs/server.crt
    key_file:  /etc/ssl/private/server.key

  # Reverse proxies / load balancers whose X-Forwarded-For you trust. Without this the
  # client IP (for rate limiting and logs) is the TCP peer address.
  trusted_proxies: [10.0.0.0/8]

  # CORS. Listed origins may send credentials (cookies). "*" allows any origin
  # WITHOUT credentials — it is never combined with credentials.
  allowed_origins:
    - https://app.example.com
  cors:
    exposed_headers: [ETag, X-Request-ID]

  security_headers: true     # nosniff, frame DENY, no-referrer, CSP (default true)
  hsts_max_age: 31536000     # add HSTS when the API is HTTPS-only (incl. behind a TLS proxy)

  health:
    enabled: true
    path: /health            # liveness: always 200
    readiness_path: /ready   # readiness: 200 if databases reachable; 503 otherwise or while draining

  rate_limit:                # global token bucket
    requests_per_minute: 120
    burst: 30
    key_by: ip               # ip (default) | user (authenticated subject)

  strict_startup: true       # a degraded subsystem stops the boot instead of warning (default)
  idempotency_ttl: 24h       # how long Idempotency-Key responses are replayed

# ── Databases ─────────────────────────────────────────────────────────────────
databases:
  - name: primary
    driver: postgres           # postgres/postgresql, mysql, sqlite/sqlite3
    dsn: ${DATABASE_URL}
    max_open_conns: 25         # default 25
    max_idle_conns: 25         # default = max_open_conns
    conn_max_lifetime: 30m     # default 30m
    conn_max_idle_time: 5m     # default 5m
    default: true

  - name: analytics
    driver: postgres
    dsn: ${ANALYTICS_DATABASE_URL}

# ── Authentication ────────────────────────────────────────────────────────────
auth:
  secret: ${JWT_SECRET}        # >= 32 bytes: openssl rand -hex 32
  algorithm: HS256             # HS256/384/512, RS256/384/512, ES256/384/512
  # private_key_file: ./keys/jwt.pem   # RS*/ES*
  # public_key_file: ./keys/jwt.pub
  # key_id: 2026-10
  issuer: my-api               # verified when set (issued as project.name otherwise)
  # audience: my-frontend
  expiry: 15m                  # access token TTL (default 15m with refresh tokens, else 1h)
  revocation_check: false      # true = logout-all, resets, role changes and deletes apply instantly

  api_keys:
    header: X-API-Key
    # Static keys:
    keys:
      - key: ${SERVICE_API_KEY}
        role: service
        name: billing-service  # caller id for logs, row_access and audit
    # Or DB-backed keys (column stores the SHA-256 digest from `yaypi apikey generate`):
    # entity: ApiKey
    # key_field: token
    # role_field: role
    # subject_field: user_id

# ── Authorization (RBAC) ──────────────────────────────────────────────────────
policy:
  engine: casbin
  model: ./policies/model.conf # relative to yaypi.yaml
  adapter: file

# ── Email (SMTP) ──────────────────────────────────────────────────────────────
smtp:                           # email triggers, password reset, verification
  host: smtp.example.com        # empty fields fall back to SMTP_HOST / SMTP_PORT / SMTP_USER /
  port: 587                     #   SMTP_PASS / SMTP_SENDER_NAME / SMTP_SENDER_EMAIL
  username: ${SMTP_USER}
  password: ${SMTP_PASS}
  from_name: My App
  from_email: no-reply@example.com
  retry:                        # default for all emails (emails[].retry overrides)
    max_attempts: 5
    initial_delay: 10s
    max_delay: 1h

# ── Background delivery, jobs, audit ──────────────────────────────────────────
outbox:
  poll_interval: 1s          # webhook/email worker poll interval
  retention: 7d              # delete delivered messages after this
cron:
  distributed: true          # one replica per tick (default when a database is configured)
audit:
  retention: 365d            # purge audit rows older than this (default: keep forever)

# ── Observability ─────────────────────────────────────────────────────────────
log:
  level: info                  # debug | info | warn | error
  format: json                 # json | console (default: console on a terminal, json otherwise)

metrics:
  enabled: true
  path: /metrics
  token: ${METRICS_TOKEN}      # optional bearer token for scrapers

# ── OpenAPI ───────────────────────────────────────────────────────────────────
spec:
  - name: api
    title: "My API"
    version: "1.0.0"
    servers:
      - url: https://api.example.com

auto_migrate: false            # development only — see Migrations

plugins:
  - name: my-plugin            # matches PluginInfo.Name; config is passed to Init
    config: {}

include:
  - entities/**/*.yaml
  - endpoints/**/*.yaml
  - policies/**/*.yaml
  - jobs/**/*.yaml
  - seeds/**/*.yaml
  - emails/**/*.yaml
  - webhooks/**/*.yaml
  - auth.yaml
```

## Minimal `yaypi.yaml`

```yaml
version: "1"
project:
  name: my-api
  base_url: /api/v1
databases:
  - name: primary
    driver: postgres
    dsn: ${DATABASE_URL}
    default: true
auth:
  secret: ${JWT_SECRET}
include:
  - entities/**/*.yaml
  - endpoints/**/*.yaml
```

## Field reference

### `server`

| Field | Type | Default | Description |
|---|---|---|---|
| `port` | integer | `8080` | Port to listen on |
| `read_timeout` | duration | `30s` | Max time to read a request |
| `read_header_timeout` | duration | `10s` | Max time to read headers |
| `write_timeout` | duration | `30s` | Max time to write a response |
| `idle_timeout` | duration | `120s` | Keep-alive idle timeout |
| `request_timeout` | duration | `write_timeout` | Request context deadline; queries are cancelled; 504 |
| `shutdown_timeout` | duration | `10s` | Graceful shutdown window |
| `drain_delay` | duration | `0` | Keep serving after SIGTERM while readiness fails |
| `max_request_body_size` | size | `1MB` | Larger bodies → 413 |
| `max_header_bytes` | size | `1MB` | |
| `trusted_proxies` | list | `[]` | CIDRs/IPs whose forwarding headers are trusted |
| `allowed_origins` | list | `[]` | CORS origins; `"*"` = any origin, never with credentials |
| `cors.*` | | | `allowed_headers`, `allowed_methods`, `exposed_headers`, `max_age` |
| `security_headers` | boolean | `true` | Default security response headers |
| `hsts_max_age` | integer | `0` | Seconds; `> 0` adds `Strict-Transport-Security` |
| `strict_startup` | boolean | `true` | Fail boot on degraded subsystems |
| `idempotency_ttl` | duration | `24h` | Idempotency-Key replay window |
| `health.*` | | | `enabled`, `path` (`/health`), `readiness_path` (`/ready`) |
| `rate_limit.*` | | | `requests_per_minute`, `burst` (= rpm), `key_by` (`ip`/`user`) |

Health and metrics endpoints are mounted **outside** `base_url`. The global rate limit and any per-endpoint limit both apply.

### `databases[]`

| Field | Default | Description |
|---|---|---|
| `name` | — | Logical name referenced by entities |
| `driver` | — | `postgres`, `mysql`, `sqlite` |
| `dsn` | — | Required. For Postgres you can add `options=-c%20statement_timeout%3D5000` to cap query time server-side |
| `max_open_conns` | `25` | |
| `max_idle_conns` | `max_open_conns` | |
| `conn_max_lifetime` | `30m` | |
| `conn_max_idle_time` | `5m` | |
| `default` | first entry | The database for entities without `database:` |

### `auth`

| Field | Description |
|---|---|
| `secret` | HS* key, at least 32 bytes. Required whenever auth is used |
| `algorithm` | `HS256` (default) … `ES512`. Only this algorithm is ever accepted |
| `private_key_file` / `public_key_file` / `key_id` | RS*/ES* keys; the public key is published at `<base>/auth/.well-known/jwks.json` |
| `issuer` / `audience` | `iss` / `aud` claims. Each is verified on every request when set explicitly (`iss` is issued as `project.name` by default but not checked) |
| `expiry` | Access token lifetime |
| `revocation_check` | Per-request user lookup for instant revocation |

### `auth.api_keys`

| Field | Default | Description |
|---|---|---|
| `header` | `X-API-Key` | Header carrying the key |
| `query_param` | — | Also accept from the query string (discouraged: keys end up in logs) |
| `keys[]` | — | `key`, `role`, `name` |
| `entity` | — | DB-backed key table |
| `key_field` | `token` | Column holding the key's SHA-256 hex digest |
| `role_field` | `role` | |
| `subject_field` | `user_id` if present, else PK | Caller id |
| `key_hash` | `sha256` | `plain` for legacy cleartext tables |

DB-backed keys also honour optional `expires_at`, `revoked_at` and `deleted_at` columns. API keys and JWTs are alternatives: either authenticates a request. A key that is present but unknown is rejected with 401.

### `policy`

| Field | Description |
|---|---|
| `engine` | `casbin` |
| `model` | Path to `model.conf`, relative to `yaypi.yaml` |
| `adapter` | `file` (roles from `policies/*.yaml`) |

Any policy error stops the server: running without the engine would skip every check.

### `smtp`

| Field | Env fallback | Description |
|---|---|---|
| `host` | `SMTP_HOST` | Required for any email feature |
| `port` | `SMTP_PORT` | Default 587 |
| `username` / `password` | `SMTP_USER` / `SMTP_PASS` | Use `${ENV_VAR}` for the password |
| `from_name` / `from_email` | `SMTP_SENDER_NAME` / `SMTP_SENDER_EMAIL` | From header |
| `retry` | — | Default delivery retry for all emails (`max_attempts`, `backoff`, `initial_delay`, `max_delay`); `emails[].retry` overrides |

### `outbox`, `cron`, `audit`

| Field | Default | Description |
|---|---|---|
| `outbox.poll_interval` | `1s` | How often queued webhooks/emails are picked up |
| `outbox.retention` | `7d` | Delivered messages are deleted after this |
| `cron.distributed` | `true` (with a DB) | Coordinate job runs across replicas via `yaypi_job_locks`; set `false` for a single instance |
| `audit.retention` | keep forever | Purge `yaypi_audit_log` rows older than this (checked hourly) |

Durations accept Go syntax (`90s`, `1h30m`) and days (`7d`).

### `log`, `metrics`

See [Production](production.md) for what is logged and the exported metrics.

## Startup checks

`yaypi validate` and `yaypi run` reject:

- a missing or short (< 32 bytes) `auth.secret` when authentication is used;
- RS*/ES* without key files; an empty database `dsn`;
- unknown `crud` operations, invalid regex `pattern`s or `format`s, `delete.soft_delete` on entities without `soft_delete`;
- invalid webhook/email `condition`s or payload templates;
- policy engine errors, password reset / email verification without SMTP.

Plain-text values for secrets that look like placeholders (`changeme`, `secret`, `dev-only`, …) produce a warning.
