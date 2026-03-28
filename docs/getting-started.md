# Getting Started

This guide walks you from zero to a running REST API in about 10 minutes.

## Prerequisites

- **Go 1.23+** — `go version`
- **PostgreSQL** — running locally or accessible via URL
- **yayPi** installed — `go install github.com/csullivan/yaypi/cmd/yaypi@latest`

## 1. Scaffold a new project

```bash
yaypi init my-api
cd my-api
```

This creates:

```
my-api/
├── yaypi.yaml          # main config
├── entities/           # entity definitions go here
├── endpoints/          # endpoint definitions go here
└── policies/
    └── model.conf      # Casbin RBAC model (pre-populated)
```

## 2. Configure your environment

Copy the example env file and fill in your values:

```bash
cp .env.example .env
```

Or export directly:

```bash
export DATABASE_URL=postgres://localhost/my_api
export JWT_SECRET=a-long-random-string-change-this-in-production
```

The generated `yaypi.yaml` uses `${DATABASE_URL}` and `${JWT_SECRET}` so you never need to hard-code secrets. See [Project Config](project-config.md) for all options.

## 3. Define an entity

Create `entities/item.yaml`:

```yaml
version: "1"
kind: entity

entity:
  name: Item
  table: items
  timestamps: true
  soft_delete: true

  fields:
    - name: id
      type: uuid
      primary_key: true
      default: gen_random_uuid()

    - name: name
      type: string
      length: 255
      nullable: false

    - name: description
      type: text
      nullable: true

    - name: price
      type: decimal
      precision: 10
      scale: 2
      nullable: false
      default: "0.00"

    - name: published
      type: boolean
      default: "false"
      nullable: false
```

See [Entities](entities.md) for all field types and options.

## 4. Define an endpoint

Create `endpoints/items.yaml`:

```yaml
version: "1"
kind: endpoints

endpoints:
  - path: /items
    entity: Item
    crud: [list, create]

    list:
      allow_filter_by: [published]
      allow_sort_by: [name, price, created_at]
      default_sort: created_at:desc
      pagination:
        style: cursor
        default_limit: 20
        max_limit: 100
      auth:
        require: false   # public read

    create:
      auth:
        require: true    # must be authenticated to create

  - path: /items/{id}
    entity: Item
    crud: [get, update, delete]

    get:
      auth:
        require: false   # public read

    update:
      allowed_fields: [name, description, price, published]
      auth:
        require: true

    delete:
      soft_delete: true
      auth:
        require: true
```

See [Endpoints](endpoints.md) for all options.

## 5. Validate your config

```bash
yaypi validate
```

Expected output:
```
INF configuration is valid
```

If there are errors they are shown with the file path and a description.

## 6. Generate and run migrations

```bash
# Generate SQL migration files from your entity definitions
yaypi migrate generate --name create_items

# Review the generated files in migrations/
cat migrations/*_create_items.up.sql

# Apply them
yaypi migrate up
```

The migration engine diffs your entity definitions against the live database and generates `CREATE TABLE` and `CREATE INDEX` statements. See [Migrations](migrations.md) for details.

## 7. Start the server

```bash
yaypi run
```

Expected output:
```
INF server starting addr=:8080 base_url=/api/v1
```

## 8. Test it

**List items (public):**
```bash
curl http://localhost:8080/api/v1/items
```
```json
{"data": [], "meta": {"count": 0, "next_cursor": null}}
```

**Create an item (unauthenticated → 401):**
```bash
curl -X POST http://localhost:8080/api/v1/items \
  -H "Content-Type: application/json" \
  -d '{"name": "Widget", "price": "9.99"}'
```
```json
{"error": "authentication required"}
```

**Create an item (authenticated → 201):**

yayPi does not issue tokens — you need to mint a JWT yourself. The token must contain `sub` (user ID), `role`, and `email` claims. The easiest way to test is [jwt.io](https://jwt.io):

1. Go to jwt.io
2. Set algorithm to **HS256**
3. Set payload:
   ```json
   {
     "sub": "user-123",
     "role": "admin",
     "email": "you@example.com",
     "exp": 9999999999
   }
   ```
4. Set secret to match `JWT_SECRET`
5. Copy the encoded token

```bash
TOKEN=<paste token here>

curl -X POST http://localhost:8080/api/v1/items \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d '{"name": "Widget", "price": "9.99", "published": false}'
```
```json
{"data": {"id": "...", "name": "Widget", "price": "9.99", "published": false, ...}}
```

**Get a single item (public):**
```bash
curl http://localhost:8080/api/v1/items/<id>
```

## Next steps

- Add roles and access control → [Authorization](authorization.md)
- Define relationships between entities → [Entities](entities.md)
- Schedule background jobs → [Jobs](jobs.md)
- Write custom logic with plugins → [Plugins](plugins.md)
- See the full community-blog example → [`examples/community-blog/`](../examples/community-blog/)
