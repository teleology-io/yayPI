# Webhooks

`kind: webhooks` covers both directions:

- **Outbound** (the default `mode`) — when an entity changes, send an HTTP request to another system.
- **Inbound** (`mode: inbound`) — receive signed HTTP requests from another system and turn them into entity writes, or hand them to a plugin.

Both directions share `secret`, `signature`, `retry` and `timeout`, and both are reliable: outbound messages go through the transactional outbox, inbound requests through an inbox table. Nothing is lost when a receiver is down or the process restarts.

## Outbound

```yaml
version: "1"
kind: webhooks
webhooks:
  - name: order-paid
    entity: Order
    trigger: after_update
    condition: record.status == "paid"
    url: https://fulfillment.example.com/orders/{{record.id}}
    headers: { X-Source: yaypi }
    payload: '{"order_id": "{{record.id}}", "total": {{record.total}}}'
    secret: ${FULFILLMENT_SECRET}      # signs with yaypi_v1 unless signature: says otherwise
    retry: { max_attempts: 8 }
```

See [Plugins → built-in webhook hooks](plugins.md#built-in-webhook-hooks-kind-webhooks) for triggers, payload templates and delivery details.

## Inbound

```yaml
  - name: crm-events
    mode: inbound
    path: /webhooks/crm                 # under project.base_url, e.g. POST /api/webhooks/crm
    secret: ${CRM_WEBHOOK_SECRET}
    signature: { scheme: hmac_sha256, header: X-Crm-Signature, prefix: "sha256=" }
    challenge: { method: GET, query_param: challenge }
    dedupe_key: body.id
    event: body.type
    routes:
      contact.created: { entity: Contact, op: upsert, map: &contact { external_id: "{{body.data.id}}", email: "{{body.data.email}}", name: "{{body.data.first}} {{body.data.last}}" } }
      contact.updated: { entity: Contact, op: upsert, map: *contact }
      contact.deleted: { entity: Contact, op: delete, key: { external_id: "{{body.data.id}}" } }
      "*":             { handler: crm.OnOther }
```

What happens to a request:

1. **Protection** — client IP (`allowed_ips`), `token`, `auth`, then the signature. Failures get 401/403 and are logged; nothing is stored.
2. **Handshake** — if `challenge` matches, the value is echoed back as `text/plain` and processing stops.
3. **Dedupe** — a repeat `dedupe_key` is acknowledged (`{"status":"duplicate"}`) and dropped.
4. **Store** — the request goes into `yaypi_inbox` and the sender gets `200 {"status":"accepted"}` straight away.
5. **Process** — a background worker picks the route by `event` (falling back to `"*"`; unmatched events are ignored) and runs it, retrying failures with exponential backoff (default 8 attempts, then `status = 'dead'` with `last_error`). Several replicas can share the work.

### Routes

| `op` | Needs | Does |
|---|---|---|
| `create` | `map` | Insert |
| `upsert` | `map`, entity `natural_key` | Create or update by the [natural key](entities.md#natural-keys-and-upserts) |
| `update` | `map`, `key` | Update the row matching `key` (primary key or natural key); retried if it doesn't exist yet |
| `delete` | `key` | Delete (soft delete when the entity has it); a missing row is fine |
| — | `handler: <plugin>.<key>` | Call a [`WebhookPlugin`](plugins.md#webhookplugin-and-webhookverifierplugin) handler |

Entity routes use the same write path as the REST API — validation, hooks, the audit log (actor `webhook:<name>`) and outbound webhooks all apply. They are trusted system writes unless you set `as_role`, which makes Casbin policies (an upsert needs `create` and `update`) and field `write_roles` apply to that role.

### Templates

`map` and `key` values can reference the request:

| Template | Reads |
|---|---|
| `{{body.data.id}}` | JSON body (form-encoded bodies work too) |
| `{{body.items.0.id}}` | array element |
| `{{headers.X-Event-Id}}` | request header |
| `{{query.account}}` | query parameter |

A value that is exactly one placeholder keeps its JSON type (number, boolean, object). Mixed text such as `"{{body.first}} {{body.last}}"` becomes a string. Non-string YAML values are used as literals. Where the meaning is unambiguous, values are adapted to the field: a numeric id into a string column, `"7"` into an integer, unix seconds into a timestamp. `event`, `dedupe_key` and `challenge.body_path` take a bare path (`body.type`).

## Signatures

`secret` plus `signature` decide how bodies are signed (outbound) and checked (inbound). With only a `secret`, the scheme is `yaypi_v1`, so two yayPi services work together without extra settings.

| `scheme` | Header | Signed content |
|---|---|---|
| `yaypi_v1` | `X-Yaypi-Signature: v1=<hex>`, `X-Yaypi-Timestamp` | `<timestamp>.<body>` |
| `github` | `X-Hub-Signature-256: sha256=<hex>` | body |
| `slack` | `X-Slack-Signature: v0=<hex>`, `X-Slack-Request-Timestamp` | `v0:<timestamp>:<body>` |
| `stripe` | `Stripe-Signature: t=<ts>,v1=<hex>` | `<timestamp>.<body>` |
| `hmac_sha256`, `hmac_sha1` | `header` (required) | `signed_payload` |
| `plugin` | inbound only: `handler: <plugin>.<verifier>` | your code |
| `none` | — | — |

Generic HMAC options: `header`, `prefix` (e.g. `sha256=`), `encoding` (`hex` or `base64`), `timestamp_header` (adds replay protection), `tolerance` (default `5m`), `signed_payload` (`{timestamp}` and `{body}` are replaced; default `{body}`, or `{timestamp}.{body}` with a timestamp header). Comparisons are constant-time.

## Protecting a receiver

Every receiver must say how it is protected; the server refuses to start otherwise.

| `secret` | `signature` | Result |
|---|---|---|
| set | omitted | `yaypi_v1` |
| set | a signing scheme | that scheme |
| not set | a signing scheme | startup error: the scheme needs a secret |
| not set | omitted or `none` | allowed only with a guard (below) |
| set | `none` | startup error: the secret would never be checked |

For senders that can't sign, use at least one guard:

```yaml
    auth: { require: true, roles: [system] }               # API key or JWT, as on endpoints
    token: { header: X-Webhook-Token, value: ${TOKEN} }     # or query_param:
    allowed_ips: [203.0.113.0/24]
    dedupe_key: headers.X-Request-Id                        # required when unsigned
```

Unsigned receivers log a warning at every startup. All receivers have a rate limit (default 300 requests per minute per IP; `rate_limit` can raise it but not remove it) and a body limit (`max_body_size`). `allowed_ips` checks the TCP peer — behind a load balancer, set `server.trusted_proxies`. Credentials (`Authorization`, cookies, the API key and `token`) are never written to the inbox.

## Operations

- **Metrics** — `yaypi_webhook_events_total{mode, webhook, result}` (outbound: `success`/`error` per delivery attempt; inbound: `accepted`, `duplicate`, `rejected`, `challenge`, `error` per request) and `yaypi_inbox_processed_total{webhook, result}`.
- **Dead events** — `SELECT * FROM yaypi_inbox WHERE status = 'dead'` shows what failed and why (`last_error`). To retry one, set `status = 'pending', attempts = 0, next_attempt_at = 0`.
- **Retention** — processed rows are deleted after `outbox.retention` (default 7 days); the poll interval is `outbox.poll_interval`.
- **OpenAPI** — receivers are listed under the `Webhooks` tag (set `spec: false` to hide one).
