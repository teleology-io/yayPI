# Production Readiness Manifest

Working checklist of gaps between yayPi today and a production-grade framework.
Work items **top to bottom** — order accounts for severity and dependencies.

**Status keys:** `[ ]` todo · `[~]` in progress · `[x]` done · `[-]` won't do (note why)

Each item: **Problem** (what's wrong, where) → **Fix** (intended approach) → **Done when** (acceptance check).
Run `go build ./...` and `go test ./...` after every item.

---

## Phase 1 — Critical security (ship-blockers)

### P1.1 `[x]` Register allows role / privileged-field mass assignment
- **Where:** `internal/auth/handler.go` (`register`, ~L126)
- **Problem:** `default_role` only applied when body has no `role`. `POST /auth/register {"role":"admin"}` creates an admin. Register also bypasses `applyWriteRoles`, so any user-entity column (`oauth_provider`, custom flags) is client-settable.
- **Fix:** Always force `role = default_role`; strip `role`, `id`, timestamps, `oauth_*`, and any field with `write_roles` from the body. Consider explicit `register.allowed_fields` allowlist in config.
- **Done when:** Registering with `role`, `id`, or a `write_roles`-restricted field is ignored; test covers it.

### P1.2 `[x]` `auth.roles` / `auth.conditions` silently ignored without Casbin
- **Where:** `internal/router/builder.go` (`buildMiddlewareChain`, ~L269), `internal/middleware/rbac.go`
- **Problem:** Role allowlist + conditions live inside the RBAC middleware, which is only mounted when `cfg.Enforcer != nil`. Without a Casbin policy, `auth: {require: true, roles: [admin]}` lets any authenticated user through.
- **Fix:** Split into two middlewares: (a) roles/conditions check — always mounted when `roles`/`conditions` set; (b) Casbin check — mounted only when enforcer exists.
- **Done when:** Endpoint with `roles: [admin]` and no policy config returns 403 for `member`; test covers it.

### P1.3 `[x]` Policy engine init failure fails open
- **Where:** `pkg/server/server.go` (~L118-138)
- **Problem:** Casbin init error → `log.Warn` and `policyEngine = nil` → RBAC skipped everywhere. Any `policy.adapter` other than `file` silently does the same.
- **Fix:** If `policy.engine` is configured, any init error (or unsupported adapter) returns an error from `Run()` — refuse to boot. Same principle for other security-relevant startup steps (see P3.6).
- **Done when:** Broken model path / unknown adapter → process exits non-zero with clear message.

### P1.4 `[x]` CORS wildcard reflects any origin with credentials
- **Where:** `internal/middleware/cors.go` (~L30)
- **Problem:** `allowed_origins: ["*"]` echoes request `Origin` and sets `Access-Control-Allow-Credentials: true` → any site can make credentialed cross-origin calls (cookie refresh token at risk). Preflight also echoes arbitrary requested headers.
- **Fix:** With `*`: send literal `Access-Control-Allow-Origin: *`, never `Allow-Credentials`. Credentials only for explicitly listed origins. Make allowed headers/methods configurable with safe defaults (incl. API key header); stop echoing requested headers.
- **Done when:** `*` config yields `ACAO: *` and no credentials header; listed origin yields echo + credentials.

### P1.5 `[x]` Validate `auth.secret` at boot
- **Where:** `internal/config/validator.go`, `pkg/server/server.go`
- **Problem:** No presence/length check. Empty secret is used to sign JWTs, pagination cursors, and OAuth state. Unresolved `${VAR}` passes through as the literal string.
- **Fix:** Require secret ≥ 32 bytes when auth, refresh, OAuth, or cursor pagination is in use; fail validation if value still contains `${`. Same unresolved-env check for DSNs.
- **Done when:** Missing/short/unresolved secret → `yaypi validate` and `run` fail.

### P1.6 `[x]` No request body size limit / missing server timeouts
- **Where:** all JSON decoders in `internal/handler/`, `internal/auth/`; `http.Server` in `pkg/server/server.go`
- **Problem:** No `http.MaxBytesReader` anywhere → memory exhaustion via large body. No `ReadHeaderTimeout` / `IdleTimeout` → slowloris exposure.
- **Fix:** Global body-limit middleware (config `server.max_body_bytes`, default 1MB; bulk endpoints may override). Set `ReadHeaderTimeout` (default 10s) and `IdleTimeout` (default 120s), both configurable. Return 413 on oversize.
- **Done when:** Oversized body → 413; server struct has all four timeouts set.

### P1.7 `[x]` Rate limiter: spoofable key + unbounded memory
- **Where:** `internal/middleware/ratelimit.go`, `internal/router/builder.go` (chi `RealIP`)
- **Problem:** Trusts `X-Forwarded-For` / `X-Real-IP` from any client (also via chi `RealIP`) → per-request spoofed keys bypass limits. Full XFF string used as key. `sync.Map` buckets never evicted → memory grows forever. Per-endpoint limiter stacks with global, despite comment saying it takes precedence.
- **Fix:** Add `server.trusted_proxies` (CIDRs); only honour forwarding headers when `RemoteAddr` is trusted, take the right-most untrusted hop. Drop unconditional `chiMiddleware.RealIP`. Add TTL eviction sweeper. Decide + document global/per-endpoint semantics. Key by subject ID when authenticated (optional). Add `X-RateLimit-*` headers.
- **Done when:** Spoofed XFF from untrusted peer has no effect; idle buckets evicted; test covers both.

### P1.8 `[x]` OAuth2 account takeover + login CSRF
- **Where:** `internal/auth/handler.go` (`oauthCallback`, `oauthUpsert`, `generateState`, `exchangeCode`, `fetchUserInfo`)
- **Problem:**
  - Existing account linked purely by email; provider's `email_verified` / `verified_email` never checked → takeover via unverified email.
  - `state` is HMAC-signed but not bound to the browser → login CSRF.
  - No PKCE.
  - JWT returned in redirect query string (`?token=`) → leaks to logs, history, Referer.
  - `http.DefaultClient` (no timeout); token/userinfo non-2xx not checked; response body unbounded.
  - GitHub private emails: `/user` often has no email → fails instead of calling `/user/emails`.
  - Upsert hardcodes `email`/`username`/`display_name` columns.
- **Fix:** Require verified email (configurable per provider; GitHub via `/user/emails` primary+verified). Store `oauth_provider` + `oauth_id` and match on those first; only link by email when verified. Set state nonce in a short-lived HttpOnly cookie and compare on callback. Add PKCE (S256). Deliver token via URL fragment or one-time code exchange, not query. Dedicated HTTP client with timeout + `io.LimitReader`.
- **Done when:** Unverified email cannot attach to existing account; callback without matching cookie rejected; token absent from query string.

### P1.9 `[x]` Webhook SSRF filter bypassable
- **Where:** `internal/webhook/webhook.go` (`isSafeURL`); compare `internal/cron/handlers.go` (`validateHost`)
- **Problem:** Hostnames trusted without resolution (`evil.example → 169.254.169.254`). Misses IPv6 ULA (`fc00::/7`), `0.0.0.0`, CGNAT `100.64.0.0/10`. Redirects followed to internal hosts. DNS-rebinding between check and connect.
- **Fix:** Enforce in a custom `net.Dialer.Control` (check resolved IP at connect time). Shared SSRF-safe client package used by webhooks, cron HTTP jobs, and OAuth custom URLs. Disable or re-validate redirects.
- **Done when:** Hostname resolving to private/loopback/metadata IP is blocked; shared helper has tests.

---

## Phase 2 — Auth completeness

### P2.1 `[x]` Refresh tokens never issued initially
- **Where:** `internal/auth/refresh.go`, `internal/auth/handler.go` (`login`, `register`, OAuth callback)
- **Problem:** `issueRefreshToken` only called inside `/refresh` — login/register never hand one out, so the refresh flow can't bootstrap.
- **Fix:** When refresh enabled, login/register/OAuth issue refresh token (cookie or body per `store`).
- **Done when:** Login → refresh → new access token works end-to-end.

### P2.2 `[x]` Stateful refresh tokens: revocation, reuse detection, logout
- **Problem:** Refresh JWTs are stateless — "rotation" doesn't invalidate the old token (valid 30d). No logout, no revoke-all, no reuse detection. Cookie `Secure` keyed off `r.TLS` (wrong behind TLS-terminating proxy); `Path: /`.
- **Fix:** Persist refresh tokens (hashed, family ID, expiry, revoked_at) in a built-in table. Rotation marks old used; reuse of a used token revokes the family. Add `POST /auth/logout` (+ optional logout-all). Cookie `Secure` configurable (default true), path scoped to auth base path.
- **Depends on:** P2.1
- **Done when:** Replayed old refresh token → 401 and family revoked; logout invalidates.

### P2.3 `[x]` Access token hygiene
- **Where:** `internal/auth/handler.go` (`issueToken`), `internal/middleware/auth.go`
- **Problem:** Always signs HS256 regardless of `auth.algorithm` (non-HS256 config breaks). Fixed 24h TTL. No `iss`/`aud`/`jti`/`nbf`. No asymmetric key support (RS256/ES256, JWKS). Role/deletion changes not reflected until expiry.
- **Fix:** Honour configured algorithm; add RS256/ES256 with key files and optional JWKS endpoint. Configurable `access_ttl` (default 15m when refresh enabled). Set + validate `iss`/`aud`. Optional `token_version` column on user checked by middleware for instant revocation.
- **Done when:** Config algorithm used for sign + verify; iss/aud enforced.

### P2.4 `[x]` Credential normalization + hardcoded columns
- **Where:** `internal/auth/handler.go`, `pkg/server/server.go` (`buildDBAPIKeyLookup`), `internal/auth/refresh.go`
- **Problem:** Login lowercases + trims credential; register doesn't → mixed-case signups can't log in. `deleted_at IS NULL` and `id` hardcoded in login/me/refresh/OAuth/API-key queries → break on user entities without soft delete or with differently-named PK.
- **Fix:** Normalize credential identically on register/login/OAuth. Build these queries from entity metadata (PK column, `SoftDelete` flag).
- **Done when:** Mixed-case register → login works; custom user entity without soft delete works.

### P2.5 `[x]` Password policy + brute-force protection
- **Problem:** No max length (bcrypt silently truncates at 72 bytes). Only min length 8. No login throttling beyond global limiter, no lockout.
- **Fix:** Reject > 72 bytes (or pre-hash). Configurable min length. Per-credential + per-IP login attempt limiter with backoff. Register enumeration: consider generic response option.
- **Depends on:** P1.7 (limiter internals)
- **Done when:** Repeated bad logins for one account throttled independent of IP.

### P2.6 `[x]` Password reset + email verification
- **Problem:** Neither exists; production auth needs both.
- **Fix:** `POST /auth/password/forgot`, `POST /auth/password/reset` (single-use hashed token, short TTL, revoke refresh tokens on reset). `email_verified_at` column + verify flow; optional `require_verified` for login. Reuse mailer.
- **Depends on:** P2.2, P4.3 (reliable email delivery)
- **Done when:** Full reset + verify flows work with expiring single-use tokens.

### P2.7 `[x]` API key hardening
- **Where:** `pkg/server/server.go` (`buildDBAPIKeyLookup`), `internal/middleware/apikey.go`
- **Problem:** Keys stored/compared in plaintext (DB and YAML). Query-param mode leaks keys into access logs. API-key Subject has empty `ID` → `:subject.id` row filters bind `""`. No expiry, no scopes, no last-used tracking. Static map compare not constant-time.
- **Fix:** Store SHA-256 of key (prefix for lookup); populate Subject ID from key owner. Add `expires_at`, optional scopes. Deprecate/warn on query-param mode; redact in logs. Constant-time compare for static keys.
- **Done when:** DB holds only hashes; row filters work for API-key callers.

### P2.8 `[-]` (Optional) MFA / TOTP
- Deferred deliberately: needs product decisions (enrolment UX, recovery codes, which roles must enrol). The token/one-time-token plumbing from P2.2/P2.6 is the foundation when it is picked up.

---

## Phase 3 — Data correctness & API behaviour

### P3.1 `[x]` Cursor pagination broken for non-id sorts and integer PKs
- **Where:** `internal/handler/list.go`, `internal/query/builder.go` (`List`), `internal/query/cursor.go`
- **Problem:** Always `WHERE id > cursor` even when `ORDER BY` another column → skipped/duplicated rows. `stringVal` returns `""` for non-string IDs → int-PK cursors broken. `created_at` stored in cursor but unused. Descending sort not handled.
- **Fix:** Keyset pagination on `(sort_col, pk)` tuple with direction-aware comparison; cursor encodes sort column, direction, and both values; reject cursor if sort changed. Stringify any PK type.
- **Done when:** Paging through a list sorted by `created_at:desc` and by int PK returns every row exactly once (test).

### P3.2 `[x]` Map DB errors to proper HTTP status
- **Where:** `internal/handler/*.go`, `internal/dialect/*`
- **Problem:** Unique / FK / not-null / check violations → generic 500. "No valid fields to update" → 500.
- **Fix:** Dialect methods `IsForeignKeyViolation`, `IsNotNullViolation` (unique exists). Map to 409 / 422 / 400 with field info where available. Typed sentinel errors from query builder (`ErrNoFields`, `ErrNotFound`) instead of string compare (`err.Error() == "record not found"` in delete).
- **Done when:** Duplicate unique value → 409; bad FK → 422; empty update → 400.

### P3.3 `[x]` Bulk create atomicity + error leakage
- **Where:** `internal/handler/create.go` (`createBulk`)
- **Problem:** Not transactional — `fail` mode leaves earlier rows committed. Partial mode returns raw `err.Error()` (DB internals). After-hooks fire for rows later rolled back (once tx added).
- **Fix:** `fail` mode wraps in a tx; after-hooks only after commit. Sanitized per-item errors via P3.2 mapping. Query builder needs a tx-capable executor interface (`QueryContext`/`ExecContext` on `*sql.DB` or `*sql.Tx`).
- **Depends on:** P3.2
- **Done when:** Failure at item N in `fail` mode leaves zero rows inserted.

### P3.4 `[x]` Input type validation for all fields
- **Where:** `internal/handler/validate.go`
- **Problem:** Type checks only run for fields with a `validate:` block — other fields pass raw JSON to DB → 500s / driver coercion. Integer fields accept `1.5`. Regex compiled per request; invalid pattern silently skipped. Unknown body keys silently dropped (maybe fine, but should be a choice).
- **Fix:** Always type-check every provided field (string/int/float/bool/uuid/timestamp/enum values/jsonb). Precompile regexes at schema build; invalid pattern = config validation error. Optional `strict: true` to 400 on unknown fields.
- **Done when:** Wrong-typed field → 400 with field error, never reaches DB.

### P3.5 `[x]` Delete/update hook + row-access ordering
- **Where:** `internal/handler/delete.go`, `update.go`
- **Problem:** `BeforeDelete` runs before row-access resolution and before existence check → hooks fire for rows caller can't touch. `opts.soft_delete: true` on entity without soft delete silently hard-deletes. Hooks can't return client errors (always 500).
- **Fix:** Resolve row access first; pass existing record to before-hooks. Config validation error for soft-delete mismatch. SDK error type carrying status + message (e.g. `sdk.HookError{Status: 422}`).
- **Done when:** Hook not called for inaccessible rows; plugin can return 4xx.

### P3.6 `[x]` Fail-fast on startup misconfiguration
- **Where:** `pkg/server/server.go`
- **Problem:** Cron init, OpenAPI handler, auto-migrate diff/apply failures all `log.Warn` and continue → server runs degraded or on wrong schema.
- **Fix:** Treat as fatal by default; optional `server.strict_startup: false` escape hatch.
- **Done when:** Any startup subsystem error exits non-zero.

### P3.7 `[x]` Create ownership / row access on create
- **Problem:** `row_access` applies to list/get/update/delete only. No way to force `author_id = subject.id` on create → users can create records owned by others.
- **Fix:** Field-level `default_from: subject.id` (server-set, client value ignored) and/or `create.row_access` check against inserted values.
- **Done when:** Owner field set from token regardless of body.

### P3.8 `[x]` Optimistic concurrency + idempotency
- **Problem:** Last-write-wins on update; retries of POST create duplicates.
- **Fix:** `ETag` (version column or `updated_at` hash) + `If-Match` → 412. Optional `Idempotency-Key` header store for POST.
- **Done when:** Stale `If-Match` → 412; repeated idempotency key returns original response.

---

## Phase 4 — Multi-replica / operational safety

### P4.1 `[x]` Migrations: transactions + locking
- **Where:** `internal/migration/runner.go`, `pkg/server/server.go`
- **Problem:** Each migration runs without a tx → partial failure leaves half-applied schema, unrecorded. No lock → N replicas booting with auto-migrate race. Auto-migrate generates files on disk at boot (bad on read-only containers) and only targets default DB. Checksum drift not enforced at `up`.
- **Fix:** Wrap tx-safe portion + bookkeeping insert in one tx (Postgres/SQLite; document MySQL DDL caveat). Advisory lock (`pg_advisory_lock`, MySQL `GET_LOCK`, SQLite file lock). Recommend auto-migrate off in prod; if on, apply in-memory without writing files. Migrate every configured DB for its entities. Refuse `up` on checksum mismatch.
- **Done when:** Two concurrent `migrate up` → one applies, other waits/no-ops; failed migration leaves no partial state (Postgres).

### P4.2 `[x]` Cron jobs run on every replica
- **Where:** `internal/cron/scheduler.go`
- **Problem:** No leader election / distributed lock → every instance runs every job. SQL job DDL filter is prefix-only (a leading `-- comment` line before `CREATE` passes) — acceptable since config is trusted, but document it as a guardrail, not a security boundary.
- **Fix:** gocron supports `Elector`/`Locker` — implement DB-backed locker (advisory lock or lock table). Config `jobs.distributed: true` default when >1 replica expected.
- **Done when:** Two instances → each tick executes once.

### P4.3 `[x]` Reliable webhook + email delivery
- **Where:** `internal/webhook/webhook.go`, `internal/mailer/mailer.go`, `internal/plugin/host.go`
- **Problem:** Fire-and-forget goroutines: no retry, no persistence, lost on shutdown/crash, unbounded goroutine fan-out. No webhook signature. `After*` hook errors discarded. Non-2xx just logged.
- **Fix:** Outbox table written in same tx as the entity change (needs P3.3 tx plumbing); worker pool with exponential backoff + max attempts + dead-letter status. HMAC-SHA256 signature header (`X-Yaypi-Signature`, timestamped). Drain on graceful shutdown.
- **Depends on:** P1.9, P3.3
- **Done when:** Target down → retried with backoff; restart doesn't lose pending deliveries; receivers can verify signature.

### P4.4 `[x]` DB pool + query timeouts
- **Where:** `internal/db/manager.go`, `internal/config/types.go`
- **Problem:** `MaxOpenConns` default unlimited; no `ConnMaxIdleTime`; no per-query timeout (only request ctx).
- **Fix:** Sensible defaults (e.g. 25 open / 25 idle / 5m lifetime / 1m idle time). Config `query_timeout` applied via ctx in builder. Postgres `statement_timeout` option.
- **Done when:** Defaults applied and visible in config reference.

### P4.5 `[x]` Graceful shutdown completeness
- **Problem:** HTTP shutdown OK, but in-flight webhooks/emails dropped, plugins' `Shutdown()` never called, cron stop not bounded by shutdown timeout. Readiness doesn't flip to failing during drain.
- **Fix:** Shutdown sequence: mark not-ready → wait drain delay → `srv.Shutdown` → stop cron → drain outbox workers → plugin `Shutdown` → close DBs, all under the timeout.
- **Done when:** SIGTERM under load completes in-flight requests and deliveries within timeout.

---

## Phase 5 — Observability

### P5.1 `[x]` Structured JSON logging + levels
- **Where:** `cmd/yaypi/main.go` (always `ConsoleWriter`), `internal/middleware/logger.go`
- **Fix:** `log.format: json|console` (default json when not a TTY), `log.level`. Add subject ID, route pattern (not raw path) to request logs. Honour `omit_log` fields anywhere records are logged. Redact query-param API keys/tokens.
- **Done when:** Prod default emits one JSON line per request with request_id.

### P5.2 `[x]` Metrics
- **Done as:** dependency-free Prometheus text exposition (`internal/metrics`), so no new module was added.
- **Fix:** Optional `/metrics` (Prometheus): request count/latency by route+status, DB pool stats, rate-limit rejections, hook/webhook outcomes, cron runs. Protect endpoint (separate port or auth).
- **Note:** New dependency (`prometheus/client_golang`) — needs discussion per AGENTS.md. Alternative: expose `expvar`-style JSON with zero deps.

### P5.3 `[x]` Tracing
- **Done as:** W3C `traceparent` propagation + trace ids in logs (`X-Trace-Id`). An OTel SDK/exporter is **not** bundled (new dependency → needs discussion); trace ids are compatible if one is added later.
- **Fix:** Optional OpenTelemetry (HTTP server spans, DB spans, outbound webhook spans); propagate `traceparent`. New dependency — discuss.

### P5.4 `[x]` Audit log
- **Fix:** Optional per-entity `audit: true` → append-only table (who, what entity/id, action, diff, request_id, timestamp). Written in same tx as change.
- **Depends on:** P3.3 tx plumbing

### P5.5 `[x]` Health endpoint hardening
- **Where:** `internal/health/handler.go`
- **Problem:** `/ready` returns raw DB error strings (may leak hosts/creds fragments).
- **Fix:** Return generic status publicly; full errors in logs only (or behind flag). Include migration-pending check option.

---

## Phase 6 — HTTP surface polish

### P6.1 `[x]` Security headers
- **Fix:** Default `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`, `X-Frame-Options: DENY`, `Cache-Control: no-store` on auth responses; HSTS when TLS configured. Configurable.

### P6.2 `[x]` Consistent error format
- **Problem:** `{"error": "..."}` vs `{"errors": {...}}` vs bulk shapes; no machine-readable codes; no request_id in body.
- **Fix:** Single envelope, e.g. RFC 7807 `application/problem+json` with `code`, `message`, `fields`, `request_id`. Update OpenAPI generator to match.

### P6.3 `[x]` Request ID trust
- **Where:** `internal/middleware/requestid.go`
- **Fix:** Validate/length-limit incoming `X-Request-ID` (log injection); generate if invalid.

---

## Phase 7 — Testing & CI

### P7.1 `[x]` CI pipeline
- **Problem:** `.github/workflows` has only `release.yml` — no PR checks.
- **Fix:** Workflow on PR: `go build ./...`, `go vet ./...`, `go test -race ./...`, `govulncheck`, `staticcheck`/`golangci-lint`.

### P7.2 `[x]` Unit test coverage for critical paths
- **Fix:** Tests for every Phase 1–3 item as they land (the "Done when" checks). Priority: auth handler, middleware chain, query builder SQL output per dialect, validation, row filter binding, cursor round-trip.

### P7.3 `[x]` Integration matrix across dialects
- **Fix:** CI job with Postgres + MySQL service containers + SQLite running CRUD, pagination, migrations, auth end-to-end (RETURNING vs. LastInsertId fallback paths differ).
- **Depends on:** P7.1

---

## Phase 8 — Feature gaps (product, not hardening)

Lower priority; pick per roadmap.

- `[x]` **P8.1** Richer filters: `gt/gte/lt/lte/ne/in/like/is_null`, with per-field allowlist (`?price[gte]=10`).
- `[x]` **P8.2** Relation includes/expansion (`?include=author`) — `LoadRelation` exists but is unused; enforce row access + field access on included entities.
- `[x]` **P8.3** Sparse fieldsets (`?fields=id,title`).
- `[x]` **P8.4** Full-text search option per entity.
- `[x]` **P8.5** PUT (full replace) alongside PATCH.
- `[~]` **P8.6** File upload/download (S3, multi-bucket, `type: file` / `type: files`, optional) — designed in [file_management.md](file_management.md); not yet implemented.
- `[x]` **P8.7** Multi-tenancy primitive (tenant column auto-scoped from token claim).
- `[-]` **P8.8** Config hot reload — not doing: restart-only is documented in docs/production.md. Draining + graceful shutdown make rolling restarts zero-downtime.

---

## Log

Record completed work here (date · item · commit · notes).

| Date | Item | Commit | Notes |
|---|---|---|---|
| 2026-10-05 | P1.1–P1.9 | uncommitted | register role lock-down, roles w/o Casbin, fail-closed policy, CORS, secret validation, body limit + timeouts, trusted-proxy rate limiter, OAuth (state cookie, PKCE, verified email, fragment), dial-time SSRF client |
| 2026-10-05 | P2.1–P2.7 | uncommitted | `internal/token` (HS/RS/ES, iss/aud, JWKS), stored rotating refresh tokens with reuse detection, logout(-all), token_version revocation, throttle, password reset, email verification, hashed API keys + `yaypi apikey generate`. P2.8 deferred |
| 2026-10-05 | P3.1–P3.8 | uncommitted | query builder rewrite (Executor, keyset cursors, typed filters, includes), DB-error mapping, transactional bulk, strict coercion, hook ordering + `sdk.HookError`, fail-fast boot, `default_from`, ETag/If-Match, Idempotency-Key |
| 2026-10-05 | P4.1–P4.5 | uncommitted | transactional + locked + drift-checked migrations per DB, cron DB lease lock/retry/tz, transactional outbox for webhooks/emails (signed), pool defaults + request timeout, staged graceful shutdown |
| 2026-10-05 | P5–P7 | uncommitted | JSON logging, metrics, traceparent, audit log, readiness hardening, security headers, error envelope + OpenAPI, CI (fmt/vet/race/staticcheck/govulncheck) + Postgres/MySQL/SQLite integration suite; bumped pgx/jwt/x-text for govulncheck |
| 2026-10-05 | P8 | uncommitted | operator filters, includes, sparse fields, `?q=` search, PUT, `tenant_scoped`; P8.6/P8.8 deferred |
| 2026-10-05 | docs | uncommitted | `.ai/*`, `docs/*` (new `docs/production.md`), JSON schemas, AGENTS.md |
| 2026-10-05 | config coverage | uncommitted | `smtp:` block (env fallback) + `smtp.retry` / `emails[].retry`; auth reset/verify emails now queued via the outbox; `outbox.poll_interval/retention`, `server.idempotency_ttl`, `cron.distributed`, `audit.retention`; per-endpoint `max_body_size`; hooks receive non-column body keys |
