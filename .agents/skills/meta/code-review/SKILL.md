---
name: meta-code-review
description: >
  Structured Go code review workflow: systematic review checklist across all quality dimensions,
  priority ordering of findings, actionable feedback format, and automated tool integration.
  Load when asked to review, audit, or critique any Go code. Applies ALL domain skills
  as lenses simultaneously. Always produces findings in priority order with line references.
---

# Go Code Review — Systematic Workflow

## Review Priority Order

```
P0 — BLOCKER (must fix before merge):
  Security vulnerabilities, data races, goroutine leaks, error suppression,
  hardcoded secrets, SQL injection, nil dereferences, broken invariants

P1 — CRITICAL (fix in this PR or immediate follow-up):
  Missing error wrapping, context not propagated, missing defer cancel(),
  unbounded goroutines/channels, missing input validation, no tests

P2 — MAJOR (fix soon — architectural):
  SOLID violations, domain layer importing adapters, god objects,
  business logic in HTTP handlers, missing interface segregation

P3 — MINOR (clean up — craft):
  Clean code violations, unclear names, long functions, comment debt,
  missing godoc, struct padding, performance red flags

P4 — SUGGESTION (optional improvements):
  Alternative patterns, idiomatic Go suggestions, test coverage gaps
```

---

## Review Lens Checklist

### Lens 1: Security (security/SKILL.md)
```
[ ] No hardcoded secrets, tokens, or credentials
[ ] SQL queries use parameterized arguments ($1, $2)
[ ] User input validated with allowlists before use
[ ] No path traversal: filepath.Join results checked against base
[ ] Crypto: only crypto/rand, no math/rand for security
[ ] TLS: minimum version enforced, cert validation on
[ ] Secrets not logged at any level
[ ] No exec.Command with user input in shell form
[ ] govulncheck passes on dependencies
```

### Lens 2: Correctness & Robustness (error-handling/SKILL.md, code-quality/SKILL.md)
```
[ ] Every error handled — no _ suppression without // nolint + reason
[ ] All errors wrapped with context: fmt.Errorf("Type.Method(param): %w", err)
[ ] No false positives: functions return error when they should, not silent zero values
[ ] Nil input handled at function boundaries
[ ] No panic in library code (only programmer-error invariants in internal/)
[ ] Idempotent operations where applicable (MQ consumers, HTTP PUT)
[ ] Defensive copies of slices/maps returned from aggregate methods
```

### Lens 3: Concurrency (concurrency/SKILL.md, packages/sync/SKILL.md)
```
[ ] go test -race passes (check CI report)
[ ] Every goroutine has clear owner and lifetime
[ ] All goroutines exit on ctx.Done()
[ ] defer cancel() immediately after WithCancel/WithTimeout
[ ] No unbounded goroutine creation (use worker pool or errgroup.SetLimit)
[ ] Mutex fields adjacent to data they protect, documented
[ ] No mutex copy (passed by pointer)
[ ] sync.Pool objects reset before use, result copied before returning to pool
[ ] Channels sized appropriately (0=sync, N=bounded async, never open-ended)
```

### Lens 4: Architecture (architecture/SKILL.md, solid-principles/SKILL.md)
```
[ ] Domain package imports: only stdlib + domain sibling packages
[ ] Repository interfaces defined in domain/, implemented in adapters/
[ ] No business logic in HTTP handlers or gRPC servers
[ ] Interfaces defined at consumer site, not provider site
[ ] Single responsibility: each type has one clear purpose
[ ] DI via constructor, not global vars or init()
[ ] CQRS: commands and queries use separate models
[ ] No anemic domain model (behavior in domain, not "service" layer)
```

### Lens 5: Performance (performance/SKILL.md)
```
[ ] No unnecessary allocations in hot paths (check with -gcflags="-m")
[ ] Pre-allocated slices/maps where size is known
[ ] sync.Pool used for high-frequency short-lived objects
[ ] No string concatenation in loops (use strings.Builder)
[ ] Struct fields ordered largest → smallest to minimize padding
[ ] GOMEMLIMIT set in container deployments
[ ] No interface{} in hot loops
[ ] DB queries: no N+1 (batch load relationships)
```

### Lens 6: Observability (observability/SKILL.md)
```
[ ] All significant events logged with structured slog
[ ] No PII, secrets in log messages
[ ] OTel span on all cross-service calls and DB operations
[ ] Errors recorded on spans with span.RecordError(err)
[ ] Prometheus metrics: at minimum request_count, error_count, duration
[ ] Health check endpoints present (/healthz, /readyz)
[ ] Graceful shutdown: context timeout on all in-flight operations
```

### Lens 7: Testing (testing/SKILL.md)
```
[ ] Table-driven tests for all functions with multiple cases
[ ] t.Parallel() on all parallelizable tests
[ ] Mocks use mockery-generated (not hand-written for large interfaces)
[ ] No time.Sleep in tests (use channels, testify.Eventually)
[ ] Integration tests behind testing.Short() guard
[ ] Fuzz targets for all public parsing/decoding functions
[ ] Benchmarks with b.ReportAllocs() for performance-critical code
[ ] Test names: TestFunc_Scenario_Expected
```

### Lens 8: Clean Code (clean-code/SKILL.md)
```
[ ] No function longer than 60 lines
[ ] No function with more than 3 non-context arguments (use struct)
[ ] No boolean flag arguments (split into two functions)
[ ] No commented-out code
[ ] Comments explain WHY, not WHAT
[ ] All exported symbols have godoc comments
[ ] Variable names reveal intent (no i, d, u for domain variables)
[ ] Command-Query Separation: functions either do or answer, not both
```

---

## Review Output Format

```markdown
## Code Review: [PR/File Name]

### Summary
[2-3 sentences: overall quality, main concerns, recommendation: APPROVE / REQUEST CHANGES]

### Findings

#### P0 — BLOCKERS
**[FILE:LINE]** [Finding title]
```go
// Current code
problematicCode()
```
**Problem**: [Exact issue and why it's a blocker]
**Fix**:
```go
// Corrected code
fixedCode()
```

#### P1 — CRITICAL
...

#### P2 — MAJOR
...

#### P3 — MINOR
...

#### P4 — SUGGESTIONS
...

### Positive Highlights
- [Good pattern used — reinforce it]
- [Good test coverage on X]

### Skills Applied
- security/SKILL.md: [finding ref]
- error-handling/SKILL.md: [finding ref]
```

---

## Automated Pre-Review

Before manual review, run and report:
```bash
# Must all pass before human review is meaningful
go build ./...          # compilation
go vet ./...            # static analysis
staticcheck ./...       # deeper static analysis
golangci-lint run       # comprehensive lint
go test -race ./...     # correctness + races
govulncheck ./...       # known CVEs
```
