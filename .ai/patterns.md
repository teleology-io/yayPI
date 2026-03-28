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

Flow: `GET /auth/oauth2/google` → provider → `GET /auth/callback/google` → JWT response.

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
