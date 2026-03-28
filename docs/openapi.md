# OpenAPI Spec Generation

yayPi automatically generates [OpenAPI 3.1](https://spec.openapis.org/oas/v3.1.0) specs from your entity and endpoint YAML files — no code required.

## Quick start

1. Define one or more named specs in `yaypi.yaml`:

```yaml
spec:
  - name: api
    title: "Blog API"
    description: "Public REST API"
    version: "1.0.0"
    servers:
      - url: https://api.example.com
        description: Production
      - url: http://localhost:8080
        description: Local
```

2. Start the server — the spec is immediately available:

```bash
curl http://localhost:8080/api/v1/openapi/api.json
```

3. Or generate a file for use with external tools:

```bash
yaypi spec generate --name api --output openapi.json
```

---

## Named specs

You can define multiple independent specs — for example a public-facing `api` spec and a more detailed `sdk` spec used for client generation:

```yaml
spec:
  - name: api
    title: "Public API"
    version: "1.0.0"

  - name: sdk
    title: "SDK — Full Schema"
    version: "1.0.0"
    servers:
      - url: https://api.example.com
```

Each spec is served at its own URL:
- `GET {base_url}/openapi/api.json`
- `GET {base_url}/openapi/sdk.json`

### `spec[]` field reference

| Field | Type | Description |
|---|---|---|
| `name` | string | Unique identifier — used in the URL path (`/openapi/{name}.json`) |
| `title` | string | API title in the OpenAPI `info` block |
| `description` | string | API description |
| `version` | string | API version string (e.g. `"1.0.0"`) |
| `servers[].url` | string | Server base URL |
| `servers[].description` | string | Server label (e.g. `"Production"`, `"Local"`) |

---

## Default behavior

All endpoints are included in all specs automatically. No per-endpoint configuration is needed for the common case.

**Tags** are assigned to every operation automatically:
1. The entity name (e.g. `Post`) — always the first tag
2. The project name from `project.name` — appended as a second tag

This gives you two useful groupings out of the box in tools like Swagger UI and Redoc.

**Schemas** are built from entity field definitions and placed in the spec's `components/schemas` section. Each entity gets one reusable schema, referenced by `$ref` in operation responses.

**Auth** — when `auth.secret` is set in `yaypi.yaml`, a `bearerAuth` HTTP Bearer security scheme is added to every spec. Operations that require authentication (`auth.require: true`) automatically get `security: [{bearerAuth: []}]` added.

---

## Per-endpoint overrides

### Exclude an endpoint from all specs

```yaml
- path: /internal/health
  entity: HealthCheck
  crud: [get]
  spec: false    # excluded from every spec
```

### Restrict to specific specs

```yaml
- path: /admin/users
  entity: User
  crud: [list, create, update, delete]
  specs:
    names: [sdk]    # only in "sdk" spec; not in "api"
```

### Add documentation metadata

```yaml
- path: /posts
  entity: Post
  crud: [list, create]
  specs:
    names: [api, sdk]
    description: "Manage blog posts. Listing is public; creating requires authentication."
    tags: [posts, content]         # prepended after the entity name
    summary: "List or create posts"
```

### `specs` field reference

| Field | Type | Default | Description |
|---|---|---|---|
| `names` | list | all specs | Restrict this endpoint to only the named spec(s) |
| `description` | string | — | Operation description (shared across all CRUD ops from this block) |
| `tags` | list | `[project.name]` | Extra tags appended after the entity name |
| `summary` | string | `"{op} {Entity}"` | Short summary; e.g. `"List Post"`, `"Create Post"` |

---

## What gets generated

For each CRUD operation on each included endpoint:

| Operation | HTTP | Path | Generated |
|---|---|---|---|
| `list` | GET | `/path` | Query params: filter fields, `sort`, `limit`, `cursor`/`page` |
| `get` | GET | `/path/{id}` | Path param `id`; 200 + 404 responses |
| `create` | POST | `/path` | Request body with required writable fields; 201 + 422 responses |
| `update` | PATCH | `/path/{id}` | Path param `id`; optional request body (respects `allowed_fields`); 200 + 404 |
| `delete` | DELETE | `/path/{id}` | Path param `id`; 204 + 404 responses |

**Entity schemas** in `components/schemas`:
- All fields except those with `serialization.omit_response: true`
- Nullable fields marked `nullable: true`
- `created_at` / `updated_at` marked `readOnly: true` when `timestamps: true`
- `deleted_at` marked `nullable: true, readOnly: true` when `soft_delete: true`
- Required fields = non-nullable, non-PK fields with no default

**Request body schemas** are inline (not `$ref`) so `update`'s schema can be constrained to `allowed_fields`.

**Filter parameters** — each field in `list.allow_filter_by` becomes a query parameter. Fields in `list.allow_sort_by` are collapsed into a single `sort` string parameter.

**Pagination parameters** vary by `pagination.style`:
- `cursor` — `limit` (integer) + `cursor` (string)
- `offset` — `limit` (integer) + `offset` (integer)
- default (page-based) — `limit` (integer) + `page` (integer)

---

## Field type mapping

| yayPi type | OpenAPI type | format |
|---|---|---|
| `uuid` | `string` | `uuid` |
| `string` | `string` | — |
| `text` | `string` | — |
| `integer` | `integer` | `int32` |
| `bigint` | `integer` | `int64` |
| `float` | `number` | `float` |
| `decimal` | `number` | `double` |
| `boolean` | `boolean` | — |
| `timestamptz` | `string` | `date-time` |
| `date` | `string` | `date` |
| `jsonb` | `object` | — |
| `enum` | `string` | — (+ `enum: [values]`) |
| `array` | `array` | items: `string` |
| `bytea` | `string` | `byte` |

---

## Using generated specs

**Swagger UI / Redoc** — point to `http://localhost:8080/api/v1/openapi/api.json`

**OpenAPI Generator** — generate clients in any language:
```bash
yaypi spec generate --name api --output openapi.json
openapi-generator generate -i openapi.json -g typescript-fetch -o ./sdk
```

**Redocly lint** — validate the generated spec:
```bash
yaypi spec generate --name api --output openapi.json
npx @redocly/cli lint openapi.json
```

---

## Full example

`yaypi.yaml`:
```yaml
spec:
  - name: api
    title: "Community Blog API"
    version: "1.0.0"
    servers:
      - url: http://localhost:8080
        description: Local

  - name: sdk
    title: "Community Blog — SDK"
    version: "1.0.0"
```

`endpoints/posts.yaml`:
```yaml
version: "1"
kind: endpoints
endpoints:
  - path: /posts
    entity: Post
    crud: [list, create]
    specs:
      tags: [posts]
      description: "Public listing; create requires editor role"
    list:
      allow_filter_by: [status, author_id]
      allow_sort_by: [created_at, published_at]
      pagination:
        style: cursor
        default_limit: 20

  - path: /post/{id}
    entity: Post
    crud: [get, update, delete]
    specs:
      tags: [posts]

  - path: /internal/jobs
    entity: Job
    crud: [list]
    spec: false    # never in any spec
```

Runtime:
- `GET /api/v1/openapi/api.json` — full spec
- `GET /api/v1/openapi/sdk.json` — SDK spec

CLI:
```bash
yaypi spec generate --name api --output api.json
yaypi spec generate --name sdk --output sdk.json
```
