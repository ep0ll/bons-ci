---
name: package-skill-generator
description: >
  Template and instructions for generating package-specific SKILL.md files when Claude creates
  a new Go package. Triggers automatically when: generating a new Go package with >100 lines,
  implementing a complex adapter (DB repo, HTTP handler, Kafka consumer), or when the user asks
  to "create a skill for [package]". Produces self-contained skills that encode all assumptions,
  contracts, and patterns so future Claude sessions never break existing code.
---

# Package Skill Generator

## When to Generate a Package SKILL.md

Generate a package-specific `SKILL.md` whenever:
1. You create a new Go package with 2+ files or >100 lines
2. You implement a complex adapter (database, HTTP, messaging)
3. A user asks "create a skill for X" after generating code
4. A package has non-obvious contracts that future Claude sessions must respect

---

## SKILL.md Template for Generated Packages

```markdown
---
name: [package-import-path]-skill
description: >
  Package-specific skill for [package path] in [project name].
  [One sentence: what this package does and why it exists.]
  Load whenever modifying, extending, or testing code in this package.
  Cross-references: [list parent skills: architecture/SKILL.md, packages/pgx/SKILL.md, etc.]
---

# Package: [import path]

## Purpose
[1-3 sentences: what problem this package solves, its role in the architecture.]

## Layer
[domain | application | adapter | infrastructure | pkg]
Imports from: [list packages it imports — establishes dependency direction]
Imported by: [list packages that import this one]

## Key Types

### [TypeName]
```go
// Exact signature with doc comment
type OrderRepository struct { ... }
```
**Contract**: [what callers can assume — invariants, thread safety, error conditions]
**Assumptions**: [what this type assumes about its dependencies]

## Exported Functions / Methods

### [FuncName]
```go
func (r *OrderRepository) FindByID(ctx context.Context, id order.ID) (*order.Order, error)
```
**Preconditions**: [what must be true before calling]
**Postconditions**: [what is guaranteed on success]
**Error conditions**:
- Returns `domain.ErrNotFound` when no row with that ID exists
- Returns `domain.ErrConflict` when version mismatch (optimistic lock)
- Wraps underlying pgx errors with `"OrderRepository.FindByID(id=%s): "`

## Database Schema (if applicable)
```sql
-- Exact table/column names this package depends on
CREATE TABLE orders (
    id UUID PRIMARY KEY,
    ...
);
```
**Assumptions**:
- Table `orders` exists with columns: id, customer_id, status, version, created_at, deleted_at
- Partial index on `(customer_id, created_at DESC) WHERE deleted_at IS NULL` exists
- `updated_at` trigger is installed

## Configuration Dependencies
```go
// Config fields this package reads
type Config struct {
    MaxConns int32 // required, default 25
    DSN      string // required, from secret store
}
```

## Error Catalogue
| Error | Condition | Mapped From |
|---|---|---|
| `domain.ErrNotFound` | row not found | `pgx.ErrNoRows` |
| `domain.ErrConflict` | version mismatch | `RowsAffected() == 0` on UPDATE |
| `domain.ErrConflict` | unique key violation | `pgconn.PgError.Code == "23505"` |

## Concurrency Contract
[Is it safe for concurrent use? What synchronization does it use?]
Example: "Thread-safe. Uses pgxpool internally. All methods safe for concurrent calls."

## Performance Characteristics
- Bulk insert: uses `CopyFrom` for >100 rows
- Reads: uses `pgx.CollectRows` (no manual scan loops)
- Cache: [if applicable, describe cache behavior]

## Extension Points
[How to add a new method / new query / new type without breaking existing callers]

## Test Fixtures
```go
// Minimum setup for testing this package
func setupTestDB(t *testing.T) *pgxpool.Pool {
    // uses testcontainers-go with postgres:16-alpine
    // runs all migrations in /migrations/
}
```

## Related Skills
- [parent-skill-1/SKILL.md] — [why relevant]
- [parent-skill-2/SKILL.md] — [why relevant]

## Non-Breaking Extension Rules
1. New methods: always add, never remove/rename existing ones
2. New error conditions: always return existing typed errors; never invent new error strings
3. New columns: add to SELECT but keep all existing columns; never remove columns from queries
4. New query parameters: always optional with sensible defaults
```

---

## Example: Generated Skill for postgres/order_repo.go

```markdown
---
name: internal-adapters-postgres-order-repo
description: >
  Package skill for internal/adapters/postgres/order_repo.go — PostgreSQL implementation
  of the domain.order.Repository interface. Load when modifying order persistence, adding
  queries, or writing integration tests for order storage. Cross-references:
  database/SKILL.md, packages/pgx/SKILL.md, architecture/SKILL.md.
---

# Package: internal/adapters/postgres — Order Repository

## Purpose
Implements `order.Repository` using pgx/v5 against PostgreSQL.
Maps between domain `order.Order` aggregates and the `orders` database table.
Handles optimistic locking via `version` column and soft deletes via `deleted_at`.

## Layer
Adapter (driven). Imports: domain/order, packages/pgx. Imported by: infrastructure/wire.

## Key Types

### postgresOrderRepo
```go
type postgresOrderRepo struct {
    pool *pgxpool.Pool
}
func NewOrderRepository(pool *pgxpool.Pool) order.Repository
```
**Contract**: Thread-safe. All methods accept context for cancellation.
Uses transaction from context when available (see database/SKILL.md TxManager).

## Exported Methods

### FindByID
```go
func (r *postgresOrderRepo) FindByID(ctx context.Context, id order.ID) (*order.Order, error)
```
**Postconditions**: Never returns `(nil, nil)`.
**Errors**: `domain.ErrNotFound` when not found; pgx errors wrapped with `"OrderRepo.FindByID(id=%s): "`.

### Save
```go
func (r *postgresOrderRepo) Save(ctx context.Context, o *order.Order) error
```
Uses `INSERT ... ON CONFLICT (id) DO UPDATE ... WHERE orders.version = $old_version`.
**Errors**: `domain.ErrConflict` when `RowsAffected() == 0` (version mismatch).

## Database Schema
```sql
CREATE TABLE orders (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id   UUID NOT NULL,
    status        TEXT NOT NULL,
    total_amount  BIGINT NOT NULL,
    total_currency CHAR(3) NOT NULL,
    version       BIGINT NOT NULL DEFAULT 1,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at    TIMESTAMPTZ
);
CREATE INDEX orders_customer_active ON orders(customer_id, created_at DESC)
    WHERE deleted_at IS NULL;
```
**Critical**: `status` column values MUST match `order.Status` constants exactly.
**Critical**: `version` incremented by aggregate, stored as-is — never auto-increment in DB.

## Error Catalogue
| Error | Condition |
|---|---|
| `domain.ErrNotFound` | No row, or `deleted_at IS NOT NULL` |
| `domain.ErrConflict` | Version mismatch on Save |
| `domain.ErrConflict` | Unique key violation (23505) on customer+status |

## Concurrency
Thread-safe. pgxpool manages connections.
TxManager injects tx via context — do not call `pool.Begin()` directly in this package.

## Non-Breaking Extension Rules
1. New queries: always add `AND deleted_at IS NULL` to WHERE clause
2. New columns: add to INSERT and SELECT in Save/FindByID; never remove existing
3. New error cases: map to existing domain errors (ErrNotFound, ErrConflict, ErrValidation)
4. Never bypass TxManager — always call `r.db(ctx)` which checks ctx for active tx
```

---

## Checklist for Generated Package Skills

- [ ] Import path in frontmatter name matches actual Go package path
- [ ] All exported types documented with exact signatures
- [ ] All error conditions catalogued with exact domain error type returned
- [ ] Database schema included (exact column names, types, constraints)
- [ ] Concurrency contract stated explicitly
- [ ] Non-breaking extension rules documented
- [ ] Links to 2-4 parent skills that govern this package's behavior
- [ ] Test fixture setup documented for integration tests
