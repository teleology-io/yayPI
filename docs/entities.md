# Entities

Entities describe your data model. Each entity maps to a database table and defines its fields, relationships, indexes, constraints, and lifecycle hooks.

## File structure

```yaml
version: "1"
kind: entity

entity:
  name: Post          # PascalCase; referenced by endpoints and relations
  table: posts        # snake_case table name
  database: primary   # optional; defaults to the default database
  timestamps: true    # adds created_at and updated_at columns
  soft_delete: true   # adds deleted_at column; deletes set it instead of removing rows

  fields: [...]
  relations: [...]
  indexes: [...]
  constraints: [...]
  hooks: {...}
```

## Entity-level options

| Field | Type | Default | Description |
|---|---|---|---|
| `name` | string | — | Entity name used in endpoints, relations, and RBAC |
| `table` | string | — | Database table name |
| `database` | string | default DB | Which database from `yaypi.yaml` to use |
| `timestamps` | boolean | `false` | Auto-add `created_at` and `updated_at` (both `timestamptz`) |
| `soft_delete` | boolean | `false` | Auto-add `deleted_at timestamptz`; filters all queries |

## Field types

| YAML type | PostgreSQL type | Notes |
|---|---|---|
| `uuid` | `uuid` | Use with `default: gen_random_uuid()` for auto IDs |
| `string` | `varchar(255)` | Override with `length:` |
| `text` | `text` | Unbounded string |
| `integer` | `integer` | 32-bit signed |
| `bigint` | `bigint` | 64-bit signed |
| `float` | `double precision` | |
| `decimal` | `numeric` | Use `precision:` and `scale:` |
| `boolean` | `boolean` | |
| `timestamptz` | `timestamptz` | Always store timestamps with timezone |
| `date` | `date` | Date only, no time |
| `jsonb` | `jsonb` | Arbitrary JSON; use for flexible metadata |
| `enum` | `text` + CHECK | Values listed in `values:`; stored as text |
| `array` | `text[]` | Array of text values |
| `bytea` | `bytea` | Binary data |

## Field options

| Option | Type | Description |
|---|---|---|
| `name` | string | Field name (camelCase → snake_case column) |
| `type` | string | One of the types above |
| `length` | integer | Override length for `string` type |
| `precision` | integer | Total digits for `decimal` |
| `scale` | integer | Decimal digits for `decimal` |
| `nullable` | boolean | Allow NULL (default: `false`) |
| `unique` | boolean | Add UNIQUE constraint |
| `primary_key` | boolean | Mark as primary key |
| `default` | string | SQL default expression (e.g. `gen_random_uuid()`, `"0"`, `"'draft'"`) |
| `index` | boolean | Shorthand for a single-column btree index |
| `immutable` | boolean | Once set on create, cannot be changed via update |
| `values` | list | Enum values (required when `type: enum`) |
| `references` | object | Foreign key definition |
| `serialization` | object | Controls API response and log behavior |
| `access` | object | ABAC: per-role read and write restrictions (opt-in) |
| `validate` | object | Validation rules run on create and update |

### Column naming

Field names are automatically converted to snake_case for column names:

- `authorId` → `author_id`
- `createdAt` → `created_at`
- `viewCount` → `view_count`

### The `default` field and quoting

SQL defaults are passed through verbatim. This means:

- Numbers: `default: "0"` → `DEFAULT 0`
- Booleans: `default: "false"` → `DEFAULT false`
- String literals: `default: "'draft'"` → `DEFAULT 'draft'` (note the inner single quotes)
- Functions: `default: gen_random_uuid()` → `DEFAULT gen_random_uuid()`

If you forget the inner quotes on a string default, PostgreSQL will treat it as a column reference and error.

## Field validation

The `validate` block defines rules that are checked on every create and update request, before any database write. Validation failures return **422 Unprocessable Entity** with a field-keyed error map.

```yaml
fields:
  - name: email
    type: string
    validate:
      required: true          # must be present and non-empty
      format: email           # must be a valid email address
      message: "must be a valid email address"  # override default error message

  - name: username
    type: string
    validate:
      required: true
      min_length: 3           # minimum character count
      max_length: 30          # maximum character count
      pattern: "^[a-z0-9_]+$"   # Go regex the value must match

  - name: price
    type: decimal
    validate:
      min: 0.01               # minimum numeric value (inclusive)
      max: 99999.99           # maximum numeric value (inclusive)
```

### Validation options

| Option | Type | Applies to | Description |
|---|---|---|---|
| `required` | boolean | all | Field must be present and non-empty |
| `min_length` | integer | string, text | Minimum character count |
| `max_length` | integer | string, text | Maximum character count |
| `min` | float | integer, bigint, float, decimal | Minimum value (inclusive) |
| `max` | float | integer, bigint, float, decimal | Maximum value (inclusive) |
| `pattern` | string | string, text | Go regular expression the value must match |
| `format` | string | string, text | Built-in format check (see below) |
| `message` | string | all | Custom error message overriding the default |

### Built-in formats

| `format` | What it validates |
|---|---|
| `email` | Valid email address (`user@domain.tld`) |
| `url` | Valid URL with `http://` or `https://` scheme |
| `uuid` | UUID v4 format |
| `slug` | Lowercase alphanumeric and hyphens only (`^[a-z0-9-]+$`) |

### Validation response

```json
{
  "errors": {
    "email": "must be a valid email address",
    "username": "must be 3–30 lowercase letters, numbers, or underscores",
    "price": "must be between 0.01 and 99999.99"
  }
}
```

## Immutable fields

Fields marked `immutable: true` are silently stripped from `PATCH` (update) payloads. They can be set during `POST` (create) but cannot be changed afterward.

```yaml
- name: order_number
  type: string
  immutable: true

- name: created_by
  type: uuid
  references:
    entity: User
    field: id
  immutable: true
```

No error is returned if an immutable field appears in an update request body — the value is silently ignored.

## References (foreign keys)

```yaml
- name: author_id
  type: uuid
  nullable: false
  references:
    entity: User      # the entity name (not the table name)
    field: id
    on_delete: CASCADE
    on_update: NO ACTION
```

| `on_delete` / `on_update` | Behavior |
|---|---|
| `CASCADE` | Propagate delete/update to child rows |
| `SET NULL` | Set FK column to NULL when parent is deleted |
| `RESTRICT` | Prevent delete/update if child rows exist |
| `NO ACTION` | Like RESTRICT but checked at end of transaction |

The migration engine generates the FK constraint as `fk_{table}_{column}`.

## Serialization

```yaml
- name: password_hash
  type: string
  serialization:
    omit_response: true   # never included in API responses
    omit_log: true        # never included in log output
```

| Option | Effect |
|---|---|
| `omit_response` | Field is stripped from all API responses (GET, LIST, CREATE, UPDATE) |
| `omit_log` | Field is stripped from structured log entries |

Use `omit_response: true` for password hashes, internal flags, or any field that should never be exposed to API consumers.

## Field access control (ABAC)

The `access` key restricts which roles can read or write a field. It is **opt-in** — omitting `access` entirely means the field is fully unrestricted.

```yaml
- name: internal_notes
  type: text
  nullable: true
  access:
    read_roles: [admin, editor]   # members & unauthenticated callers get this field stripped
    write_roles: [admin]          # only admins may set this field; others' values are ignored
```

| Key | Behavior |
|---|---|
| `read_roles` | Field is removed from all API responses for callers whose role is not in the list. Unauthenticated callers are treated as having an empty role. |
| `write_roles` | Field is silently ignored in `create` and `update` request bodies for callers not in the list. No error is returned — the field is simply dropped. |

Both keys are optional independently. You can restrict reads without restricting writes and vice versa.

See [Authorization](authorization.md) for the full ABAC reference.

## Relations

Relations define how entities are connected at runtime for eager loading via `include:` on endpoints. They do **not** create database constraints — use `references:` on a field for the FK constraint.

```yaml
relations:
  - name: author
    type: belongs_to
    entity: User
    foreign_key: author_id     # column on this entity

  - name: comments
    type: has_many
    entity: Comment
    foreign_key: post_id       # column on Comment pointing to Post

  - name: tags
    type: many_to_many
    entity: Tag
    through: PostTag           # junction entity name
    foreign_key: post_id       # column on PostTag pointing to Post
    other_key: tag_id          # column on PostTag pointing to Tag
```

| Relation type | Description |
|---|---|
| `belongs_to` | This entity holds the FK column |
| `has_one` | The other entity holds the FK column; returns one record |
| `has_many` | The other entity holds the FK column; returns many records |
| `many_to_many` | Uses a junction entity; requires `through`, `foreign_key`, `other_key` |

### Self-referential relations

For threaded comments or tree hierarchies, an entity can relate to itself:

```yaml
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

## Indexes

```yaml
indexes:
  # Named multi-column index
  - name: idx_posts_author_status
    columns: [author_id, status]

  # Unique index
  - name: idx_posts_slug
    columns: [slug]
    unique: true

  # Specify index type (default: btree)
  - name: idx_events_created_at
    columns: [created_at]
    type: brin        # good for append-only time-series data
```

Index types: `btree` (default), `brin`, `hash`.

The migration engine generates all indexes with `CREATE INDEX CONCURRENTLY` so they can be added to live tables without locking.

Shorthand: add `index: true` to a field definition to create a single-column btree index automatically.

## Constraints

```yaml
constraints:
  # CHECK constraint
  - name: chk_posts_view_count
    type: check
    check: view_count >= 0

  # Multi-column UNIQUE constraint
  - name: uq_post_tags
    type: unique
    columns: [post_id, tag_id]
```

## Hooks

Hooks connect entity lifecycle events to plugins. See [Plugins](plugins.md) for how to write a plugin.

```yaml
hooks:
  before_create: [hash-password]
  after_create:  [send-welcome-email]
  before_update: [hash-password]
  after_update:  []
  before_delete: []
  after_delete:  [audit-log]
```

Each value is a list of plugin names (matching the `name` in `yaypi.yaml` `plugins:` section).

**Built-in hooks:** Email (`kind: email`) and webhook (`kind: webhooks`) files automatically register their own hooks — you do not need to list them in `entity.hooks`. See [Plugins](plugins.md) for details.

## Complete examples

See these files in `examples/community-blog/`:

- [`entities/user.yaml`](../examples/community-blog/entities/user.yaml) — enum field, serialization, soft delete
- [`entities/post.yaml`](../examples/community-blog/entities/post.yaml) — FK reference, relations, indexes, jsonb
- [`entities/comment.yaml`](../examples/community-blog/entities/comment.yaml) — self-referential relation
- [`entities/post_tag.yaml`](../examples/community-blog/entities/post_tag.yaml) — junction table, composite unique constraint
