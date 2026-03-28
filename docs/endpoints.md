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

**List:**
```json
{
  "data": [
    {"id": "...", "name": "..."},
    ...
  ],
  "meta": {
    "count": 20,
    "next_cursor": "eyJ..."
  }
}
```

**Single item (get, create, update):**
```json
{
  "data": {"id": "...", "name": "..."}
}
```

**Error:**
```json
{
  "error": "authentication required"
}
```

## HTTP status codes

| Operation | Success | Notes |
|---|---|---|
| `list` | 200 | |
| `get` | 200 | 404 if not found |
| `create` | 201 | |
| `update` | 200 | 404 if not found |
| `delete` | 204 | No body |

Error codes: 400 (bad request / validation), 401 (unauthenticated), 403 (forbidden), 404 (not found), 500 (server error).

## `list` options

```yaml
list:
  allow_filter_by: [status, author_id]   # query params allowed as WHERE filters
  allow_sort_by: [created_at, title]     # query params allowed as ORDER BY columns
  default_sort: created_at:desc          # default sort (format: column:asc or column:desc)
  pagination:
    style: cursor                        # only "cursor" is supported
    default_limit: 20
    max_limit: 100
  include: [author, tags]               # relations to eager-load
  auth:
    require: false
```

Clients filter and sort by passing query parameters:

```
GET /posts?status=published&sort=title:asc&limit=10
GET /posts?cursor=eyJ...&limit=10
```

Only columns listed in `allow_filter_by` and `allow_sort_by` are accepted — any other value returns 400. This prevents both SQL injection and unindexed queries.

## `get` options

```yaml
get:
  include: [author, tags, comments]   # relations to eager-load
  auth:
    require: false
```

## `create` options

```yaml
create:
  auth:
    require: true
    roles: [editor, admin]
```

The request body is `application/json`. All entity fields can be set on create (there is no field whitelist for create — use serialization if needed).

## `update` options

```yaml
update:
  allowed_fields: [title, body, status]   # mass-assignment protection whitelist
  auth:
    require: true
    roles: [editor, admin]
```

`allowed_fields` is a security-critical whitelist. Only the listed fields can be changed via PATCH. Fields not in the list are silently ignored from the request body. Without this whitelist, any field on the entity can be updated — which can allow callers to escalate privileges (e.g. by updating a `role` field).

## `delete` options

```yaml
delete:
  soft_delete: true    # sets deleted_at instead of removing the row
  auth:
    require: true
    roles: [admin]
```

`soft_delete: true` on the endpoint requires `soft_delete: true` on the entity. Hard deletes issue a real `DELETE FROM` statement.

## `auth` object

Used at top-level and per-operation:

```yaml
auth:
  require: true          # false = public access; true = JWT required
  roles: [admin, editor] # optional; if set, role must be in this list (AND passes Casbin)
```

When `roles:` is omitted, any authenticated user is allowed (subject only to Casbin enforcement). When `roles:` is set, the JWT `role` claim must match one of the listed values.

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

### `specs` field reference

| Field | Type | Default | Description |
|---|---|---|---|
| `names` | list | all specs | Restrict this endpoint to only the named specs |
| `description` | string | — | Operation description in the spec |
| `tags` | list | — | Extra tags; entity name is always the first tag |
| `summary` | string | `"{op} {Entity}"` | Short operation summary |

Each CRUD operation on the endpoint gets its own Operation in the spec. Tags are shared across all operations generated from the same endpoint block.

## Complete examples

See the community-blog example:

- [`endpoints/posts.yaml`](../examples/community-blog/endpoints/posts.yaml)
- [`endpoints/comments.yaml`](../examples/community-blog/endpoints/comments.yaml)
- [`endpoints/tags.yaml`](../examples/community-blog/endpoints/tags.yaml)
- [`endpoints/users.yaml`](../examples/community-blog/endpoints/users.yaml)

