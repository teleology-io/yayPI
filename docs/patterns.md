# Patterns Cookbook

Common real-world configurations with explanation and complete YAML.

---

## 1. Public read / authenticated write

The most common pattern: anyone can read, but you must be logged in to create or modify.

```yaml
- path: /posts
  entity: Post
  crud: [list, create]
  auth:
    require: true         # default: auth required

  list:
    auth:
      require: false      # override: no token needed for GET /posts

  create:
    auth:
      require: true
      roles: [editor, admin]

- path: /posts/{id}
  entity: Post
  crud: [get, update, delete]
  auth:
    require: true

  get:
    auth:
      require: false      # GET /posts/{id} is public

  update:
    allowed_fields: [title, body, status]
    auth:
      require: true
      roles: [editor, admin]

  delete:
    soft_delete: true
    auth:
      require: true
      roles: [admin]
```

---

## 2. Admin-only endpoint

Only users with the `admin` role can access this endpoint at all.

```yaml
- path: /users
  entity: User
  crud: [list, create]
  auth:
    require: true
    roles: [admin]

- path: /users/{id}
  entity: User
  crud: [get, update, delete]
  auth:
    require: true
    roles: [admin]

  get:
    auth:
      require: false      # public profiles — override the admin-only default
```

---

## 3. Any authenticated user (no role restriction)

Any valid JWT or API key is accepted. The role in the token is not checked by the endpoint.

```yaml
create:
  auth:
    require: true    # roles: omitted — any role is accepted
```

This is common for comment creation: any logged-in user can comment regardless of role.

---

## 4. Soft delete with public read

Soft-deleted records are invisible to all API operations automatically. No special endpoint config needed beyond `soft_delete: true` on both the entity and the delete operation.

**Entity:**
```yaml
entity:
  name: Post
  soft_delete: true   # adds deleted_at column
```

**Endpoint:**
```yaml
delete:
  soft_delete: true   # sets deleted_at instead of DELETE FROM
  auth:
    require: true
    roles: [admin]
```

Soft-deleted posts are excluded from `GET /posts`, `GET /posts/{id}`, and all updates automatically.

---

## 5. Field validation

```yaml
entity:
  name: Product
  fields:
    - name: name
      type: string
      validate:
        required: true
        min_length: 2
        max_length: 100

    - name: sku
      type: string
      validate:
        required: true
        pattern: "^[A-Z0-9-]+$"
        message: "SKU must be uppercase letters, numbers, and hyphens only"

    - name: price
      type: decimal
      validate:
        min: 0.01
        max: 99999.99

    - name: website
      type: string
      nullable: true
      validate:
        format: url
        message: "must be a valid https:// URL"
```

Validation errors return 422 with a field-keyed map:
```json
{ "errors": { "sku": "SKU must be uppercase letters, numbers, and hyphens only" } }
```

---

## 6. Immutable fields

Fields you want to set once and never change:

```yaml
entity:
  name: Order
  fields:
    - name: order_number
      type: string
      immutable: true        # silently ignored on PATCH

    - name: created_by
      type: uuid
      immutable: true
```

---

## 7. Per-endpoint rate limiting

Protect sensitive endpoints like registration and login from abuse:

```yaml
- path: /auth/register
  entity: User
  crud: [create]
  rate_limit:
    requests_per_minute: 5
    burst: 2
    key_by: ip

- path: /auth/login
  entity: User
  crud: [create]
  rate_limit:
    requests_per_minute: 10
    burst: 3
    key_by: ip
```

---

## 8. Offset pagination with total count

Good for admin dashboards that need "page N of M":

```yaml
list:
  pagination:
    style: offset
    default_limit: 25
    max_limit: 100
    include_total: true   # adds "total" to meta (extra COUNT(*) query)
```

Response:
```json
{
  "data": [...],
  "meta": { "count": 25, "limit": 25, "offset": 0, "page": 1, "total": 312 }
}
```

Navigate: `GET /posts?limit=25&offset=25` for page 2.

---

## 9. Bulk create (all-or-nothing)

```yaml
- path: /products/import
  entity: Product
  crud: [create]
  create:
    bulk: true
    bulk_max: 500
    bulk_error_mode: abort    # stop on first error
    auth:
      require: true
      roles: [admin]
```

POST body: `[{"name":"A","price":9.99}, {"name":"B","price":14.99}]`

---

## 10. Bulk create (partial success)

Good for import flows where you want to commit valid rows and report failures:

```yaml
create:
  bulk: true
  bulk_max: 1000
  bulk_error_mode: partial  # continue; return 207 Multi-Status
```

Response:
```json
{
  "results": [
    { "index": 0, "data": { "id": "...", "name": "A" } },
    { "index": 1, "error": "price: must be greater than 0.01" }
  ]
}
```

---

## 11. Token refresh

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
    expiry: 30d       # refresh token TTL
    store: cookie     # HttpOnly cookie (best for web apps)
```

For native/mobile apps, use `store: body`:
```yaml
  refresh:
    enabled: true
    expiry: 90d
    store: body
```

---

## 12. API key authentication (static)

```yaml
# yaypi.yaml
auth:
  secret: ${JWT_SECRET}
  api_keys:
    header: X-API-Key
    keys:
      - key: ${ADMIN_API_KEY}
        role: admin
      - key: ${CI_API_KEY}
        role: service
```

API keys and JWTs are OR-logic — either authenticates the request.

---

## 13. API key authentication (DB-backed, revocable)

```yaml
# yaypi.yaml
auth:
  api_keys:
    entity: ApiKey
    key_field: token
    role_field: role

# Entity
entity:
  name: ApiKey
  soft_delete: true           # soft-delete to revoke without data loss
  fields:
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

## 14. Email notification on signup

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

      Thanks for signing up. Your account is ready.
```

Add to `include:` in `yaypi.yaml`:
```yaml
include:
  - emails/**/*.yaml
```

---

## 15. Webhook on order creation

```yaml
# webhooks/fulfillment.yaml
version: "1"
kind: webhooks
webhooks:
  - entity: Order
    trigger: after_create
    url: "https://fulfillment.example.com/hooks"
    method: POST
    headers:
      Authorization: "Bearer ${FULFILLMENT_SECRET}"
    payload: |
      {"order_id": "{{record.id}}", "total": "{{record.total}}"}
    retry:
      max_attempts: 3
      backoff: 5s
```

---

## 16. Seed data for lookup tables

```yaml
# seeds/roles.yaml
version: "1"
kind: seed
seeds:
  - entity: Role
    key_field: name
    data:
      - name: admin
        description: "Full administrative access"
      - name: editor
        description: "Can create and edit content"
      - name: member
        description: "Standard member access"
```

Rows are skipped if they already exist — safe to run repeatedly.

---

## 17. Filtering related records via query param

Instead of nested routes (`/posts/{id}/comments`), use a flat path with a filter parameter.

```yaml
- path: /comments
  entity: Comment
  crud: [list, create]

  list:
    allow_filter_by: [post_id, author_id, parent_id]
    auth:
      require: false
```

**Usage:**
```bash
GET /comments?post_id=<uuid>
GET /comments?parent_id=<uuid>    # threaded replies
```

---

## 18. Protecting a sensitive field

```yaml
- name: password_hash
  type: string
  serialization:
    omit_response: true   # never in GET/LIST/CREATE/UPDATE responses
    omit_log: true        # never in structured log output
```

---

## 19. Many-to-many with junction table

**`post_tag.yaml` — junction entity:**
```yaml
entity:
  name: PostTag
  table: post_tags

  fields:
    - name: post_id
      type: uuid
      primary_key: true
      references:
        entity: Post
        field: id
        on_delete: CASCADE

    - name: tag_id
      type: uuid
      primary_key: true
      references:
        entity: Tag
        field: id
        on_delete: CASCADE

  constraints:
    - name: uq_post_tags
      type: unique
      columns: [post_id, tag_id]
```

**`post.yaml` — relation:**
```yaml
relations:
  - name: tags
    type: many_to_many
    entity: Tag
    through: PostTag
    foreign_key: post_id
    other_key: tag_id
```

**Endpoint — eager-load tags on list:**
```yaml
list:
  include: [tags]
```

---

## 20. Self-referential hierarchy (threaded comments)

```yaml
entity:
  name: Comment
  fields:
    - name: parent_id
      type: uuid
      nullable: true
      references:
        entity: Comment
        field: id
        on_delete: CASCADE

  relations:
    - name: parent
      type: belongs_to
      entity: Comment
      foreign_key: parent_id

    - name: replies
      type: has_many
      entity: Comment
      foreign_key: parent_id
```

---

## 21. Multiple databases

```yaml
# yaypi.yaml
databases:
  - name: primary
    dsn: ${DATABASE_URL}
    default: true

  - name: analytics
    dsn: ${ANALYTICS_DATABASE_URL}
    read_only: true
```

Entity opts in:
```yaml
entity:
  name: PageView
  database: analytics
```

---

## 22. Scheduled data maintenance

```yaml
jobs:
  - name: purge-soft-deleted-posts
    schedule: "@daily"
    handler: sql
    config:
      sql: |
        DELETE FROM posts
        WHERE deleted_at IS NOT NULL
          AND deleted_at < now() - INTERVAL '90 days'
      database: primary
```

---

## 23. Row ownership — users can only modify their own records

```yaml
- path: /post/{id}
  entity: Post
  crud: [update, delete]

  update:
    allowed_fields: [title, slug, excerpt, body, status, published_at]
    auth:
      require: true
      roles: [member, editor, admin]
    row_access:
      - when: "subject.role == \"admin\""
        filter: ""                             # admins: no restriction
      - when: "*"
        filter: "author_id = :subject.id"     # everyone else: only own posts

  delete:
    soft_delete: true
    auth:
      require: true
      roles: [member, editor, admin]
    row_access:
      - when: "subject.role == \"admin\""
        filter: ""
      - when: "*"
        filter: "author_id = :subject.id"
```

Non-owners get **404** (not 403), which avoids leaking existence information.

---

## 24. Role-based row visibility for lists

Admins see all; editors see own + published; everyone else sees published only.

```yaml
list:
  auth:
    require: false   # public endpoint — subject may be nil
  row_access:
    - when: "subject.role == \"admin\""
      filter: ""
    - when: "subject.role == \"editor\""
      filter: "status = 'published' OR author_id = :subject.id"
    - when: "*"
      filter: "status = 'published'"
```

---

## 25. Attribute condition — restrict by email domain

```yaml
create:
  auth:
    require: true
    roles: [editor, admin]
    conditions:
      - subject.email ends_with "@acme.com"
      - subject.role != "suspended"
```

---

## 26. Field-level access — admin-only fields

```yaml
fields:
  - name: internal_notes
    type: text
    nullable: true
    access:
      read_roles: [admin]
      write_roles: [admin]

  - name: flagged
    type: boolean
    default: "false"
    access:
      read_roles: [admin, editor]
      write_roles: [admin]
```

---

## 27. Self-service profile update

Users can edit their own profile; admins can edit any user. Only admins can change `role`.

**Entity:**
```yaml
fields:
  - name: role
    type: enum
    values: [admin, editor, member]
    access:
      write_roles: [admin]
```

**Endpoint:**
```yaml
update:
  allowed_fields: [display_name, bio, avatar_url, role]
  auth:
    require: true
  row_access:
    - when: "subject.role == \"admin\""
      filter: ""
    - when: "*"
      filter: "id = :subject.id"
```

---

## 28. Health and readiness probes (Kubernetes)

```yaml
# yaypi.yaml
server:
  health:
    enabled: true
    path: /health         # liveness probe — always 200
    readiness_path: /ready # readiness probe — 503 if DB unreachable
```

Kubernetes pod spec:
```yaml
livenessProbe:
  httpGet:
    path: /health
    port: 8080
readinessProbe:
  httpGet:
    path: /ready
    port: 8080
```

Both endpoints are mounted outside the `base_url` prefix.

---

## 29. Composite primary key (junction table)

```yaml
entity:
  name: PostTag
  table: post_tags

  fields:
    - name: post_id
      type: uuid
      primary_key: true

    - name: tag_id
      type: uuid
      primary_key: true
```

---

## 30. JSONB metadata field

```yaml
- name: metadata
  type: jsonb
  nullable: true
```

Clients can set and read arbitrary JSON:
```json
{ "metadata": { "source": "import", "tags": ["featured"] } }
```
