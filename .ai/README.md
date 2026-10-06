# AI Context

This directory contains structured context files designed for AI assistants (Claude, Copilot, Cursor, etc.) to understand the yayPi framework quickly without scanning the entire codebase.

## Files

| File | Purpose |
|---|---|
| [overview.md](overview.md) | Architecture, request flow, package map, and design decisions |
| [config-reference.md](config-reference.md) | Complete YAML config field reference for all file kinds (`entity`, `endpoints`, `auth`, `jobs`, `seed`, `email`, `webhooks`) |
| [patterns.md](patterns.md) | YAML pattern cookbook — common use cases as copy-paste snippets |
| [production-manifest.md](production-manifest.md) | Production-readiness work log: what was hardened, what was deferred and why |

Start with [overview.md](overview.md) to understand how the pieces fit together, then consult [config-reference.md](config-reference.md) for specific fields.

## YAML kinds supported

| kind | What it defines |
|---|---|
| `entity` | Database table, fields, relations, indexes, constraints, hooks |
| `endpoints` | REST routes: list/get/create/update/replace/delete with auth, filters, keyset pagination, includes, bulk create, rate limiting |
| `auth` | register/login/me/refresh/logout, password reset, email verification, OAuth2 (PKCE) |
| `jobs` | Cron jobs (SQL or HTTP), run once across replicas |
| `seed` | Idempotent data applied by `yaypi seed` |
| `email` | SMTP email on lifecycle events, delivered via the transactional outbox |
| `webhooks` | Signed HTTP webhooks on lifecycle events, delivered via the transactional outbox |
| `policy` | Casbin RBAC role definitions |
