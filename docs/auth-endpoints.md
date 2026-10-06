# Auth Endpoints (`kind: auth`)

Adding a `kind: auth` file gives you registration, login, "me", refresh tokens with logout, password reset, email verification, and OAuth2 sign-in — all driven from YAML, no code required.

## File structure

```yaml
version: "1"
kind: auth

auth:
  base_path: /auth        # all routes mount under this prefix (default: /auth)

  # Optional: extend the built-in User with custom fields.
  user:
    fields:
      - name: display_name
        type: string
        length: 128
        nullable: true

  register:
    enabled: true
    credential_field: email       # default: email
    password_field: password      # default: password (never stored)
    hash_field: password_hash     # default: password_hash
    default_role: member          # role every self-registered user gets
    min_password_length: 8        # default 8; max is always 72 bytes

  login:
    enabled: true
    max_attempts: 5               # failed logins per account (4x per IP) per window → 429
    lockout_window: 15m

  me:
    enabled: true

  refresh:
    enabled: true
    expiry: 30d                   # refresh token lifetime
    store: cookie                 # cookie (HttpOnly) | body (JSON, for native apps)

  cookie:
    secure: true                  # set false only for local http development
    same_site: lax                # lax | strict | none

  password_reset:                 # requires SMTP (see below)
    enabled: true
    expiry: 1h
    reset_url: ${FRONTEND_URL}/reset-password?token={{token}}

  email_verification:             # requires SMTP
    enabled: true
    required: false               # true = refuse password login until verified
    verify_url: ${FRONTEND_URL}/verify-email?token={{token}}

  oauth2:
    providers:
      - name: google
        client_id: ${GOOGLE_CLIENT_ID}
        client_secret: ${GOOGLE_CLIENT_SECRET}
        redirect_uri: ${APP_URL}/api/v1/auth/callback/google
        success_redirect: ${FRONTEND_URL}/auth/done
        error_redirect: ${FRONTEND_URL}/login
      - name: github
        client_id: ${GITHUB_CLIENT_ID}
        client_secret: ${GITHUB_CLIENT_SECRET}
        redirect_uri: ${APP_URL}/api/v1/auth/callback/github
        success_redirect: ${FRONTEND_URL}/auth/done
        error_redirect: ${FRONTEND_URL}/login
```

`auth.secret` in `yaypi.yaml` must be at least 32 bytes (generate one with `openssl rand -hex 32`); the server refuses to start otherwise. Email features use the `smtp:` block in `yaypi.yaml` (empty fields fall back to `SMTP_*` env vars) and refuse to start without an SMTP host. Reset and verification emails are queued in the outbox and retried per `smtp.retry`.

## Endpoints

All routes are mounted under `{base_url}{base_path}` (e.g. `/api/v1/auth`). Responses carry `Cache-Control: no-store`.

| Method | Path | Auth | Description |
|---|---|---|---|
| `POST` | `/auth/register` | – | Create account → `{token, expires_in, user[, refresh_token]}` |
| `POST` | `/auth/login` | – | Verify credentials → same shape |
| `GET` | `/auth/me` | Bearer | Current user |
| `POST` | `/auth/refresh` | refresh token | New access + refresh token |
| `POST` | `/auth/logout` | refresh token | End this session (always 204) |
| `POST` | `/auth/logout-all` | Bearer | End every session for the user |
| `POST` | `/auth/password/forgot` | – | Email a reset link (always 202) |
| `POST` | `/auth/password/reset` | – | Set a new password with the emailed token |
| `POST` | `/auth/verify-email` | – | Confirm the email with the emailed token |
| `POST` | `/auth/verify-email/resend` | Bearer | Resend (once a minute) |
| `GET` | `/auth/{provider}` | – | Start OAuth2 sign-in |
| `GET` | `/auth/callback/{provider}` | – | OAuth2 callback |
| `GET` | `/auth/.well-known/jwks.json` | – | Public signing keys (RS*/ES* algorithms only) |

## Register

```bash
POST /api/v1/auth/register
Content-Type: application/json

{ "email": "Alice@Example.com", "display_name": "Alice", "password": "supersecret123" }
```

**Response `201`:**
```json
{ "token": "<jwt>", "expires_in": 900, "user": { "id": "...", "email": "alice@example.com", "role": "member" } }
```

- The password is hashed with **bcrypt** (cost 12); it is never stored or logged. 8–72 bytes.
- The email is trimmed and lowercased (login does the same), and must look like an email.
- **The role is always `default_role`.** A `role` in the body is ignored, as are `id`, timestamps, OAuth columns, `email_verified_at`, and any field with `write_roles`.
- `409` if the email is taken.
- With `email_verification.enabled`, a verification email is sent.

## Login

```bash
POST /api/v1/auth/login
{ "email": "alice@example.com", "password": "supersecret123" }
```

- `401 "invalid credentials"` for both unknown user and wrong password, with equal timing.
- After `max_attempts` failures for an account (or 4× that from one IP) within `lockout_window`, attempts get `429` with `Retry-After` — even with the right password — until the window passes. Counters are per server instance.
- With `email_verification.required`, unverified users get `403`.

## Tokens

**Access tokens** are JWTs signed with `auth.algorithm` (HS256 by default; RS*/ES* with `auth.private_key_file`). Lifetime is `auth.expiry` — default 15 minutes with refresh enabled, otherwise 1 hour.

```json
{ "sub": "<user id>", "role": "member", "email": "...", "tv": 0, "typ": "access",
  "iss": "<auth.issuer or project.name>", "aud": "<auth.audience>", "iat": 0, "nbf": 0, "exp": 0 }
```

`iss` defaults to `project.name`; when `auth.issuer` is set explicitly it is also verified on every request, as is `aud` when `auth.audience` is set. If the user has a `tenant_id` column, its value is issued as a `tenant` claim (see `tenant_scoped` entities).

**Refresh tokens** are opaque random strings, stored only as SHA-256 hashes in `yaypi_refresh_tokens`:

- Issued on register, login, and OAuth sign-in.
- **Rotated** on every `/auth/refresh`. Each login starts a session "family".
- **Reuse detection:** presenting an already-used refresh token means it was copied; the whole family is revoked, so both the attacker and the victim must sign in again.
- `/auth/logout` revokes the current family; `/auth/logout-all` revokes all of the user's sessions and bumps `token_version`.

### Cookie store (default, for browsers)

The refresh token is an `HttpOnly`, `Secure`, `SameSite=Lax` cookie scoped to the auth path, so it isn't sent with ordinary API calls. `POST /auth/refresh` needs no body and returns `{token, expires_in}` while setting the rotated cookie.

### Body store (native/mobile)

```bash
POST /api/v1/auth/refresh
{ "refresh_token": "<refresh>" }
→ { "token": "<access>", "expires_in": 900, "refresh_token": "<new refresh>" }
```

### Immediate revocation

Access tokens are stateless, so by default a deleted user, a role change, `logout-all` or a password reset takes effect when the token expires (≤ 15 min with refresh). Set `auth.revocation_check: true` in `yaypi.yaml` to re-read the user on every authenticated request (one indexed query): revoked tokens are then rejected immediately, and the current role is used.

## Password reset

```bash
POST /api/v1/auth/password/forgot   { "email": "alice@example.com" }   → 202 always
POST /api/v1/auth/password/reset    { "token": "<from email>", "password": "new-password" } → 200
```

The emailed link is `reset_url` with `{{token}}` replaced. Customise the email with `subject` and `body` (HTML; `{{link}}` is replaced). Tokens are single-use, stored hashed, and expire after `expiry` (default 1h). A successful reset revokes all of the user's sessions and marks the email verified.

## Email verification

On register a link (`verify_url` with `{{token}}`) is emailed. `POST /auth/verify-email {"token": "..."}` sets `email_verified_at`. Logged-in users can request another email with `POST /auth/verify-email/resend`. With `required: true`, password login is refused until verified.

## OAuth2

### Flow

1. Send the browser to `GET /api/v1/auth/google`. yayPi sets a short-lived `HttpOnly` state cookie and redirects to the provider with a random `state` and a **PKCE** challenge.
2. The provider redirects to `redirect_uri` (`/auth/callback/google?code&state`). yayPi checks `state` against the cookie (this blocks login-CSRF), exchanges the code with the PKCE verifier, and fetches the profile.
3. yayPi resolves the account:
   - an account already linked to this provider user id → sign in;
   - otherwise an account with the same email → linked **only if the provider says the email is verified**;
   - otherwise a new account → **only with a verified email** (created with `email_verified_at` set and no password).
4. Tokens are issued. With `success_redirect`, the browser goes to `success_redirect#token=<jwt>` — the URL **fragment** is never sent to servers or leaked in `Referer`. Read it client-side with `location.hash`. (`token_delivery: query` restores the legacy `?token=`.) With cookie refresh, the refresh cookie is set as well.

Errors redirect to `error_redirect?error=<message>`, or return JSON.

### Built-in providers

| `name` | Verified-email source | Default scopes |
|---|---|---|
| `google` | `verified_email` in userinfo | `openid email profile` |
| `github` | primary entry of `GET /user/emails` | `read:user user:email` |

### Custom providers

```yaml
- name: corp-sso
  client_id: ${SSO_CLIENT_ID}
  client_secret: ${SSO_CLIENT_SECRET}
  auth_url: https://sso.example.com/oauth2/authorize
  token_url: https://sso.example.com/oauth2/token
  userinfo_url: https://sso.example.com/oauth2/userinfo
  redirect_uri: ${APP_URL}/api/v1/auth/callback/corp-sso
  id_field: sub                     # default "sub"
  email_field: email
  email_verified_field: email_verified
  trust_email: false                # true only if the IdP guarantees verified emails
  name_field: name
  username_field: preferred_username
  pkce: true
```

OAuth-only accounts have no password. They can set one through the password-reset flow.

## Built-in User

| Column | Type | Notes |
|---|---|---|
| `id` | `uuid` | Primary key |
| `email` | `varchar(255)` | Unique, lowercased |
| `password_hash` | `varchar(255)` | Null for OAuth-only users; never returned |
| `role` | `varchar(64)` | Default `'member'` |
| `oauth_provider`, `oauth_id` | `varchar` | Provider link |
| `email_verified_at` | `timestamptz` | Set by verification, reset, or OAuth |
| `token_version` | `integer` | Bumped by logout-all / reset; never returned |
| `created_at`, `updated_at`, `deleted_at` | `timestamptz` | Soft delete |

Add your own columns with `user.fields`.

## Complete example

See [`examples/community-blog/auth.yaml`](../examples/community-blog/auth.yaml).
