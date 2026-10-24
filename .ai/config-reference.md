# Complete Config Field Reference

Every YAML field accepted by yayPi, organized by file kind. `${VAR}` and `${VAR:-default}` are interpolated from the environment in every file; an unset variable becomes `""` (which validation then rejects for secrets and DSNs).

---

## `yaypi.yaml` — root config

```yaml
version: "1"                    # required
project:
  name: string                  # project name; default JWT issuer and OpenAPI tag
  base_url: /api/v1             # URL prefix for all routes

server:
  port: 8080
  read_timeout: 30s
  read_header_timeout: 10s      # slowloris protection (default 10s)
  write_timeout: 30s
  idle_timeout: 120s            # keep-alive idle (default 120s)
  request_timeout: 30s          # request context deadline incl. queries → 504 (default: write_timeout)
  shutdown_timeout: 10s
  drain_delay: 0s               # after SIGTERM: /ready fails, keep serving this long (e.g. 5s on k8s)
  max_request_body_size: 1MB    # default 1MB; oversize → 413
  max_header_bytes: 1MB
  trusted_proxies: [10.0.0.0/8] # only these peers' X-Forwarded-For / X-Real-IP are believed
  allowed_origins: []           # CORS; "*" = any origin WITHOUT credentials; listed origins get credentials
  cors:
    allowed_headers: [...]      # default: Authorization, Content-Type, Accept, X-API-Key, X-Request-ID, If-Match, Idempotency-Key
    allowed_methods: [...]      # default: GET, POST, PUT, PATCH, DELETE, OPTIONS
    exposed_headers: [ETag, X-Request-ID]
    max_age: 600
  security_headers: true        # nosniff / frame DENY / no-referrer / CSP (default true)
  hsts_max_age: 0               # > 0 adds Strict-Transport-Security (set when HTTPS-only)
  idempotency_ttl: 24h          # Idempotency-Key replay window
  strict_startup: true          # degraded subsystems (cron, OpenAPI, email w/o SMTP) fail boot (default true)
  tls:
    cert_file: string
    key_file: string
  health:
    enabled: true
    path: /health               # liveness: always 200
    readiness_path: /ready      # 200 if all DBs reachable; 503 otherwise or while draining (no error text exposed)
  rate_limit:
    requests_per_minute: 60     # token bucket refill rate
    burst: 20                   # bucket capacity
    key_by: ip                  # ip (default) | user (subject id; mounted after auth per route)

databases:
  - name: primary               # logical name; referenced by entity.database
    driver: postgres            # postgres/postgresql | mysql | sqlite/sqlite3
    dsn: ${DATABASE_URL}        # required (empty = validation error)
    max_open_conns: 25          # default 25
    max_idle_conns: 25          # default = max_open_conns
    conn_max_lifetime: 30m      # default 30m
    conn_max_idle_time: 5m      # default 5m
    default: true               # used when entity has no database:

auth:
  secret: ${JWT_SECRET}         # HS* key, >= 32 bytes (validated when any auth is used)
  algorithm: HS256              # HS256 (default) | HS384 | HS512 | RS256/384/512 | ES256/384/512
  private_key_file: ./keys/jwt.pem   # RS*/ES*: signing key (auth endpoints need it)
  public_key_file: ./keys/jwt.pub    # RS*/ES*: verify key (derived from private key if unset)
  key_id: k1                    # "kid" header + JWKS key id
  issuer: my-api                # "iss" on issued tokens (default project.name); verified only when set
  audience: my-clients          # "aud"; verified when set
  expiry: 15m                   # access token TTL; default 15m with refresh enabled, else 1h
  revocation_check: false       # re-read user per request: deletion/role change/logout-all/reset apply instantly
  api_keys:
    header: X-API-Key           # default X-API-Key
    query_param: api_key        # discouraged (keys leak into logs); warns at boot
    keys:                       # static keys (use this OR entity)
      - key: ${ADMIN_API_KEY}
        role: admin
        name: ci                # subject id (default apikey:<index>)
    entity: ApiKey              # DB-backed keys
    key_field: token            # column holding the key DIGEST (default token)
    role_field: role            # default role
    subject_field: user_id      # subject id column (default user_id if present, else PK)
    key_hash: sha256            # sha256 (default; store output of `yaypi apikey generate`) | plain (legacy)
    # optional columns honoured automatically: expires_at, revoked_at, deleted_at (soft delete)

policy:
  engine: casbin                # any init error fails startup (never fails open)
  model: ./policies/model.conf  # relative to yaypi.yaml
  adapter: file                 # only "file" is supported

auto_migrate: false             # apply schema diff at boot under a lock, no files written (dev only)

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

outbox:
  poll_interval: 1s             # how often queued webhooks/emails are picked up
  retention: 7d                 # delivered rows deleted after this

cron:
  distributed: true             # one replica per tick via yaypi_job_locks (default with a DB)

audit:
  retention: 365d               # purge audit rows older than this (default: keep forever)

log:
  level: info                   # debug | info | warn | error
  format: json                  # json | console (default: console on a TTY, else json)

metrics:
  enabled: false                # Prometheus text format, no extra deps
  path: /metrics                # mounted outside base_url
  token: ${METRICS_TOKEN}       # optional bearer token to scrape

plugins:
  - name: string                # matched to PluginInfo.Name; config passed to Init
    config: {}                  # listing any plugin also creates yaypi_sync_cursors (Store.Cursor)

include:
  - entities/**/*.yaml
  - endpoints/**/*.yaml
  - policies/**/*.yaml
  - jobs/**/*.yaml
  - seeds/**/*.yaml
  - emails/**/*.yaml
  - webhooks/**/*.yaml
  - auth.yaml

spec:                           # named OpenAPI specs → /openapi/{name}.json
  - name: api
    title: string
    version: "1.0.0"
    servers:
      - url: https://api.example.com
```

---

## Entity YAML (`kind: entity`)

```yaml
version: "1"
kind: entity
entity:
  name: Post                    # PascalCase identifier
  table: posts                  # default: snake_case(name) + "s"
  database: primary             # default: the default database
  timestamps: true              # adds created_at, updated_at (server-managed)
  soft_delete: true             # adds deleted_at; DELETE sets it; reads exclude deleted rows
  audit: true                   # yaypi_audit_log row per change (actor, field diff), same transaction
  tenant_scoped: true           # rows isolated by tenant_field = caller's "tenant" claim
  tenant_field: tenant_id       # default tenant_id (must be a declared field)
  natural_key: [external_id]    # identifies a row from an outside system; enables upserts.
                                # Must be a unique field or a unique index over exactly these fields
  stale_guard: source_updated_at  # timestamp/number: an upsert that is not newer is skipped
  rules:                        # checks across fields on create/update, against the stored row + change
    - { check: ends_at > starts_at, message: End must be after start }   # == != > < >= <=; fields, numbers,
    - { check: seats >= 1 }       # "strings", true/false, null, now; null operand passes; failure → 422 rule_failed

  fields:
    - name: id
      type: uuid                # uuid|string|text|integer|bigint|float|decimal|boolean|
                                # timestamptz|date|jsonb|enum|array|bytea
      primary_key: true
      default: uuid               # a value of the field's type (draft, "", 0, false); uuid / now generate; {sql: "expr"} for raw SQL

    - name: title
      type: string
      length: 255
      nullable: false
      unique: false
      index: false
      immutable: true           # settable on create only (dropped from PATCH/PUT)

    - name: completed_at
      type: timestamptz
      nullable: true
      stamp_when: done          # now when the condition turns true, null when false; server-managed

    - name: author_id
      type: uuid
      default_from: subject.id  # set from caller on create (client value ignored), frozen after
                                # subject.id | subject.email | subject.role | subject.tenant
      references:
        entity: User
        field: id
        on_delete: CASCADE      # CASCADE | SET NULL | RESTRICT | NO ACTION
        on_update: NO ACTION

    - name: email
      type: string
      validate:
        required: true
        min_length: 3           # characters (runes)
        max_length: 255
        pattern: "^[^@]+@[^@]+$"  # compiled at boot; invalid regex = startup error
        format: email           # email | url | uuid | slug (others = startup error)
        message: "must be a valid email address"

    - name: age
      type: integer
      validate: { min: 18, max: 120 }

    - name: password_hash
      type: string
      serialization:
        omit_response: true     # never returned
        omit_log: true          # redacted in audit log

    - name: internal_notes
      type: text
      access:
        read_roles: [admin]     # others don't see the field
        write_roles: [admin]    # others' values are silently dropped

  relations:                    # loadable via ?include= when listed in endpoint include:
    - { name: author, type: belongs_to, entity: User, foreign_key: author_id }
    - { name: comments, type: has_many, entity: Comment, foreign_key: post_id }
    - { name: tags, type: many_to_many, entity: Tag, through: PostTag, foreign_key: post_id, other_key: tag_id }

  indexes:
    - { name: idx_posts_slug, columns: [slug], unique: true, type: btree }

  constraints:
    - { name: chk_price_positive, type: check, check: "price > 0" }
```

### Input handling

Every provided body field is type-checked and coerced to its declared type before it reaches the database (wrong type → 400 `validation_failed` with per-field `errors`). Integers must be integral; `timestamptz` takes RFC 3339; `date` takes `YYYY-MM-DD`; `bytea` takes base64; `jsonb` takes any JSON; `decimal` keeps exact precision. Explicit `null` on a non-nullable field is a 400. Unknown fields are dropped (or 400 with `strict: true`). Server-managed columns (`created_at`, `updated_at`, `deleted_at`, `default_from` and `stamp_when` fields, PK on update) are never client-writable.

### Upserts (`natural_key`)

An entity with `natural_key` can be written by key instead of by id — through the `upsert` crud op, `create.on_conflict`, inbound webhook routes (`op: upsert`) and plugin `Store.Upsert`. One code path does all of them:

- No row with the key → create (`created`).
- A row exists → update it in place (`updated`); the id, `created_at`, immutable and `default_from` fields keep their stored values. A soft-deleted match is revived (`deleted_at` cleared).
- With `stale_guard`, the update only applies when the incoming value is newer than the stored one (or the stored one is NULL); otherwise nothing is written (`unchanged`). This keeps out-of-order deliveries from overwriting newer data.
- Upserts run the `before_create` hooks (the input is a full representation), then `after_create` or `after_update` for what happened. The audit log and outbound webhooks see a normal create or update.
- The lookup locks the row (Postgres/MySQL). Two concurrent upserts of a *new* key can still race; the loser gets 409 and should retry.

---

## Endpoint YAML (`kind: endpoints`)

```yaml
version: "1"
kind: endpoints
endpoints:
  - path: /posts
    entity: Post
    crud: [list, get, create, update, replace, delete]

    max_body_size: 64MB         # overrides server.max_request_body_size (upload routes)
    rate_limit:                 # in addition to the global limiter (both must pass)
      requests_per_minute: 10
      burst: 5
      key_by: user              # ip | user

    auth:                       # endpoint default; per-op auth overrides
      require: true
      roles: [admin, editor]    # always enforced (with or without Casbin); implies require
      conditions:               # all must pass; implies require
        - subject.email ends_with "@company.com"

    list:
      allow_filter_by: [status, author_id, score]
      allow_sort_by: [created_at, title]
      default_sort: created_at:desc
      search: [title, body]     # ?q=term → case-insensitive substring across these
      pagination:
        style: cursor           # cursor (default, keyset) | offset
        default_limit: 20
        max_limit: 100
        include_total: true     # adds meta.total (COUNT query)
      include: [author, tags]   # relations clients may request with ?include=
      row_access:
        - when: "subject.role == \"admin\""
          filter: ""
        - when: "*"
          filter: "author_id = :subject.id"   # :subject.id | :subject.role | :subject.email | :subject.tenant

    get:
      include: [author, tags]
      row_access: [...]

    create:
      bulk: false               # true = body is an array
      bulk_max: 500
      bulk_error_mode: abort    # abort (default): one transaction, nothing inserted on error
                                # partial: each item commits alone; 207 with per-item results
      strict: false             # 400 on unknown fields
      on_conflict: error        # existing natural_key: error (409, default) | update (upsert) | ignore
                                # update/ignore need natural_key; X-Write-Action (bulk: results[].action)
                                # reports created | updated | unchanged | ignored

    update:                     # also used by replace (PUT); row_access also limits upsert targets
      allowed_fields: [title, body, status]
      strict: false
      row_access: [...]         # no match → 404

    delete:
      row_access: [...]         # soft delete happens automatically when the entity has soft_delete

  - path: /calendar             # custom route: method + handler (a RouteHandlerPlugin)
    method: GET
    entity: Event               # RBAC resource; the action follows the method
    handler: cal.view
    operation_id: calendarView  # OpenAPI operationId (default: handler key)
    params:                     # query params: checked + typed before the handler (c.Params)
      date: { type: date, required: true }
      limit: { type: integer, validate: { min: 1, max: 100 } }
    body:                       # POST/PUT/PATCH: JSON fields (c.Body); unknown keys → 400
      note: string
    response: { list: Event }   # data shape: {one|list: Entity | {fields: {...}}}, or {status: 204};
                                # object members may nest: events: {list: Event}
```

Custom routes appear in OpenAPI from these declarations (walm `op:` requests
are checked against them). Plugins answer with `c.Data(v)` / `c.Created(v)` /
`c.Error(status, msg)` and read on a caller's behalf with
`Store.Read(ctx, sdk.ReadRequest{Entity, Subject, Where: {"starts_at[gte]": t}})`
(a page; `Next` → `Cursor`) or `Store.ReadAll`, which apply the list
endpoint's `row_access`, tenant scope and field rules.

### CRUD → HTTP mapping

| crud | Method | Path | Notes |
|---|---|---|---|
| `list` | GET | `/path` | filters, sort, cursor/offset, `?fields=`, `?include=`, `?q=` |
| `get` | GET | `/path/{id}` | `ETag`; `If-None-Match` → 304; `?fields=`, `?include=` |
| `create` | POST | `/path` | `Idempotency-Key` header replays the first response for 24h |
| `update` | PATCH | `/path/{id}` | partial; `If-Match` → 412 on stale ETag |
| `replace` | PUT | `/path/{id}` | full replace: omitted nullable writable fields become null |
| `delete` | DELETE | `/path/{id}` | `If-Match` supported; 204 |
| `upsert` | PUT | `/path` | create or update by `natural_key`: 201 created, 200 updated/unchanged; `X-Write-Action` |

`upsert` (and `create` with `on_conflict: update`) can both create and update, so Casbin must allow **both** `create` and `update` for the role. `create.auth` applies; `update.row_access` and tenant scope limit which existing rows can be overwritten (a hidden match → 409).

### List query syntax

```
?status=published                 equality
?score[gte]=10&score[lt]=50       gt gte lt lte ne
?status[in]=draft,published       in / nin (max 100 values)
?title[contains]=go               contains / starts_with (case-insensitive, text fields)
?published_at[is_null]=true       is_null
?sort=-created_at                 or created_at:desc
?limit=50&cursor=<meta.next_cursor>
?fields=id,title&include=author
?q=search+term                    when list.search is set
```

Values are coerced to the field type (bad value → 400). Filtering on a field not in `allow_filter_by` is a 400. Cursor pagination is keyset on (sort column, primary key) — complete and stable for any sort; a cursor is only valid for the sort it was issued with.

### Responses

```json
{ "data": [...], "meta": { "count": 20, "limit": 20, "has_more": true, "next_cursor": "…", "total": 157 } }
{ "data": [...], "meta": { "count": 20, "limit": 20, "has_more": true, "offset": 40, "page": 3 } }
{ "data": { ... } }
{ "results": [ { "index": 0, "data": {...} }, { "index": 1, "error": "...", "errors": {...} } ] }
```

### Error envelope (every error, everywhere)

```json
{ "error": "human message", "code": "validation_failed", "request_id": "…", "errors": { "title": "title is required" } }
```

| Status | code | When |
|---|---|---|
| 400 | `validation_failed` / `bad_request` | bad input, filters, cursor |
| 401 | `unauthorized` | missing/invalid/revoked token |
| 403 | `forbidden` | role/condition/row-access/tenant denial |
| 404 | `not_found` | missing or hidden by row access |
| 409 | `conflict` | unique violation; idempotent request in progress |
| 412 | `precondition_failed` | `If-Match` mismatch |
| 413 | `payload_too_large` | body over `max_request_body_size` |
| 422 | `reference_violation` / `required_field_missing` / `constraint_violation` / hook code | FK / NOT NULL / CHECK / `sdk.HookError` |
| 429 | `rate_limited` | rate limit or login throttle (`Retry-After`) |
| 504 | `timeout` | `request_timeout` exceeded |

---

## Auth YAML (`kind: auth`)

Built-in `users` table: `id` (uuid), `email`, `password_hash`, `role` (default `'member'`), `oauth_provider`, `oauth_id`, `email_verified_at`, `token_version`, `created_at`, `updated_at`, `deleted_at`. Add columns with `user.fields`. Declaring auth also creates internal `yaypi_refresh_tokens` and `yaypi_auth_tokens` tables.

```yaml
version: "1"
kind: auth
auth:
  base_path: /auth
  user:
    fields:
      - { name: display_name, type: string, length: 128, nullable: true }
      - { name: tenant_id, type: string, nullable: true }   # if present, issued as the "tenant" claim

  register:
    enabled: true
    default_role: member        # always applied; a client-sent role is ignored
    min_password_length: 8      # max is always 72 bytes (bcrypt)

  login:
    enabled: true
    max_attempts: 5             # per account (4x per IP) per window → 429; -1 disables
    lockout_window: 15m

  me:
    enabled: true

  refresh:
    enabled: true               # issued on register/login/OAuth; stored hashed; rotated; reuse revokes the session
    expiry: 30d
    store: cookie               # cookie (HttpOnly, path-scoped) | body

  cookie:
    secure: true                # false only for local http dev
    same_site: lax              # lax | strict | none

  password_reset:               # needs smtp: (or SMTP_* env)
    enabled: true
    expiry: 1h
    reset_url: https://app.example.com/reset?token={{token}}
    subject: Reset your password
    body: '<a href="{{link}}">Reset</a>'

  email_verification:           # needs smtp: (or SMTP_* env)
    enabled: true
    required: false             # true = password login refused until verified
    expiry: 24h
    verify_url: https://app.example.com/verify?token={{token}}

  oauth2:
    providers:
      - name: google            # google | github built in; anything else = custom OIDC-style
        client_id: ${GOOGLE_CLIENT_ID}
        client_secret: ${GOOGLE_CLIENT_SECRET}
        redirect_uri: https://api.example.com/api/v1/auth/callback/google
        success_redirect: https://app.example.com/auth/done   # token arrives as #token=…
        error_redirect: https://app.example.com/login
        pkce: true              # default
        token_delivery: fragment  # fragment (default) | query (legacy)
        # custom providers:
        auth_url: …
        token_url: …
        userinfo_url: …
        id_field: sub           # default id (google/github) | sub
        email_verified_field: email_verified
        trust_email: false      # only for IdPs that guarantee verified emails
```

### Auth endpoints

| Method | Path | Auth | Description |
|---|---|---|---|
| POST | `/auth/register` | – | Create account → `{token, expires_in, user[, refresh_token]}` |
| POST | `/auth/login` | – | Authenticate (throttled) |
| GET | `/auth/me` | JWT | Current user |
| POST | `/auth/refresh` | refresh token | Rotate; replaying a used token revokes the whole session family |
| POST | `/auth/logout` | refresh token | Revoke this session; always 204 |
| POST | `/auth/logout-all` | JWT | Revoke all sessions + bump token_version |
| POST | `/auth/password/forgot` | – | `{email}` → always 202 |
| POST | `/auth/password/reset` | – | `{token, password}`; revokes all sessions |
| POST | `/auth/verify-email` | – | `{token}` |
| POST | `/auth/verify-email/resend` | JWT | 1/minute |
| GET | `/auth/{provider}` | – | OAuth redirect (state cookie + PKCE) |
| GET | `/auth/callback/{provider}` | – | OAuth callback |
| GET | `/auth/.well-known/jwks.json` | – | Public keys (RS*/ES* only) |

OAuth account resolution: (1) existing link by `(oauth_provider, oauth_id)`; (2) existing account with the same email — only if the provider verified it; (3) new account — only with a verified email.

### JWT claims

```json
{ "sub": "<user id>", "role": "member", "email": "a@b.c", "tenant": "acme", "tv": 0,
  "typ": "access", "iss": "my-api", "aud": "my-clients", "iat": 0, "nbf": 0, "exp": 0 }
```

`tv` is the user's `token_version`; with `revocation_check: true` a mismatch (after logout-all or a password reset) is rejected. Refresh tokens are opaque random strings, not JWTs.

---

## Seed YAML (`kind: seed`)

```yaml
version: "1"
kind: seed
seeds:
  - entity: User
    key_field: email            # existing row with this value → skipped (idempotent)
    data:
      - email: admin@example.com
        password: ${ADMIN_PASSWORD}   # User only: bcrypt-hashed into password_hash
        role: admin
```

Seeds run via `yaypi seed` (not at server start).

---

## Email YAML (`kind: email`)

SMTP comes from the root `smtp:` block (fields fall back to `SMTP_*` env vars).

```yaml
version: "1"
kind: email
emails:
  - name: welcome
    entity: User
    trigger: after_create       # after_create | after_update | after_delete
    condition: record.role == "member"   # optional; see condition syntax
    to: "{{record.email}}"
    subject: "Welcome, {{record.display_name}}"
    body: "<p>Hi {{record.display_name}}</p>"   # HTML; values are HTML-escaped
    retry: { max_attempts: 10 }  # optional; overrides smtp.retry
```

## Webhooks YAML (`kind: webhooks`)

One kind, two directions, chosen by `mode`. Omitted `mode` = `outbound`, so existing files are unchanged. Fields of the other mode fail validation.

### Outbound (default)

```yaml
version: "1"
kind: webhooks
webhooks:
  - name: order-created
    entity: Order
    trigger: after_create
    condition: record.total >= 100 and record.status == "paid"
    url: "https://hooks.example.com/orders/{{record.id}}"   # values path-escaped
    method: POST
    headers: { X-Source: yaypi }
    payload: '{"order_id": "{{record.id}}", "total": {{record.total}}}'  # JSON-safe substitution
    secret: ${ORDER_WEBHOOK_SECRET}   # signs deliveries (yaypi_v1 unless signature: says otherwise)
    signature: { scheme: github }     # optional; see "Signature schemes"
    timeout: 10s
    allow_private_network: false      # private/loopback/metadata IPs blocked by default
    retry:
      max_attempts: 8           # then status=dead
      initial_delay: 10s        # exponential, ±20% jitter
      max_delay: 1h
```

Emails and webhooks use a **transactional outbox** (`yaypi_outbox`): the message is rendered and stored in the same transaction as the change, then delivered by a background worker with retries, across replicas, surviving restarts. Default webhook body (no `payload`): `{"event": "Order.created", "entity", "id", "data"}`. Headers on every delivery: `X-Yaypi-Event-Id` (stable across retries — dedupe on it), `X-Yaypi-Timestamp`, and with `secret`, `X-Yaypi-Signature: v1=hex(HMAC-SHA256(secret, timestamp + "." + body))`.

Condition syntax: `record.<field> <op> <value>` with `== != > >= < <=`; value is a quoted string, number, `true`/`false` or `null`; join with `and`/`&&`. Invalid conditions fail at boot.

### Inbound (`mode: inbound`)

```yaml
  - name: crm-events
    mode: inbound
    path: /webhooks/crm                   # mounted under project.base_url
    secret: ${CRM_WEBHOOK_SECRET}
    signature:
      scheme: hmac_sha256
      header: X-Crm-Signature
      encoding: hex                       # hex | base64
      prefix: "sha256="                   # optional
      timestamp_header: X-Crm-Timestamp   # optional replay window
      tolerance: 5m
    challenge: { method: GET, query_param: challenge }   # echo during endpoint setup
    dedupe_key: body.id                   # repeats are acknowledged and dropped
    event: body.type                      # selects the route
    routes:
      contact.created: { entity: Contact, op: upsert, map: { external_id: "{{body.data.id}}", email: "{{body.data.email}}" } }
      contact.deleted: { entity: Contact, op: delete, key: { external_id: "{{body.data.id}}" } }
      "*":             { handler: crm.OnOther }   # plugin fallback (WebhookPlugin)
    as_role: integration                  # optional: Casbin + write_roles checked against it
    retry: { max_attempts: 8 }
    timeout: 30s                          # per processing attempt
    rate_limit: { requests_per_minute: 600 }   # default 300/min per IP; cannot be disabled
    max_body_size: 1MB
```

Flow: verify (signature or guard) → answer a handshake if this is one → dedupe on `(name, dedupe_key)` → store in `yaypi_inbox` → `200 {"status":"accepted"}` (repeats: `"duplicate"`). A worker then runs the route with retries (same backoff and dead-lettering as the outbox, shared through `internal/queue`).

- **Routes**: the event value picks a route; `"*"` matches events without their own route; anything else is acknowledged and ignored. `handler: x.y` at the top level is shorthand for `routes: {"*": {handler: x.y}}`.
- **Entity routes** (`op: create | update | upsert | delete`) go through the same write path as the CRUD API: validation, hooks, audit log, outbound webhooks. `upsert` matches the entity's `natural_key`; `update`/`delete` need `key` (the primary key or exactly the natural key). Deleting a missing row is a no-op; updating one is retried (the create may arrive later), then dead.
- **Templates** in `map`/`key`: `{{body.a.b}}`, `{{body.items.0.id}}`, `{{headers.X-Id}}`, `{{query.x}}`. A value that is exactly one placeholder keeps its JSON type; mixed text renders as a string; non-string YAML values are literals. Values are adapted to the field type where unambiguous (number → string column, `"7"` → integer, unix seconds → timestamp). Form-encoded bodies are readable as `body.*` too.
- **Writes are system writes** unless `as_role` is set: no role checks, `default_from`/tenant columns come from `map`. With `as_role`, Casbin must allow the op (upsert = create + update) and field `write_roles` apply. The audit actor is `webhook:<name>`.
- **Plugin handlers** get `sdk.WebhookContext` (raw body, headers without credentials, query, event, attempt, `Store`). Return `sdk.Permanent(err)` to skip retries.

### Signature schemes

`secret` + `signature` drive both directions: outbound signs, inbound verifies.

| scheme | header | signed | notes |
|---|---|---|---|
| `yaypi_v1` (default with a secret) | `X-Yaypi-Signature: v1=<hex>` + `X-Yaypi-Timestamp` | `{timestamp}.{body}` | yayPi ↔ yayPi |
| `github` | `X-Hub-Signature-256: sha256=<hex>` | `{body}` | |
| `slack` | `X-Slack-Signature: v0=<hex>` + `X-Slack-Request-Timestamp` | `v0:{timestamp}:{body}` | |
| `stripe` | `Stripe-Signature: t=…,v1=<hex>` | `{timestamp}.{body}` | multiple `v1` accepted |
| `hmac_sha256` / `hmac_sha1` | `header` (required) | `signed_payload` (default `{body}`, or `{timestamp}.{body}` with `timestamp_header`) | `prefix`, `encoding` |
| `plugin` | — | — | inbound only: `handler: <plugin>.<verifier>` (WebhookVerifierPlugin) |
| `none` | — | — | see rules below |

All comparisons are constant-time. Timestamps (unix seconds) must be within `tolerance` (default 5m).

### Inbound protection rules (checked at boot)

| `secret` | `signature` | Result |
|---|---|---|
| set | omitted | `yaypi_v1` |
| set | signing scheme | that scheme |
| not set | signing scheme | error: scheme needs secret |
| not set | omitted, or `scheme: none` | allowed only with a guard: `auth`, `token` or `allowed_ips` |
| set | `scheme: none` | error: the secret would never be checked |

Guards (at least one when unsigned):

```yaml
    auth: { require: true, roles: [system] }        # API key or JWT, like endpoints
    token: { header: X-Webhook-Token, value: ${TOKEN} }   # or query_param:; constant-time
    allowed_ips: [203.0.113.0/24]                   # matched against the client IP
```

Unsigned receivers also need `dedupe_key` (the only replay protection), log a warning at every boot, and keep the always-on rate limit. `allowed_ips` matches the TCP peer unless `server.trusted_proxies` is set — behind a reverse proxy, set it. The auth chain is mounted only when `auth` is declared, so a provider's own `Authorization` header is never mistaken for a yayPi token. Credential headers (`Authorization`, `Cookie`, API key, `token` header/param) are never stored in the inbox.

---

## Jobs YAML (`kind: jobs`)

```yaml
version: "1"
kind: jobs
jobs:
  - name: purge-deleted-posts
    schedule: "0 3 * * *"       # 5/6-field cron, @hourly/@daily/…, or "@every 10m"
    timezone: America/New_York  # IANA zone (default UTC)
    handler: sql                # sql | http | plugin
    timeout: 5m                 # default 5m
    retry:
      max_attempts: 3
      backoff: exponential      # exponential (default) | fixed
      initial_delay: 1s
      max_delay: 1m
    on_failure: alerts.JobFailed  # optional <plugin>.<job>, run once after the last attempt fails
    config:
      sql: DELETE FROM posts WHERE deleted_at < NOW() - INTERVAL '30 days'
      database: primary

  - name: ping-uptime
    schedule: "*/5 * * * *"
    handler: http
    config:
      url: https://uptime.example.com/ping
      method: POST
      headers: { Authorization: "Bearer ${UPTIME_TOKEN}" }
      body: { source: yaypi }   # string, or a map/list sent as JSON
      secret: ${PING_SECRET}    # optional: sign the body like an outbound webhook
      signature: { scheme: github }
      allowed_hosts: [uptime.example.com]   # checked at boot
      allow_private_network: false

  - name: sync-contacts
    schedule: "@every 15m"
    handler: plugin
    plugin: crm.SyncContacts    # JobHandlerPlugin entry, registered with Server.RegisterJobs
    config: { page_size: 200 }  # → sdk.JobContext.Config
```

A plugin job gets `sdk.JobContext` (`Ctx` with the timeout, `Config`, `Attempt`, `Store`, `Logger`, `HTTP`). Returning `sdk.Permanent(err)` stops the retries. An unregistered `plugin`/`on_failure` name fails the boot (subject to `strict_startup`).

With a database configured (and `cron.distributed` not false), each run is coordinated through `yaypi_job_locks`, so a tick executes on exactly one replica. A job never overlaps itself on one instance.
