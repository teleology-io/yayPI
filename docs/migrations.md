# Migrations

yayPi includes a diff-based migration engine that generates SQL migration files from your entity definitions.

## How the diff engine works

When you run `yaypi migrate generate`, the engine:

1. Queries `information_schema.tables` and `information_schema.columns` to see what currently exists in the database
2. Queries `pg_indexes` for existing indexes
3. Compares the live schema to your entity registry
4. Generates only the DDL statements needed to close the gap

**What is auto-detected:**
- New tables → `CREATE TABLE IF NOT EXISTS`
- New columns → `ALTER TABLE … ADD COLUMN`
- New indexes → `CREATE INDEX CONCURRENTLY IF NOT EXISTS`
- New FK constraints (as part of `CREATE TABLE`)
- New CHECK and UNIQUE constraints (as part of `CREATE TABLE`)

**What is NOT auto-detected:**
- Dropped tables or columns (the engine warns and skips)
- Renamed tables or columns (appear as new table/column + old one still exists)

Never auto-drops prevent accidental data loss. To drop a column, write the DDL manually.

## Migration file format

Generated files are named `{timestamp}_{name}.up.sql` and `{timestamp}_{name}.down.sql`:

```
migrations/
├── 20240315120000_create_users.up.sql
├── 20240315120000_create_users.down.sql
├── 20240315120001_add_avatar_url.up.sql
└── 20240315120001_add_avatar_url.down.sql
```

### Up file structure

```sql
-- Migration: create_users
-- Direction: up
-- SHA-256: a3f2c1...

BEGIN;

CREATE TABLE IF NOT EXISTS "users" (
  "id" uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  "email" varchar(255) NOT NULL UNIQUE,
  "created_at" timestamptz NOT NULL DEFAULT now(),
  "updated_at" timestamptz NOT NULL DEFAULT now()
);

COMMIT;

-- Run outside transaction (CONCURRENTLY cannot run in a transaction block):
CREATE INDEX CONCURRENTLY IF NOT EXISTS "idx_users_email" ON "users" USING btree ("email");
```

The file has two sections:
1. `BEGIN`/`COMMIT` block — DDL that runs in a transaction
2. `-- Run outside transaction` — `CREATE INDEX CONCURRENTLY` statements that must run outside a transaction

### Down file structure

```sql
-- Migration: create_users
-- Direction: down
-- SHA-256: a3f2c1...

BEGIN;

DROP TABLE IF EXISTS "users";

COMMIT;
```

Down files are best-effort reverse DDL. Always review them before running — the engine cannot always infer the correct reversal (e.g., for `ADD COLUMN` it generates `DROP COLUMN`, which loses data).

## `yaypi_migrations` tracking table

yayPi creates a `yaypi_migrations` table in your database to track which migrations have been applied:

```sql
CREATE TABLE IF NOT EXISTS yaypi_migrations (
  name       text PRIMARY KEY,
  checksum   text NOT NULL,
  applied_at timestamptz NOT NULL DEFAULT now()
);
```

## Type mapping

yayPi maps YAML field types to the appropriate SQL type for the target database.

| YAML type | PostgreSQL | MySQL | SQLite |
|---|---|---|---|
| `uuid` | `uuid` | `CHAR(36)` | `TEXT` |
| `string` | `varchar(255)` / `varchar(N)` | `VARCHAR(255)` / `VARCHAR(N)` | `TEXT` |
| `text` | `text` | `TEXT` | `TEXT` |
| `integer` | `integer` | `INT` | `INTEGER` |
| `bigint` | `bigint` | `BIGINT` | `INTEGER` |
| `float` | `double precision` | `DOUBLE` | `REAL` |
| `decimal` | `numeric(P,S)` | `DECIMAL(P,S)` | `REAL` |
| `boolean` | `boolean` | `TINYINT(1)` | `INTEGER` |
| `timestamptz` | `timestamptz` | `DATETIME` | `TEXT` |
| `date` | `date` | `DATE` | `TEXT` |
| `jsonb` | `jsonb` | `JSON` | `TEXT` |
| `enum` | `text` + CHECK constraint | `ENUM(values)` | `TEXT` + CHECK constraint |
| `array` | `text[]` | `TEXT` (serialized) | `TEXT` |
| `bytea` | `bytea` | `BLOB` | `BLOB` |

`length:`, `precision:`, and `scale:` are respected where the target type supports them.

### Switching databases

The migration engine automatically uses the correct type mapping based on the `driver` field in your database config:

```yaml
databases:
  - name: primary
    driver: sqlite        # generates SQLite-compatible DDL
    dsn: ./dev.db
    default: true
```

Running `yaypi migrate generate` against a SQLite database produces SQLite-compatible `CREATE TABLE` statements, while PostgreSQL produces PostgreSQL DDL.

## Full CLI workflow

### 1. Generate

```bash
yaypi migrate generate --name add_user_bio
```

Output:
```
INF migration files generated up=migrations/20240315120000_add_user_bio.up.sql down=migrations/20240315120000_add_user_bio.down.sql
```

### 2. Review

Always read the generated files before applying:

```bash
cat migrations/20240315120000_add_user_bio.up.sql
```

Check that the DDL matches your intent. The engine is conservative — it should never generate destructive statements — but it's good practice.

### 3. Apply

```bash
yaypi migrate up
```

To apply a specific number of pending migrations:

```bash
yaypi migrate up --steps 1
```

### 4. Check status

```bash
yaypi migrate status
```

Output:
```
APPLIED  20240315120000_create_users  (at 2024-03-15 12:00:00)
APPLIED  20240315120001_create_posts  (at 2024-03-15 12:00:01)
PENDING  20240315120002_add_user_bio
```

### 5. Verify checksums

After deploying, verify that no migration files have been modified since they were applied:

```bash
yaypi migrate verify
```

Output on success:
```
INF all migration checksums verified
```

If a checksum fails, it means the `.up.sql` file was edited after being applied — a potential sign of tampering.

### 6. Roll back

```bash
yaypi migrate down --steps 1
```

`--steps` is required for `down` to prevent accidental rollbacks.

## `auto_migrate`

Setting `auto_migrate: true` in `yaypi.yaml` makes yayPi automatically run `generate` + `up` at startup:

```yaml
auto_migrate: true
```

**Use only in development or CI.** Never use in production — auto-migrations run without review and can cause downtime if a long-running index build blocks startup.

## Example: adding a field

**Before** — `entities/user.yaml` has no `bio` field.

**Step 1:** Add the field:

```yaml
- name: bio
  type: text
  nullable: true
```

**Step 2:** Generate:

```bash
yaypi migrate generate --name add_user_bio
```

Generated `up.sql`:

```sql
BEGIN;
ALTER TABLE "users" ADD COLUMN "bio" text;
COMMIT;
```

Generated `down.sql`:

```sql
BEGIN;
ALTER TABLE "users" DROP COLUMN "bio";
COMMIT;
```

**Step 3:** Apply:

```bash
yaypi migrate up
```
