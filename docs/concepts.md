# Concepts

Understanding how yayPi works will help you configure it correctly and debug problems quickly.

## The YAML-to-API pipeline

yayPi is a **runtime interpreter**, not a code generator. When you run `yaypi run`, it reads your YAML files, builds an in-memory representation of your API, and starts an HTTP server — no Go source files are generated or compiled.

**Startup sequence:**

1. Load and parse `yaypi.yaml`
2. Expand env var interpolations (`${VAR:-default}`)
3. Glob-expand `include:` patterns and load entity/endpoint/job/policy files
4. Validate cross-references (entity names, field names, etc.)
5. Build the **schema registry** (resolved entity definitions)
6. Connect to the database(s)
7. Optionally auto-migrate (dev only)
8. Load the Casbin policy engine from `roles.yaml` + `model.conf`
9. Initialize plugins
10. Build the chi router (one route per CRUD operation)
11. Start cron scheduler
12. Listen for HTTP requests

## The `kind` system

Every YAML file (other than `yaypi.yaml` itself) must declare a `kind`. The loader uses this to know how to parse and register the file:

| `kind` | Purpose |
|---|---|
| `entity` | Defines a data model and its database table |
| `endpoints` | Defines REST routes for one or more entities |
| `auth` | Defines register, login, me, and OAuth2 routes |
| `jobs` | Defines background cron jobs |
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
  → RequireAuth (JWT verification — per route)          ← 401 on failure
  → RBAC (Casbin enforcement — per route)               ← 403 on failure
  → RBAC: auth.roles check                              ← 403 if role not in list
  → RBAC: auth.conditions check                         ← 403 if any condition fails
  → Handler (list / get / create / update / delete)
      → write_roles: strip restricted fields from body  ← silent drop
      → row_access: resolve SQL WHERE fragment
      → Query Builder (parameterized SQL + row filter)
      → Database
      → read_roles: strip restricted fields from response
  → Response
```

## Auth: three layers

yayPi enforces three independent security checks:

### Layer 1 — JWT (`RequireAuth`)
Verifies the token is cryptographically valid, not expired, and uses the configured algorithm. Extracts `sub`, `role`, and `email` from claims and attaches them to the request context as the *subject*.

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

## Cursor pagination

`list` endpoints use cursor-based pagination instead of offset/limit. Cursors are HMAC-SHA256 signed and base64url-encoded to prevent tampering.

```json
{
  "data": [...],
  "meta": {
    "count": 20,
    "next_cursor": "eyJ..."
  }
}
```

To get the next page:
```
GET /items?cursor=eyJ...&limit=20
```

When `next_cursor` is `null` there are no more results. Cursors are opaque — do not parse them.

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
