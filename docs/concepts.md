# Concepts

Understanding how yayPi works will help you configure it correctly and debug problems quickly.

## The YAML-to-API pipeline

yayPi is a **runtime interpreter**, not a code generator. When you run `yaypi run`, it reads your YAML files, builds an in-memory representation of your API, and starts an HTTP server — no Go source files are generated or compiled.

**Startup sequence:**

1. Load and parse `yaypi.yaml`
2. Expand env var interpolations (`${VAR:-default}`)
3. Glob-expand `include:` patterns and load entity/endpoint/auth/job/seed/email/webhook/policy files
4. Validate cross-references (entity names, field names, etc.)
5. Build the **schema registry** (resolved entity definitions)
6. Connect to the database(s)
7. Optionally auto-migrate (dev only)
8. Run seed files (idempotent)
9. Load the Casbin policy engine from `roles.yaml` + `model.conf`
10. Initialize plugins; auto-register email and webhook hooks
11. Build the chi router (one route per CRUD operation)
12. Start cron scheduler
13. Listen for HTTP requests

## The `kind` system

Every YAML file (other than `yaypi.yaml` itself) must declare a `kind`. The loader uses this to know how to parse and register the file:

| `kind` | Purpose |
|---|---|
| `entity` | Defines a data model and its database table |
| `endpoints` | Defines REST routes for one or more entities |
| `auth` | Defines register, login, me, OAuth2, and refresh routes |
| `jobs` | Defines background cron jobs |
| `seed` | Defines idempotent seed rows inserted at startup |
| `email` | Defines email notifications triggered by lifecycle events |
| `webhooks` | Defines HTTP webhooks triggered by lifecycle events |
| `policy` | Defines RBAC roles and permissions |

Files are discovered via the `include:` globs in `yaypi.yaml`.

## Env var interpolation

yayPi supports `${VAR:-default}` syntax anywhere in `yaypi.yaml` and any included file. Interpolation happens at load time, before parsing.

```yaml
databases:
  - name: primary
    dsn: ${DATABASE_URL:-postgres://localhost/myapp}

auth:
  secret: ${JWT_SECRET}   # required, no default — fails if unset
```

- `${VAR}` — required; startup fails if `VAR` is unset
- `${VAR:-default}` — optional; uses `default` if `VAR` is unset

## How a request is processed

Every request goes through this pipeline:

```
Request
  → RealIP (extracts real client IP behind proxies)
  → RequestID (generates/propagates X-Request-Id)
  → Logger (structured zerolog entry)
  → Recover (catches panics, returns 500)
  → RateLimit (token bucket — global or per-endpoint)        ← 429 on excess
  → APIKeyAuth (X-API-Key header check, if configured)       ← 401 on bad key
  → RequireAuth (JWT verification — per route)               ← 401 on failure
  → RBAC (Casbin enforcement — per route)                    ← 403 on failure
  → RBAC: auth.roles check                                   ← 403 if role not in list
  → RBAC: auth.conditions check                              ← 403 if any condition fails
  → Handler (list / get / create / update / delete)
      → validateFields() (field validation rules)            ← 422 on failure
      → immutable field stripping (update only)
      → write_roles: strip restricted fields from body       ← silent drop
      → row_access: resolve SQL WHERE fragment
      → Query Builder (parameterized SQL + row filter)
      → Database
      → read_roles: strip restricted fields from response
      → plugin.Dispatcher (after hooks — email, webhook, custom)
  → Response
```

## Auth: four layers

yayPi enforces four independent security mechanisms:

### API keys (optional)

If `auth.api_keys` is configured, `APIKeyAuth` middleware runs first. It checks the `X-API-Key` header (or a query param). A valid key sets the request Subject with the key's associated role. If a key is present but invalid, the request is rejected with 401.

When both API key and JWT auth are configured, **either one is sufficient** — if the API key succeeds, JWT is skipped. This lets services use API keys while users use JWTs.

### Layer 1 — JWT (`RequireAuth`)

Verifies the token is cryptographically valid, not expired, and uses the configured algorithm. Extracts `sub`, `role`, and `email` from claims and attaches them to the request context as the *subject*. Skipped if API key already set the Subject.

Returns **401** if the token is absent (when `require: true`), invalid, or expired.

### Layer 2 — Casbin RBAC + roles + conditions

Three checks happen in sequence in the RBAC middleware:

1. **Casbin** — `Enforce(role, EntityName, action)` against `roles.yaml` permissions. Returns 403 if not allowed.
2. **`auth.roles`** — if set, the subject's role must be in the list. Returns 403 if not.
3. **`auth.conditions`** — if set, all CEL-lite expressions against the subject must be true. Returns 403 if any fails.

An endpoint with `auth.require: false` skips all three checks entirely (public access).

### Layer 3 — ABAC in the handler

After the middleware, three more checks run inside the handler itself:

4. **`access.write_roles`** — restricted fields are silently stripped from the request body before any DB write (no error).
5. **`row_access`** — a SQL `WHERE` fragment is resolved from the rules and injected into the query. No match → 403/404.
6. **`access.read_roles`** — restricted fields are stripped from the response JSON before it is sent.

See [Authorization](authorization.md) for the full reference.

## Field validation

Fields can declare validation rules under `validate:`. Validation runs on both create and update, before any database write.

```yaml
- name: email
  type: string
  validate:
    required: true
    format: email         # email | url | uuid | slug
    message: "must be a valid email address"

- name: price
  type: decimal
  validate:
    min: 0.01
    max: 99999.99
```

Validation errors return **422 Unprocessable Entity** with a field-keyed error map:
```json
{ "errors": { "email": "must be a valid email address" } }
```

## Immutable fields

Fields marked `immutable: true` are silently stripped from `PATCH` (update) payloads. They can be set on `POST` (create) but never changed afterward:

```yaml
- name: order_number
  type: string
  immutable: true
```

## Rate limiting

A token bucket limiter can be applied globally or per-endpoint:

```yaml
# yaypi.yaml — global
server:
  rate_limit:
    requests_per_minute: 120
    burst: 30
    key_by: ip      # ip (default) | user (JWT sub)

# endpoints — per-endpoint (overrides global for this route)
- path: /auth/register
  rate_limit:
    requests_per_minute: 5
    burst: 2
```

Excess requests receive **429 Too Many Requests**.

## Pagination

Two pagination styles are available on `list` endpoints:

**Cursor pagination** (default) — HMAC-signed opaque cursors. Best for live data feeds. Clients use `next_cursor` to fetch the next page:
```json
{ "data": [...], "meta": { "count": 20, "next_cursor": "eyJ..." } }
```

**Offset pagination** — traditional page/offset. Supports optional total count:
```json
{ "data": [...], "meta": { "count": 20, "limit": 20, "offset": 0, "page": 1, "total": 312 } }
```

Configure with `pagination.style: cursor` or `pagination.style: offset`.

## Bulk create

When `create.bulk: true`, a `POST` endpoint accepts a JSON array instead of a single object. Two error modes are available:

- `abort` (default) — stop on the first error, return 400. All-or-nothing.
- `partial` — continue past errors, return 207 Multi-Status with per-item results.

## Email and webhook hooks

Email and webhook hooks are built-in plugin implementations that are auto-registered from their respective YAML files. No Go code is needed.

**Email** (`kind: email`) — sends SMTP email on lifecycle events. Requires SMTP env vars (`SMTP_HOST`, `SMTP_PORT`, `SMTP_USER`, `SMTP_PASS`, `SENDER_EMAIL`, `SENDER_NAME`). Templates use `{{record.FIELD}}` syntax.

**Webhooks** (`kind: webhooks`) — fires HTTP requests on lifecycle events. Runs in a goroutine (non-blocking). Has SSRF protection blocking RFC-1918 and loopback addresses. Supports retry with backoff.

## Seed data

Seed files (`kind: seed`) define rows that should exist in the database. They run at startup before routes are registered and are **idempotent** — if a row with the given `key_field` value already exists, it is skipped.

```yaml
kind: seed
seeds:
  - entity: Role
    key_field: name
    data:
      - name: admin
      - name: member
```

## Token refresh

When `refresh.enabled: true` in an auth file, yayPi issues long-lived refresh tokens alongside regular JWTs. `POST /auth/refresh` validates the refresh token, issues a new access token, and rotates the refresh token (single-use).

Storage options: `cookie` (HttpOnly, sent automatically by browsers) or `body` (JSON — better for native apps).

## Soft delete

Entities with `soft_delete: true` get a `deleted_at timestamptz` column. When a record is soft-deleted, `deleted_at` is set to the current timestamp. yayPi automatically appends `WHERE deleted_at IS NULL` to every `SELECT`, `UPDATE`, and soft-`DELETE` query, so soft-deleted records are invisible to all API operations.

Hard delete (`soft_delete: false` on the endpoint) issues a real `DELETE FROM` statement.

## Migration engine

The migration engine is **diff-based**: it queries `information_schema` to discover what currently exists in the database, then compares that to your entity definitions and generates only the DDL statements needed to close the gap.

What it auto-detects:
- New tables (generates `CREATE TABLE`)
- New columns (generates `ALTER TABLE … ADD COLUMN`)
- New indexes (generates `CREATE INDEX CONCURRENTLY`)

What it does NOT do:
- Drop tables or columns (warns only — to prevent accidental data loss)
- Rename tables or columns (must be done manually)

See [Migrations](migrations.md) for the full workflow.
