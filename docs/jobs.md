# Jobs

Background jobs run on a schedule inside the same process as your API server. They start and stop with `yaypi run`.

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
| `timezone` | string | no | IANA timezone name (e.g. `America/New_York`); default UTC |
| `handler` | string | yes | `sql` or `http` |
| `timeout` | duration | no | Max execution time (default: 30s for HTTP) |
| `retry` | object | no | Retry configuration |
| `on_failure` | string | no | `log` (default) |
| `config` | map | yes | Handler-specific configuration |

### `retry` fields

| Field | Type | Description |
|---|---|---|
| `max_attempts` | integer | Maximum number of total attempts |
| `backoff` | string | `exponential` or `linear` |
| `initial_delay` | duration | Delay before first retry |
| `max_delay` | duration | Cap on delay between retries |

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

**Security restrictions:**
- DDL statements (`CREATE`, `DROP`, `ALTER`, `TRUNCATE`, `GRANT`, `REVOKE`) are rejected at startup
- Multi-statement SQL (containing `;` mid-statement) is rejected
- Only uses parameterized queries via the connection pool

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
- Requests to loopback addresses (`127.x.x.x`, `::1`) are always blocked
- Requests to link-local addresses (`169.254.x.x`) are always blocked
- Requests to RFC-1918 private addresses (`10.x`, `172.16-31.x`, `192.168.x`) are always blocked
- DNS names are resolved before checking — hostnames that resolve to blocked IPs are rejected
- The `allowed_hosts` list is an allowlist; if set, the target hostname must be in the list

These restrictions prevent SSRF (Server-Side Request Forgery) attacks if job configs are user-configurable.

## Complete example

See [`examples/blog/jobs/maintenance.yaml`](../examples/blog/jobs/maintenance.yaml) for a working example with two SQL jobs and one HTTP job.
