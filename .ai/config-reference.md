# Complete Config Field Reference

Every YAML field accepted by yayPi, organized by file kind.

---

## `yaypi.yaml` — root config

```yaml
version: "1"                    # required
project:
  name: string                  # project name; used in logs and default OpenAPI tags
  base_url: /api/v1             # URL prefix for all routes

server:
  port: 8080
  read_timeout: 30s
  write_timeout: 30s
  shutdown_timeout: 10s
  max_request_body_size: 4MB
  max_header_bytes: 1MB
  allowed_origins: []           # CORS origins; ["*"] = allow all
  tls:
    cert_file: string
    key_file: string
  health:
    enabled: true               # mount liveness + readiness endpoints
    path: /health               # GET /health → 200 always (liveness)
    readiness_path: /ready      # GET /ready  → 200 if all DBs reachable, 503 otherwise
  rate_limit:
    requests_per_minute: 60     # token bucket fill rate
    burst: 20                   # bucket capacity (allows short bursts above the rate)
    key_by: ip                  # ip (default) | user (JWT sub claim)

databases:
  - name: primary               # logical name; referenced by entity.database
    driver: postgres            # postgres/postgresql | mysql/mariadb | sqlite/sqlite3
    dsn: ${DATABASE_URL}        # Postgres: URL; MySQL: user:pass@tcp(host)/db; SQLite: path or :memory:
    max_open_conns: 25
    max_idle_conns: 5
    conn_max_lifetime: 1h
    default: true               # used when entity has no database: field
    read_only: false            # disallow writes on this connection
    schema: public              # PostgreSQL schema name

auth:
  provider: jwt
  secret: ${JWT_SECRET}         # HMAC signing secret — always use env var
  algorithm: HS256              # HS256 | HS384 | HS512
  reject_algorithms: [none]     # always include "none"
  expiry: 24h                   # informational; exp claim is always validated
  api_keys:
    header: X-API-Key           # request header to check (default: X-API-Key)
    query_param: api_key        # optional: also accept ?api_key= query param
    keys:                       # static key list (use this OR entity below, not both)
      - key: ${ADMIN_API_KEY}
        role: admin
      - key: ${READONLY_API_KEY}
        role: readonly
    entity: ApiKey              # DB-backed: entity name containing key records
    key_field: token            # column holding the key value (default: token)
    role_field: role            # column holding the role (default: role)

policy:
  engine: casbin
  model: ./policies/model.conf
  adapter: file                 # file | database
  adapter_table: casbin_rules   # table name when adapter: database

auto_migrate: false             # apply schema diff at startup (dev only)

plugins:
  - name: string
    path: ./plugins/myplugin
    checksum: sha256:abc123     # optional integrity check
    config: {}                  # arbitrary plugin-specific config

include:
  - entities/**/*.yaml
  - endpoints/**/*.yaml
  - policies/**/*.yaml
  - jobs/**/*.yaml
  - seeds/**/*.yaml             # kind: seed files
  - emails/**/*.yaml            # kind: email files
  - webhooks/**/*.yaml          # kind: webhooks files

spec:                           # named OpenAPI specs
  - name: api                   # used in URL: /openapi/api.json
    title: string
    description: string
    version: "1.0.0"
    servers:
      - url: https://api.example.com
        description: Production
```

---

## Entity YAML (`kind: entity`)

```yaml
version: "1"
kind: entity
entity:
  name: Post                    # PascalCase; used as entity identifier everywhere
  table: posts                  # snake_case table name (defaults to pluralized name)
  database: primary             # which database: entry to use (defaults to default: true)
  timestamps: true              # auto-adds created_at, updated_at (timestamptz)
  soft_delete: true             # auto-adds deleted_at; DELETE sets it instead of removing row

  fields:
    - name: id
      type: uuid                # uuid|string|text|integer|bigint|float|decimal|boolean|
                                # timestamptz|date|jsonb|enum|array|bytea
      primary_key: true
      default: gen_random_uuid()

    - name: title
      type: string
      length: 255               # for string/varchar — maps to VARCHAR(N)
      nullable: false
      unique: false
      index: false              # creates a single-column index
      default: string           # SQL default expression
      immutable: true           # once set, this field cannot be changed via PATCH

    - name: email
      type: string
      validate:
        required: true          # field must be present and non-empty
        min_length: 3           # minimum string length
        max_length: 255         # maximum string length
        pattern: "^[^@]+@[^@]+$"  # regex the value must match
        format: email           # built-in format: email | url | uuid | slug
        message: "must be a valid email address"  # custom error message

    - name: age
      type: integer
      validate:
        min: 18                 # numeric minimum (inclusive)
        max: 120                # numeric maximum (inclusive)
        message: "must be between 18 and 120"

    - name: price
      type: decimal
      precision: 10             # total digits
      scale: 2                  # decimal places

    - name: status
      type: enum
      values: [draft, published, archived]   # CHECK constraint values

    - name: author_id
      type: uuid
      references:
        entity: User            # entity name (not table name)
        field: id
        on_delete: CASCADE      # CASCADE | SET NULL | RESTRICT | NO ACTION
        on_update: NO ACTION

    - name: password_hash
      type: string
      serialization:
        omit_response: true     # never returned in API responses
        omit_log: true          # never logged

    - name: internal_notes
      type: text
      access:                   # ABAC: field-level access control (opt-in)
        read_roles: [admin, editor]   # roles that may see this field; omit = unrestricted
        write_roles: [admin]          # roles that may set this field; omit = unrestricted

  relations:                    # eager-loadable joins (used by include: in endpoints)
    - name: author
      type: belongs_to          # belongs_to | has_many | has_one | many_to_many
      entity: User
      foreign_key: author_id

    - name: tags
      type: many_to_many
      entity: Tag
      through: PostTag          # junction entity name
      foreign_key: post_id
      other_key: tag_id

    - name: comments
      type: has_many
      entity: Comment
      foreign_key: post_id

  indexes:
    - name: idx_posts_slug
      columns: [slug]
      unique: true
      type: btree               # btree | brin | hash (Postgres); ignored on SQLite/MySQL

  constraints:
    - name: pk_post_tags
      type: primary_key         # primary_key | unique | check
      columns: [post_id, tag_id]

    - name: chk_price_positive
      type: check
      check: "price > 0"

  hooks:                        # plugin hook names to call on lifecycle events
    before_create: [validate-post]
    after_create: [notify-followers]
    before_update: []
    after_update: []
    before_delete: []
    after_delete: [invalidate-cache]
```

### Field types

| Type | Notes |
|---|---|
| `uuid` | PostgreSQL `uuid`; MySQL `CHAR(36)`; SQLite `TEXT` |
| `string` | `VARCHAR(255)` or `VARCHAR(N)` with `length:` |
| `text` | Unbounded text |
| `integer` | 32-bit integer |
| `bigint` | 64-bit integer |
| `float` | Floating point |
| `decimal` | Fixed precision with `precision:` and `scale:` |
| `boolean` | PostgreSQL `boolean`; MySQL `TINYINT(1)`; SQLite `INTEGER` |
| `timestamptz` | Timestamp with timezone; MySQL `DATETIME`; SQLite `TEXT` |
| `date` | Date only |
| `jsonb` | JSON blob; MySQL `JSON`; SQLite `TEXT` |
| `enum` | String with `values:` list; stored as text with CHECK constraint |
| `array` | PostgreSQL `text[]`; other drivers: `TEXT` (serialized) |
| `bytea` | Binary data; MySQL/SQLite `BLOB` |

### Validation formats

| `format` value | What it checks |
|---|---|
| `email` | Valid email address |
| `url` | Valid URL with http/https scheme |
| `uuid` | UUID v4 format |
| `slug` | Lowercase alphanumeric and hyphens only |

---

## Endpoint YAML (`kind: endpoints`)

```yaml
version: "1"
kind: endpoints
endpoints:
  - path: /posts                # URL path (chi syntax: {param} for path params)
    entity: Post                # entity name from entity YAML
    crud: [list, create]        # list | get | create | update | delete

    # Optional per-endpoint rate limit (overrides global server.rate_limit)
    rate_limit:
      requests_per_minute: 10
      burst: 5
      key_by: ip

    # Top-level auth applies to all ops unless overridden per-op
    auth:
      require: false
      roles: [admin, editor]    # JWT role claim must match one of these (enforced)
      conditions:               # ABAC: all must pass (AND logic); 403 on failure
        - subject.email ends_with "@company.com"
        - subject.role in ["admin", "editor"]

    # OpenAPI spec control
    spec: false                 # true (default) | false = exclude from all specs
    specs:
      names: [api, sdk]         # restrict to these named specs; omit = all specs
      description: string       # operation description
      tags: [posts, content]    # extra tags; entity name is always prepended first
      summary: string           # defaults to "{op} {EntityName}"

    list:
      allow_filter_by: [status, author_id]   # query params for WHERE clauses
      allow_sort_by: [created_at, title]      # fields allowed in ?sort=
      default_sort: created_at:desc
      pagination:
        style: cursor           # cursor (default) | offset
        default_limit: 20
        max_limit: 100
        include_total: true     # offset only: include total row count in meta
      include: [author, tags]   # relation names to eager-load
      auth:                     # overrides top-level auth for list only
        require: false
      row_access:               # ABAC: row-level filter rules (opt-in; absent = open)
        - when: "subject.role == \"admin\""
          filter: ""            # empty = no extra WHERE condition (see all rows)
        - when: "*"             # catch-all; always include to avoid unexpected 403
          filter: "status = 'published'"

    get:
      include: [author, tags, comments]
      auth:
        require: false
      row_access:               # same syntax as list.row_access
        - when: "*"
          filter: "status = 'published'"

    create:
      auth:
        require: true
        roles: [editor, admin]
        conditions:
          - subject.email ends_with "@company.com"
      before_hooks: [validate-post]    # plugin hook names
      after_hooks: [notify-followers]
      bulk: false               # true = accept an array body; false (default) = single object
      bulk_max: 100             # max items in a bulk request (default: 100)
      bulk_error_mode: abort    # abort (default) = fail all on first error
                                # partial = continue; return 207 with per-item results

    update:
      allowed_fields: [title, body, status]   # mass-assignment whitelist
      auth:
        require: true
        roles: [editor, admin]
      row_access:               # callers without a matching rule get 404
        - when: "subject.role == \"admin\""
          filter: ""
        - when: "*"
          filter: "author_id = :subject.id"   # :subject.id | :subject.role | :subject.email

    delete:
      soft_delete: true         # sets deleted_at (entity must have soft_delete: true)
      auth:
        require: true
        roles: [admin]
      row_access:
        - when: "subject.role == \"admin\""
          filter: ""
        - when: "*"
          filter: "author_id = :subject.id"
```

### CRUD → HTTP mapping

| crud | Method | Path |
|---|---|---|
| `list` | GET | `/path` |
| `get` | GET | `/path/{id}` |
| `create` | POST | `/path` |
| `update` | PATCH | `/path/{id}` |
| `delete` | DELETE | `/path/{id}` |

If the path already contains `{param}` (e.g. `/post/{id}`), no `/{id}` is appended.

### Offset pagination response

When `style: offset`, the `meta` envelope changes:

```json
{
  "data": [...],
  "meta": {
    "count":  20,
    "limit":  20,
    "offset": 40,
    "page":   3,
    "total":  157    // only present when include_total: true
  }
}
```

### Bulk create response

When `bulk: true`, POST accepts a JSON array. On `bulk_error_mode: abort`:
```json
{ "data": [...] }   // 201 — all succeeded
{ "error": "..." }  // 400/422 — rolled back on first error
```

On `bulk_error_mode: partial`:
```json
// 207 Multi-Status — mix of successes and failures
{
  "results": [
    { "index": 0, "data": { ... } },
    { "index": 1, "error": "title is required" }
  ]
}
```

---

## Auth YAML (`kind: auth`)

```yaml
version: "1"
kind: auth
auth:
  base_path: /auth              # all auth routes are mounted under this prefix
  user_entity: User             # entity used for user records

  register:
    enabled: true
    credential_field: email     # column used as username/identifier
    password_field: password    # plaintext password field in request body (not stored)
    hash_field: password_hash   # column where the bcrypt hash is stored
    default_role: member        # value written to the role column on registration

  login:
    enabled: true
    credential_field: email
    password_field: password
    hash_field: password_hash

  me:
    enabled: true               # GET /auth/me returns current user from JWT sub

  refresh:
    enabled: true               # POST /auth/refresh rotates access + refresh tokens
    expiry: 30d                 # refresh token TTL; supports d/h/m/s and Go duration syntax
    store: cookie               # cookie (default, HttpOnly) | body (returns JSON)

  oauth2:
    enabled: true
    providers:
      - name: google
        client_id: ${GOOGLE_CLIENT_ID}
        client_secret: ${GOOGLE_CLIENT_SECRET}
        redirect_url: https://app.example.com/auth/callback/google
        scopes: [email, profile]
        user_entity: User
        email_field: email      # entity field to match/store the OAuth email
        role: member            # default role for OAuth-created users

      - name: github
        client_id: ${GITHUB_CLIENT_ID}
        client_secret: ${GITHUB_CLIENT_SECRET}
        redirect_url: https://app.example.com/auth/callback/github
        scopes: [user:email]
        user_entity: User
        email_field: email
        role: member
```

### Auth endpoints

| Method | Path | Description |
|---|---|---|
| POST | `/auth/register` | Create account, returns JWT (+ refresh token if enabled) |
| POST | `/auth/login` | Authenticate, returns JWT (+ refresh token if enabled) |
| GET | `/auth/me` | Returns current user (requires JWT) |
| POST | `/auth/refresh` | Exchange refresh token for new access + refresh tokens |
| GET | `/auth/{provider}` | Redirect to OAuth2 provider |
| GET | `/auth/callback/{provider}` | OAuth2 callback, returns JWT |

### Token refresh flow

With `store: cookie` (default):
1. Login response sets `refresh_token` HttpOnly cookie + returns `{"token": "<access>"}` in body
2. `POST /auth/refresh` reads cookie, validates refresh JWT, issues new access + refresh tokens, rotates cookie
3. Refresh token is single-use (rotated on every call)

With `store: body`:
1. Login response returns `{"token": "<access>", "refresh_token": "<refresh>"}` in body
2. `POST /auth/refresh` reads `{"refresh_token": "<token>"}` from request body

---

## Seed YAML (`kind: seed`)

```yaml
version: "1"
kind: seed
seeds:
  - entity: Role                # entity name to insert into
    key_field: name             # field used to check if row already exists (idempotent)
    data:
      - name: admin
        description: "Full access"
      - name: editor
        description: "Can create and edit content"
      - name: member
        description: "Read-only access"
```

Seeds are idempotent: before each INSERT, yayPi checks whether a row with the `key_field` value already exists. If it does, the row is skipped. Seeds run once at server startup before routes are registered.

---

## Email YAML (`kind: email`)

Requires SMTP environment variables: `SMTP_HOST`, `SMTP_PORT`, `SMTP_USER`, `SMTP_PASS`, `SENDER_EMAIL`, `SENDER_NAME`.

```yaml
version: "1"
kind: email
emails:
  - entity: User                # entity that triggers this email
    trigger: after_create       # before_create | after_create | before_update |
                                # after_update | before_delete | after_delete
    condition: ""               # optional: "record.field != value" to skip some triggers
    to: "{{record.email}}"      # recipient; supports {{record.FIELD}} template syntax
    subject: "Welcome to our platform"
    body: |
      Hi {{record.name}},

      Your account has been created. Welcome aboard!
    html: |
      <p>Hi {{record.name}},</p>
      <p>Your account has been created. Welcome aboard!</p>
```

Template syntax: `{{record.FIELD}}` where `FIELD` is any column name in the entity.

Condition syntax: `record.FIELD != ""` or `record.FIELD == "value"` (simple equality/inequality only).

---

## Webhooks YAML (`kind: webhooks`)

```yaml
version: "1"
kind: webhooks
webhooks:
  - entity: Order               # entity that triggers this webhook
    trigger: after_create       # before_create | after_create | before_update |
                                # after_update | before_delete | after_delete
    condition: ""               # optional: skip trigger if condition fails
    url: "https://hooks.example.com/orders"
    method: POST                # default: POST
    headers:
      Authorization: "Bearer ${WEBHOOK_SECRET}"
      Content-Type: "application/json"
    payload: |
      {
        "event": "order.created",
        "order_id": "{{record.id}}",
        "amount": "{{record.total}}"
      }
    timeout: 10s                # default: 10s
    retry:
      max_attempts: 3
      backoff: 5s
```

Webhook requests are sent in a goroutine (non-blocking). The SSRF blocklist rejects RFC-1918, loopback, and link-local addresses.

---

## Jobs YAML (`kind: jobs`)

```yaml
version: "1"
kind: jobs
jobs:
  - name: purge-deleted-posts
    schedule: "0 3 * * *"       # cron expression (5-field, UTC by default)
    timezone: America/New_York   # IANA timezone for schedule
    handler: sql                 # sql | http
    timeout: 30s
    retry:
      max_attempts: 3
      backoff: 5s               # delay between retries
    on_failure: log             # log (only option currently)
    config:
      sql: >
        DELETE FROM posts
        WHERE deleted_at IS NOT NULL
        AND deleted_at < NOW() - INTERVAL '30 days'
      database: primary         # which database to run against

  - name: ping-uptime
    schedule: "*/5 * * * *"
    handler: http
    timeout: 10s
    config:
      url: https://uptime.example.com/ping
      method: GET
      allowed_hosts: [uptime.example.com]   # SSRF allowlist
```

---

## Response format

All CRUD endpoints return JSON in a consistent envelope:

```json
// cursor-paginated list
{ "data": [...], "meta": { "count": 20, "next_cursor": "abc" } }

// offset-paginated list
{ "data": [...], "meta": { "count": 20, "limit": 20, "offset": 0, "page": 1, "total": 157 } }

// get / create / update
{ "data": { ...entity fields... } }

// bulk create (partial mode) — 207 Multi-Status
{ "results": [ { "index": 0, "data": {...} }, { "index": 1, "error": "..." } ] }

// delete
// 204 No Content

// validation errors
{ "errors": { "email": "must be a valid email address", "age": "must be between 18 and 120" } }

// other errors
{ "error": "message" }
```

---

## JWT claims

Tokens issued by `/auth/register` and `/auth/login` contain:

```json
{
  "sub": "<user id>",
  "role": "<user role>",
  "email": "<user email>",
  "iat": 1711234567,
  "exp": 1711320967
}
```

Refresh tokens contain an additional `"type": "refresh"` claim and are validated separately.

The `sub` claim is used to identify the current user in `GET /auth/me` and RBAC enforcement.
