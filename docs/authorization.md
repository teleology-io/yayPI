# Authorization

yayPi uses three independent security layers: **API keys** for service authentication, **JWT** for user identity, and **Casbin RBAC** for permissions. ABAC rules provide a fourth, optional layer for fine-grained attribute-based control.

> **Need a `/login` or `/register` endpoint?** See [Auth Endpoints](auth-endpoints.md) — yayPi can generate these from a `kind: auth` YAML file.

## Security layers overview

```
Request
  ↓
API key middleware (optional)
  checks X-API-Key header → sets Subject if valid
  ↓
JWT middleware
  validates Bearer token → sets Subject if valid (skipped if API key already set)
  ↓
Casbin RBAC middleware
  Enforce(role, EntityName, action)
  ↓
Handler
  → row_access (SQL WHERE filter)
  → write_roles (field stripping on write)
  → read_roles (field stripping on response)
```

## API key authentication

API keys provide an alternative to JWTs for service-to-service calls, scripts, or integrations.

Configure in `yaypi.yaml`:

```yaml
auth:
  api_keys:
    header: X-API-Key           # default header name
    query_param: api_key        # also accept ?api_key= (optional)
    keys:
      - key: ${ADMIN_API_KEY}
        role: admin
      - key: ${SERVICE_API_KEY}
        role: service
```

Or use DB-backed keys (looked up per request):

```yaml
auth:
  api_keys:
    entity: ApiKey              # entity holding key records
    key_field: token            # column with the key value (default: token)
    role_field: role            # column with the role (default: role)
```

### OR-logic with JWT

API keys and JWTs are **or-logic** — a request authenticated by either is considered authenticated. If an `X-API-Key` header is present and valid, JWT is skipped entirely. If the key is present but invalid, the request is rejected with **401** even if a JWT is also present.

### Security notes

- API keys grant a static role. Choose roles with the minimum required permissions.
- DB-backed keys support soft-delete for revocation without data loss.
- Never log or expose API key values. Use `${ENV_VAR}` for static keys.

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

### yayPi can issue tokens

Use a `kind: auth` file to add `/register` and `/login` endpoints — yayPi will issue JWTs directly. See [Auth Endpoints](auth-endpoints.md).

For testing without auth endpoints, use [jwt.io](https://jwt.io):
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
| `name` | Role name — must match the `role` claim in JWTs and the `role` value in API keys |
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

The enforcement call is: `Enforce(subject.role, EntityName, action)`.

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
| `subject.id` | JWT `sub` claim or API key subject ID |
| `subject.role` | JWT `role` claim or API key role |
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

`auth.roles` is a shorthand that is equivalent to `subject.role in [...]`. Adding both `roles:` and `conditions:` is valid — both are checked.

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
| `:subject.id` | JWT `sub` or API key subject ID |
| `:subject.role` | JWT `role` or API key role |
| `:subject.email` | JWT `email` |

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
1. API key check            (middleware — sets Subject if key valid)
2. JWT validation           (middleware — sets Subject if token valid)
3. Casbin RBAC              (middleware)
4. auth.roles check         (middleware)
5. auth.conditions check    (middleware)
6. Handler runs
7. row_access filter        (injected into SQL WHERE)
8. write_roles stripping    (on create/update, before DB call)
9. read_roles masking       (on all responses, after DB call)
```

Each layer is independent. You can use one, two, or all layers.

---

## Common patterns

### Fully public endpoint

```yaml
get:
  auth:
    require: false   # no JWT or API key check; Casbin not enforced
```

### Fully private (any authenticated user)

```yaml
create:
  auth:
    require: true    # JWT or API key required; no roles restriction
```

Any valid JWT or API key passes — Casbin still runs, so ensure the role has the permission.

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

The built-in `password_hash` field is always `omit_response: true` and `omit_log: true`.
For custom fields added via `auth.user.fields`, you can add the same controls:

```yaml
# auth.yaml — user.fields
user:
  fields:
    - name: internal_notes
      type: text
      access:
        read_roles: [admin]     # admins only; members get null/absent field
        write_roles: [admin]
```

## Complete example

See [`examples/community-blog/policies/`](../examples/community-blog/policies/) for a working `roles.yaml` and `model.conf`.
