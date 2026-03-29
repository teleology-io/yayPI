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

Any valid JWT is accepted. The role in the token is not checked by the endpoint (Casbin still runs).

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

## 5. Filtering related records via query param

Instead of nested routes (`/posts/{id}/comments`), use a flat path with a filter parameter. This works with the existing `allow_filter_by` mechanism.

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
# Comments on a post
GET /comments?post_id=<uuid>

# Replies to a comment
GET /comments?parent_id=<uuid>

# All comments by a user
GET /comments?author_id=<uuid>
```

---

## 6. Protecting a sensitive field

Use `serialization` to prevent a field from appearing in API responses or logs.

```yaml
- name: password_hash
  type: string
  serialization:
    omit_response: true   # never in GET/LIST/CREATE/UPDATE responses
    omit_log: true        # never in structured log output
```

The field is still stored in the database and can be read by plugins (e.g. for password verification). It just never leaves the server boundary.

---

## 7. Many-to-many with junction table

Define three entities: the two main entities and a junction entity.

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

## 8. Self-referential hierarchy (threaded comments)

An entity can have a FK to itself for parent-child trees.

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

Fetch replies:
```bash
GET /comments?parent_id=<uuid>
```

---

## 9. Limiting pagination per audience

Tighter limits for public endpoints, higher for authenticated admin views.

```yaml
# Public endpoint — low limits
- path: /posts
  entity: Post
  crud: [list]
  list:
    pagination:
      style: cursor
      default_limit: 10
      max_limit: 50
    auth:
      require: false

# Admin endpoint — higher limits
- path: /admin/posts
  entity: Post
  crud: [list]
  list:
    pagination:
      style: cursor
      default_limit: 100
      max_limit: 1000
    auth:
      require: true
      roles: [admin]
```

---

## 10. Multiple databases (entity-level routing)

Some entities live in a secondary database. Declare both databases and set `database:` on each entity that doesn't use the default.

**`yaypi.yaml`:**
```yaml
databases:
  - name: primary
    dsn: ${DATABASE_URL}
    default: true

  - name: analytics
    dsn: ${ANALYTICS_DATABASE_URL}
    read_only: true
```

**Entity in the analytics database:**
```yaml
entity:
  name: PageView
  table: page_views
  database: analytics   # routes queries to the analytics pool
```

---

## 11. Scheduled data maintenance (SQL cron job)

```yaml
version: "1"
kind: jobs

jobs:
  - name: purge-soft-deleted-posts
    description: Remove posts soft-deleted more than 90 days ago
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

## 12. Enum field with default

The `default:` value for string literals needs double quoting — the outer quotes are YAML, the inner single quotes are SQL.

```yaml
- name: status
  type: enum
  values: [draft, published, archived]
  default: "'draft'"   # SQL: DEFAULT 'draft'
```

If you write `default: "draft"` (no inner quotes), PostgreSQL treats `draft` as a column reference and the migration fails.

---

## 13. JSONB metadata field

Store flexible, schema-less data alongside structured fields.

```yaml
- name: metadata
  type: jsonb
  nullable: true
```

Clients can set and read arbitrary JSON objects:

```json
{
  "metadata": {
    "source": "import",
    "tags": ["featured"],
    "reading_time_minutes": 5
  }
}
```

Note: You cannot filter or sort by JSONB sub-fields via the standard `allow_filter_by` mechanism. For querying inside JSONB, write a custom SQL job or use a plugin.

---

## 15. Row ownership — users can only modify their own records

Authors can update/delete their own posts; admins can touch any post.

**`endpoints/posts.yaml`:**
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

When a non-admin tries to update/delete a post they don't own, the row filter makes the SQL match zero rows — the handler returns **404** (indistinguishable from a missing record, which avoids leaking existence information).

---

## 16. Role-based row visibility (row-level security for lists)

Admins see all posts (including drafts). Editors see their own drafts plus all published. Everyone else sees published only.

```yaml
list:
  allow_filter_by: [status, author_id]
  allow_sort_by: [published_at, title]
  auth:
    require: false   # public endpoint — sub may be nil
  row_access:
    - when: "subject.role == \"admin\""
      filter: ""                                         # all rows
    - when: "subject.role == \"editor\""
      filter: "status = 'published' OR author_id = :subject.id"
    - when: "*"
      filter: "status = 'published'"                     # guests + members
```

The `*` catch-all is critical here: it handles unauthenticated callers (sub is nil — `subject.id` resolves to empty string, which won't match any valid UUID). Without a catch-all, unauthenticated requests would return **403**.

---

## 17. Attribute condition — restrict by email domain

Only users from `@acme.com` can create records.

```yaml
create:
  auth:
    require: true
    conditions:
      - subject.email ends_with "@acme.com"
```

Combined with roles:

```yaml
create:
  auth:
    require: true
    roles: [editor, admin]
    conditions:
      - subject.email ends_with "@acme.com"
      - subject.role != "suspended"
```

Both `roles` and all `conditions` must pass — they are ANDed together.

---

## 18. Field-level access — admin-only fields

Some fields should only be visible to or writable by specific roles.

**Entity:**
```yaml
fields:
  - name: internal_notes
    type: text
    nullable: true
    access:
      read_roles: [admin]       # members see null/absent for this field
      write_roles: [admin]      # members' values silently ignored on create/update

  - name: flagged
    type: boolean
    default: "false"
    access:
      read_roles: [admin, editor]
      write_roles: [admin]      # only admins can flag a record
```

There is no error or warning when a restricted field is omitted from a response or dropped from a write — the behavior is transparent to the caller.

---

## 19. Self-service profile update (users can only edit their own profile)

Users can edit their own profile fields. Admins can edit any field on any user.

**Entity:**
```yaml
fields:
  - name: role
    type: enum
    values: [admin, editor, member]
    access:
      write_roles: [admin]       # only admins can change a user's role
```

**Endpoint:**
```yaml
update:
  allowed_fields: [display_name, bio, avatar_url, role]
  auth:
    require: true
  row_access:
    - when: "subject.role == \"admin\""
      filter: ""                          # admins: any user
    - when: "*"
      filter: "id = :subject.id"          # everyone else: only themselves
```

With this config:
- A member can `PATCH /user/{their-own-id}` and change `display_name`, `bio`, `avatar_url`. Their `role` value is silently dropped because `write_roles: [admin]`.
- A member calling `PATCH /user/{someone-elses-id}` gets **404**.
- An admin can update any field on any user.

---

## 14. Composite primary key (junction table, no `id` column)

When a table uses a composite primary key, mark both fields with `primary_key: true`. Do not add an `id` field.

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

The migration engine includes both in the `PRIMARY KEY (post_id, tag_id)` constraint.
