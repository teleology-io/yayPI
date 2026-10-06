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
    max_body_size: 64MB    # optional: raise server.max_request_body_size (e.g. uploads)

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
| `replace` | `PUT` | `/path/{id}` |
| `delete` | `DELETE` | `/path/{id}` |

The `{id}` path parameter is validated against the primary key type (UUID or integer) before reaching the handler. `replace` (PUT) is a full update: writable nullable fields you omit are set to null. It uses the `update:` options and auth.

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
  "data": [{"id": "...", "name": "..."}],
  "meta": { "count": 20, "limit": 20, "has_more": true, "next_cursor": "eyJ..." }
}
```

**Offset-paginated list:**
```json
{
  "data": [],
  "meta": { "count": 20, "limit": 20, "has_more": true, "offset": 0, "page": 1, "total": 312 }
}
```

**Single item (get, create, update, replace):**
```json
{ "data": {"id": "...", "name": "..."} }
```

**Errors** — every error uses one envelope:
```json
{
  "error": "validation failed",
  "code": "validation_failed",
  "request_id": "6f1c…",
  "errors": { "email": "email must be a valid email address" }
}
```

`code` is stable and meant for programs; `errors` appears for validation failures.

## HTTP status codes

| Status | `code` | When |
|---|---|---|
| 200 / 201 / 204 | — | Success (`create` → 201, `delete` → 204) |
| 207 | — | Bulk create in `partial` mode with some failures |
| 304 | — | `GET` with a matching `If-None-Match` |
| 400 | `validation_failed`, `bad_request` | Wrong type, failed rule, bad filter/sort/cursor |
| 401 | `unauthorized` | Missing, invalid or revoked credentials |
| 403 | `forbidden` | Role, condition, row-access or tenant denial |
| 404 | `not_found` | Missing, or hidden by row access |
| 409 | `conflict` | Duplicate unique value; Idempotency-Key request in progress |
| 412 | `precondition_failed` | `If-Match` did not match the current version |
| 413 | `payload_too_large` | Body over `server.max_request_body_size` |
| 415 | `unsupported_media_type` | Write without `Content-Type: application/json` |
| 422 | `reference_violation`, `required_field_missing`, `constraint_violation` | Foreign key / NOT NULL / CHECK; or a hook's `HookError` |
| 429 | `rate_limited` | Rate limit (`Retry-After` header set) |
| 504 | `timeout` | `server.request_timeout` exceeded |

Database error details are logged with the request id and never returned to clients.

## Concurrency and retries

**ETags.** `GET`, `create`, `update` and `replace` return an `ETag`. Send it back as `If-Match` on `PATCH`/`PUT`/`DELETE`: if the record changed since you read it, the write is refused with **412** and nothing changes. `GET` with `If-None-Match` returns **304** when unchanged.

**Idempotency keys.** Send `Idempotency-Key: <unique string>` on `POST` to make retries safe. The first response is stored for 24 hours, and a retry with the same key and body replays it (`Idempotent-Replayed: true`) instead of creating a duplicate. Keys are scoped to the caller and path. Reusing a key with a different body returns 422. A request that is still running returns 409.

## Rate limiting

A token bucket rate limiter can be applied per endpoint. It applies **in addition to** the global `server.rate_limit`: a request must pass both. `key_by: user` limits per authenticated caller and falls back to the client IP for anonymous requests.

```yaml
- path: /auth/register
  entity: User
  crud: [create]
  rate_limit:
    requests_per_minute: 5   # fill rate
    burst: 2                 # bucket capacity (allows short burst above the rate)
    key_by: ip               # ip (default) | user (authenticated subject)
```

Excess requests receive **429 Too Many Requests** with `Retry-After`. Responses carry `X-RateLimit-Limit` and `X-RateLimit-Remaining`. Limits are counted per server instance.

## `list` options

```yaml
list:
  allow_filter_by: [status, author_id]   # fields clients may filter on
  search: [title, body]                  # fields matched by ?q= (case-insensitive substring)
  allow_sort_by: [created_at, title]     # query params allowed as ORDER BY columns
  default_sort: created_at:desc          # default sort (format: column:asc or column:desc)
  pagination:
    style: cursor                        # cursor (default) | offset
    default_limit: 20
    max_limit: 100
    include_total: true                  # add meta.total (a COUNT query)
  include: [author, tags]               # relations clients may request with ?include=
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
GET /posts?score[gte]=10&score[lt]=100          # gt gte lt lte ne
GET /posts?status[in]=draft,review              # in / nin (max 100 values)
GET /posts?title[contains]=postgres             # contains / starts_with (case-insensitive)
GET /posts?published_at[is_null]=false
GET /posts?q=migration                          # full-text-ish search over list.search fields
GET /posts?sort=-created_at&cursor=eyJ...&limit=10
GET /posts?fields=id,title&include=author       # sparse fields + embedded relations
GET /posts?limit=20&offset=40                   # offset pagination
```

Filter values are converted to the field's type (`score[gte]=abc` → 400). Filtering on a field not in `allow_filter_by`, or sorting on one not in `allow_sort_by`, returns 400 rather than silently ignoring the parameter. Unrelated query parameters (e.g. cache busters) are ignored.

`?include=` embeds relations listed in `include:` — `belongs_to`/`has_one` as an object, `has_many`/`many_to_many` as an array — loaded with one batched query per relation. Included rows have the related entity's `omit_response` and `read_roles` applied. They are **not** filtered by the related entity's endpoint `row_access`, so only list relations every caller of this endpoint may see.

### Pagination styles

**Cursor** (default) — keyset pagination on (sort column, primary key) with an HMAC-signed opaque cursor. Every row is returned exactly once, for any sort order and direction, even while rows are inserted. Pass `meta.next_cursor` back as `?cursor=` with the same `sort` (a cursor issued for another sort is rejected). `meta.has_more` is `false` on the last page.

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
  bulk_max: 500             # max items per bulk request (default: 500)
  bulk_error_mode: abort    # abort (default) | partial
  strict: false             # true = unknown body fields are a 400 instead of being dropped
```

### Single create

The request body is `application/json` containing a single object. Every provided field is type-checked and converted to the field's declared type. Server-managed columns can't be set: timestamps, `deleted_at`, `default_from` fields (filled from the caller), and fields whose `access.write_roles` exclude the caller.

### Bulk create

When `bulk: true`, `POST` accepts a JSON array:

```json
[
  {"name": "Widget A", "price": 9.99},
  {"name": "Widget B", "price": 14.99}
]
```

**`bulk_error_mode: abort`** (default) — the whole batch runs in one transaction. Any validation or database error fails the request (400/409/422; `X-Failed-Index` names the failing item) and **no rows are written**.

**`bulk_error_mode: partial`** — processing continues past errors. Returns **207 Multi-Status** with a per-item result array:

```json
{
  "results": [
    { "index": 0, "data": { "id": "...", "name": "Widget A" } },
    { "index": 1, "error": "validation failed", "errors": { "price": "price must be at least 0" } }
  ]
}
```

Each item commits on its own; failed items report a client-safe message.

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

`strict: true` turns unknown body fields into a 400. `allowed_fields` is a security-critical whitelist. Only the listed fields can be changed via PATCH. Fields not in the list are silently ignored. Without this whitelist, any entity field can be updated — which can allow callers to escalate privileges (e.g. by updating a `role` field).

Immutable fields (`immutable: true`), `default_from` fields and the primary key are always stripped from update payloads regardless of `allowed_fields`. Row access is checked, and the row is locked, before `BeforeUpdate` hooks run, so hooks never see rows the caller can't touch.

## `delete` options

```yaml
delete:
  auth:
    require: true
    roles: [admin]
  row_access:          # ABAC: prevents deleting rows the caller can't access
    - when: "subject.role == \"admin\""
      filter: ""
    - when: "*"
      filter: "author_id = :subject.id"
```

Entities with `soft_delete: true` are always soft-deleted (`deleted_at` is set and the row disappears from reads); other entities are hard-deleted. `delete.soft_delete: true` on an entity without `soft_delete` is a configuration error. `BeforeDelete` hooks only run for rows that exist and that the caller may delete.

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
| `roles` | Allowlist of roles. The subject's role must match one, or 403. Always enforced, with or without a Casbin policy, and implies `require: true`. |
| `conditions` | CEL-lite expressions evaluated against the subject. All must pass (AND), or 403. Implies `require: true`. |

If `require: true` is set but no JWT secret or API keys are configured, the route rejects every request (fail closed) rather than being open.

**Condition operators:** `==`, `!=`, `>`, `<`, `>=`, `<=`, `in`, `not_in`, `starts_with`, `ends_with`, `*` (always true)

**Subject attributes:** `subject.id` (JWT `sub` or API key subject), `subject.role`, `subject.email`, `subject.tenant` (JWT `tenant` claim)

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
| `:subject.tenant` | JWT `tenant` |

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
