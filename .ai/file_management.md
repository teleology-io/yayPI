# File management (S3) — design

Status: **planned** (not implemented). Tracked as P8.6 in [production-manifest.md](production-manifest.md).

## Context

`.ai/production-manifest.md` deferred **P8.6 File upload support** (object storage needed a cloud SDK and product decisions). Goal now: make files as painless and transparent as everything else in yayPi — declared in YAML, no Go code:

- Upload/download via S3 (and S3-compatible: R2, MinIO, Spaces, B2).
- One dedicated `files` table; any entity references it with a field `type: file` (single) or `type: files` (ordered list). Responses expand transparently to `{id, url, filename, ...}`.
- Multiple named buckets per project (avatars, documents, exports…).
- Buckets assumed **public** by default; `visibility: private` + access rules (reusing existing auth/roles/conditions/row-access syntax) for full control.
- **Entirely optional**: nothing changes for apps without a `storage:` block — no `files` table, no routes, no extra deps.

Decisions (confirmed): own zero-dependency SigV4 signer; direct-to-S3 presigned uploads by default (per-bucket proxy mode available); field syntax `type: file` / `type: files`.

## Opt-in rule

- Feature is active only when root `storage:` exists with ≥1 bucket.
- Only then: `File` entity (`files` table) is registered (migrations create it), `/files` routes mount, the storage janitor runs.
- `type: file`/`files` anywhere while `storage:` is absent → `config.Validate` error ("add a storage: block").
- `go.mod` unchanged.

## Configuration

```yaml
# yaypi.yaml
storage:
  default_bucket: media
  base_path: /files              # routes under base_url (default /files)
  auth: { require: true }        # default auth for file routes; buckets override
  pending_ttl: 24h               # unconfirmed uploads cleaned up after this
  buckets:
    - name: media                # logical name used by fields and API
      bucket: acme-media-prod    # real S3 bucket
      region: us-east-1
      endpoint: ""               # set for R2/MinIO/Spaces; empty = AWS
      path_style: false          # true for MinIO
      access_key_id: ${S3_KEY}
      secret_access_key: ${S3_SECRET}
      session_token: ""          # optional (STS)
      visibility: public         # public (default) | private
      public_url: https://cdn.acme.com   # base for public URLs (default: S3 virtual-host URL)
      key_prefix: uploads/
      upload: presigned          # presigned (default) | proxy | both
      max_size: 25MB
      accept: [image/*, application/pdf]  # MIME allowlist (default: any)
      url_ttl: 15m               # presigned GET lifetime for private files
      upload_ttl: 15m            # presigned upload lifetime
      delete_objects: true       # delete S3 object when the file row is deleted
      access:                    # optional; defaults: storage.auth
        upload: { require: true, roles: [member, admin] }
        read:                    # private buckets: who gets a download URL via /files/{id}
          - when: 'subject.role == "admin"'
          - when: "*"
            filter: "owner_id = :subject.id"
        delete:
          - when: "*"
            filter: "owner_id = :subject.id"

    - name: documents
      bucket: acme-docs
      region: us-east-1
      visibility: private
      access_key_id: ${S3_KEY}
      secret_access_key: ${S3_SECRET}
```

```yaml
# entity
fields:
  - name: avatar
    type: file
    bucket: media              # default: storage.default_bucket
    accept: [image/png, image/jpeg]   # narrows the bucket allowlist
    max_size: 5MB              # narrows the bucket limit
    nullable: true
    on_delete: keep            # keep (default) | delete_file — when this record is deleted
  - name: attachments
    type: files                # ordered list
    bucket: documents
    max_items: 10
```

## Data model

Built-in `File` entity (like `NewBuiltinUser` in `internal/schema/builtinuser.go`; new `internal/schema/builtinfile.go`), table `files`, **not** `Internal` (so users may FK to it and expose their own endpoints if they want):

| column | type | notes |
|---|---|---|
| id | uuid PK | |
| bucket | string(64) | logical bucket name |
| object_key | string(512) unique (bucket, object_key) | server-generated, never client-controlled |
| filename | string(255) | sanitized original name |
| content_type | string(127) | |
| size | bigint | verified against S3 HEAD |
| checksum | string(64) nullable | sha256 when proxy-uploaded / provided |
| status | string(16) | pending \| ready |
| owner_id | uuid nullable | uploader (subject.id) |
| tenant_id | string nullable | set when uploader has a tenant claim |
| metadata | jsonb nullable | client-supplied small map (validated size) |
| created_at / updated_at / deleted_at | timestamps, soft delete | |

Key format: `{key_prefix}{yyyy}/{mm}/{file_id}/{sanitized-filename}`.

Field mapping (in `internal/schema/resolver.go` `buildField`):
- `type: file` → `Field{Type: uuid, Reference: {File.id, ON DELETE SET NULL}, File: &FileFieldOpts{Bucket, Accept, MaxSize, OnDelete}}`. Existing migration engine then emits the column + FK with no special casing.
- `type: files` → no column on the owner; schema auto-registers an internal join entity `{table}_{field}_files` (`owner_id`, `file_id`, `position`, PK (owner_id, position)) — same pattern as internal entities in `internal/schema/builtinauth.go`.

## Runtime pieces

New package `internal/storage`:
- `sigv4.go` — AWS SigV4 signing: request signing, query presign (GET), and **POST policy** signing (`content-length-range` + `Content-Type` + key conditions → S3 enforces size/type for direct uploads). Unit-tested with AWS's published SigV4 test vectors.
- `s3.go` — minimal client over `net/http`: `PresignGet`, `PresignPost`, `Put` (streaming, proxy mode), `Head`, `Delete`; virtual-host or path-style; custom endpoint.
- `bucket.go` — `Registry` of named buckets from config; `PublicURL(file)`, `DownloadURL(ctx, file)` (public URL or presigned GET), accept/size checks (MIME wildcard match).
- `service.go` — create pending upload, confirm (HEAD → verify size/type → status ready), proxy upload, delete (enqueue), janitor.

HTTP routes (`internal/storage/handler.go`, mounted by `router.Build` only when configured; reuse `middleware.RequireAuth`/`RBAC`, `apierr`, `decodeBody`-style helpers, `withIdempotency`-style safety):

| Method | Path | Purpose |
|---|---|---|
| POST | `/files/uploads` | `{bucket, filename, content_type, size, metadata?}` → creates pending row; returns `{file, upload: {url, fields}}` (presigned POST form) |
| POST | `/files/{id}/complete` | HEAD the object, verify, mark `ready` → `{file}` |
| POST | `/files` | proxy mode: multipart `file` (+ `bucket`) streamed to S3 with sha256; returns ready `{file}`; honours bucket `max_size` via per-route body limit (reuse `middleware.BodyLimit`) |
| GET | `/files/{id}` | metadata + `url` (access: bucket `read` rules for private) |
| GET | `/files/{id}/download` | 302 to public URL or fresh presigned GET |
| DELETE | `/files/{id}` | bucket `delete` rules; soft delete + object deletion via outbox |

Access rules reuse existing types: `schema.Auth` for upload, `[]schema.RowAccessRule` + `policy.ResolveRowFilter` for read/delete (filters evaluated against the `files` table — `owner_id`, `tenant_id`, `bucket` available). Tenant isolation applies automatically to `files` when the uploader has a tenant claim (reuse `scopeTenant` logic from `internal/handler/common.go`).

### Transparent integration with CRUD (`internal/handler`)

- **Write** (create/update/replace, in `prepareInput` → new `coerceFile` in `internal/handler/validate.go`): `type: file` accepts a file id (string uuid) or `null`; `type: files` accepts an array of ids. Inside the write transaction (existing `withTx`), verify each file: exists, `status = ready`, not deleted, bucket matches field, content type/size satisfy field rules, and **owner = caller** (or caller passes bucket `read` rules) — prevents attaching someone else's file. Errors → 400 `validation_failed` per field.
- `type: files` join rows written in the same tx (delete + insert ordered positions).
- **Read** (list/get/create/update responses): after `presentRecord`, a batched loader (one `IN` query per request, like `query.Builder.Include` in `internal/query/relations.go`) replaces file ids with `{id, url, filename, content_type, size, metadata}`; private files get a fresh presigned GET URL (TTL `url_ttl`) — the caller can already read the record, so the URL is granted transparently. `?fields=` and `read_roles` still apply.
- **Filters**: `?avatar[is_null]=true` works as today (uuid column).
- **Delete**: field `on_delete: delete_file` → after commit, enqueue deletion of referenced files (soft delete row + S3 delete via outbox).

### Background work (reuse outbox + cron infrastructure)

- New outbox kind `storage_delete` (`internal/outbox/worker.go` `deliver` switch) → S3 `DELETE`, retried with backoff; dead-letters visible in `yaypi_outbox`.
- Janitor (ticker like `audit.StartRetention` in `internal/audit/audit.go`): hourly, deletes `pending` rows older than `pending_ttl` and their objects; safe across replicas (idempotent deletes).

### Other integration points

- `config/types.go`: `StorageConfig`, `BucketConfig`, `BucketAccess`; `FieldDef.Bucket/Accept/MaxSize/MaxItems/OnDelete`.
- `config/validator.go`: bucket names unique, `default_bucket` exists, field `bucket` exists, `accept` patterns valid, sizes parse (reuse `ParseByteSize`), durations (reuse `ParseDuration`), private bucket needs credentials, file fields require `storage:`.
- `openapi/builder.go`: `File` component schema; `type: file` fields documented as `File` on read, `uuid` on write; `/files` operations.
- `pkg/sdk`: optional `Storage` accessor in `InitContext` (`PresignGet`, `Put`, `Delete`) so plugins (e.g. `curriculumsync`) can use buckets without their own S3 code.
- `schemas/root.schema.json`, `schemas/entity.schema.json`: `storage` block; `file`/`files` types + field options.
- Metrics: `yaypi_storage_operations_total{bucket,op,result}`.
- Docs: new `docs/files.md`; `.ai/config-reference.md`, `docs/entities.md`, `docs/project-config.md`, `docs/production.md` (CORS on the S3 bucket for browser uploads, public-bucket guidance, lifecycle rules), manifest P8.6 → done.

## Security notes

- Object keys are server-generated; filenames sanitized (strip paths/control chars, cap length) and sent as `Content-Disposition` on download.
- Size/type enforced by S3 POST policy at upload **and** re-verified via HEAD on complete.
- `Content-Type` never trusted for `text/html`/`image/svg+xml` on public buckets unless explicitly in `accept` (stored XSS); downloads of private files use `response-content-disposition=attachment` by default.
- Attaching a file requires ownership/read access (no "file id guessing" to hijack other users' uploads).
- Credentials only via `${ENV}`; `WarnSensitiveValues` extended to bucket secrets.

## Implementation order

1. `internal/storage` signer + client + test vectors.
2. Config types/validation, `File` entity, `type: file|files` mapping, join entities.
3. `/files` routes (presigned + proxy), access rules, tenant scoping.
4. CRUD integration: write validation, response expansion, `on_delete`.
5. Outbox `storage_delete`, janitor, metrics.
6. OpenAPI, JSON schemas, SDK accessor, docs.

## Verification

- Unit: SigV4 against AWS test vectors; POST policy encoding; MIME wildcard matching; filename sanitizing.
- Handler tests (SQLite + `httptest` fake S3 that validates signatures and stores objects in memory): upload → complete → attach to entity → GET expands with URL; private bucket gets presigned URL; attaching another user's file → 400; oversize/wrong type rejected; `files` ordering; `on_delete: delete_file` enqueues deletion; pending janitor.
- Integration (`internal/integration`): add MinIO service to `.github/workflows/ci.yml`; full flow against real S3 API on Postgres/MySQL/SQLite.
- Opt-out check: existing integration + example configs produce no `files` table and identical route set when `storage:` is absent.
- `go build ./... && go vet ./... && staticcheck && go test -race ./...`; `yaypi validate` on examples.
