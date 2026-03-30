# YAML IntelliSense

yayPi ships JSON Schema files for every YAML file kind. When paired with the Red Hat YAML extension in VS Code or Cursor's built-in YAML support, you get:

- **Autocomplete** — type a partial key and press Tab or Ctrl+Space to see all valid options with descriptions
- **Hover docs** — hover over any key to read its description, type, and default value
- **Validation** — typos and unknown keys show as red squiggles immediately

The `.vscode/settings.json` in the repo root activates schemas automatically for all matching glob patterns. No installation steps beyond the extension.

## Requirements

| IDE | What to install |
|-----|----------------|
| VS Code | [Red Hat YAML](vscode:extension/redhat.vscode-yaml) (`redhat.vscode-yaml`) |
| Cursor | Built-in — no extension needed |
| Neovim | `yaml-language-server` via mason or your LSP config |
| JetBrains | Built-in YAML plugin (supports `$schema` comment) |

## How It Works

The `schemas/` directory contains one JSON Schema draft-07 file per YAML kind:

```
schemas/
├── root.schema.json        # yaypi.yaml
├── entity.schema.json      # kind: entity
├── endpoints.schema.json   # kind: endpoints
├── auth.schema.json        # kind: auth
├── jobs.schema.json        # kind: jobs
├── seed.schema.json        # kind: seed
├── email.schema.json       # kind: email
├── webhooks.schema.json    # kind: webhooks
└── policy.schema.json      # kind: policy
```

The `.vscode/settings.json` maps schemas to glob patterns:

```json
"yaml.schemas": {
  "./schemas/entity.schema.json": ["**/entities/**/*.yaml"]
}
```

Files matching a pattern automatically get the corresponding schema.

## Per-File Override

If a file is in an unconventional location, add an inline comment at the top to force a schema:

```yaml
# yaml-language-server: $schema=./schemas/entity.schema.json
kind: entity
entity:
  name: MyEntity
```

## What You Get

### Field validation autocomplete

```yaml
fields:
  - name: email
    type: string
    validate:
      #   ^--- Ctrl+Space here shows:
      #   required, min_length, max_length, min, max, pattern, format, message
      format: email
      required: true
```

### Enum suggestions

```yaml
type: string  # suggests: uuid, string, text, integer, bigint, float, decimal, boolean, timestamptz, date, jsonb, enum, array, bytea
```

```yaml
on_delete: CASCADE  # suggests: CASCADE, SET NULL, RESTRICT, NO ACTION
```

### Required field warnings

If you omit a required key (e.g. `name` on an entity field), a yellow warning underline appears on the parent object.

### Unknown key squiggles

```yaml
validate:
  requird: true   # red squiggle — did you mean 'required'?
```

## Directory Layout Tips

The glob patterns in `.vscode/settings.json` cover these conventional layouts:

| Kind | Conventional location |
|------|----------------------|
| entity | `entities/user.yaml`, `entities/auth/user.yaml` |
| endpoints | `endpoints/users.yaml`, `endpoints/admin/reports.yaml` |
| auth | `auth/auth.yaml` |
| jobs | `jobs/cleanup.yaml` |
| seed | `seeds/roles.yaml`, `seed/initial.yaml` |
| email | `emails/welcome.yaml` |
| webhooks | `webhooks/crm.yaml` |
| policy | `policies/roles.yaml` |

If your layout differs, either add patterns to `.vscode/settings.json` or use the per-file `# yaml-language-server: $schema=...` comment.
