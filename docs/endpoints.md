# Endpoints

Endpoint files define which REST routes are exposed for each entity and how they behave.

## File structure

```yaml
version: "1"
kind: endpoints

endpoints:
  - path: /posts           # URL path (relative to base_url)
    entity: Post           # must match an entity name exactly
    crud: [list, create]   # operations to register on this path
    auth:                  # top-level auth applies to all operations
      require: true        # unless overridden per operation
    rate_limit:            # optional per-endpoint rate limit
      requests_per_minute: 30
      burst: 10

    list:    {...}
    create:  {...}

  - path: /posts/{id}
    entity: Post
    crud: [get, update, delete]
    auth:
      require: true

    get:     {...}
    update:  {...}
    delete:  {...}
```

One file can contain multiple endpoint blocks. It is conventional to split collection and item endpoints into two blocks as shown above.

## HTTP method mapping

| CRUD operation | HTTP method | Path |
|---|---|---|
| `list` | `GET` | `/path` |
| `create` | `POST` | `/path` |
| `get` | `GET` | `/path/{id}` |
| `update` | `PATCH` | `/path/{id}` |
| `delete` | `DELETE` | `/path/{id}` |

The `{id}` path parameter is validated as a UUID format before reaching the handler.

## Auth inheritance rule

Auth can be set at three levels: top-level, operation-level, or both. The most specific level wins:

```
operation auth  →  wins if present
top-level auth  →  fallback
```

This enables the common "public read / auth write" pattern in a single block:

```yaml
- path: /posts
  entity: Post
  crud: [list, create]
  auth:
    require: true      # default: auth required

  list:
    auth:
      require: false   # override: list is public

  create:
    auth:
      require: true    # inherits from top-level (same result, explicit here)
      roles: [editor, admin]
```

## Response format

**Cursor-paginated list:**
```json
{
  "data": [{"id": "...", "name": "..."}, ...],
  "meta": { "count": 20, "next_cursor": "eyJ..." }
}
```

**Offset-paginated list:**
```json
{
  "data": [...],
  "meta": { "count": 20, "limit": 20, "offset": 0, "page": 1, "total": 312 }
}
```

**Single item (get, create, update):**
```json
{ "data": {"id": "...", "name": "..."} }
```

**Validation error (422):**
```json
{ "errors": { "email": "must be a valid email address" } }
```

**Other error:**
```json
{ "error": "authentication required" }
```

## HTTP status codes

| Operation | Success | Notes |
|---|---|---|
| `list` | 200 | |
| `get` | 200 | 404 if not found |
| `create` | 201 | 422 on validation failure |
| `update` | 200 | 404 if not found; 422 on validation failure |
| `delete` | 204 | No body |
| `create` (bulk, partial) | 207 | Multi-Status with per-item results |

Other error codes: 400 (bad request), 401 (unauthenticated), 403 (forbidden), 429 (rate limited), 500 (server error).

## Rate limiting

A token bucket rate limiter can be applied per-endpoint. It overrides the global `server.rate_limit` for requests to this path.

```yaml
- path: /auth/register
  entity: User
  crud: [create]
  rate_limit:
    requests_per_minute: 5   # fill rate
    burst: 2                 # bucket capacity (allows short burst above the rate)
    key_by: ip               # ip (default) | user (JWT sub)
```

Excess requests receive **429 Too Many Requests**.

## `list` options

```yaml
list:
  allow_filter_by: [status, author_id]   # query params allowed as WHERE filters
  allow_sort_by: [created_at, title]     # query params allowed as ORDER BY columns
  default_sort: created_at:desc          # default sort (format: column:asc or column:desc)
  pagination:
    style: cursor                        # cursor (default) | offset
    default_limit: 20
    max_limit: 100
    include_total: true                  # offset only: include total row count in meta
  include: [author, tags]               # relations to eager-load
  auth:
    require: false
  row_access:                           # ABAC: row-level filter rules (opt-in)
    - when: "subject.role == \"admin\""
      filter: ""                        # empty = no extra WHERE (see all rows)
    - when: "*"
      filter: "status = 'published'"   # catch-all: unauthenticated/other roles see published only
```

Clients filter and sort by passing query parameters:

```
GET /posts?status=published&sort=title:asc&limit=10
GET /posts?cursor=eyJ...&limit=10
GET /posts?limit=20&offset=40        # offset pagination
```

Only columns listed in `allow_filter_by` and `allow_sort_by` are accepted — any other value returns 400. This prevents both SQL injection and unindexed queries.

### Pagination styles

**Cursor** (default) — HMAC-signed opaque cursor. Best for live feeds where rows may be inserted during pagination.

**Offset** — traditional page/offset. Use when clients need to jump to arbitrary pages or display "page N of M". Enable total count (a separate `COUNT(*)` query) with `include_total: true`:

```yaml
pagination:
  style: offset
  default_limit: 25
  max_limit: 100
  include_total: true
```

Response:
```json
{ "meta": { "count": 25, "limit": 25, "offset": 0, "page": 1, "total": 312 } }
```

## `get` options

```yaml
get:
  include: [author, tags, comments]   # relations to eager-load
  auth:
    require: false
  row_access:                         # ABAC: same syntax as list; no match → 404
    - when: "*"
      filter: "status = 'published'"
```

## `create` options

```yaml
create:
  auth:
    require: true
    roles: [editor, admin]
    conditions:                               # ABAC: all must pass; 403 on failure
      - subject.email ends_with "@company.com"
  before_hooks: [validate-post]              # plugin hook names
  after_hooks: [notify-followers]
  bulk: false               # true = accept a JSON array; false (default) = single object
  bulk_max: 100             # max items per bulk request (default: 100)
  bulk_error_mode: abort    # abort (default) | partial
```

### Single create

The request body is `application/json` containing a single object. All entity fields can be set on create (use `serialization` or `access.write_roles` if needed).

### Bulk create

When `bulk: true`, `POST` accepts a JSON array:

```json
[
  {"name": "Widget A", "price": 9.99},
  {"name": "Widget B", "price": 14.99}
]
```

**`bulk_error_mode: abort`** (default) — the first validation or DB error stops the entire batch. Returns 400/422. No rows are written.

**`bulk_error_mode: partial`** — processing continues past errors. Returns **207 Multi-Status** with a per-item result array:

```json
{
  "results": [
    { "index": 0, "data": { "id": "...", "name": "Widget A" } },
    { "index": 1, "error": "price: must be greater than 0" }
  ]
}
```

## `update` options

```yaml
update:
  allowed_fields: [title, body, status]   # mass-assignment protection whitelist
  auth:
    require: true
    roles: [editor, admin]
  row_access:                             # ABAC: no match → 404 (row invisible to caller)
    - when: "subject.role == \"admin\""
      filter: ""                          # admins update anything
    - when: "*"
      filter: "author_id = :subject.id"  # others: only their own rows
```

`allowed_fields` is a security-critical whitelist. Only the listed fields can be changed via PATCH. Fields not in the list are silently ignored. Without this whitelist, any entity field can be updated — which can allow callers to escalate privileges (e.g. by updating a `role` field).

Immutable fields (`immutable: true` on the entity) are always stripped from update payloads regardless of `allowed_fields`.

## `delete` options

```yaml
delete:
  soft_delete: true    # sets deleted_at instead of removing the row
  auth:
    require: true
    roles: [admin]
  row_access:          # ABAC: prevents deleting rows the caller can't access
    - when: "subject.role == \"admin\""
      filter: ""
    - when: "*"
      filter: "author_id = :subject.id"
```

`soft_delete: true` on the endpoint requires `soft_delete: true` on the entity. Hard deletes issue a real `DELETE FROM` statement.

## `auth` object

Used at top-level and per-operation:

```yaml
auth:
  require: true          # false = public access; true = JWT or API key required
  roles: [admin, editor] # ABAC: role must be in this list (enforced, returns 403 if not)
  conditions:            # ABAC: ALL expressions must pass; 403 if any fails
    - subject.email ends_with "@company.com"
    - subject.role in ["editor", "admin"]
```

| Field | Description |
|---|---|
| `require` | `false` = no auth needed (public). `true` = JWT or API key required; returns 401 if absent/invalid. |
| `roles` | Allowlist of roles. The subject's role must match one. Returns 403 if not in list. |
| `conditions` | CEL-lite expressions evaluated against the subject. All must pass (AND). Returns 403 if any fails. |

**Condition operators:** `==`, `!=`, `>`, `<`, `>=`, `<=`, `in`, `not_in`, `starts_with`, `ends_with`, `*` (always true)

**Subject attributes:** `subject.id` (JWT `sub` or API key user ID), `subject.role`, `subject.email`

## `row_access` rules

Used on `list`, `get`, `update`, and `delete` operations:

```yaml
row_access:
  - when: "subject.role == \"admin\""
    filter: ""                           # empty = no extra WHERE condition (unrestricted)
  - when: "subject.role == \"editor\""
    filter: "author_id = :subject.id OR status = 'published'"
  - when: "*"                            # catch-all — always include to avoid accidental 403
    filter: "status = 'published'"
```

Rules are evaluated **in order** — the first matching `when` wins. If no rule matches, the request returns **403** (list/create) or **404** (get/update/delete).

| `when` expression | Same operators as `auth.conditions` |
|---|---|
| `filter` | SQL fragment appended to `WHERE` with `AND`. Empty string = no filter (allow all rows). |

**Bind variables in `filter`:**

| Placeholder | Value |
|---|---|
| `:subject.id` | JWT `sub` or API key subject ID |
| `:subject.role` | JWT `role` or API key role |
| `:subject.email` | JWT `email` |

`row_access` is **opt-in**: omitting it means all rows are accessible. Defining it without a catch-all `when: "*"` means any caller not matched by a rule is denied.

## OpenAPI spec integration

When you define named specs in `yaypi.yaml` (see [OpenAPI](openapi.md)), all endpoints are automatically included in all specs. You can override this per endpoint.

### Exclude an endpoint from all specs

```yaml
- path: /internal/admin
  entity: AdminLog
  crud: [list]
  spec: false    # never appears in any OpenAPI spec
```

### Add metadata or restrict to specific specs

```yaml
- path: /posts
  entity: Post
  crud: [list, create]
  specs:
    names: [api]               # only in the "api" spec; omit to include in all specs
    description: "Manage blog posts"
    tags: [posts, content]     # extra tags; entity name ("Post") is always prepended
    summary: "List or create posts"
```

## Complete examples

See the community-blog example:

- [`endpoints/posts.yaml`](../examples/community-blog/endpoints/posts.yaml)
- [`endpoints/comments.yaml`](../examples/community-blog/endpoints/comments.yaml)
- [`endpoints/tags.yaml`](../examples/community-blog/endpoints/tags.yaml)
- [`endpoints/users.yaml`](../examples/community-blog/endpoints/users.yaml)
