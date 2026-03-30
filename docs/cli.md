# CLI Reference

## Global flag

All commands accept:

```
--config, -c <path>   Path to yaypi.yaml (default: yaypi.yaml)
```

## Commands

### `yaypi init <name>`

Scaffold a new yayPi project.

```bash
yaypi init my-api
```

Creates:

```
my-api/
├── yaypi.yaml
├── entities/
├── endpoints/
└── policies/
    └── model.conf
```

Then prints next steps:

```
Project "my-api" created. Next steps:
  cd my-api
  export DATABASE_URL=postgres://localhost/my-api
  yaypi run
```

---

### `yaypi validate`

Validate all configuration files. Loads config, expands includes, checks cross-references, and warns about sensitive plain-text values. Exits 0 on success, non-zero on error.

```bash
yaypi validate
yaypi validate --config path/to/yaypi.yaml
```

**Success:**
```
INF configuration is valid
```

**With warning:**
```
WRN auth.secret contains a plain-text value; use ${ENV_VAR} instead
INF configuration is valid
```

**With error:**
```
ERR entity "Author" referenced in endpoint but not defined  file=endpoints/posts.yaml
1 validation error(s)
```

**What it checks:**
- All entity names referenced in endpoints exist
- All entity names referenced in relations exist
- No circular entity references
- `references.entity` targets exist
- Sensitive fields are not plain-text values (warning only)

---

### `yaypi run`

Start the API server. Blocks until interrupted.

```bash
yaypi run
yaypi run --config path/to/yaypi.yaml
```

**Startup output:**
```
INF server starting addr=:8080 base_url=/api/v1
```

**Graceful shutdown** (on SIGINT or SIGTERM):
```
INF shutting down signal=interrupt
```

The server waits up to `server.shutdown_timeout` for in-flight requests to complete before exiting.

On startup, seed files (`kind: seed`) are applied automatically before routes are registered.

**Plugins:** When you need plugins, use yaypi as a library in your own `main.go` instead of this command. See [Plugins](plugins.md) for details.

---

### `yaypi seed`

Run seed files manually (outside of server startup). Useful for populating a fresh database or re-seeding in CI.

```bash
yaypi seed
yaypi seed --config path/to/yaypi.yaml
```

Seeds are **idempotent** — rows are only inserted if a row with the matching `key_field` value does not already exist. Running this multiple times is safe.

**Output:**
```
INF seed applied entity=Role key=admin
INF seed skipped (already exists) entity=Role key=editor
INF seed complete
```

Seed files must be included via the `include:` globs in `yaypi.yaml`:

```yaml
include:
  - seeds/**/*.yaml
```

See [Concepts — Seed data](concepts.md#seed-data) for the full file format reference.

---

### `yaypi migrate generate`

Generate migration files by diffing entity definitions against the live database.

```bash
yaypi migrate generate
yaypi migrate generate --name add_user_bio
```

**Flags:**

| Flag | Default | Description |
|---|---|---|
| `--name` | `migration` | Name embedded in the migration filename |

**Output:**
```
INF migration files generated up=migrations/20240315120000_add_user_bio.up.sql down=migrations/20240315120000_add_user_bio.down.sql
```

If the schema is already up to date (no diff), no files are generated and the command exits cleanly.

---

### `yaypi migrate up`

Apply pending migrations in chronological order.

```bash
yaypi migrate up
yaypi migrate up --steps 1
```

**Flags:**

| Flag | Default | Description |
|---|---|---|
| `--steps` | `0` (all) | Number of pending migrations to apply |

**Output:**
```
INF migrations applied
```

---

### `yaypi migrate down`

Roll back applied migrations.

```bash
yaypi migrate down --steps 1
```

**Flags:**

| Flag | Required | Description |
|---|---|---|
| `--steps` | yes | Number of migrations to roll back |

`--steps` is required to prevent accidental rollbacks.

---

### `yaypi migrate status`

Show the status of all migrations.

```bash
yaypi migrate status
```

**Output:**
```
APPLIED  20240315120000_create_users     (at 2024-03-15 12:00:00 +0000 UTC)
APPLIED  20240315120001_create_posts     (at 2024-03-15 12:00:01 +0000 UTC)
PENDING  20240315120002_add_user_bio
```

---

### `yaypi migrate verify`

Verify that all applied migration files match their recorded SHA-256 checksums. Detects tampering or accidental edits.

```bash
yaypi migrate verify
```

**Success:**
```
INF all migration checksums verified
```

**Failure:**
```
ERR checksum mismatch for migration "20240315120000_create_users"
```

## CI/CD recommended pipeline

```yaml
# Example GitHub Actions step
- name: Migrate and verify
  run: |
    yaypi validate
    yaypi migrate generate --name ci_$(date +%Y%m%d)
    yaypi migrate up
    yaypi migrate verify
    yaypi seed
```

In production, always run `verify` after deploying to confirm migration files have not been modified.

## `yaypi spec`

Commands for generating OpenAPI specs. Requires at least one `spec:` entry in `yaypi.yaml`. See [OpenAPI](openapi.md) for configuration details.

### `yaypi spec generate`

Generate an OpenAPI 3.1 JSON spec to a file.

```bash
yaypi spec generate --name api
yaypi spec generate --name api --output docs/openapi.json
yaypi spec generate --name sdk --output sdk-spec.json --config path/to/yaypi.yaml
```

| Flag | Default | Description |
|---|---|---|
| `--name` | required | Name of the spec to generate (must match a `spec[].name` in `yaypi.yaml`) |
| `--output` | `openapi.json` | Output file path |
| `--config` | `yaypi.yaml` | Path to `yaypi.yaml` |

The generated file is valid OpenAPI 3.1 JSON that can be used with tools like Swagger UI, Redoc, or OpenAPI Generator.
