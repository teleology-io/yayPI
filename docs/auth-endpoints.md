# Auth Endpoints (`kind: auth`)

Adding a `kind: auth` file to your project gives you built-in registration, login, a "me" endpoint, and OAuth2 sign-in — all driven from YAML, no code required.

## File structure

```yaml
version: "1"
kind: auth

auth:
  base_path: /auth        # all routes mount under this prefix (default: /auth)
  user_entity: User       # entity name to read/write users (must exist in entities/)

  register:
    enabled: true
    credential_field: email       # field used as the login identifier
    password_field: password      # field in the request body (never stored)
    hash_field: password_hash     # entity field that stores the bcrypt hash
    default_role: member          # role assigned when not provided by the caller

  login:
    enabled: true
    credential_field: email
    password_field: password
    hash_field: password_hash

  me:
    enabled: true                 # GET /auth/me — returns the token owner's user record

  oauth2:
    providers:
      - name: google
        client_id: ${GOOGLE_CLIENT_ID}
        client_secret: ${GOOGLE_CLIENT_SECRET}
        redirect_uri: ${APP_URL}/auth/callback/google
        success_redirect: ${FRONTEND_URL}      # redirect here after success (optional)
        error_redirect: ${FRONTEND_URL}/login  # redirect here on failure (optional)

      - name: github
        client_id: ${GITHUB_CLIENT_ID}
        client_secret: ${GITHUB_CLIENT_SECRET}
        redirect_uri: ${APP_URL}/auth/callback/github
        success_redirect: ${FRONTEND_URL}
        error_redirect: ${FRONTEND_URL}/login
```

Add it to the `include:` list in `yaypi.yaml`:

```yaml
include:
  - entities/**/*.yaml
  - endpoints/**/*.yaml
  - policies/**/*.yaml
  - auth.yaml
```

## Endpoints

All routes are mounted under `{base_url}{base_path}` (e.g. `/api/v1/auth`).

| Method | Path | Auth required | Description |
|---|---|---|---|
| `POST` | `/auth/register` | No | Create account, returns `{token, user}` |
| `POST` | `/auth/login` | No | Verify credentials, returns `{token, user}` |
| `GET` | `/auth/me` | Bearer token | Returns current user record |
| `GET` | `/auth/{provider}` | No | Redirects to OAuth2 provider |
| `GET` | `/auth/callback/{provider}` | No | Handles OAuth2 callback, returns/redirects with token |

## Register

```bash
POST /api/v1/auth/register
Content-Type: application/json

{
  "email": "alice@example.com",
  "username": "alice",
  "display_name": "Alice",
  "password": "supersecret123"
}
```

**Response `201`:**
```json
{
  "token": "<jwt>",
  "user": {
    "id": "...",
    "email": "alice@example.com",
    "username": "alice",
    "display_name": "Alice",
    "role": "member",
    ...
  }
}
```

- `password_field` (e.g. `password`) is hashed with **bcrypt** (cost 12) before storage. It is never saved or logged.
- `hash_field` (e.g. `password_hash`) is stripped from the response automatically via `omit_response: true` on the entity.
- `default_role` is applied when the caller does not include a `role` in the body.
- Returns `409 Conflict` if the credential (email) is already taken.
- Returns `400` if the password is shorter than 8 characters.

## Login

```bash
POST /api/v1/auth/login
Content-Type: application/json

{
  "email": "alice@example.com",
  "password": "supersecret123"
}
```

**Response `200`:**
```json
{
  "token": "<jwt>",
  "user": { ... }
}
```

- Returns `401` for both "user not found" and "wrong password" — a generic `"invalid credentials"` message prevents user enumeration.
- A constant-time dummy bcrypt comparison runs even when the user is not found, preventing timing-based enumeration.

## Me

```bash
GET /api/v1/auth/me
Authorization: Bearer <token>
```

**Response `200`:**
```json
{
  "user": { ... }
}
```

Returns the user record for the `sub` claim in the token. Sensitive fields (e.g. `password_hash`) are stripped.

## OAuth2

### Initiating sign-in

Redirect the user's browser to:

```
GET /api/v1/auth/google
GET /api/v1/auth/github
```

yayPi redirects to the provider's auth page with a signed `state` parameter (HMAC-SHA256, expires in 10 minutes).

### Callback

After the user authorises, the provider redirects back to `redirect_uri`:

```
GET /api/v1/auth/callback/google?code=...&state=...
```

yayPi:
1. Verifies the `state` signature and expiry
2. Exchanges the `code` for an access token
3. Fetches the user's profile from the provider's userinfo endpoint
4. **Finds or creates** a user by email (upsert)
5. Issues a JWT

If `success_redirect` is configured, the user is redirected there with `?token=<jwt>` appended. Otherwise the response is JSON `{token, user}`.

If `error_redirect` is configured, errors redirect there with `?error=<message>`. Otherwise errors return JSON.

### Built-in providers

| `name` | Auth URL | Userinfo URL | Default scopes |
|---|---|---|---|
| `google` | `accounts.google.com/o/oauth2/v2/auth` | `googleapis.com/oauth2/v2/userinfo` | `openid email profile` |
| `github` | `github.com/login/oauth/authorize` | `api.github.com/user` | `user:email` |

### Custom providers

Supply `auth_url`, `token_url`, and `userinfo_url` explicitly:

```yaml
- name: my-provider
  client_id: ${MY_CLIENT_ID}
  client_secret: ${MY_CLIENT_SECRET}
  auth_url: https://sso.company.com/oauth2/authorize
  token_url: https://sso.company.com/oauth2/token
  userinfo_url: https://sso.company.com/oauth2/userinfo
  redirect_uri: ${APP_URL}/auth/callback/my-provider
  email_field: email       # JSON key in userinfo response for email
  name_field: full_name    # JSON key for display name
  username_field: username # JSON key for username
```

### OAuth2 users and passwords

Users created via OAuth2 are assigned a **random unusable bcrypt hash** as their `hash_field`. They cannot log in via `POST /auth/login`. To let OAuth2 users set a password later, build a separate "set password" endpoint using a plugin.

## Entity requirements

The `user_entity` must have fields matching your config. For the defaults (`email`, `password_hash`, `role`):

```yaml
fields:
  - name: email
    type: string
    unique: true
    nullable: false

  - name: password_hash
    type: string
    nullable: false
    serialization:
      omit_response: true   # required — keeps hash out of all API responses
      omit_log: true

  - name: role
    type: enum
    values: [admin, editor, member]
    default: "'member'"
```

## Token format

The JWT issued by all auth endpoints contains:

```json
{
  "sub": "<user id>",
  "role": "<user role>",
  "email": "<user email>",
  "iat": 1711234567,
  "exp": 1711320967
}
```

This matches the claims expected by the JWT middleware and Casbin RBAC on all other endpoints — the token works everywhere in the same API.

## Complete example

See [`examples/community-blog/auth.yaml`](../examples/community-blog/auth.yaml).
