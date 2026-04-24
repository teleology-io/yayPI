# Auth Endpoints (`kind: auth`)

Adding a `kind: auth` file to your project gives you built-in registration, login, a "me" endpoint, token refresh, and OAuth2 sign-in — all driven from YAML, no code required.

## File structure

```yaml
version: "1"
kind: auth

auth:
  base_path: /auth        # all routes mount under this prefix (default: /auth)

  # Optional: extend the built-in User with custom fields.
  # Built-in fields are always present and cannot be overridden (see "Built-in User" below).
  user:
    fields:
      - name: display_name
        type: string
        length: 128
        nullable: true
      - name: avatar_url
        type: string
        length: 512
        nullable: true

  register:
    enabled: true
    credential_field: email       # default: email
    password_field: password      # default: password (never stored)
    hash_field: password_hash     # default: password_hash
    default_role: member          # default: member

  login:
    enabled: true
    credential_field: email
    password_field: password
    hash_field: password_hash

  me:
    enabled: true                 # GET /auth/me — returns the token owner's user record

  refresh:
    enabled: true                 # POST /auth/refresh — rotate refresh token, issue new access token
    expiry: 30d                   # refresh token TTL (supports d/h/m/s and Go duration strings)
    store: cookie                 # cookie (default, HttpOnly) | body (JSON)

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
| `POST` | `/auth/refresh` | Refresh token | Issue new access + refresh tokens |
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
    "role": "member"
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

## Token refresh

Token refresh issues short-lived access tokens (JWTs) alongside long-lived refresh tokens. This allows access tokens to be kept short-lived (minutes/hours) for security while keeping users logged in for days or weeks without re-authenticating.

### Cookie store (default, recommended for web apps)

With `store: cookie`:

- Login response sets `refresh_token` as an **HttpOnly** cookie and returns the access token in the JSON body.
- The cookie is `Secure` when the connection is TLS, `SameSite: Lax`.
- `POST /auth/refresh` reads the cookie automatically (no body needed).

```bash
# No body needed — the browser sends the cookie automatically
POST /api/v1/auth/refresh

# Response 200:
{
  "token": "<new access jwt>"
}
# Also sets a new refresh_token cookie (the old one is now invalid)
```

### Body store (for native/mobile apps)

With `store: body`:

- Login response returns both tokens in the JSON body.
- `POST /auth/refresh` reads `refresh_token` from the request body.

```bash
# Login
POST /api/v1/auth/login
→ { "token": "<access jwt>", "refresh_token": "<refresh jwt>" }

# Refresh
POST /api/v1/auth/refresh
Content-Type: application/json
{ "refresh_token": "<refresh jwt>" }

→ { "token": "<new access jwt>", "refresh_token": "<new refresh jwt>" }
```

### Refresh token security

- Refresh tokens are **single-use** — every call to `/auth/refresh` issues a new refresh token and invalidates the old one.
- Refresh tokens contain `"type": "refresh"` in their claims. They cannot be used as access tokens.
- The default TTL is 30 days. Configure with `expiry: 7d`, `expiry: 90d`, etc.

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

## Built-in User

yayPi owns the user account model. A `users` table is always created with these fields — you do not need to define a User entity:

| Column | Type | Notes |
|---|---|---|
| `id` | `uuid` | Primary key, auto-generated |
| `email` | `varchar(255)` | Unique, required, validated as email |
| `password_hash` | `varchar(255)` | Nullable (null for OAuth-only users); never returned in responses or logs |
| `role` | `varchar(64)` | Default: `'member'` |
| `oauth_provider` | `varchar(64)` | Nullable; set on OAuth2 sign-in |
| `oauth_id` | `varchar(256)` | Nullable; set on OAuth2 sign-in |
| `created_at` | `timestamptz` | Auto-set |
| `updated_at` | `timestamptz` | Auto-set |
| `deleted_at` | `timestamptz` | Nullable; soft delete |

To add application-specific fields (e.g. `display_name`, `bio`), use the `user.fields` block in `auth.yaml` — they are merged into the `users` table and participate in migrations automatically.

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

Refresh tokens additionally contain `"type": "refresh"`. They are validated separately and cannot be used as access tokens.

## Complete example

See [`examples/community-blog/auth.yaml`](../examples/community-blog/auth.yaml).
