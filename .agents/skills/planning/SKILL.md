---
name: golang-planning
description: >
  Planning and iterative development workflow for Go projects: PLAN.md driven development,
  task decomposition, incremental implementation, assumption tracking, decision logging,
  and file-based coordination across sessions. Use at the START of any large Go task (>3 files,
  >200 lines, or multi-package) before writing any code. Cross-references: multi-agent/SKILL.md.
---

# Go Planning — File-Driven Iterative Development

## 1. PLAN.md — Start Every Large Task Here

Before writing any code, create `PLAN.md` in the project root (or task directory):

```markdown
# Task: [Task Name]
Date: [ISO date]
Status: IN_PROGRESS | BLOCKED | COMPLETE

## Problem Statement
[What problem are we solving? What is the expected outcome?]

## Constraints
- Hard constraints (non-negotiable):
  - [ ] Domain layer: zero external imports
  - [ ] All errors wrapped with context
  - [ ] All goroutines cancellable via context
  - [ ] go test -race ./... must pass
- Soft constraints (prefer but can trade off):
  - [ ] Test coverage > 80% on business logic
  - [ ] golangci-lint: zero warnings

## Architecture Decisions
| Decision | Options Considered | Chosen | Reason |
|---|---|---|---|
| Router | chi / gin / stdlib | chi | lightweight, idiomatic middleware |
| DB driver | pgx / sqlx | pgx/v5 | native protocol, best performance |
| Event bus | Kafka / NATS | Kafka | existing infrastructure |

## Task Decomposition
### Phase 1: Foundation (blocking — must complete first)
- [ ] Domain: entities, value objects, events, errors
- [ ] Domain: repository interface
- [ ] Domain: unit tests

### Phase 2: Application (depends on Phase 1)
- [ ] App: CreateOrder command handler
- [ ] App: GetOrder query handler
- [ ] App: command/query tests with mocks

### Phase 3: Adapters (depends on Phase 2)
- [ ] HTTP: chi router + handlers
- [ ] Postgres: repository implementation
- [ ] Postgres: integration tests (testcontainers)

### Phase 4: Infrastructure (depends on Phase 3)
- [ ] main.go + wire setup
- [ ] Dockerfile + Makefile
- [ ] Config schema
- [ ] Package SKILL.md files

## Files To Create
| File | Package | Purpose | Phase |
|---|---|---|---|
| internal/domain/order/order.go | order | Aggregate root | 1 |
| internal/domain/order/events.go | order | Domain events | 1 |
| internal/domain/order/repository.go | order | Port interface | 1 |
| internal/app/order/create_order.go | order | Use case | 2 |
| internal/adapters/http/order_handler.go | http | Handler | 3 |
| internal/adapters/postgres/order_repo.go | postgres | DB impl | 3 |

## Assumptions
- [ ] PostgreSQL 16+ with UUID extension enabled
- [ ] Kafka topic `orders` already exists with 12 partitions
- [ ] JWT RS256 public key path in APP_AUTH_JWT_PUBLIC_KEY_PATH env

## Open Questions
- [ ] Should order cancellation trigger inventory release synchronously or via event?
- [ ] What is the SLA for list orders endpoint? (determines if we need read replica)

## Progress Log
[YYYY-MM-DD HH:MM] Started — created PLAN.md
[YYYY-MM-DD HH:MM] Phase 1 complete — domain layer with 100% test coverage
[YYYY-MM-DD HH:MM] Blocked: Kafka topic schema not finalized — waiting on platform team
```

---

## 2. ASSUMPTIONS.md — Track All Assumptions

```markdown
# Assumptions

Generated: [date]
Project: [name]

## Database Schema
- Table `orders` exists with columns: id (UUID), customer_id (UUID), status (TEXT),
  version (BIGINT), created_at (TIMESTAMPTZ), updated_at (TIMESTAMPTZ), deleted_at (TIMESTAMPTZ)
- Index on (customer_id, created_at DESC) WHERE deleted_at IS NULL
- Trigger `set_updated_at` on orders table

## External Services
- Kafka: broker at $KAFKA_BROKERS (comma-separated), topic `orders.events`
- Redis: at $REDIS_URL, used for session cache (TTL 24h)
- Auth service: JWT RS256, public key at $AUTH_JWT_PUBLIC_KEY_PATH

## Environment Variables
| Variable | Required | Default | Description |
|---|---|---|---|
| APP_DATABASE_DSN | YES | — | PostgreSQL connection string |
| APP_KAFKA_BROKERS | YES | — | Comma-separated Kafka brokers |
| APP_SERVER_ADDR | NO | :8080 | HTTP listen address |
| APP_LOG_LEVEL | NO | info | Log level: debug\|info\|warn\|error |

## Interface Contracts (from other bounded contexts)
- Inventory service: CheckStock gRPC endpoint at $INVENTORY_GRPC_ADDR
  Proto: inventory.v1.StockService/CheckStock
  Returns: { all_available: bool, unavailable_skus: []string }
```

---

## 3. DECISIONS.md — Architecture Decision Records (ADR)

```markdown
# ADR-001: Use pgx/v5 instead of database/sql

Date: 2024-01-15
Status: Accepted

## Context
We need a PostgreSQL driver. Options: database/sql+lib/pq, database/sql+pgx, pgx native.

## Decision
Use pgx/v5 in native mode (not database/sql compatible layer).

## Consequences
+ 40% faster than database/sql for batch operations
+ Native COPY protocol support for bulk inserts
+ pgx.CollectRows eliminates scan boilerplate
- Cannot swap to MySQL/SQLite (acceptable: we're PostgreSQL-only)
- Requires learning pgx API (offset by packages/pgx/SKILL.md)

## References
- Benchmark: https://github.com/jackc/pgx/blob/master/BENCHMARK.md
```

---

## 4. Iterative Implementation Flow

```
ITERATION LOOP:
  1. Read PLAN.md → identify next incomplete task
  2. Load relevant SKILL.md files for that task
  3. Check ASSUMPTIONS.md — are all assumptions still valid?
  4. Write code for ONE phase at a time
  5. Run quality gates: go build, go vet, go test -race
  6. Update PLAN.md: mark tasks done, log decisions, note blockers
  7. Update ASSUMPTIONS.md if new assumptions discovered
  8. Generate package SKILL.md for completed packages
  9. Repeat from step 1

NEVER:
  - Skip to a later phase before earlier phase passes quality gates
  - Write placeholder/TODO code in production paths
  - Break existing tests while adding new features
  - Leave PLAN.md stale (update it every session)
```

---

## 5. Session Handoff

At the end of each session, update PLAN.md with:
```markdown
## Session End: [datetime]
### Completed this session
- [x] internal/domain/order/order.go — aggregate with 5 methods
- [x] internal/domain/order/order_test.go — 12 test cases, all passing

### State of codebase
- go build ./... → PASSES
- go test -race ./... → PASSES (24 tests)
- go vet ./... → PASSES

### Next session: start with
1. internal/app/order/create_order.go — implement CreateOrderHandler
2. Load: architecture/SKILL.md, error-handling/SKILL.md, packages/errgroup/SKILL.md

### Blockers
- None

### Key decisions made
- Using value receiver for Order.Items() (returns defensive copy)
- Status transitions enforced in domain layer, not application layer
```

---

## Planning Checklist

- [ ] PLAN.md created before writing any code
- [ ] Task decomposed into phases with explicit dependencies
- [ ] All files to be created listed in PLAN.md table
- [ ] ASSUMPTIONS.md created with all external dependencies documented
- [ ] One DECISIONS.md ADR per significant architecture choice
- [ ] Each session ends with PLAN.md updated (progress + next steps)
- [ ] Quality gates pass before marking any phase complete
- [ ] Package SKILL.md generated for each completed package
