# YAML Pattern Cookbook

Copy-paste patterns for common yayPi use cases.

---

## Public list + auth-required create

```yaml
- path: /posts
  entity: Post
  crud: [list, create]
  auth:
    require: false          # list is public
  create:
    auth:
      require: true
      roles: [editor, admin]
```

---

## Admin-only endpoint

```yaml
- path: /users
  entity: User
  crud: [list, create, update, delete]
  auth:
    require: true
    roles: [admin]
```

---

## Soft delete

Entity:
```yaml
entity:
  name: Post
  soft_delete: true
```

Endpoint:
```yaml
- path: /post/{id}
  entity: Post
  crud: [delete]
  delete:
    soft_delete: true
```

---

## Field validation

```yaml
entity:
  name: User
  fields:
    - name: email
      type: string
      validate:
        required: true
        format: email
        message: "must be a valid email address"

    - name: username
      type: string
      validate:
        required: true
        min_length: 3
        max_length: 30
        pattern: "^[a-z0-9_]+$"
        message: "must be 3–30 lowercase letters, numbers, or underscores"

    - name: age
      type: integer
      validate:
        min: 18
        max: 120
```

Validation runs on create and update. Immutable fields are stripped from update payloads before validation.

---

## Immutable fields

```yaml
entity:
  name: Order
  fields:
    - name: order_number
      type: string
      immutable: true       # set on create, silently ignored on PATCH
    - name: created_by
      type: uuid
      immutable: true
```

---

## Filtered + sorted list with cursor pagination

```yaml
- path: /posts
  entity: Post
  crud: [list]
  list:
    allow_filter_by: [status, author_id, tag_id]
    allow_sort_by: [created_at, published_at, title]
    default_sort: created_at:desc
    pagination:
      style: cursor
      default_limit: 20
      max_limit: 100
```

---

## Offset pagination with total count

```yaml
- path: /posts
  entity: Post
  crud: [list]
  list:
    pagination:
      style: offset
      default_limit: 25
      max_limit: 100
      include_total: true   # adds "total": N to meta
```

Response:
```json
{
  "data": [...],
  "meta": { "count": 25, "limit": 25, "offset": 0, "page": 1, "total": 312 }
}
```

---

## Per-endpoint rate limiting

```yaml
- path: /auth/register
  entity: User
  crud: [create]
  rate_limit:
    requests_per_minute: 5
    burst: 2
    key_by: ip
```

---

## Eager-load relations

```yaml
- path: /post/{id}
  entity: Post
  crud: [get]
  get:
    include: [author, tags, comments]
```

Entity must have matching `relations:` entries.

---

## Mass-assignment protection on update

```yaml
- path: /post/{id}
  entity: Post
  crud: [update]
  update:
    allowed_fields: [title, body, status, published_at]
    auth:
      require: true
      roles: [editor, admin]
```

---

## Bulk create (all-or-nothing)

```yaml
- path: /products
  entity: Product
  crud: [create]
  create:
    bulk: true
    bulk_max: 500
    bulk_error_mode: abort    # default; any error rejects the whole batch
    auth:
      require: true
      roles: [admin]
```

POST body: `[{"name":"A","price":9.99}, {"name":"B","price":14.99}]`

---

## Bulk create (partial success)

```yaml
- path: /imports/products
  entity: Product
  crud: [create]
  create:
    bulk: true
    bulk_max: 1000
    bulk_error_mode: partial  # continue after errors; return 207
```

Response on partial failure:
```json
{
  "results": [
    { "index": 0, "data": { "id": "...", "name": "A" } },
    { "index": 1, "error": "price: must be greater than 0" }
  ]
}
```

---

## Token refresh (cookie store)

```yaml
# auth.yaml
auth:
  base_path: /auth
  user_entity: User
  login:
    enabled: true
    credential_field: email
    password_field: password
    hash_field: password_hash
  refresh:
    enabled: true
    expiry: 30d               # refresh token valid for 30 days
    store: cookie             # HttpOnly cookie (default)
```

Flow: login sets `refresh_token` cookie → `POST /auth/refresh` rotates both tokens.

---

## Token refresh (body store, for mobile/native apps)

```yaml
auth:
  refresh:
    enabled: true
    expiry: 90d
    store: body               # tokens returned/accepted in JSON body
```

---

## API key auth (static keys)

```yaml
# yaypi.yaml
auth:
  secret: ${JWT_SECRET}
  api_keys:
    header: X-API-Key
    keys:
      - key: ${ADMIN_API_KEY}
        role: admin
      - key: ${SERVICE_API_KEY}
        role: service
```

Any request with a valid `X-API-Key` header is treated as authenticated. JWT is still accepted in parallel — whichever is present is used.

---

## API key auth (DB-backed)

```yaml
# yaypi.yaml
auth:
  secret: ${JWT_SECRET}
  api_keys:
    header: X-API-Key
    query_param: api_key      # also accept ?api_key= for simple integrations
    entity: ApiKey            # entity holding key records
    key_field: token          # column with the key value
    role_field: role          # column with the associated role
```

Entity:
```yaml
entity:
  name: ApiKey
  table: api_keys
  soft_delete: true           # soft-delete to revoke keys without data loss
  fields:
    - name: id
      type: uuid
      primary_key: true
      default: gen_random_uuid()
    - name: token
      type: string
      length: 64
      unique: true
    - name: role
      type: string
    - name: description
      type: string
      nullable: true
```

---

## Email notification on user signup

Requires env: `SMTP_HOST`, `SMTP_PORT`, `SMTP_USER`, `SMTP_PASS`, `SENDER_EMAIL`, `SENDER_NAME`.

```yaml
# emails/welcome.yaml
version: "1"
kind: email
emails:
  - entity: User
    trigger: after_create
    to: "{{record.email}}"
    subject: "Welcome to our platform!"
    body: |
      Hi {{record.name}},

      Thanks for signing up! Your account is ready.
```

---

## Webhook on order creation

```yaml
# webhooks/orders.yaml
version: "1"
kind: webhooks
webhooks:
  - entity: Order
    trigger: after_create
    url: "https://fulfillment.example.com/hooks/new-order"
    method: POST
    headers:
      Authorization: "Bearer ${FULFILLMENT_WEBHOOK_SECRET}"
    payload: |
      {
        "order_id": "{{record.id}}",
        "customer_id": "{{record.customer_id}}",
        "total": "{{record.total}}"
      }
    retry:
      max_attempts: 3
      backoff: 5s
```

---

## Conditional webhook (only for large orders)

```yaml
webhooks:
  - entity: Order
    trigger: after_create
    condition: "record.total != \"\""   # fires only when total is set
    url: "https://example.com/hooks/order"
    payload: '{"id":"{{record.id}}"}'
```

---

## Seed data for lookup tables

```yaml
# seeds/roles.yaml
version: "1"
kind: seed
seeds:
  - entity: Role
    key_field: name           # skip INSERT if row with this name already exists
    data:
      - name: admin
        description: "Full administrative access"
      - name: editor
        description: "Can create and edit content"
      - name: member
        description: "Standard member access"
```

---

## Many-to-many via junction table

Junction entity with composite primary key:
```yaml
entity:
  name: PostTag
  table: post_tags
  fields:
    - name: post_id
      type: uuid
      references:
        entity: Post
        field: id
        on_delete: CASCADE
    - name: tag_id
      type: uuid
      references:
        entity: Tag
        field: id
        on_delete: CASCADE
  constraints:
    - name: pk_post_tags
      type: primary_key
      columns: [post_id, tag_id]
```

Post entity relation:
```yaml
relations:
  - name: tags
    type: many_to_many
    entity: Tag
    through: PostTag
    foreign_key: post_id
    other_key: tag_id
```

---

## Self-referential relation (threaded comments)

```yaml
entity:
  name: Comment
  fields:
    - name: id
      type: uuid
      primary_key: true
    - name: parent_id
      type: uuid
      nullable: true
      references:
        entity: Comment
        field: id
        on_delete: CASCADE
  relations:
    - name: replies
      type: has_many
      entity: Comment
      foreign_key: parent_id
    - name: parent
      type: belongs_to
      entity: Comment
      foreign_key: parent_id
```

---

## Multiple databases

```yaml
# yaypi.yaml
databases:
  - name: primary
    driver: postgres
    dsn: ${DATABASE_URL}
    default: true
  - name: analytics
    driver: postgres
    dsn: ${ANALYTICS_URL}
    read_only: true
```

Entity opts into non-default database:
```yaml
entity:
  name: Event
  database: analytics
```

---

## SQLite for development, Postgres for production

```yaml
# dev.yaml
databases:
  - name: primary
    driver: sqlite
    dsn: ./dev.db
    default: true
```

```yaml
# production yaypi.yaml
databases:
  - name: primary
    driver: postgres
    dsn: ${DATABASE_URL}
    default: true
```

---

## Multi-spec OpenAPI (public API + SDK)

`yaypi.yaml`:
```yaml
spec:
  - name: api
    title: "Public API"
    version: "1.0.0"
    servers:
      - url: https://api.example.com
  - name: sdk
    title: "Full SDK"
    version: "1.0.0"
```

Endpoint — restrict to only `api` spec:
```yaml
- path: /posts
  entity: Post
  crud: [list, create]
  specs:
    names: [api]
    tags: [posts]
```

Endpoint — exclude from all specs:
```yaml
- path: /internal/health
  entity: Health
  crud: [get]
  spec: false
```

Runtime: `GET /api/v1/openapi/api.json` and `GET /api/v1/openapi/sdk.json`

---

## OAuth2 login

```yaml
# auth.yaml
auth:
  base_path: /auth
  user_entity: User
  oauth2:
    enabled: true
    providers:
      - name: google
        client_id: ${GOOGLE_CLIENT_ID}
        client_secret: ${GOOGLE_CLIENT_SECRET}
        redirect_url: https://app.example.com/auth/callback/google
        scopes: [email, profile]
        user_entity: User
        email_field: email
        role: member
```

Flow: `GET /auth/google` → provider → `GET /auth/callback/google` → JWT response.

---

## Background SQL job with retry

```yaml
jobs:
  - name: purge-soft-deleted
    schedule: "0 3 * * *"       # 3am daily
    timezone: America/New_York
    handler: sql
    timeout: 60s
    retry:
      max_attempts: 3
      backoff: 10s
    config:
      sql: >
        DELETE FROM posts
        WHERE deleted_at IS NOT NULL
        AND deleted_at < NOW() - INTERVAL '30 days'
      database: primary
```

---

## HTTP job with SSRF allowlist

```yaml
jobs:
  - name: ping-uptime
    schedule: "*/5 * * * *"
    handler: http
    timeout: 10s
    config:
      url: https://uptime.betterstack.com/api/v1/heartbeat/abc123
      method: GET
      allowed_hosts: [uptime.betterstack.com]
```

---

## Plugin lifecycle hook

Register plugin in `yaypi.yaml`:
```yaml
plugins:
  - name: hash-password
    path: ./plugins/hashpassword
    config:
      bcrypt_cost: 12
```

Hook it on entity:
```yaml
entity:
  name: User
  hooks:
    before_create: [hash-password]
    before_update: [hash-password]
```

---

## Health + readiness endpoints

```yaml
# yaypi.yaml
server:
  health:
    enabled: true
    path: /health       # GET → 200 always (Kubernetes liveness probe)
    readiness_path: /ready  # GET → 200 if DB reachable, 503 if not (readiness probe)
```

Both endpoints are mounted outside the `base_url` prefix so they are always reachable.

---

## Field serialization — omit sensitive fields from responses

```yaml
fields:
  - name: password_hash
    type: string
    serialization:
      omit_response: true     # never returned in GET/list/create/update responses
      omit_log: true          # never written to logs
```

---

## Timestamps + soft delete together

```yaml
entity:
  name: Post
  timestamps: true        # adds created_at, updated_at
  soft_delete: true       # adds deleted_at; all queries auto-filter deleted_at IS NULL
```

All list/get queries automatically exclude soft-deleted rows. No endpoint configuration needed.

---

## CORS for a frontend on a different origin

```yaml
server:
  allowed_origins:
    - https://app.example.com
    - http://localhost:3000
    - http://localhost:5173
```
