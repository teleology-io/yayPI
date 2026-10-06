# Jobs

Background jobs run on a schedule inside the same process as your API server. They start and stop with `yaypi run`.

**Multiple replicas:** when a database is configured (and `cron.distributed` isn't `false` in `yaypi.yaml`), every run takes a lease row in `yaypi_job_locks`, so each scheduled tick executes on exactly one replica, however many are running. A job never overlaps a still-running previous run on the same instance.

## File structure

```yaml
version: "1"
kind: jobs

jobs:
  - name: purge-old-sessions
    description: Remove expired sessions nightly
    schedule: "@daily"
    timezone: UTC
    handler: sql
    timeout: 60s
    retry:
      max_attempts: 3
      backoff: exponential
      initial_delay: 5s
      max_delay: 60s
    on_failure: log
    config:
      sql: DELETE FROM sessions WHERE expires_at < now()
      database: primary
```

## Job fields

| Field | Type | Required | Description |
|---|---|---|---|
| `name` | string | yes | Unique name for logging and identification |
| `description` | string | no | Human-readable description |
| `schedule` | string | yes | Cron expression or shortcut (see below) |
| `timezone` | string | no | IANA timezone name (e.g. `America/New_York`); default UTC. Invalid names fail startup |
| `handler` | string | yes | `sql` or `http` |
| `timeout` | duration | no | Max time per attempt (default 5m); the job's context is cancelled after it |
| `retry` | object | no | Retry configuration |
| `on_failure` | string | no | `log` (default). Failures are also counted in `yaypi_cron_runs_total` |
| `config` | map | yes | Handler-specific configuration |

### `retry` fields

| Field | Type | Description |
|---|---|---|
| `max_attempts` | integer | Maximum number of total attempts (default 1 — no retry) |
| `backoff` | string | `exponential` (default, doubles each time) or `fixed` |
| `initial_delay` | duration | Delay before the first retry (default 1s) |
| `max_delay` | duration | Cap on the delay between retries (default 1m) |

## Schedules

### Named shortcuts

| Shortcut | Equivalent | Fires at |
|---|---|---|
| `@yearly` / `@annually` | `0 0 1 1 *` | Jan 1 at midnight |
| `@monthly` | `0 0 1 * *` | 1st of month at midnight |
| `@weekly` | `0 0 * * 0` | Sunday at midnight |
| `@daily` / `@midnight` | `0 0 * * *` | Every day at midnight |
| `@hourly` | `0 * * * *` | Every hour at :00 |
| `@minutely` | `* * * * *` | Every minute |

### `@every` intervals

```yaml
schedule: "@every 15m"    # every 15 minutes from server boot
schedule: "@every 1h30m"  # every 90 minutes from server boot
schedule: "@every 24h"    # every 24 hours from server boot
```

**Important distinction:** `@every 1h` counts from when the server starts. `@hourly` fires at `:00` on the clock regardless of when the server started. Use named shortcuts when you need clock-aligned execution (e.g. run at midnight exactly).

### 5-field cron

Standard cron format: `"minute hour day-of-month month day-of-week"`

```yaml
schedule: "30 2 * * *"     # 2:30 AM every day
schedule: "0 9 * * 1"      # 9:00 AM every Monday
schedule: "0 */6 * * *"    # every 6 hours at :00
schedule: "15 10 1 * *"    # 10:15 AM on the 1st of every month
```

### 6-field cron (with seconds)

Add a seconds field at the start: `"second minute hour day-of-month month day-of-week"`

```yaml
schedule: "30 * * * * *"   # every minute at :30 seconds
schedule: "0 0 * * * *"    # every hour at :00:00
```

## SQL handler

Executes a single SQL DML statement against a database.

```yaml
handler: sql
config:
  sql: |
    DELETE FROM posts
    WHERE deleted_at IS NOT NULL
      AND deleted_at < now() - INTERVAL '90 days'
  database: primary    # optional; defaults to the default database
```

**Guardrails** (job SQL is trusted config, so these catch mistakes rather than defend against attackers):
- Statements starting with DDL (`CREATE`, `DROP`, `ALTER`, `TRUNCATE`, `GRANT`, `REVOKE`) are rejected when the job runs
- Multi-statement SQL (a `;` before the end) is rejected

## HTTP handler

Makes an outbound HTTP request on a schedule (e.g. ping an uptime monitor).

```yaml
handler: http
config:
  url: https://uptime.example.com/ping/abc123
  method: GET           # default GET
  allowed_hosts:
    - uptime.example.com
    - api.monitoring.io
```

**Security restrictions:**
- The connection is refused if the target's resolved IP is loopback, private (RFC 1918, IPv6 ULA), link-local (including the `169.254.169.254` cloud metadata address), CGNAT, or unspecified. The check runs at connect time, so DNS names and redirects can't be used to bypass it.
- Set `allow_private_network: true` in `config` for jobs that intentionally call internal services.
- `allowed_hosts`, if set, is an allowlist of target hostnames.
- Responses with status ≥ 400 count as failures (and are retried per `retry`).

## Complete example

See [`examples/blog/jobs/maintenance.yaml`](../examples/blog/jobs/maintenance.yaml) for a working example with two SQL jobs and one HTTP job.
