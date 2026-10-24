# Building an app with yayPi — read before writing Go

yayPi is meant to be configured, not programmed around. Most Go that agents
write in yayPi apps re-implements something the YAML already does, and does
it worse: it skips access rules, drops out of the OpenAPI spec (so typed
clients can't check calls), and needs tests the framework already has.

**Rule: before writing a plugin, a hook or raw SQL, find the YAML for it.**
Search [`config-reference.md`](config-reference.md) and the
[docs](../docs/) for the thing you need. Write Go only for logic YAML can't
express (domain algorithms, third-party APIs), and keep that Go small.

## If you're about to… use this instead

| You're about to | Use instead | Docs |
|---|---|---|
| Check a number range or a string format in a hook | `validate: {min, max, min_length, max_length, pattern, format}` on the field | [entities.md#field-validation](../docs/entities.md#field-validation) |
| Check one field against another (end after start) in a hook | entity `rules: [{check: ends_at > starts_at, message: …}]` (checked against the stored row on partial updates) | [entities.md#rules](../docs/entities.md#rules) |
| Set `completed_at` / `published_at` in a hook when a flag changes | `stamp_when: done` (or `status == "published"`) on a nullable timestamptz | [entities.md#stamp_when](../docs/entities.md#stamp_when) |
| Store dates or times as `text` and parse them in Go | `type: date` or `timestamptz`: typed input checks, filters, sorting, formats | [entities.md#field-types](../docs/entities.md#field-types) |
| Write `default: "'draft'"` or `"''"` | Defaults are literal values: `default: draft`, `default: ""`, `default: 0`, `default: uuid`, `default: now`; `{sql: "…"}` only for real expressions | [entities.md](../docs/entities.md#the-default-field) |
| Write a custom route to let users edit their own row (profile, settings) | CRUD `update` on the entity with `row_access: [{when: "*", filter: "id = :subject.id"}]` and `allowed_fields` | [authorization.md](../docs/authorization.md), [endpoints.md](../docs/endpoints.md#update-options) |
| Set an owner column in a hook | `default_from: subject.id` | [entities.md](../docs/entities.md#owner-fields-default_from) |
| Filter rows per user in a plugin with `WHERE owner_id = $1` | `Store.Read(ctx, sdk.ReadRequest{Entity, Subject, Where})`: applies the list endpoint's `row_access`, tenant scope and hidden fields | [plugins.md#store-database-access](../docs/plugins.md#store-database-access) |
| Use `Store.Query` on a caller's behalf | `Store.Read`. `Query` is raw SQL with no access rules: system work only (jobs, reports) | same |
| Write a custom route without declaring its input/output | Declare `params`, `body`, `response` on the endpoint: input is checked and typed (`c.Params`, `c.Body`), and the route appears in OpenAPI so clients are checked against it | [plugins.md#declaring-a-routes-contract](../docs/plugins.md#declaring-a-routes-contract) |
| Hand-roll `respond()` / `fail()` / JSON decoding helpers | `c.Data(v)`, `c.Created(v)`, `c.Error(status, msg)`, `c.Decode(&v)` (yayPi's envelopes) | [plugins.md](../docs/plugins.md#supporting-types) |
| Implement all six hook methods and switch on the entity name | `s.RegisterHooks("Event", sdk.Hooks{BeforeUpdate: …})` per entity, or embed `sdk.NoHooks` | [plugins.md#entityhookplugin](../docs/plugins.md#entityhookplugin) |
| Re-query the row inside `BeforeUpdate`, or avoid queries there to dodge a deadlock | `c.Before` is the stored row; `c.Tx` (and `Store` calls with `c.Ctx`) run in the write's transaction | [plugins.md#hook-behavior](../docs/plugins.md#hook-behavior) |
| Return `&sdk.HookError{Status: 422, Message: …}` by hand | `sdk.Reject(422, "…")` | same |
| Send email or webhooks from a hook | `kind: email` / `kind: webhooks` files (transactional outbox) | [plugins.md](../docs/plugins.md#when-to-use-plugins-vs-built-in-hooks) |
| Write a polling loop or goroutine | `kind: jobs` with a schedule (`handler: sql`, `http` or `plugin`) | [jobs.md](../docs/jobs.md) |
| Write audit rows yourself | `audit: true` on the entity | [entities.md#audit-log](../docs/entities.md#audit-log) |
| Upsert by an external id with select-then-insert | `natural_key` + `Store.Upsert` / `crud: [upsert]` | [entities.md#natural-keys-and-upserts](../docs/entities.md#natural-keys-and-upserts) |

## Things that look like gaps but aren't

- **Conditions** (`row_access.when`, `auth.conditions`) take `"double"` or
  `'single'` quoted strings.
- **Times** in responses follow `serialization` (RFC 3339 UTC by default,
  or a project/field format): don't re-format them in Go.
- **A frontend that needs your API's types**: `yaypi spec generate --name
  <spec> --walm` writes the OpenAPI spec and the walm profile. If a route
  you need is missing from the spec, the fix is a declared contract on the
  endpoint, not a hand-written URL in the client.

## Before you call it done

1. `yaypi validate` is clean (it catches bad defaults, rules, contracts and
   unknown fields with the file and field named).
2. The spec has every route the client calls: `yaypi spec generate`.
3. Any Go you kept is something the table above doesn't cover. Say why in a
   comment next to it.
4. If you hit a real framework limit, write it down (what you needed, what
   you tried, the workaround) instead of patching the framework.
