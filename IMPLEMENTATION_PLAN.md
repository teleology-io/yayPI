# Yaypi: Yet Another YAML-Powered API — Complete Technical Implementation Plan

---

## Table of Contents

1. [High-Level Architecture](#1-high-level-architecture)
2. [YAML Specification Design](#2-yaml-specification-design)
3. [Entity System](#3-entity-system)
4. [API Generation](#4-api-generation)
5. [Migration System](#5-migration-system)
6. [Plugin System](#6-plugin-system)
7. [Multi-Database Support](#7-multi-database-support)
8. [Background Jobs / Cron](#8-background-jobs--cron)
9. [Authorization: RBAC](#9-authorization-rbac)
10. [Developer Experience](#10-developer-experience)
11. [End-to-End Flow Example](#11-end-to-end-flow-example)
12. [Risks and Tradeoffs](#12-risks-and-tradeoffs)
13. [Suggested Tech Stack](#13-suggested-tech-stack)

---

## 1. High-Level Architecture

### Core Philosophy

Yaypi operates as a **runtime interpreter** of YAML configuration, not a code generator. This is the single most consequential architectural decision. The distinction:

- **Code generation** (like protoc, sqlc): YAML is compiled to Go source, then compiled. Fast at runtime, slow iteration cycle, debugging involves generated artifacts.
- **Runtime reflection** (like this framework): YAML is parsed at startup, a live server is constructed dynamically. Slower startup, but instant iteration — change YAML, restart, done.

**Recommendation: Runtime interpretation with optional ahead-of-time validation.** The runtime reflection approach is chosen because developer experience is listed as a top priority and YAML-driven systems thrive on fast iteration. Performance concerns are addressed at the handler and query layer, not the architectural layer.

### Component Map

```
┌─────────────────────────────────────────────────────────────────┐
│                        yaypi runtime                            │
│                                                                 │
│  ┌──────────┐    ┌──────────────┐    ┌───────────────────────┐  │
│  │  CLI     │───▶│  Config      │───▶│  Schema Registry      │  │
│  │  (cobra) │    │  Loader      │    │  (entities, endpoints) │  │
│  └──────────┘    └──────────────┘    └───────────┬───────────┘  │
│                                                  │               │
│                        ┌─────────────────────────┼─────────────┐│
│                        │                         │             ││
│               ┌────────▼──────┐    ┌─────────────▼──────────┐ ││
│               │  Router       │    │  DB Layer              │ ││
│               │  Builder      │    │  (multi-conn pool)     │ ││
│               │  (chi/fiber)  │    │  (sqlx/pgx)            │ ││
│               └────────┬──────┘    └─────────────┬──────────┘ ││
│                        │                         │             ││
│               ┌────────▼──────┐    ┌─────────────▼──────────┐ ││
│               │  Handler      │    │  Query Builder         │ ││
│               │  Factory      │◀───│  (entity-aware)        │ ││
│               └────────┬──────┘    └────────────────────────┘ ││
│                        │                                       ││
│               ┌────────▼──────────────────────────────────┐   ││
│               │  Middleware Chain                          │   ││
│               │  (auth → RBAC → rate-limit → log)         │   ││
│               └───────────────────────────────────────────┘   ││
│                                                                 │
│  ┌──────────────────────┐    ┌──────────────────────────────┐  │
│  │  Plugin Host         │    │  Cron Scheduler              │  │
│  │  (lifecycle hooks)   │    │  (gocron / robfig)           │  │
│  └──────────────────────┘    └──────────────────────────────┘  │
│                                                                 │
│  ┌──────────────────────┐    ┌──────────────────────────────┐  │
│  │  Migration Engine    │    │  Policy Engine               │  │
│  │  (schema diff)       │    │  (RBAC / Casbin)             │  │
│  └──────────────────────┘    └──────────────────────────────┘  │
└─────────────────────────────────────────────────────────────────┘
```

### Startup Sequence

```
1. Parse CLI flags and locate config root directory
2. Load and merge all YAML files (entities/, endpoints/, jobs/, policies/, etc.)
3. Validate YAML against the internal schema (JSON Schema or custom validator)
4. Build Schema Registry (entity graph, relationship resolution)
5. Initialize DB connection pools (one per named connection)
6. Run pending migrations (if --migrate flag is set or auto_migrate: true)
7. Compile and load policy engine (RBAC roles)
8. Build Router from endpoint definitions
9. Load and initialize plugins (call Plugin.Init())
10. Register cron jobs
11. Start HTTP server
```

### Repository Layout

```
yaypi/
├── cmd/
│   └── yaypi/          # CLI entrypoint
├── internal/
│   ├── config/         # YAML loader, merger, validator
│   ├── schema/         # Schema registry, entity graph
│   ├── db/             # Connection pool manager
│   ├── router/         # Route builder
│   ├── handler/        # Handler factory (CRUD + custom)
│   ├── middleware/      # Auth, logging, rate limit
│   ├── policy/         # RBAC policy engine
│   ├── migration/      # Diff engine, migration runner
│   ├── plugin/         # Plugin host, hook dispatcher
│   ├── cron/           # Job scheduler
│   └── validator/      # YAML + HTTP validation
├── pkg/
│   ├── types/          # Shared public types (FieldType, etc.)
│   └── sdk/            # Plugin SDK (exported for plugin authors)
├── examples/
│   └── blog/           # Complete working example
└── yaypi.schema.json   # Published JSON Schema for YAML validation
```

---

## 2. YAML Specification Design

### Configuration Root

Every yaypi project has a single root config file. All other YAML files are referenced or auto-discovered.

```yaml
# yaypi.yaml  — the project root config
version: "1"

project:
  name: blog-api
  base_url: /api/v1

server:
  port: 8080
  read_timeout: 30s
  write_timeout: 30s
  shutdown_timeout: 10s
  max_request_body_size: 4mb   # hard cap; 413 if exceeded
  max_header_bytes: 8kb

  tls:                         # optional; recommended behind a reverse proxy
    cert_file: ${TLS_CERT_FILE}
    key_file: ${TLS_KEY_FILE}

databases:
  - name: primary
    driver: postgres
    dsn: ${DATABASE_URL}          # env var interpolation
    max_open_conns: 25
    max_idle_conns: 5
    conn_max_lifetime: 5m
    default: true

  - name: analytics
    driver: postgres
    dsn: ${ANALYTICS_DATABASE_URL}
    max_open_conns: 10

auth:
  provider: jwt
  secret: ${JWT_SECRET}
  expiry: 24h
  refresh_expiry: 7d
  algorithm: HS256             # enforced — tokens claiming a different alg are rejected
  reject_algorithms: [none, RS256, ES256]  # explicit blocklist; "none" always blocked

policy:
  engine: casbin                  # or: opa, builtin
  model: ./policies/model.conf    # Casbin model file
  adapter: db                     # or: file
  adapter_table: yaypi_policies   # if adapter: db

auto_migrate: false               # never auto-migrate in prod; use CLI

plugins:
  - path: ./plugins/audit-log
    checksum: sha256:abc123...   # binary checksum verified at startup; startup fails if mismatch
    config:
      table: audit_events
  - name: yaypi/cors             # built-in plugin by name
    config:
      allowed_origins: ["https://myapp.com"]  # never use "*" in production

include:
  - entities/**/*.yaml
  - endpoints/**/*.yaml
  - jobs/**/*.yaml
  - policies/**/*.yaml
```

### Directory Structure for a Project

```
my-api/
├── yaypi.yaml
├── entities/
│   ├── user.yaml
│   ├── post.yaml
│   └── tag.yaml
├── endpoints/
│   ├── auth.yaml
│   └── posts.yaml
├── jobs/
│   └── cleanup.yaml
├── policies/
│   ├── roles.yaml
│   ├── rules.yaml
│   └── model.conf
└── plugins/
    └── audit-log/
        ├── plugin.go
        └── plugin.yaml
```

### Environment Variable Interpolation

All YAML string values support `${VAR_NAME}` and `${VAR_NAME:-default_value}` syntax. This is resolved at load time before validation.

```yaml
dsn: ${DATABASE_URL:-postgres://localhost/devdb}
port: ${PORT:-8080}
```

**Secret hygiene rules enforced by yaypi:**

1. Any field whose YAML key matches `*secret*`, `*password*`, `*token*`, `*key*`, or `*dsn*` is flagged with a startup warning if its value is not an `${ENV_VAR}` reference (i.e., a hardcoded secret).
2. Resolved secret values are **never** written to logs. If a DB connection fails, the log entry shows `dsn=postgres://user:***@host/db` (credentials redacted).
3. `yaypi validate` prints resolved config in debug mode but always masks secret fields with `***`.

### YAML Validation Strategy

**Recommended approach: Two-layer validation.**

**Layer 1 — Structural (JSON Schema).** A published `yaypi.schema.json` enables IDE autocompletion and catches structural errors. Tools: `santhosh-tekuri/jsonschema` in Go.

**Layer 2 — Semantic (custom Go validator).** After structural parsing, run semantic checks:
- All `@references(fk=?)` targets exist in the schema registry
- No circular dependencies in entity relationships that would cause infinite recursion
- Endpoint `entity:` references an existing entity
- Policy rules reference roles and attributes that exist in the schema
- Auth roles referenced in endpoints exist in the policy config

Errors are reported as a structured list with file, line number (via YAML anchors and position tracking), and a plain-English message.

---

## 3. Entity System

### Basic Entity Definition

```yaml
# entities/user.yaml
version: "1"
kind: entity

entity:
  name: User
  table: users
  database: primary          # optional, uses default if omitted
  timestamps: true           # adds created_at, updated_at automatically
  soft_delete: true          # adds deleted_at; queries filter deleted_at IS NULL

  fields:
    - name: id
      type: uuid
      primary_key: true
      default: gen_random_uuid()

    - name: email
      type: string
      length: 255
      unique: true
      nullable: false
      index: true             # simple index shorthand

    - name: password_hash
      type: string
      length: 60
      nullable: false
      serialization:
        omit_response: true   # never appears in API responses
        omit_log: true        # never written to structured request logs

    - name: role
      type: enum
      values: [admin, editor, viewer]
      default: viewer

    - name: status
      type: enum
      values: [active, suspended, pending]
      default: pending

    - name: department
      type: string
      length: 100
      nullable: true

    - name: profile
      type: jsonb             # stored as JSONB in Postgres
      nullable: true

    - name: login_count
      type: integer
      default: 0
      nullable: false

  indexes:
    - name: idx_users_email_status
      columns: [email, status]
      unique: false

    - name: idx_users_created_at
      columns: [created_at]
      type: brin              # BRIN for time-series-like columns

  constraints:
    - name: chk_login_count_non_negative
      check: "login_count >= 0"

  hooks:
    before_create:
      - hash_password          # references a plugin-provided hook by name
    after_create:
      - send_welcome_email
```

### Relationship Definitions

Relationships are declared on the "child" or "join" side. The `references` directive is the canonical syntax for declaring foreign key intent.

```yaml
# entities/post.yaml
entity:
  name: Post
  table: posts
  timestamps: true
  soft_delete: false

  fields:
    - name: id
      type: uuid
      primary_key: true
      default: gen_random_uuid()

    - name: author_id
      type: uuid
      nullable: false
      references:
        entity: User
        field: id
        on_delete: CASCADE
        on_update: NO ACTION

    - name: title
      type: string
      length: 512
      nullable: false

    - name: body
      type: text
      nullable: false

    - name: published_at
      type: timestamptz
      nullable: true

    - name: view_count
      type: bigint
      default: 0

  relations:
    - name: author
      type: belongs_to        # many Posts belong_to one User
      entity: User
      foreign_key: author_id

    - name: tags
      type: many_to_many
      entity: Tag
      through: PostTag        # join entity name
      foreign_key: post_id
      other_key: tag_id

    - name: comments
      type: has_many
      entity: Comment
      foreign_key: post_id
```

### Join Entity (Many-to-Many)

```yaml
# entities/post_tag.yaml
entity:
  name: PostTag
  table: post_tags
  timestamps: false

  fields:
    - name: post_id
      type: uuid
      nullable: false
      references:
        entity: Post
        field: id
        on_delete: CASCADE

    - name: tag_id
      type: uuid
      nullable: false
      references:
        entity: Tag
        field: id
        on_delete: CASCADE

  constraints:
    - name: pk_post_tags
      type: primary_key
      columns: [post_id, tag_id]
```

### Supported Field Types

| YAML Type     | Go Type              | Postgres Type        |
|---------------|----------------------|----------------------|
| `uuid`        | `[16]byte`           | `UUID`               |
| `string`      | `string`             | `VARCHAR(n)`         |
| `text`        | `string`             | `TEXT`               |
| `integer`     | `int32`              | `INTEGER`            |
| `bigint`      | `int64`              | `BIGINT`             |
| `float`       | `float64`            | `DOUBLE PRECISION`   |
| `decimal`     | `pgtype.Numeric`     | `NUMERIC(p,s)`       |
| `boolean`     | `bool`               | `BOOLEAN`            |
| `timestamptz` | `time.Time`          | `TIMESTAMPTZ`        |
| `date`        | `pgtype.Date`        | `DATE`               |
| `jsonb`       | `json.RawMessage`    | `JSONB`              |
| `enum`        | `string` (validated) | `TEXT` with CHECK    |
| `array`       | `[]T`                | `T[]`                |
| `bytea`       | `[]byte`             | `BYTEA`              |

### Internal Go Representation

```go
// internal/schema/entity.go

type Entity struct {
    Name        string
    Table       string
    Database    string
    Fields      []Field
    Relations   []Relation
    Indexes     []Index
    Constraints []Constraint
    Hooks       EntityHooks
    SoftDelete  bool
    Timestamps  bool
}

type Field struct {
    Name          string
    ColumnName    string
    Type          FieldType
    Nullable      bool
    Unique        bool
    PrimaryKey    bool
    Default       string
    Reference     *Reference
    Serialization FieldSerialization
    EnumValues    []string
    Length        int
    Precision     int
    Scale         int
}

type Reference struct {
    Entity   string
    Field    string
    OnDelete ReferentialAction
    OnUpdate ReferentialAction
}
```

---

## 4. API Generation

### Endpoint Definition

```yaml
# endpoints/posts.yaml
version: "1"
kind: endpoints

endpoints:
  - path: /posts
    entity: Post
    crud: [list, create]
    middleware: [authenticate, rate_limit]

    list:
      allow_filter_by: [status, author_id, published_at]
      allow_sort_by: [created_at, view_count, title]
      default_sort: created_at:desc
      pagination:
        style: cursor
        default_limit: 20
        max_limit: 100
      include:
        - author
        - tags
      auth:
        require: true
        roles: [admin, editor, viewer]

    create:
      auth:
        require: true
        roles: [admin, editor]
      before_hooks: [validate_post_content]
      after_hooks: [notify_subscribers]

  - path: /posts/{id}
    entity: Post
    crud: [get, update, delete]
    middleware: [authenticate]

    get:
      include: [author, tags]
      auth:
        require: false

    update:
      allowed_fields: [title, body, published_at]
      auth:
        require: true
        roles: [admin, editor]

    delete:
      auth:
        require: true
        roles: [admin]
      soft_delete: true

  - path: /posts/{id}/publish
    entity: Post
    method: POST
    handler: custom
    middleware: [authenticate]
    auth:
      require: true
      roles: [admin, editor]
```

### CRUD Handler Auto-Generation

Each CRUD operation is a `HandlerFunc` constructed by the Handler Factory. The factory closes over the entity definition and connection pool.

**LIST handler logic:**

```
1. Parse query params: filters, sort, pagination cursor/offset
2. Validate filter fields against entity's allow_filter_by list (reject unknown fields → 400)
3. Validate sort fields against allow_sort_by list (reject unknown fields → 400)
4. Validate and decode pagination cursor: cursor is HMAC-signed JSON — reject tampered cursors
5. Clamp limit to [1, max_limit]; reject non-integer values
6. Build SQL: SELECT <fields> FROM <table> WHERE <filters> ORDER BY <sort> LIMIT/OFFSET
7. If soft_delete: automatically append AND deleted_at IS NULL
8. If include: execute follow-up queries for each relation (IN-clause batching, not N+1)
9. Serialize result, omitting fields with omit_response: true
10. Return paginated envelope
```

**GET/UPDATE/DELETE handler logic (path param validation):**

```
1. Extract {id} from path
2. Validate format matches the entity's primary key type (e.g. UUID format check → 400)
   This prevents probing the DB with arbitrary strings
3. Proceed with RBAC check, then DB query
```

**Response envelope format:**

```json
{
  "data": [...],
  "meta": {
    "total": 142,
    "limit": 20,
    "cursor": "eyJpZCI6IjEyMyJ9.<hmac-signature>"
  }
}
```

Cursors are HMAC-signed with the server's `${CURSOR_SECRET}` (defaults to a derived key from `${JWT_SECRET}`). An invalid signature returns 400, not 500, to avoid leaking implementation details.

### Middleware Pipeline

```yaml
middleware:
  global:
    - name: request_id
    - name: logger
    - name: cors
      config:
        allowed_origins: ["https://myapp.com"]
    - name: recover

  # Per-endpoint middleware defined inline in the endpoint yaml
```

**Auth Middleware — JWT flow:**

```
1. Extract Bearer token from Authorization header
2. Reject if Authorization header is missing and endpoint requires auth
3. Enforce that token's "alg" header matches the configured algorithm exactly;
   reject "none" and any algorithm not in the allowlist unconditionally
4. Validate signature with configured secret/public key
5. Validate standard claims: exp (not expired), nbf (not before), iss (if configured)
6. Reject any claim map that contains a role not defined in the policy config
7. Attach parsed principal (subject) to request context
8. Policy middleware runs after — see Section 9
```

**Content-Type enforcement:**

All `POST`, `PUT`, and `PATCH` endpoints reject requests where `Content-Type` is not `application/json`. This prevents CSRF attacks via form-based submissions and catches malformed clients early.

**Request body size:**

Enforced at the server level via `max_request_body_size` (default 4 MB). Applied before any handler or middleware runs to prevent memory exhaustion from large payloads.

**Rate Limit Middleware:**

```yaml
- name: rate_limit
  config:
    strategy: sliding_window
    limit: 100
    window: 1m
    key_by: ip
    store: memory
    # When behind a reverse proxy, use the real IP from a trusted header.
    # Do NOT trust X-Forwarded-For blindly — it can be spoofed.
    # Instead, configure your proxy to set a non-spoofable header:
    trusted_ip_header: X-Real-IP    # only read if request comes from trusted_proxy_cidrs
    trusted_proxy_cidrs: ["10.0.0.0/8", "172.16.0.0/12"]
```

### Request/Response Validation

Yaypi auto-derives a JSON Schema from entity fields and compiles it at startup using `santhosh-tekuri/jsonschema`. For request bodies:
- Required fields: `nullable: false` with no `default`
- String fields with `length` → `maxLength`
- Enum fields → `enum` constraint
- UUID fields → `format: uuid`

---

## 5. Migration System

### Design Philosophy

Migrations in yaypi are **schema-diff driven**: yaypi compares the desired schema (from YAML) against the current DB schema and generates SQL to reconcile them. Migrations are never destructive by default.

### Migration Table

```sql
CREATE TABLE yaypi_migrations (
    id          SERIAL PRIMARY KEY,
    version     VARCHAR(14) NOT NULL UNIQUE,
    name        TEXT NOT NULL,
    checksum    TEXT NOT NULL,
    applied_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    applied_by  TEXT,
    duration_ms INTEGER NOT NULL
);
```

### Migration Generation Algorithm

```
1. Load current DB schema via information_schema + pg_indexes + pg_constraint
2. Load target schema from YAML entity definitions
3. Diff:
   a. New tables → CREATE TABLE
   b. Dropped tables → WARN only (never auto-drop; require explicit config)
   c. New columns → ALTER TABLE ... ADD COLUMN
   d. Dropped columns → WARN only unless allow_destructive
   e. Changed column types → ALTER TABLE ... ALTER COLUMN TYPE (with USING clause)
   f. New indexes → CREATE INDEX CONCURRENTLY
   g. Dropped indexes → DROP INDEX CONCURRENTLY
   h. New constraints → ALTER TABLE ... ADD CONSTRAINT
   i. New enum values → ALTER TYPE ... ADD VALUE
4. Wrap all DDL in a single transaction (except CREATE INDEX CONCURRENTLY)
5. Write migration file: migrations/20260327120000_auto.up.sql
6. Write rollback: migrations/20260327120000_auto.down.sql
```

### Migration File Format

```sql
-- migrations/20260327120000_add_post_status.up.sql
-- yaypi:version 20260327120000
-- yaypi:name add_post_status

BEGIN;

ALTER TABLE posts ADD COLUMN status VARCHAR(20) NOT NULL DEFAULT 'draft';
ALTER TABLE posts ADD CONSTRAINT chk_post_status
    CHECK (status IN ('draft', 'published', 'archived'));

COMMIT;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_posts_status ON posts(status);
```

### Migration Integrity

Every migration file embeds a SHA-256 checksum of its own SQL content in a header comment. Before applying a migration, yaypi recomputes the checksum and aborts if it has changed since the file was generated. This catches accidental edits and detects tampered migration files before they touch the database.

Previously applied migrations are also checksum-verified on every `migrate up` — if a file on disk no longer matches the checksum recorded in `yaypi_migrations`, the run is aborted and the discrepancy is reported.

### CLI Migration Commands

```bash
yaypi migrate generate --name "add_post_status"
yaypi migrate up
yaypi migrate up --steps 1
yaypi migrate down --steps 1
yaypi migrate status
yaypi migrate validate        # dry-run, print SQL without applying
yaypi migrate verify          # re-check all on-disk file checksums against DB records
yaypi migrate squash --up-to 20260327120000
```

---

## 6. Plugin System

### Plugin Interface (SDK)

**Recommendation: Use `hashicorp/go-plugin` with gRPC transport.**
- Crash isolation (plugin crash does not kill yaypi)
- Language agnosticism (gRPC-capable language)
- Versioned interfaces via protobuf

### Plugin YAML Definition

```yaml
# plugins/audit-log/plugin.yaml
version: "1"
kind: plugin

plugin:
  name: audit-log
  version: "1.0.0"
  description: Records all mutations to an audit log table

  capabilities:
    - entity_hooks
    - middleware

  config_schema:
    table:
      type: string
      required: true
      default: audit_events
```

### Plugin SDK (pkg/sdk)

```go
type Plugin interface {
    Info() PluginInfo
    Init(ctx InitContext) error
    Shutdown(ctx context.Context) error
}

type EntityHookPlugin interface {
    Plugin
    BeforeCreate(ctx HookContext, entity string, data map[string]any) (map[string]any, error)
    AfterCreate(ctx HookContext, entity string, record map[string]any) error
    BeforeUpdate(ctx HookContext, entity string, id string, data map[string]any) (map[string]any, error)
    AfterUpdate(ctx HookContext, entity string, record map[string]any) error
    BeforeDelete(ctx HookContext, entity string, id string) error
    AfterDelete(ctx HookContext, entity string, id string) error
}

type MiddlewarePlugin interface {
    Plugin
    Middleware() func(http.Handler) http.Handler
}

type HandlerPlugin interface {
    Plugin
    RegisterHandlers(r HandlerRegistry)
}
```

### Plugin Verification and Transport Security

Before starting a plugin subprocess, yaypi verifies the binary:

```
1. Compute SHA-256 of the plugin binary
2. Compare against checksum declared in yaypi.yaml (startup fails if missing or mismatched)
3. Optionally verify a detached signature file (.sig) against a trusted public key
```

gRPC communication between yaypi and plugin subprocesses uses **mutual TLS** (mTLS). `hashicorp/go-plugin` generates ephemeral self-signed certificates per-session automatically. This prevents a rogue process on the same host from impersonating a plugin over the Unix socket.

Plugin `InitContext` provides a deliberately restricted DB accessor — plugins receive a connection scoped only to the entity's configured database and cannot access other connections by name unless explicitly listed in `allowed_databases` in the plugin config.

### Plugin Lifecycle

```
Startup:
  1. Verify plugin binary checksum (and signature if configured)
  2. Start plugin subprocess via go-plugin (mTLS established automatically)
  3. Establish gRPC connection
  4. Call Plugin.Init() with config (scoped DB accessor provided)
  5. Register entity hooks and middleware

Per-Request:
  1. BeforeCreate hooks run in order; any error aborts with 400/500
  2. INSERT executes
  3. AfterCreate hooks run; errors logged but non-fatal (configurable)

Shutdown:
  1. Drain in-flight requests
  2. Call Plugin.Shutdown() on all plugins
  3. Close gRPC connections; send SIGTERM to subprocesses
```

### Built-in Plugins

| Plugin Name       | Capability                                      |
|-------------------|-------------------------------------------------|
| `yaypi/cors`      | CORS headers middleware                         |
| `yaypi/rate-limit`| Token bucket rate limiting                      |
| `yaypi/audit-log` | AfterCreate/Update/Delete → append-only audit table (no UPDATE/DELETE on audit rows) |
| `yaypi/soft-delete`| Adds deleted_at without schema definition      |
| `yaypi/search`    | `GET /entity/search?q=` via `tsvector`          |
| `yaypi/graphql`   | GraphQL schema from entity definitions          |

---

## 7. Multi-Database Support

### Connection Definition

```yaml
databases:
  - name: primary
    driver: postgres
    dsn: ${DATABASE_URL}
    max_open_conns: 25
    max_idle_conns: 5
    conn_max_lifetime: 5m
    default: true
    options:
      sslmode: require

  - name: analytics
    driver: postgres
    dsn: ${ANALYTICS_URL}
    max_open_conns: 5
    read_only: true

  - name: legacy
    driver: postgres
    dsn: ${LEGACY_DB_URL}
    schema: legacy_schema
```

### Routing Entities to Databases

```yaml
entity:
  name: Event
  table: events
  database: analytics
```

### Transaction Handling

Cross-database transactions are not supported transparently. Single-database transactions work via `*sql.Tx` scoped to the entity's pool. For cross-database atomicity, plugins must implement saga/compensating transactions.

```go
// In a custom handler plugin:
return ctx.DB("primary").WithTx(ctx.Ctx, func(tx sdk.Tx) error {
    _, err := tx.Exec("INSERT INTO orders ...")
    if err != nil { return err }
    _, err = tx.Exec("UPDATE inventory ...")
    return err
})
```

---

## 8. Background Jobs / Cron

### YAML Format

### Schedule Format Reference

The `schedule` field accepts four forms:

| Form | Example | Description |
|---|---|---|
| Standard cron (5-field) | `"0 2 * * *"` | minute hour dom month dow |
| Seconds-precision (6-field) | `"*/30 * * * * *"` | sec min hour dom month dow |
| `@` named shortcuts | `@hourly` | See table below |
| `@every` duration | `@every 15m` | Repeat on a fixed interval |

**Named `@` shortcuts:**

| Shortcut | Equivalent cron | Runs |
|---|---|---|
| `@yearly` / `@annually` | `0 0 1 1 *` | Once a year, Jan 1 at midnight |
| `@monthly` | `0 0 1 * *` | Once a month, 1st at midnight |
| `@weekly` | `0 0 * * 0` | Once a week, Sunday at midnight |
| `@daily` / `@midnight` | `0 0 * * *` | Once a day at midnight |
| `@hourly` | `0 * * * *` | Once an hour, on the hour |
| `@minutely` | `* * * * *` | Every minute |

All `@` shortcuts and `@every` durations are resolved via `robfig/cron` (which `go-co-op/gocron` wraps), so no additional parsing logic is required.

**`@every` duration units:** `ns`, `us`, `ms`, `s`, `m`, `h` — combinable: `@every 1h30m`.

> **Note on `@every` vs cron expressions**: `@every` starts counting from server startup, not from a clock boundary. `@hourly` fires at `:00` every hour regardless of when the server started. Prefer `@hourly` over `@every 1h` when alignment to the clock matters.

```yaml
# jobs/cleanup.yaml
version: "1"
kind: jobs

jobs:
  - name: expire_pending_users
    description: Delete users stuck in pending status for over 30 days
    schedule: "0 2 * * *"       # 2:00 AM daily (5-field cron)
    timezone: UTC
    handler: sql
    config:
      database: primary
      # SQL handler restriction: only DML (SELECT/INSERT/UPDATE/DELETE) is permitted.
      # DDL (CREATE/DROP/ALTER), COPY, and multiple statements are rejected at startup.
      # Queries may not contain dynamic interpolation — only static SQL is allowed.
      query: |
        DELETE FROM users
        WHERE status = 'pending'
          AND created_at < NOW() - INTERVAL '30 days'
    retry:
      max_attempts: 3
      backoff: exponential
      initial_delay: 5s
      max_delay: 5m
    timeout: 10m
    on_failure: log

  - name: generate_analytics_report
    schedule: "@weekly"          # named shortcut — every Sunday at midnight
    timezone: America/New_York
    handler: plugin
    plugin: analytics-reporter
    config:
      report_type: weekly
    retry:
      max_attempts: 1
    timeout: 30m

  - name: clear_expired_sessions
    schedule: "@hourly"          # named shortcut — top of every hour
    timezone: UTC
    handler: sql
    config:
      database: primary
      query: "DELETE FROM sessions WHERE expires_at < NOW()"
    timeout: 1m

  - name: sync_external_data
    schedule: "@every 15m"       # @every duration — 15m after each prior run completes
    handler: http
    config:
      url: ${EXTERNAL_SYNC_URL}
      method: POST
      headers:
        Authorization: "Bearer ${SYNC_TOKEN}"
      expected_status: 200
      # SSRF protection: restrict the HTTP handler to an allowlist of target hosts.
      # If allowed_hosts is omitted, the job is rejected at startup with a validation error.
      # Internal RFC-1918 addresses (10.x, 172.16.x, 192.168.x) and loopback are always blocked.
      allowed_hosts: ["api.example.com"]
      timeout: 10s               # per-request timeout for the HTTP call itself
    retry:
      max_attempts: 5
      backoff: linear
      initial_delay: 30s

  - name: health_ping
    schedule: "@every 30s"       # sub-minute interval using @every
    handler: http
    config:
      url: ${HEALTHCHECK_URL}
      method: GET
      expected_status: 200
    timeout: 5s
```

### Execution Engine

**Recommended library: `go-co-op/gocron` v2**

```
Startup:
  1. Parse job definitions from YAML
  2. Initialize gocron.Scheduler with timezone
  3. Register each job with its handler

Job Execution:
  1. Acquire distributed lock (if Redis configured)
  2. Insert job_runs record with status=running
  3. Execute handler with context.WithTimeout
  4. On success: update status=success, duration
  5. On failure: update status=failed, apply retry backoff
  6. Release distributed lock
```

### Job Runs Table

```sql
CREATE TABLE yaypi_job_runs (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    job_name     TEXT NOT NULL,
    status       TEXT NOT NULL,
    started_at   TIMESTAMPTZ NOT NULL,
    finished_at  TIMESTAMPTZ,
    duration_ms  INTEGER,
    error        TEXT,
    attempt      INTEGER NOT NULL DEFAULT 1,
    triggered_by TEXT NOT NULL DEFAULT 'scheduler'
);

CREATE INDEX idx_job_runs_job_name_started ON yaypi_job_runs(job_name, started_at DESC);
```

---

## 9. Authorization: RBAC

> **Note — ABAC not included in v1.** Attribute-Based Access Control (evaluating conditions against the resource record — e.g. "editor can only update their own posts") requires fetching the resource from the database *before* the authorization check runs. For write operations this is a mandatory extra round-trip; for list operations it requires SQL injection into the WHERE clause with author-supplied fragments. Both introduce meaningful complexity and latency overhead that aren't justified at this stage. ABAC is a natural future extension once the RBAC layer is proven in production. Fine-grained ownership rules should be handled in custom plugin handlers for now.

### Overview

Yaypi uses **Role-Based Access Control (RBAC)**: a user's role (e.g. `admin`, `editor`) grants or denies access to an endpoint and action. Checks run entirely against data already present in the JWT — no DB query is needed for authorization on the hot path.

**Recommended engine: Casbin v2.** Go-native, supports role inheritance, persists policies to a DB adapter, and evaluates in microseconds.

### Policy Engine Architecture

```
JWT Middleware → parse token → attach Subject (id, role) to context
                                        │
                                        ▼
                         PolicyMiddleware (runs per-request)
                                        │
                                   RBAC check
                             casbin.Enforce(role, resource, action)
                                        │
                                   allow / deny
                                        │
                              Handler runs (or 403)
```

### Casbin Model Definition

```ini
# policies/model.conf

[request_definition]
r = sub, obj, act

[policy_definition]
p = sub, obj, act

[role_definition]
g = _, _          # user → role assignment

[policy_effect]
e = some(where (p.eft == allow))

[matchers]
m = g(r.sub, p.sub) && r.obj == p.obj && r.act == p.act
```

### RBAC Policy Definition (YAML)

```yaml
# policies/roles.yaml
version: "1"
kind: policy

roles:
  - name: admin
    inherits: [editor]        # role inheritance: admin gets all editor permissions too
    permissions:
      - { resource: Post,    actions: [list, get, create, update, delete, publish] }
      - { resource: User,    actions: [list, get, create, update, delete] }
      - { resource: Tag,     actions: [list, get, create, update, delete] }
      - { resource: "system", actions: [migrate, trigger_job, reload_policy] }

  - name: editor
    inherits: [viewer]
    permissions:
      - { resource: Post,    actions: [list, get, create, update] }
      - { resource: Tag,     actions: [list, get] }

  - name: viewer
    permissions:
      - { resource: Post,    actions: [list, get] }
      - { resource: Tag,     actions: [list, get] }

  - name: service_account
    allow_wildcard: true      # required to use wildcard resource
    permissions:
      - { resource: "*",     actions: [list, get] }   # read-only across all resources
```

Yaypi loads this YAML and writes the corresponding Casbin `p` and `g` lines to the policy adapter at startup (or syncs them on reload if using the DB adapter).

### JWT Claims Mapping

The JWT payload is mapped to a `Subject` struct via a configurable claims map, allowing yaypi to work with tokens from external providers (Auth0, Keycloak, Cognito):

```yaml
# yaypi.yaml
auth:
  provider: jwt
  secret: ${JWT_SECRET}
  algorithm: HS256
  reject_algorithms: [none, RS256, ES256]
  claims_map:
    subject_id: sub           # JWT claim name → Subject field
    role: role
    email: email
```

```go
// internal/policy/subject.go
type Subject struct {
    ID    string
    Role  string
    Email string
}
```

Claims not in the map are discarded. Any JWT that carries a `role` value not defined in `policies/roles.yaml` is rejected at the auth middleware layer with 401 — it cannot reach the policy check.

### Policy Evaluation Flow (per request)

```
1. Extract Subject from JWT context (claims_map applied)
2. Determine action from HTTP method:
   GET → get, POST → create, PATCH/PUT → update, DELETE → delete
3. RBAC check:
   a. casbin.Enforce(subject.Role, entityName, action)
   b. If deny → 403 immediately, no DB query executed
4. Handler runs
```

Authorization never touches the database. All necessary information (role, entity name, action) is available from the JWT and the request itself.

### Policy Persistence and Hot-Reload

**File adapter** (default for development): policies are read from `policies/roles.yaml` at startup. Reload requires restart.

**DB adapter** (recommended for production): policies are stored in `yaypi_policies` via Casbin's DB adapter. A `POST /system/policies/reload` endpoint (requires `system` role) reloads policies in-process without restart. The reload endpoint logs a before/after diff of changed rules to the audit log.

```sql
-- Auto-created by yaypi migrate up when policy.adapter = db
CREATE TABLE yaypi_policies (
    id    BIGSERIAL PRIMARY KEY,
    ptype TEXT NOT NULL,
    v0    TEXT, v1 TEXT, v2 TEXT, v3 TEXT, v4 TEXT, v5 TEXT
);
```

### Endpoint-Level Auth Declaration

```yaml
endpoints:
  - path: /posts/{id}
    entity: Post
    crud: [update, delete]

    update:
      allowed_fields: [title, body, published_at]
      auth:
        require: true
        roles: [admin, editor]   # RBAC: only these roles may call this action

    delete:
      auth:
        require: true
        roles: [admin]
```

If `auth.require: true` is set but `roles` is omitted, any authenticated user is allowed. If `auth.require: false`, the endpoint is public (no JWT required). Yaypi emits a startup warning for any endpoint that has `auth.require: true` but no role in the policy config covers the entity+action combination.

---

## 10. Developer Experience

### CLI Tool

Built with `spf13/cobra`:

```
yaypi init [project-name]        # scaffold new project
yaypi validate                   # validate all YAML files + policies
yaypi generate                   # generate Go types / OpenAPI spec
yaypi run                        # start the server (development mode)
yaypi run --env production
yaypi migrate generate
yaypi migrate up
yaypi migrate down --steps 1
yaypi migrate status
yaypi plugin new [name]
yaypi policy check               # test policy rules against example inputs
yaypi docs                       # open OpenAPI docs in browser
yaypi job run [name]
yaypi health
```

### `yaypi policy check` — Policy Testing CLI

```bash
$ yaypi policy check \
    --subject '{"id":"user-1","role":"editor"}' \
    --resource Post \
    --action update

Evaluating RBAC: editor → update on Post → PASS

Result: 200 OK (RBAC allows; no further checks in v1)
```

### `yaypi init` Scaffold Output

```bash
$ yaypi init blog-api
  create blog-api/yaypi.yaml
  create blog-api/entities/user.yaml
  create blog-api/entities/post.yaml
  create blog-api/endpoints/auth.yaml
  create blog-api/endpoints/posts.yaml
  create blog-api/policies/model.conf
  create blog-api/policies/roles.yaml
  create blog-api/.env.example
  create blog-api/.gitignore
```

### `yaypi validate` Output

```bash
$ yaypi validate
Validating 9 YAML files...

ERROR  policies/roles.yaml:22   Role 'superuser' is referenced in endpoints/admin.yaml
                                but is not defined in policies/roles.yaml.
WARN   endpoints/posts.yaml:12  Middleware 'throttle' is not registered.

2 issues found (1 error, 1 warning)
```

### Hot Reload in Development

Use `cosmtrek/air`. When YAML changes, air restarts yaypi. This is preferred over runtime YAML watching because config changes often affect schema.

```toml
# .air.toml
[build]
  cmd = "yaypi validate && yaypi run"
  include_ext = ["yaml", "yml"]
  exclude_dir = ["migrations"]
```

### OpenAPI Generation

`yaypi generate` produces `openapi.yaml` including:
- Paths from endpoint YAML
- Schemas from entity YAML
- Security schemes from auth + policy config

Served at `GET /docs/openapi.yaml` and rendered via embedded Swagger UI at `GET /docs`.

### Debugging

- Every request has `X-Request-ID`
- Structured JSON logging via `rs/zerolog`
- In development: SQL logging with duration, policy evaluation trace logs
- `GET /system/info` (dev only): loaded entities, routes, plugins, DB status, active policies
- Policy evaluation reason included in 403 response body in development mode:
  ```json
  { "error": "Forbidden", "debug": { "denied_by": "editor_owns_posts", "condition": "resource.author_id == subject.id → false" } }
  ```

---

## 11. End-to-End Flow Example

### Scenario: Blog API with Users, Posts, and Tags

#### Step 1 — Initialize

```bash
yaypi init blog-api && cd blog-api
cp .env.example .env
```

#### Step 2 — Define Entities (abbreviated)

```yaml
# entities/user.yaml
entity:
  name: User
  table: users
  timestamps: true
  soft_delete: true
  fields:
    - { name: id, type: uuid, primary_key: true, default: gen_random_uuid() }
    - { name: email, type: string, length: 255, unique: true, nullable: false }
    - { name: password_hash, type: string, length: 60, nullable: false, serialization: { omit_response: true } }
    - { name: display_name, type: string, length: 100, nullable: false }
    - { name: role, type: enum, values: [admin, editor, viewer], default: viewer }
    - { name: status, type: enum, values: [active, suspended, pending], default: pending }
```

```yaml
# entities/post.yaml
entity:
  name: Post
  table: posts
  timestamps: true
  fields:
    - { name: id, type: uuid, primary_key: true, default: gen_random_uuid() }
    - name: author_id
      type: uuid
      nullable: false
      references: { entity: User, field: id, on_delete: CASCADE }
    - { name: title, type: string, length: 512, nullable: false }
    - { name: body, type: text, nullable: false }
    - { name: status, type: enum, values: [draft, published, archived], default: draft }
    - { name: published_at, type: timestamptz, nullable: true }
  relations:
    - { name: author, type: belongs_to, entity: User, foreign_key: author_id }
    - { name: tags, type: many_to_many, entity: Tag, through: PostTag, foreign_key: post_id, other_key: tag_id }
```

#### Step 3 — Define Policies

```yaml
# policies/roles.yaml
roles:
  - name: admin
    inherits: [editor]
    permissions:
      - { resource: Post, actions: [list, get, create, update, delete, publish] }
      - { resource: User, actions: [list, get, create, update, delete] }

  - name: editor
    inherits: [viewer]
    permissions:
      - { resource: Post, actions: [list, get, create, update] }

  - name: viewer
    permissions:
      - { resource: Post, actions: [list, get] }
```

```yaml
# policies/rules.yaml
rules:
  - name: editor_owns_posts
    resource: Post
    actions: [update, delete]
    condition: "role == 'editor' implies resource.author_id == subject.id"

  - name: no_suspended
    resource: "*"
    actions: [create, update, delete]
    condition: "subject.status != 'suspended'"
```

#### Step 4 — Define Endpoints

```yaml
# endpoints/posts.yaml
endpoints:
  - path: /posts
    entity: Post
    crud: [list, create]
    middleware: [authenticate]
    list:
      auth: { require: false }
    create:
      auth: { require: true, roles: [admin, editor] }

  - path: /posts/{id}
    entity: Post
    crud: [get, update, delete]
    middleware: [authenticate]
    get:
      auth: { require: false }
    update:
      allowed_fields: [title, body, status, published_at]
      auth: { require: true, roles: [admin, editor] }
    delete:
      auth: { require: true, roles: [admin] }
```

#### Step 5 — Validate and Migrate

```bash
$ yaypi validate
All files valid. 4 entities, 6 endpoints, 3 roles registered.

$ yaypi migrate generate --name initial_schema
$ yaypi migrate up
Applying 20260327120000_initial_schema ... done (342ms)
```

#### Step 6 — Start and Test

```bash
$ yaypi run
2026-03-27T12:00:00Z INF connected to database name=primary
2026-03-27T12:00:00Z INF policy engine loaded roles=3
2026-03-27T12:00:00Z INF router built routes=6
2026-03-27T12:00:00Z INF server listening addr=:8080
```

```bash
# Viewer tries to create a post → 403 (RBAC: viewer lacks 'create' on Post)
POST /api/v1/posts
Authorization: Bearer <viewer-token>
→ 403 Forbidden

# Editor creates a post → 201
POST /api/v1/posts
Authorization: Bearer <editor-token>
→ 201 Created

# Admin deletes any post → 200
DELETE /api/v1/posts/post-123
Authorization: Bearer <admin-token>
→ 200 OK

# Unauthenticated read → 200 (auth.require: false on get)
GET /api/v1/posts/post-123
→ 200 OK
```

#### Step 7 — Runtime Transformation Summary

```
YAML (entities + endpoints + policies/roles.yaml)
       │
       ▼  Config Loader (gopkg.in/yaml.v3)
       │
       ▼  Schema Registry (entity graph, relation resolution)
       │
       ▼  Policy Engine (Casbin RBAC — role → resource → action)
       │
       ▼  Query Builder (parameterized SQL templates at startup)
       │
       ▼  Handler Factory (HandlerFunc closures per endpoint)
       │
       ▼  Router Builder (chi.Router, path params registered)
       │
       ▼  Middleware: request_id → logger → JWT auth → RBAC → rate_limit → validate
       │
  HTTP Request arrives
       │
       ▼  JWT parsed → Subject{id, role} built → RBAC check (no DB query)
       │
       ▼  Handler: parse params → validate path params → build SQL → execute → serialize → respond
```

---

## 12. Risks and Tradeoffs

This section catalogs every identified risk across the system, its severity, and its concrete mitigation. Risks are grouped by domain.

---

### Performance

**Risk — Dynamic query overhead at request time.**
The query builder runs per-request rather than being compiled to static code.
**Mitigation**: SQL query templates (with positional `$N` placeholders) are constructed and validated at startup, not per-request. Per-request work is parameter binding only. Prepared statements are registered with `pgxpool` at startup and reused. Target overhead: ≤0.5ms vs hand-written handlers for simple CRUD.

**Risk — N+1 queries for eager-loaded relations.**
`include: [author, tags]` naively issues one query per record.
**Mitigation**: The query builder batches all relation lookups using `WHERE id = ANY($1::uuid[])` after collecting all foreign key values from the primary result set. One follow-up query per included relation, not per row.

**Risk — Plugin gRPC serialization overhead per hook call.**
Every entity hook crosses a process boundary via gRPC.
**Mitigation**: Hook registration is opt-in per-entity. If no plugin registers a hook for a given entity+action, the dispatcher short-circuits with zero overhead. Document the per-call cost (~0.1–0.5ms on loopback) so authors can make informed decisions about which hooks to register.

---

### Security: Injection

**Risk — SQL injection via dynamic query building.**
Filter values or column names might be interpolated directly.
**Mitigation**: Filter and sort column names are validated at startup against the entity's `allow_filter_by` / `allow_sort_by` whitelists — only pre-approved identifiers are ever used as SQL identifiers, never from raw user input. All filter values are bound as `$N` parameters; never interpolated. Query templates use `pgx`'s native parameterization which is enforced at the protocol level.

**Risk — SQL injection via cron `sql` handler queries.**
Job YAML files contain raw SQL executed on a schedule.
**Mitigation**: At startup, the SQL handler validates each configured query using a parser (e.g., `pganalyze/pg_query_go`) to confirm it is a single DML statement (SELECT/INSERT/UPDATE/DELETE only). DDL (CREATE, DROP, ALTER), COPY, and multi-statement batches are rejected. Queries must be static — no string interpolation syntax is supported.

**Risk — SSRF via cron `http` handler.**
A job configured with an internal URL could probe the host network.
**Mitigation**: The HTTP handler requires an explicit `allowed_hosts` allowlist in the job config; if absent the job is rejected at startup. RFC-1918 addresses (`10.x`, `172.16.x`, `192.168.x`), loopback (`127.x`, `::1`), link-local, and metadata service ranges (e.g., `169.254.169.254`) are always blocked at the DNS resolution layer, regardless of the allowlist. DNS rebinding is mitigated by validating the resolved IP again after DNS lookup.

**Risk — Mass assignment on entity create/update.**
A client sends undeclared fields, potentially overwriting internal columns.
**Mitigation**: `create` accepts only fields explicitly declared in the entity definition. `update` additionally requires an `allowed_fields` whitelist — any field not on the whitelist is silently stripped before SQL is built. Internal fields (`id`, `created_at`, `updated_at`, `deleted_at`) are always stripped regardless of whitelist.

---

### Security: Authentication & Authorization

**Risk — JWT algorithm confusion attack.**
An attacker submits a token with `"alg": "none"` or switches from asymmetric to symmetric.
**Mitigation**: The JWT middleware enforces the configured algorithm exactly using an allowlist check on the token header before signature verification. `none` is unconditionally rejected at the library level (`golang-jwt/jwt` v5 with `WithValidMethods`). Other algorithms can be explicitly blocked via `reject_algorithms` in config.

**Risk — JWT token not validated for expiry or `nbf`.**
Expired or future-dated tokens are accepted.
**Mitigation**: `exp`, `nbf`, and `iat` claims are validated on every request. Clock skew tolerance is configurable (default: 30 seconds). There is no way to disable expiry validation.

**Risk — No JWT revocation.**
A stolen token remains valid until expiry.
**Mitigation**: Short expiry (`24h` default, configurable down to minutes). For revocation, the `yaypi/token-denylist` built-in plugin (opt-in) stores revoked `jti` claims in Redis with TTL equal to token expiry. This adds one Redis read per authenticated request — only enable if the threat model requires it.

**Risk — Wildcard `resource: "*"` in RBAC grants too broad access.**
A `service_account` role with `resource: "*"` may accidentally cover sensitive entities added later.
**Mitigation**: Wildcard resource grants emit a startup warning and require an explicit `allow_wildcard: true` flag on the role definition to be accepted. Auditors can grep for `allow_wildcard: true` to enumerate all wildcard grants.

**Risk — Eager-loaded relations bypass RBAC on the related entity.**
`include: [author]` on a Post endpoint loads User records without checking if the caller has `get` on `User`.
**Mitigation**: Each relation in an `include` list is checked against RBAC for the related entity's type and action (`get`). If the role lacks permission, the relation field is omitted from the response rather than returning a 403 (to avoid leaking existence). Configurable via `include_on_unauthorized: omit | error`.

**Risk — Pagination cursors leak internal record ordering or IDs.**
A base64 cursor like `eyJpZCI6IjEyMyJ9` is trivially decodable.
**Mitigation**: Cursors are HMAC-signed with a server secret derived from `${JWT_SECRET}` (or an independent `${CURSOR_SECRET}`). A cursor with an invalid signature returns 400. The cursor payload encodes a position (e.g., `{id, created_at}`) but not the total count or other metadata. Cursors are opaque to clients.

**Risk — Path parameter type confusion.**
`GET /posts/'; DROP TABLE posts; --` reaches the DB query before validation.
**Mitigation**: Path parameters are validated against the primary key type of the entity before any DB interaction. UUID primary keys validate the UUID format and return 400 on mismatch. Integer keys validate numeric format. This is enforced in the router's middleware chain, not inside handlers.

---

### Security: Secrets & Configuration

**Risk — Secrets hardcoded in YAML files.**
A DSN or JWT secret written literally in `yaypi.yaml` ends up in version control.
**Mitigation**: At startup, yaypi scans all resolved YAML values. Any value for a key matching `*secret*`, `*password*`, `*token*`, `*key*`, or `*dsn*` that is not an `${ENV_VAR}` reference triggers a startup error in production mode (`YAYPI_ENV=production`) and a warning in development. This cannot be suppressed without an explicit `allow_plaintext_secrets: true` flag, which is itself logged as a warning.

**Risk — DSN credentials logged on connection failure.**
A failed DB connect might log the full DSN including password.
**Mitigation**: The DB manager stores DSNs in a `SecretString` type that implements `fmt.Stringer` by returning a redacted form (`postgres://user:***@host/db`). The raw value is never passed to zerolog or any fmt function.

**Risk — Sensitive field values written to access logs.**
Request bodies containing `password` fields are logged verbatim.
**Mitigation**: Fields with `serialization.omit_log: true` are scrubbed from the request body before it is written to the structured log. Yaypi auto-detects fields named `password*`, `secret*`, `token*` and sets `omit_log: true` by default unless explicitly overridden. Response bodies are never logged (only status codes and sizes).

**Risk — Debug endpoints expose internals in production.**
`GET /system/info` and `GET /docs` leak entity schema and route structure.
**Mitigation**: System endpoints are disabled by default. Enabling them requires `system.enabled: true` in config and always requires auth with the `system` role. The `GET /system/info` response is never cached by the browser or intermediate proxies (`Cache-Control: no-store`).

---

### Security: Plugins

**Risk — Malicious or tampered plugin binary.**
An attacker replaces a plugin binary with a backdoored version.
**Mitigation**: Plugin binaries must declare a SHA-256 `checksum` in `yaypi.yaml`. Yaypi verifies the binary against this checksum before spawning the subprocess; startup fails on mismatch. Optionally, a detached `.sig` file can be verified against a trusted Ed25519 public key (`plugin.trusted_public_key` in config).

**Risk — Plugin subprocess intercepts gRPC communication from another process.**
A rogue process on the same host claims to be a plugin.
**Mitigation**: `hashicorp/go-plugin` generates ephemeral per-session mTLS certificates. The plugin binary receives a magic cookie via an environment variable; the gRPC listener is bound to a random local port known only to the parent. A rogue process cannot connect without the magic cookie.

**Risk — Plugin accesses DB connections beyond its scope.**
A plugin's `HookContext` could be used to query sensitive databases.
**Mitigation**: `HookContext.DB` is a restricted accessor scoped to the entity's configured database. Accessing other named connections requires explicit declaration in `plugin.allowed_databases`. The DB accessor only exposes query/exec methods — it cannot reconfigure the connection or access connection internals.

---

### Security: Operations & Deployment

**Risk — Destructive migrations applied automatically in production.**
`yaypi migrate up` with a DROP COLUMN drops live data.
**Mitigation**: Destructive operations (DROP TABLE, DROP COLUMN, column type narrowing) are detected by the diff engine and excluded from auto-generated migrations by default. They appear as comments in the migration file prefixed with `-- DESTRUCTIVE:`. Applying them requires adding an explicit `-- yaypi:allow-destructive` annotation to the file AND passing `--allow-destructive` to `migrate up`.

**Risk — Migration files tampered after generation.**
A developer edits a migration file after the checksum was recorded.
**Mitigation**: Each migration file embeds its own SHA-256 in a header comment. `migrate up` recomputes and compares before execution. `migrate verify` checks all applied migrations on disk against DB-recorded checksums. A mismatch aborts with an error identifying the offending file.

**Risk — Policy changes take effect without audit trail.**
An admin updates a policy rule via the reload API with no record of what changed.
**Mitigation**: `POST /system/policies/reload` records the old and new policy state to the audit log table (if `yaypi/audit-log` is enabled) and requires the `system` role. Policy changes via DB adapter are subject to whatever audit controls the DB itself provides (WAL, pgaudit). The API returns a diff of changed rules in the response body.

**Risk — Cron jobs running concurrently on multiple instances cause duplicate work or data corruption.**
Two instances of yaypi both run `expire_pending_users` at 2 AM.
**Mitigation**: Distributed locking via Redis (configurable). If Redis is not configured and multiple instances are detected at startup (via DB heartbeat table), yaypi logs a warning on any job that does not declare `allow_concurrent: true`. The lock TTL is set to the job's `timeout` value plus a 10% buffer; a job that exceeds its timeout releases the lock automatically.

---

### Complexity vs Flexibility

**Risk — YAML is not Turing-complete; complex logic requires plugins.**
A developer tries to express a conditional workflow in YAML and hits a wall.
**Mitigation**: The boundary is explicit and documented: YAML handles schema, routing, auth rules, and simple hook registration. Anything requiring loops, conditionals, or external calls beyond a simple HTTP request uses a plugin. Scaffold: `yaypi plugin new` generates a working plugin stub in under 30 seconds.

**Risk — RBAC is too coarse for some use cases (e.g., per-record ownership).**
RBAC alone cannot express "editors can only edit their own posts" without custom code.
**Mitigation**: This is a known v1 limitation. Per-record rules belong in custom plugin handlers for now. ABAC is the planned v2 extension — deferred because it requires a DB round-trip per write request to fetch the resource before the authorization check, which adds latency and implementation complexity that isn't warranted yet. See the note at the top of Section 9.

**Risk — YAML schema evolution breaks existing projects on upgrade.**
A new yaypi version changes a YAML key name or removes a field.
**Mitigation**: Every YAML file declares `version: "1"`. Breaking schema changes require a version bump. Yaypi ships a compatibility shim for the previous major version. `yaypi validate` detects files using deprecated keys and prints migration instructions. The published `yaypi.schema.json` is versioned and follows semver.

---

## 13. Suggested Tech Stack

### Core

| Concern | Library | Rationale |
|---|---|---|
| HTTP routing | `go-chi/chi` v5 | Lightweight, stdlib-compatible, excellent middleware support |
| YAML parsing | `gopkg.in/yaml.v3` | De-facto standard; AST with position info for error reporting |
| PostgreSQL driver | `jackc/pgx` v5 | Best-in-class; native protocol, prepared statements |
| SQL pool | `jackc/pgx/v5/pgxpool` | Included with pgx; tunable, context-aware |
| Structured logging | `rs/zerolog` | Zero-alloc, JSON output |
| CLI | `spf13/cobra` | Standard for Go CLIs |
| Config/env | `spf13/viper` | Env vars, multiple files, defaults |
| Validation | `go-playground/validator` v10 | Struct-level validation |
| JSON Schema | `santhosh-tekuri/jsonschema` v6 | YAML structural validation |
| JSON encoding | `bytedance/sonic` | 2-3x faster than stdlib for large payloads |
| UUID | `google/uuid` | Standard |

### Authorization & Security

| Concern | Library | Rationale |
|---|---|---|
| Policy engine | `casbin/casbin` v2 | RBAC with role inheritance; DB adapter; microsecond eval |
| DB policy adapter | `casbin/xorm-adapter` or `casbin/gorm-adapter` | Persistent policy storage |
| JWT parsing | `golang-jwt/jwt` v5 | `WithValidMethods` enforces algorithm allowlist |
| SQL query parsing | `pganalyze/pg_query_go` | Parse and validate cron SQL queries at startup |
| HMAC cursor signing | stdlib `crypto/hmac` + `crypto/sha256` | Opaque, tamper-proof pagination cursors |
| Rate limit store | `redis/go-redis` v9 (optional) | Distributed rate limiting across instances |

### Plugins and Jobs

| Concern | Library | Rationale |
|---|---|---|
| Plugin host | `hashicorp/go-plugin` | Subprocess isolation + gRPC |
| gRPC | `google.golang.org/grpc` | Required by go-plugin |
| Cron scheduler | `go-co-op/gocron` v2 | Context support, distributed locking |
| Distributed cron lock | `redis/go-redis` v9 (optional) | For multi-instance deployments |

### Development and Observability

| Concern | Library | Rationale |
|---|---|---|
| Hot reload | `cosmtrek/air` | Zero yaypi dependency; watches files |
| Metrics | `prometheus/client_golang` | Standard; opt-in plugin |
| Tracing | `open-telemetry/opentelemetry-go` | Trace IDs on DB queries + HTTP spans |
| Testing | `testify/suite` + `ory/dockertest` | Real Postgres containers for integration tests |

### What to Avoid

- **GORM**: Too much magic, N+1 footguns, poor performance at scale. Use `pgx` + thin query builder.
- **`database/sql` with `lib/pq`**: `pgx` is strictly superior; `pq` is in maintenance mode.
- **`gin` or `echo`**: More opinionated than needed; `chi` is a better fit.
- **Go `plugin` package (`.so`)**: Fragile; requires identical Go version + compile flags. Use `hashicorp/go-plugin`.
- **OPA** (for the default case): Rego is powerful but adds significant operational complexity. Casbin covers 95% of use cases with far less overhead. OPA is appropriate if you need a centralized, language-agnostic policy service — implement it as a plugin.
