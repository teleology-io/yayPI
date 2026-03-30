# AI Context

This directory contains structured context files designed for AI assistants (Claude, Copilot, Cursor, etc.) to understand the yayPi framework quickly without scanning the entire codebase.

## Files

| File | Purpose |
|---|---|
| [overview.md](overview.md) | Architecture, request flow, package map, and design decisions |
| [config-reference.md](config-reference.md) | Complete YAML config field reference for all file kinds (`entity`, `endpoints`, `auth`, `jobs`, `seed`, `email`, `webhooks`) |
| [patterns.md](patterns.md) | YAML pattern cookbook — common use cases as copy-paste snippets |

Start with [overview.md](overview.md) to understand how the pieces fit together, then consult [config-reference.md](config-reference.md) for specific fields.

## YAML kinds supported

| kind | What it defines |
|---|---|
| `entity` | Database table, fields, relations, indexes, constraints, hooks |
| `endpoints` | REST routes: list/get/create/update/delete with auth, pagination, bulk create, rate limiting |
| `auth` | register/login/me/refresh/OAuth2 endpoints |
| `jobs` | Cron jobs (SQL or HTTP) |
| `seed` | Idempotent startup data |
| `email` | SMTP email hooks on lifecycle events |
| `webhooks` | HTTP webhook hooks on lifecycle events |
| `policy` | Casbin RBAC role definitions |
