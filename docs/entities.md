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
| `table` | string | — | PostgreSQL table name |
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
| `values` | list | Enum values (required when `type: enum`) |
| `references` | object | Foreign key definition |
| `serialization` | object | Controls API response and log behavior |
| `access` | object | ABAC: per-role read and write restrictions (opt-in) |

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

Use `omit_response: true` for password hashes, internal flags, or any field that should never be exposed to API consumers. Use `omit_log: true` for any sensitive value you don't want appearing in log files.

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

**Relationship to `omit_response`:** `omit_response: true` removes a field from *all* responses unconditionally (e.g. `password_hash`). `access.read_roles` removes it only for *some* callers. Use both if needed:

```yaml
- name: password_hash
  type: string
  serialization:
    omit_response: true    # never in any response — absolute
    omit_log: true

- name: billing_tier
  type: string
  access:
    read_roles: [admin]    # only admins see this; members don't
```

See [Authorization](authorization.md#3c-field-level-access-fieldaccess) for the full ABAC reference.

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

## Complete examples

See these files in `examples/community-blog/`:

- [`entities/user.yaml`](../examples/community-blog/entities/user.yaml) — enum field, serialization, soft delete
- [`entities/post.yaml`](../examples/community-blog/entities/post.yaml) — FK reference, relations, indexes, jsonb
- [`entities/comment.yaml`](../examples/community-blog/entities/comment.yaml) — self-referential relation
- [`entities/post_tag.yaml`](../examples/community-blog/entities/post_tag.yaml) — junction table, composite unique constraint
