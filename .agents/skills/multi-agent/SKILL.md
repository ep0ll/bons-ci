---
name: golang-multi-agent
description: >
  Multi-agent parallel orchestration for Go development: how to decompose large Go tasks into
  parallel sub-agents, agent communication patterns, parallel skill invocation, result synthesis,
  and agent workflow design for code generation, review, and refactoring. Use when a task is large
  enough to benefit from parallel execution (>300 lines of generated code, >3 independent modules,
  or complex cross-cutting concerns). Reference: planning/SKILL.md for task decomposition.
---

# Multi-Agent Parallel Orchestration for Go Development

## 1. When to Use Multi-Agent

```
Single agent is fine for:
  - Single file changes (< 200 lines)
  - Simple CRUD endpoint
  - Adding a middleware
  - Writing tests for existing code
  - Fixing a specific bug

Multi-agent is valuable for:
  - New microservice (multiple layers: domain, adapters, handlers, tests)
  - Large refactoring (split god object into multiple packages)
  - Cross-cutting feature (touching N services)
  - Generating full package + tests + skill documentation simultaneously
  - Code review of large PR (parallel review of different concerns)
```

---

## 2. Decomposition Strategy

```
For a new Go service, decompose by ARCHITECTURAL LAYER (independent):

  Agent 1: Domain layer
    - Entities, value objects, domain events
    - Repository interfaces, domain errors
    - Unit tests for domain logic

  Agent 2: Application layer
    - Command handlers, query handlers
    - Port interfaces (notifier, publisher)
    - Use case tests with mocks

  Agent 3: Adapters — HTTP
    - chi router setup
    - HTTP handlers, request/response DTOs
    - Handler tests with httptest

  Agent 4: Adapters — Database
    - pgx repository implementations
    - Migration files
    - Integration tests with testcontainers

  Agent 5: Infrastructure
    - main.go + wire setup
    - Dockerfile, Makefile
    - Config schema + validation
    - Package-specific SKILL.md

All agents work in parallel. Domain layer result feeds agents 2-5.
```

---

## 3. Agent Task Template

```markdown
## Agent Task: [LAYER NAME]

### Context
- Service: order-service
- Domain: Order management (create, confirm, cancel)
- Read skills: golang-core/SKILL.md, architecture/SKILL.md, [layer-specific skills]
- Dependencies from other agents: [list what this agent depends on]

### Constraints (HARD — never violate)
- Domain package: zero imports from adapters/ or external frameworks
- All errors wrapped with fmt.Errorf("ReceiverType.Method(param): %w", err)
- All functions with context.Context as first param
- Unit tests required for all business logic
- No TODO comments in production code

### Deliverables
1. internal/domain/order/order.go — aggregate root
2. internal/domain/order/events.go — domain events
3. internal/domain/order/errors.go — typed errors
4. internal/domain/order/repository.go — port interface
5. internal/domain/order/order_test.go — unit tests

### Quality Gates
- go vet ./...  passes
- go test -race ./... passes
- golangci-lint run passes
```

---

## 4. Parallel Skill Loading

When Claude generates code for a task that touches multiple packages, it should
load and apply ALL relevant skills simultaneously, not sequentially:

```
Task: "Create an HTTP handler that reads from PostgreSQL and publishes to Kafka"

Load simultaneously:
  - golang-core/SKILL.md           (core idioms)
  - architecture/SKILL.md          (layer structure)
  - api-design/SKILL.md            (endpoint design)
  - packages/chi/SKILL.md          (chi router patterns)
  - packages/pgx/SKILL.md          (database queries)
  - packages/kafka/SKILL.md        (producer patterns)
  - packages/otel/SKILL.md         (tracing)
  - packages/slog/SKILL.md         (logging)
  - error-handling/SKILL.md        (error strategy)
  - testing/SKILL.md               (test patterns)

Apply ALL constraints from all loaded skills simultaneously.
No constraint from any loaded skill is optional.
```

---

## 5. Result Synthesis

When multiple agents produce output, the synthesizing agent must:

```
1. VERIFY consistency:
   - Interface definitions match between domain (producer) and adapters (consumer)
   - Import paths are consistent across all files
   - Error types referenced in handlers match those defined in domain
   - Config struct fields match what main.go reads

2. VERIFY completeness:
   - Every interface defined in domain/ has an implementation in adapters/
   - Every handler has a corresponding test
   - Every exported symbol has a godoc comment
   - wire provider set includes all declared providers

3. VERIFY quality gates (run on combined output):
   - go build ./... — must compile
   - go vet ./...   — zero issues
   - go test ./...  — all pass

4. GENERATE package SKILL.md:
   - One SKILL.md per generated package (see meta/package-skill-generator/SKILL.md)
   - Documents all assumptions, contracts, and patterns used
```

---

## 6. Agent Communication Pattern

```go
// When implementing parallel generation in tooling,
// model agent communication as typed messages:

type AgentInput struct {
    TaskID       string
    Layer        string          // "domain", "app", "adapters/http", etc.
    Skills       []string        // skill paths to load
    Context      ProjectContext  // shared context (module path, config, etc.)
    Dependencies []AgentOutput   // results from prerequisite agents
}

type AgentOutput struct {
    TaskID    string
    Layer     string
    Files     []GeneratedFile
    Contracts []InterfaceContract // interfaces this agent defines/implements
    Errors    []string
}

type GeneratedFile struct {
    Path    string
    Content string
    Tests   bool
}

type InterfaceContract struct {
    Name    string
    Package string
    Methods []string
    Role    string // "defines" | "implements"
}
```

---

## 7. Package SKILL Auto-Generation Rule

**Every time Claude generates a complete package (>1 file, >100 lines),
it must also generate a package-specific SKILL.md** (see meta/package-skill-generator/SKILL.md):

```
Generated package: internal/adapters/postgres/order_repo.go

Auto-generate: internal/adapters/postgres/SKILL.md
  - Lists all exported types and their contracts
  - Documents assumptions (table schema, column names)
  - Lists all error types returned and their conditions
  - Notes performance characteristics (uses CopyFrom for bulk, etc.)
  - Links to: database/SKILL.md, packages/pgx/SKILL.md, architecture/SKILL.md
  - Usage example for the main use cases
```

---

## Multi-Agent Checklist

- [ ] Task decomposed by architectural layer (not by feature)
- [ ] Each agent has explicit list of skills to load
- [ ] Dependencies between agents explicitly stated (not assumed)
- [ ] Interface contracts documented by producing agent, verified by consuming agent
- [ ] Synthesis agent verifies cross-agent consistency before finalizing
- [ ] Package SKILL.md generated for every substantial new package
- [ ] All agents apply same quality gates (vet, lint, test)
- [ ] No agent produces TODO or placeholder code
