---
name: meta-agent-patterns
description: >
  Patterns for Claude operating as a Go development agent: how to decompose tasks, maintain
  state across sessions via files, self-verify output, run quality gates, generate documentation
  alongside code, and coordinate parallel work streams. Load for complex multi-file Go tasks,
  agentic coding sessions, or when building systems that span multiple packages.
---

# Agent Patterns for Go Development

## 1. Agent Self-Verification Loop

Before outputting any Go code, the agent runs this internal loop:

```
FOR EACH file to generate:
  1. LOAD relevant skills:
     - golang-core/SKILL.md (always)
     - [domain-specific skills from routing table]
     - [package-specific skills for all imports used]

  2. APPLY all constraint checklists from loaded skills

  3. VERIFY internal consistency:
     - Interfaces defined before referenced
     - Error types imported from correct package
     - Context propagated through all function calls
     - All goroutines have exit conditions
     - No circular imports

  4. CHECK quality gates mentally:
     - go vet: any obvious issues?
     - golangci-lint: any linter violations?
     - race detector: any unsynchronized shared state?

  5. GENERATE file

  6. SELF-REVIEW against clean-code/SKILL.md:
     - Functions ≤60 lines?
     - Arguments ≤3 (excluding ctx)?
     - Exports have godoc?
     - Comments explain WHY not WHAT?

  7. OUTPUT file + generate package SKILL.md if new package
```

---

## 2. Session State via Files

For multi-session agentic work, maintain state in project files:

```
.agent/
├── PLAN.md          ← current plan + progress (see planning/SKILL.md)
├── ASSUMPTIONS.md   ← tracked assumptions
├── DECISIONS.md     ← ADRs for architectural choices
├── CONTEXT.md       ← running context for current session
└── HANDOFF.md       ← end-of-session state for next session
```

### CONTEXT.md (update each session start)
```markdown
# Session Context
Date: [ISO datetime]
Agent: Claude [model]
Session goal: [what we're doing today]

## Current code state
- go build ./... → [PASSES|FAILS: reason]
- go test ./... → [N tests passing, M failing]
- go vet ./... → [CLEAN|N issues]

## Active constraints from loaded skills
- Domain: zero external imports (architecture/SKILL.md)
- Errors: always wrapped with %w (error-handling/SKILL.md)
- Tests: table-driven, t.Parallel() (testing/SKILL.md)

## What was done last session
[summary]

## What to do this session
1. [task 1]
2. [task 2]
```

---

## 3. Parallel Task Detection

When given a large task, identify parallelizable subtasks:

```
SEQUENTIAL (order matters):
  Domain model → Application layer → HTTP adapters → Tests

PARALLEL within phase (can be done simultaneously):
  Within application layer:
    - CreateOrderHandler (no deps on other handlers)
    - GetOrderHandler (no deps on other handlers)
    - ListOrdersHandler (no deps on other handlers)

  Within adapters:
    - HTTP handler for orders
    - HTTP handler for users
    - (both depend on application layer, but not on each other)

SIGNAL: "Stream N sub-agents" when detecting >3 independent tasks
in the same phase — decompose with explicit contracts between them.
```

---

## 4. Contract-First Development

For parallel agent streams, define contracts before implementation:

```go
// STEP 1: Define all interfaces (can be done by one agent)
// internal/domain/order/repository.go
type Repository interface {
    FindByID(ctx context.Context, id ID) (*Order, error)  // → ErrNotFound if missing
    Save(ctx context.Context, o *Order) error              // → ErrConflict on version mismatch
}

// internal/app/ports/event_publisher.go
type EventPublisher interface {
    Publish(ctx context.Context, event domain.Event) error
}

// STEP 2: Agents work in parallel against contracts
// Agent A: implements postgres.OrderRepository (satisfies domain.Repository)
// Agent B: implements app.CreateOrderHandler (uses domain.Repository + EventPublisher)
// Agent C: implements http.OrderHandler (uses app.CreateOrderHandler)
// All work simultaneously with the interface as the contract
```

---

## 5. Incremental Quality Gate

After each file generation, run mental quality checks:

```
TIER 1 — Must never violate (check every file):
  □ No error discarded without explicit comment
  □ No goroutine without exit condition
  □ No context lost (passed through all calls)
  □ No hardcoded values (timeouts, limits, addresses)
  □ No sensitive data in log messages

TIER 2 — Check for each type/function:
  □ Constructor validates all inputs at boundaries
  □ Interface methods return typed errors (not raw strings)
  □ All exported symbols have godoc comments
  □ No business logic in HTTP/gRPC handlers

TIER 3 — Check for each package:
  □ Package has one clear purpose
  □ No circular imports
  □ Package-level SKILL.md generated if new package
  □ Tests cover all exported behavior
```

---

## 6. Package SKILL.md Auto-Generation Trigger

Generate a package-specific SKILL.md when ALL of these are true:
1. New package created (not modifying existing)
2. Package has ≥2 files OR ≥100 lines of production code
3. Package has ≥1 exported interface or type with complex contracts

Use `meta/package-skill-generator/SKILL.md` as the template.

---

## 7. Asking vs Acting

```
ACT (generate code) when:
  - Task is clear and within loaded skill constraints
  - All dependencies (interfaces, types) are defined
  - Quality gates can be verified mentally

ASK (request clarification) when:
  - Ambiguity in requirements would cause different architectures
  - External system contracts unknown (API shape, DB schema)
  - Business rules unclear (what happens when X?)
  - Task requires >5 new packages without prior planning

DO NOT ask about:
  - Implementation style (use skills to decide)
  - Which patterns to use (use skills to decide)
  - Whether to add tests (always yes)
  - Whether to add error handling (always yes)
```

---

## 8. Output Ordering for Large Tasks

When generating multiple files, output in this order:

```
1. PLAN.md / CONTEXT.md update (always first — document what we're doing)
2. Domain types (entities, value objects, errors, interfaces)
3. Application layer (use cases, commands, queries)
4. Adapter implementations (DB repos, HTTP handlers)
5. Infrastructure wiring (main.go, wire setup)
6. Test files (alongside each production file)
7. Package SKILL.md files (one per new package)
8. Configuration files (Dockerfile, Makefile updates)
9. PLAN.md update (mark tasks complete, note next steps)
```

---

## Agent Checklist

- [ ] PLAN.md read/created before writing any code
- [ ] All relevant skills loaded before generating any file
- [ ] Contracts (interfaces) defined before parallel implementation
- [ ] Every new package gets a SKILL.md
- [ ] Quality gates verified mentally after each file
- [ ] PLAN.md updated at session end with state + next steps
- [ ] ASSUMPTIONS.md updated with any new assumptions discovered
- [ ] No placeholder/TODO code in production paths
