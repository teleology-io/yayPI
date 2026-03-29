# Authorization

yayPi uses two independent security layers: **JWT** for identity and **Casbin RBAC** for permissions.

> **Need a `/login` or `/register` endpoint?** See [Auth Endpoints](auth-endpoints.md) — yayPi can generate these from a `kind: auth` YAML file.

## Two-layer model

```
Request
  ↓
JWT middleware          — "Who are you?"
  validates token, extracts role
  ↓
Casbin RBAC middleware  — "Are you allowed to do this?"
  Enforce(role, EntityName, action)
  ↓
Handler
```

Both layers must pass. They are independent — JWT doesn't know about roles, and Casbin doesn't know about tokens.

## Layer 1: JWT

### Required claims

Your tokens must include these claims:

| Claim | Type | Description |
|---|---|---|
| `sub` | string | User ID (any string, typically a UUID) |
| `role` | string | User's role (e.g. `admin`, `editor`, `member`) |
| `email` | string | User's email address |
| `exp` | Unix timestamp | Expiration time — always validated |

Example payload:
```json
{
  "sub": "550e8400-e29b-41d4-a716-446655440000",
  "role": "editor",
  "email": "alice@example.com",
  "exp": 1893456000
}
```

### Token placement

Send the token in the `Authorization` header:

```
Authorization: Bearer <token>
```

### Algorithm enforcement

Configure the allowed algorithm in `yaypi.yaml`:

```yaml
auth:
  algorithm: HS256          # HS256, HS384, or HS512
  reject_algorithms: [none] # always include this
```

The `none` algorithm is **always rejected**, even if not listed in `reject_algorithms`, as a belt-and-suspenders defense against algorithm confusion attacks.

### yayPi does not issue tokens

yayPi validates tokens but does not have a `/login` endpoint. Your auth service (or a plugin) is responsible for issuing JWTs.

For testing, use [jwt.io](https://jwt.io):
1. Select **HS256** algorithm
2. Set the payload with required claims
3. Enter your `JWT_SECRET` as the "your-256-bit-secret" value
4. Copy the encoded token from the left panel

## Layer 2: Casbin RBAC

### `model.conf`

Casbin needs a model file that defines how policy enforcement works. Use this standard file for yayPi:

```ini
[request_definition]
r = sub, obj, act

[policy_definition]
p = sub, obj, act

[role_definition]
g = _, _

[policy_effect]
e = some(where (p.eft == allow))

[matchers]
m = g(r.sub, p.sub) && r.obj == p.obj && r.act == p.act
```

- `r = sub, obj, act` — a request has a subject (role), object (entity), and action
- `g = _, _` — role inheritance graph
- The matcher checks that the subject's role graph includes a role that has the required `(obj, act)` permission

Place this file at the path configured in `yaypi.yaml`'s `policy.model`.

### `roles.yaml`

Define your roles and their permissions:

```yaml
version: "1"
kind: policy

roles:
  - name: admin
    inherits: [editor]          # admin gets all editor permissions plus its own
    permissions:
      - { resource: User, actions: [list, get, create, update, delete] }
      - { resource: Post, actions: [list, get, create, update, delete] }

  - name: editor
    inherits: [member]
    permissions:
      - { resource: Post,    actions: [list, get, create, update] }
      - { resource: Tag,     actions: [list, get] }
      - { resource: Comment, actions: [list, get, create, update] }

  - name: member
    permissions:
      - { resource: Post,    actions: [list, get] }
      - { resource: Tag,     actions: [list, get] }
      - { resource: Comment, actions: [list, get, create, update] }
      - { resource: User,    actions: [get] }
```

| Field | Description |
|---|---|
| `name` | Role name — must match the `role` claim in JWTs |
| `inherits` | List of roles whose permissions are also granted (additive, transitive) |
| `permissions[].resource` | Entity name (must match `entity.name` exactly) |
| `permissions[].actions` | List of: `list`, `get`, `create`, `update`, `delete` |

### Role inheritance

Inheritance is **additive and transitive**. In the example above:
- `member` can `list`/`get` posts and `create`/`update` comments
- `editor` inherits all member permissions plus can `create`/`update` posts
- `admin` inherits all editor permissions plus can `delete` anything

### How enforcement works

yayPi maps HTTP methods to Casbin actions:

| HTTP method | Casbin action |
|---|---|
| `GET /entity` | `list` |
| `POST /entity` | `create` |
| `GET /entity/{id}` | `get` |
| `PATCH /entity/{id}` | `update` |
| `DELETE /entity/{id}` | `delete` |

The enforcement call is: `Enforce(jwt.role, EntityName, action)`.

If the role does not have permission, the request returns 403.

## Layer 3: ABAC — Attribute-Based Access Control

ABAC is an optional, additive layer on top of JWT + Casbin. It lets you write fine-grained rules based on who the caller is (role, email, id) rather than just whether they have a permission string.

There are three independent sub-layers. Use only what you need.

---

### 3a. Route-level conditions (`auth.conditions`)

Evaluated **before** the handler runs. All conditions must pass (AND logic). Failing conditions return 403.

```yaml
create:
  auth:
    require: true
    roles: [editor, admin]              # shorthand role allowlist
    conditions:
      - subject.email ends_with "@company.com"
      - subject.role in ["editor", "admin"]
```

**Subject attributes available in conditions:**

| Attribute | Description |
|---|---|
| `subject.id` | JWT `sub` claim — user ID |
| `subject.role` | JWT `role` claim |
| `subject.email` | JWT `email` claim |

**Supported operators:**

| Operator | Example |
|---|---|
| `==` | `subject.role == "admin"` |
| `!=` | `subject.role != "guest"` |
| `>` `<` `>=` `<=` | `subject.level >= "5"` (numeric when both sides parse as numbers) |
| `in` | `subject.role in ["editor", "admin"]` |
| `not_in` | `subject.role not_in ["guest", "banned"]` |
| `starts_with` | `subject.email starts_with "alice"` |
| `ends_with` | `subject.email ends_with "@company.com"` |
| `*` | always true (catch-all) |

`auth.roles` is an allowed shorthand that is now fully enforced (equivalent to `subject.role in [...]`). Adding both `roles:` and `conditions:` is valid — both are checked.

---

### 3b. Row-level filters (`row_access`)

Injects a SQL `WHERE` fragment so users only see rows they're allowed to access. Rules are evaluated in order — the first matching rule wins.

**Opt-in:** if `row_access` is omitted, all rows are accessible. If `row_access` is defined but no rule matches the current subject, the request returns **403**.

Use `when: "*"` as a catch-all to avoid accidental 403s for any role you don't explicitly restrict.

```yaml
list:
  row_access:
    - when: "subject.role == \"admin\""
      filter: ""                              # admins see all rows (no filter)
    - when: "subject.role == \"editor\""
      filter: "author_id = :subject.id OR status = 'published'"
    - when: "*"
      filter: "status = 'published'"          # everyone else: published only
```

**Bind variables in `filter`:**

| Placeholder | Resolved to |
|---|---|
| `:subject.id` | JWT `sub` value |
| `:subject.role` | JWT `role` value |
| `:subject.email` | JWT `email` value |

`row_access` is supported on `list`, `get`, `update`, and `delete` operations. On `get`/`update`/`delete`, a non-matching row is returned as **404** (indistinguishable from "not found").

---

### 3c. Field-level access (`field.access`)

Controls which roles can read or write specific fields. Opt-in — omitting `access` means the field is fully open.

**In an entity definition:**

```yaml
fields:
  - name: internal_notes
    type: text
    access:
      read_roles: [admin, editor]   # other roles get field stripped from responses
      write_roles: [admin]          # other roles' values silently ignored on create/update
```

| Key | Behavior when set |
|---|---|
| `read_roles` | Field is stripped from all responses for callers not in the list |
| `write_roles` | Field is silently ignored in request bodies for callers not in the list |

Omitting `read_roles` → field is readable by everyone.
Omitting `write_roles` → field is writable by everyone.

---

### ABAC evaluation order

For every request:

```
1. JWT validation           (middleware)
2. Casbin RBAC              (middleware)
3. auth.roles check         (middleware)  ← now enforced
4. auth.conditions check    (middleware)
5. Handler runs
6. row_access filter        (injected into SQL WHERE)
7. write_roles stripping    (on create/update, before DB call)
8. read_roles masking       (on all responses, after DB call)
```

Each layer is independent. You can use one, two, or all three sub-layers.

---

## Common patterns

### Fully public endpoint

```yaml
get:
  auth:
    require: false   # no JWT check; Casbin not enforced
```

### Fully private (any authenticated user)

```yaml
create:
  auth:
    require: true    # JWT required; no roles restriction
```

Any valid JWT passes — Casbin still runs, so ensure the role has the permission or remove the roles list.

### Public read / authenticated write

```yaml
- path: /posts
  entity: Post
  crud: [list, create]
  auth:
    require: true       # default

  list:
    auth:
      require: false    # public

  create:
    auth:
      require: true
      roles: [editor, admin]
```

### Admin-only endpoint

```yaml
delete:
  auth:
    require: true
    roles: [admin]
```

### Row ownership (users can only update their own records)

```yaml
update:
  auth:
    require: true
    roles: [member, editor, admin]
  row_access:
    - when: "subject.role == \"admin\""
      filter: ""                          # admins can edit any record
    - when: "*"
      filter: "author_id = :subject.id"  # everyone else: only their own
```

### Company-only access via email domain

```yaml
create:
  auth:
    require: true
    conditions:
      - subject.email ends_with "@acme.com"
```

### Hide sensitive fields from non-admins

```yaml
# In entities/user.yaml
fields:
  - name: password_hash
    type: string
    serialization:
      omit_response: true     # always hidden (not in any response)

  - name: internal_notes
    type: text
    access:
      read_roles: [admin]     # admins only; members get null/absent field
      write_roles: [admin]
```

## Complete example

See [`examples/community-blog/policies/`](../examples/community-blog/policies/) for a working `roles.yaml` and `model.conf`.
