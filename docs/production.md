# Running in Production

yayPi is secure by default where it can be. This page covers what you still need to decide when deploying, and how to operate the server.

## Checklist

**Secrets**
- [ ] `auth.secret` comes from the environment and is ≥ 32 random bytes (`openssl rand -hex 32`). The server refuses to start otherwise.
- [ ] Database DSNs, SMTP credentials, webhook `secret`s, `metrics.token` and API keys come from `${ENV_VAR}`s.
- [ ] DB-backed API keys store digests only: create keys with `yaypi apikey generate`.

**Network edge**
- [ ] `server.trusted_proxies` lists your load balancer / ingress CIDRs, so rate limiting and logs see real client IPs. Leave it empty if clients connect directly.
- [ ] `server.allowed_origins` lists your frontends explicitly. Avoid `"*"`: it is allowed but never with credentials.
- [ ] `server.hsts_max_age: 31536000` once the API is HTTPS-only.
- [ ] `auth.cookie.secure` is left at `true`.
- [ ] A global `server.rate_limit` is set, plus tighter per-endpoint limits on expensive routes.

**Auth**
- [ ] `refresh.enabled: true` with a short `auth.expiry` (default 15m).
- [ ] Consider `auth.revocation_check: true` if logouts and role changes must apply instantly (one indexed query per request).
- [ ] With OAuth, keep `token_delivery: fragment` and `pkce: true` (the defaults). Use `trust_email` only for IdPs you control.

**Data**
- [ ] `auto_migrate: false`. Run migrations as a deploy step (below).
- [ ] Every `row_access` list ends with a rule that matches everyone you intend to serve (`when: "*"`), so nobody falls through to 403/404 unexpectedly.
- [ ] Owner columns use `default_from: subject.id` so clients can't create records owned by others.
- [ ] Sensitive entities have `audit: true`.

**Operations**
- [ ] `server.health.enabled: true`. Point liveness at `/health` and readiness at `/ready`.
- [ ] `server.drain_delay` is about your load balancer's deregistration time (5–10s on Kubernetes), and `terminationGracePeriodSeconds` exceeds `drain_delay + shutdown_timeout`.
- [ ] `metrics.enabled: true` behind `metrics.token` or network policy.
- [ ] Logs are shipped as JSON (the default when not attached to a terminal).

## Migrations as a deploy step

```bash
yaypi migrate generate --name add_orders   # in development; commit the files
yaypi migrate up                           # in the deploy pipeline, before rolling out
```

- Files live next to `yaypi.yaml`: `migrations/` for the default database, `migrations/<name>/` for others.
- Each migration runs in a transaction together with its bookkeeping row. On Postgres and SQLite a failure leaves nothing behind. MySQL commits DDL implicitly, so a failed MySQL migration can be partial — review it before re-running.
- `up` and `down` take a database lock (`pg_advisory_lock` / `GET_LOCK`), so concurrent deploy jobs or replicas apply migrations one at a time.
- `up` refuses to run if an already-applied file was edited (checksum drift). Fix the file or pass `--allow-drift` deliberately.
- `CREATE INDEX CONCURRENTLY` statements run after the transaction. They are `IF NOT EXISTS`, so re-running one by hand is safe.

## Multiple replicas

Everything that must happen once is coordinated through the database:

| Concern | How |
|---|---|
| Cron jobs | `yaypi_job_locks` lease per job: each tick runs on one replica |
| Webhooks / emails | `yaypi_outbox`: rows are claimed with a lease, so each message is delivered once (at-least-once on crashes) |
| Migrations | Advisory lock |
| Refresh tokens / reset tokens | Stored in the database |
| Idempotency keys | `yaypi_idempotency_keys` |

Two things are per instance: the rate limiter and the login throttle. With N replicas, a client gets up to N× the configured budget. Size the limits accordingly, or enforce them at the edge.

## Webhooks and email delivery

Messages are written to `yaypi_outbox` in the same transaction as the change that triggered them, then delivered by a background worker:

- Failures retry with exponential backoff and jitter (`retry.max_attempts`, default 8 for webhooks, 5 for emails). After that the row gets `status = 'dead'` with `last_error`. Query the table to monitor or replay deliveries.
- Delivered rows are deleted after `outbox.retention` (default 7 days). Email retry defaults come from `smtp.retry`.
- Receivers should verify `X-Yaypi-Signature` (`v1=` + hex HMAC-SHA256 of `"<X-Yaypi-Timestamp>.<raw body>"` with the webhook `secret`), reject old timestamps, and dedupe on `X-Yaypi-Event-Id`.
- Webhook targets resolving to private, loopback or link-local addresses (including cloud metadata) are refused unless `allow_private_network: true`.

## Graceful shutdown

On SIGTERM/SIGINT:

1. `/ready` returns 503 `draining`, and the server keeps serving for `drain_delay`.
2. It stops accepting connections and lets in-flight requests finish (`shutdown_timeout`).
3. Cron stops. The outbox worker finishes its current delivery; undelivered messages stay queued for the next start.
4. Plugins' `Shutdown` runs, then database pools close.

## Observability

**Logs:** one JSON line per request with `request_id`, `trace_id`, `method`, `route` (the pattern, e.g. `/api/v1/posts/{id}`), `path`, `status`, `size`, `duration`, `client_ip` and `subject`. Query strings are never logged.

**Tracing:** an incoming W3C `traceparent` is continued, and its trace id is logged and returned as `X-Trace-Id`. Requests without one start a new trace. (An OpenTelemetry exporter is not bundled.)

**Metrics** (`/metrics`, Prometheus text format):

| Metric | Labels |
|---|---|
| `yaypi_http_requests_total` | method, route, status |
| `yaypi_http_request_duration_seconds` (histogram) | method, route |
| `yaypi_db_connections` | database, state (open/in_use/idle) |
| `yaypi_db_wait_seconds_total` | database |
| `yaypi_cron_runs_total` | job, result |
| `yaypi_outbox_deliveries_total` | kind, result |

**Audit log** (`audit.retention` purges old rows; default keeps everything): with `audit: true` on an entity, every change writes a row to `yaypi_audit_log`: entity, record id, action, actor id and role, request id, and a field-level diff with `omit_log` fields redacted. The row is written in the same transaction as the change.

## Configuration changes

Configuration is read at startup; there is no hot reload. With readiness draining and graceful shutdown, a rolling restart applies config changes without dropping requests.
