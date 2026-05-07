---
name: pkg-sqlc
description: >
  sqlc type-safe SQL code generation reference. See packages/uuid/SKILL.md Part 3 for the
  complete sqlc reference including sqlc.yaml config, query file format, generated API usage,
  and CI integration. Load packages/uuid/SKILL.md when working with sqlc.
---

# Package: sqlc-dev/sqlc

See **`packages/uuid/SKILL.md` — Part 3: sqlc** for the full reference.

That file covers:
- `sqlc.yaml` configuration (pgx/v5, emit_empty_slices, type overrides)
- SQL query file format (`:one`, `:many`, `:exec`, `:execrows`)
- Generated Go API usage
- Optimistic lock detection via `:execrows`
- CI integration (`sqlc vet`, `sqlc generate`)

Quick import:
```go
import "github.com/sqlc-dev/sqlc/cmd/sqlc"  // tool only, not imported in code

// Generated code lives in the configured output package, e.g.:
import "github.com/org/project/internal/adapters/postgres/pgstore"
```
