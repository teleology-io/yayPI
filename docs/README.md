# yayPi Documentation

**yayPi** (Yet Another YAML-Powered API) is a Go framework that generates a fully functional REST API backend from YAML configuration files — no code generation, no templates, no boilerplate.

## Quick Start

```bash
# 1. Scaffold a new project
yaypi init my-api
cd my-api

# 2. Set your database URL
export DATABASE_URL=postgres://localhost/my_api
export JWT_SECRET=your-secret-here

# 3. Start the server
yaypi run
```

## Documentation

| I want to… | Go to… |
|---|---|
| Build my first API in 10 minutes | [Getting Started](getting-started.md) |
| Understand how yayPi works | [Concepts](concepts.md) |
| Configure `yaypi.yaml` | [Project Config](project-config.md) |
| Define entities and fields | [Entities](entities.md) |
| Configure REST endpoints | [Endpoints](endpoints.md) |
| Set up JWT auth and roles | [Authorization](authorization.md) |
| Generate and run migrations | [Migrations](migrations.md) |
| Schedule background jobs | [Jobs](jobs.md) |
| Write a plugin (hooks) | [Plugins](plugins.md) |
| See all CLI commands | [CLI Reference](cli.md) |
| See common patterns | [Patterns Cookbook](patterns.md) |

## Examples

- [`examples/blog/`](../examples/blog/) — A simple single-author blog (users, posts, tags)
- [`examples/community-blog/`](../examples/community-blog/) — A multi-author community blog (users, posts, tags, threaded comments, roles)
