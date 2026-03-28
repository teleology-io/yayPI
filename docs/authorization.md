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

## Complete example

See [`examples/community-blog/policies/`](../examples/community-blog/policies/) for a working `roles.yaml` and `model.conf`.
