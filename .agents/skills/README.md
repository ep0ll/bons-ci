# Go Skills for Claude Code — v4.0

**68 SKILL.md files · ~20,500 lines · Production-grade Go guidance**

A complete, structured skill system for Claude Code that produces world-class Go:
cohesive, composable, pluggable, extensible, robust, observable, secure, performant.

---

## Install

```bash
# Project-level (recommended)
unzip golang-skills.zip
mv golang-skills .claude/skills/golang-skills

# Global (all projects)
unzip golang-skills.zip -d ~/.claude/skills/
```

Add to your project's `CLAUDE.md`:

```markdown
## Engineering Standards
Always read `.claude/skills/golang-skills/SKILL.md` before writing any Go code.
Load sub-skills for the domain at hand. All quality gates must pass before output.
```

---

## Slash Commands (Claude Code)

Copy from `.claude/commands/` into your project's `.claude/commands/`:

| Command | What it does |
|---|---|
| `/go-new-service` | Scaffold complete microservice: domain → app → adapters → infra → tests |
| `/go-review` | Full code review across 8 quality lenses with P0–P4 priority findings |
| `/go-add-endpoint` | Add HTTP endpoint + use case + tests to an existing service |
| `/go-add-package-skill` | Generate SKILL.md for a package Claude just created |
| `/go-plan` | Create PLAN.md + ASSUMPTIONS.md + first ADR before coding starts |
| `/go-tdd` | TDD Red-Green-Refactor cycle for any function, type, or use case |
| `/go-benchmark` | Add benchmarks, profile hot path, produce optimized version |
| `/go-refactor` | Safe numbered refactoring sequence for any code smell |

---

## Project Templates

Copy from `templates/` into your project root:

| File | Purpose |
|---|---|
| `CLAUDE.md` | Base Claude Code project instructions |
| `CLAUDE.md.microservice` | Full microservice variant with tech stack table |
| `CLAUDE.md.cli` | CLI tool variant |
| `.golangci.yml` | Strict linter config matching all skill rules |
| `Makefile` | Canonical build/test/lint/migrate/docker targets |
| `.github/workflows/ci.yaml` | Full GitHub Actions CI pipeline |

---

## Skill Map — 68 Skills, ~20,500 Lines

### Master Index
`SKILL.md` — routing table, universal quality gates, Makefile + golangci.yml templates

### Core Engineering (6 skills)
| Skill | Lines | Key Coverage |
|---|---|---|
| `golang-core/` | 352 | Naming, zero values, memory, generics, escape analysis, struct padding |
| `concurrency/` | 350 | Worker pools, pipelines, actors, pub/sub, rate limiters, singleflight |
| `dsa/` | 350 | Stack, heap, LRU, ring buffer, bloom filter, graph algorithms |
| `error-handling/` | 327 | Error taxonomy, wrapping, multi-error, HTTP/gRPC mapping, panic policy |
| `performance/` | **963** | Profiling workflow, GC tuning, escape analysis, SIMD, Linux tuning, Docker perf |
| `generics/` | 280 | Type params, constraints, Optional/Result types, when to use generics |

### Architecture & Design (7 skills)
| Skill | Lines | Key Coverage |
|---|---|---|
| `architecture/` | 410 | Hexagonal arch, DDD, CQRS, event sourcing, anti-corruption layers |
| `event-driven/` | 340 | Outbox pattern, idempotent consumers, saga choreography + orchestration |
| `api-design/` | 380 | REST naming, cursor pagination, PATCH, ETags, gRPC service design |
| `code-quality/` | 295 | Cohesion, composability, pluggability, robustness, non-false-positive |
| `microservices/` | 310 | Boundaries, inter-service auth, contract testing, service mesh |
| `caching/` | 270 | Cache-aside, write-through, L1+L2, stampede prevention, TTL strategy |
| `resilience/` | 300 | Circuit breakers, bulkheads, retry+jitter, hedged requests, fallbacks |

### Infrastructure (10 skills)
| Skill | Lines | Key Coverage |
|---|---|---|
| `security/` | 350 | Argon2id, AES-GCM, TLS 1.3, JWT RS256, input validation, supply chain |
| `linux/` | **873** | Syscalls, all 7 namespaces, cgroups v2 full API, epoll, inotify, capabilities, seccomp |
| `docker-containerd/` | **745** | Docker full API, containerd tasks/snapshots, OCI hardening, overlay FS |
| `networking/` | 371 | HTTP server/client, gRPC, TCP server, circuit breaker, retry+jitter |
| `database/` | 340 | Transaction manager, repository pattern, cursor pagination, bulk ops |
| `configuration/` | 260 | 12-factor config, typed structs, hot reload, secret injection |
| `cli/` | 290 | Cobra hierarchy, exit codes, output formatters, shell completion |
| `code-generation/` | 250 | go:generate, mockery, buf/protoc, sqlc, wire, AST generators |
| `deployment/` | 310 | K8s manifests, Helm, Dockerfile, docker-compose, CI/CD |
| `workspace/` | 190 | go.work, multi-module monorepo, CI with workspaces |

### Quality & Craft (5 skills)
| Skill | Lines | Key Coverage |
|---|---|---|
| `solid-principles/` | 311 | All 5 SOLID rules with Go examples, anti-patterns, composition root |
| `design-patterns/` | 395 | 15+ patterns: functional options, decorator, strategy, event bus, state machine |
| `clean-code/` | **1237** | **ALL Uncle Bob rules**: N1-N7 naming, F1-F12 functions, C1-C5 comments, G1-G36 heuristics, T1-T9 tests, concurrency, formatting, Law of Demeter, CQS |
| `testing/` | 335 | Table-driven, fuzz, benchmarks, testcontainers, golden files, race detector |
| `observability/` | 327 | slog JSON, OTel tracing, Prometheus RED metrics, pprof, graceful shutdown |

### Agent & Planning (7 skills)
| Skill | Lines | Key Coverage |
|---|---|---|
| `planning/` | 240 | PLAN.md workflow, task decomposition, ADRs, session handoff |
| `multi-agent/` | 220 | Parallel decomposition, contract-first, result synthesis |
| `meta/agent-patterns/` | 195 | Self-verification loop, parallel detection, output ordering |
| `meta/code-review/` | 200 | P0-P4 priorities, 8-lens checklist, structured finding format |
| `meta/tdd-flow/` | 190 | Red-Green-Refactor for all architectural layers |
| `meta/refactor/` | 185 | Safe sequences, extract interface, strangler fig |
| `meta/package-skill-generator/` | 180 | Template for auto-generating package SKILL.md files |

### Package Skills (33 packages)
| Package | Key Coverage |
|---|---|
| `context/` | Propagation, value keys, leak prevention, WithoutCancel |
| `sync/` | Mutex, RWMutex, WaitGroup, Once, Map, Pool, Cond, all atomics |
| `http-std/` | Server timeouts, client hardening, Go 1.22 mux, reverse proxy |
| `errgroup/` | Parallel execution, SetLimit, bounded concurrency, pipelines |
| `slog/` | JSON handler, context logger, OTel handler, ReplaceAttr |
| `grpc/` | Server/client setup, interceptors, streaming, error mapping |
| `pgx/` | Pool setup, CollectRows, batch, COPY, LISTEN/NOTIFY, pgtype |
| `sqlx/` | Named queries, struct scanning, IN expansion, transactions |
| `redis/` | Pipeline, WATCH/MULTI, streams, KV store, distributed locks |
| `kafka/` | Producer, consumer group, DLQ, exactly-once, admin ops |
| `chi/` | Router setup, URL params, context middleware, testing |
| `gin/` | Router, binding, validation integration, middleware, testing |
| `cobra-viper/` | Command hierarchy, flag binding, config layering |
| `protobuf/` | proto3 design, marshaling, well-known types, compat rules |
| `buf/` | buf.yaml, lint rules, breaking detection, BSR, CI |
| `wire/` | Providers, sets, injectors, interface binding, test injectors |
| `fx/` | Lifecycle, Provide/Invoke, fx.In/Out, named values, fxtest |
| `testify-mock/` | EXPECT() API, matchers, return funcs, suite lifecycle |
| `testcontainers/` | Postgres/Redis/Kafka containers, wait strategies, reuse |
| `otel/` | Tracer init, spans, attributes, metrics, propagation, sampling |
| `migrate/` | File conventions, embedded migrations, zero-downtime patterns |
| `zap/` | Core vs Sugar, child loggers, dynamic level, slog bridge |
| `ent/` | Schema DSL, code gen, query API, edges, hooks, transactions |
| `gorm/` | Model definition, scopes, preload, hooks, raw SQL |
| `nats/` | Core pub/sub, JetStream, KV store, queue groups, streams |
| `ristretto/` | Cost eviction, cache-aside, async Set behavior, metrics |
| `lo/` | Map, Filter, GroupBy, Chunk, ToPtr, Must, ternary, sets |
| `validator/` | Struct tags, custom validators, cross-field, error extraction |
| `jwt/` | RS256/ES256 generation, validation, refresh rotation, JWKS |
| `uuid/` + `decimal/` + `sqlc/` | UUID types, decimal money, sqlc config + usage |
| `decimal/` | Reference → uuid/SKILL.md Part 2 |
| `sqlc/` | Reference → uuid/SKILL.md Part 3 |

---

## Enhanced Skills in v4.0

### Clean Code (1,237 lines — 4× expansion)
Now covers **every Uncle Bob rule** from all chapters:
- **N1–N17**: All 17 naming rules with Go examples and anti-patterns
- **F1–F12**: All function rules (small, one thing, step-down, arguments, CQS, DRY, early returns)
- **C1–C5**: Comment philosophy with every acceptable and unacceptable comment type
- **G1–G36**: Complete general heuristics catalog
- **T1–T9**: Complete test heuristics
- **Concurrency rules**: Shared scope, copies, sync sections, thread isolation
- Chapter 5 (Formatting), Chapter 6 (Objects/Demeter), Chapter 7 (Error Handling), Chapter 8 (Boundaries)

### Performance (963 lines — 3× expansion)
Now covers **Go + Linux + Docker** performance:
- Full pprof workflow (CPU, memory, goroutine, block, mutex, trace)
- Benchmark design with `benchstat` statistical comparison
- Escape analysis techniques and heap escape prevention
- Allocation elimination (sync.Pool, pre-allocation, zero-copy)
- CPU cache optimization (false sharing, AoS vs SoA, cache line padding)
- GC tuning (GOGC, GOMEMLIMIT, batch job patterns)
- Linux network tuning (sysctl, SO_REUSEPORT, TCP_NODELAY)
- Container performance (cgroup limits, overlay FS, seccomp overhead)
- 8 performance anti-patterns with fixes

### Linux (873 lines — comprehensive rewrite)
- Full syscall layer with `golang.org/x/sys/unix`
- All 7 namespace types with clone flags, setns, WithNetNS pattern
- pivot_root for container isolation
- **Cgroups v2 full API**: memory (max/high/swap/OOM), CPU (max/weight/stats), I/O (max/pressure), PID (max), PSI notifications, freeze/thaw/kill
- Signal handling: SIGTERM/SIGHUP/SIGUSR1/SIGUSR2/SIGCHLD
- epoll and inotify production implementations
- Linux capabilities with libcap
- seccomp BPF with complete HTTP service syscall allowlist
- /proc filesystem interfaces (status, maps, net, PSI)
- Production daemonization with PID file locking
- systemd socket activation

### Docker/Containerd (745 lines — comprehensive rewrite)
- Full Docker Engine API: lifecycle, image management, stats, exec, events
- Container spec with all security options (seccomp, AppArmor, capabilities, read-only FS)
- Event streaming with automatic reconnection
- containerd gRPC client with leases, snapshots, OCI spec hardening
- OCI image building with go-containerregistry
- Multi-stage Dockerfile with BuildKit cache mounts
- Overlay FS internals and performance implications
- Container stats parsing (CPU%, memory, network, block I/O)

---

## Quality Guarantees

Every Go file produced with these skills:

```
✅ Cohesive          one clear purpose per type/package
✅ Composable        middleware/decorators combine without side effects
✅ Pluggable         all IO behind interfaces, swappable implementations
✅ Extensible        new behaviors via new types, not modifying existing
✅ Robust            nil/input validated at all boundaries
✅ Non-false-positive errors always propagated explicitly
✅ Reactive          outbox, sagas, event-driven patterns
✅ Observable        OTel traces + Prometheus + structured slog
✅ Secure            TLS 1.3, argon2id, parameterized SQL, allowlists
✅ Performant        profiling-first, allocation-aware, GC-tuned
```

Quality gates on every file:
```bash
go vet ./...         # zero issues
staticcheck ./...    # zero issues
golangci-lint run    # zero issues (strict config in SKILL.md)
go test -race ./...  # zero data races, zero failures
govulncheck ./...    # zero known CVEs
```

---

## Package Skill Auto-Generation

When Claude generates any new Go package:
```
Generated: internal/adapters/postgres/order_repo.go
→ Auto-generates: internal/adapters/postgres/SKILL.md
    - All exported types + exact contracts
    - Complete error catalogue
    - Exact DB schema dependencies
    - Concurrency guarantees
    - Non-breaking extension rules
    - Links to parent skills
```

---

## File Summary

```
Domain skills:      28 files  (core + architecture + infrastructure + quality)
Agent/Meta skills:   7 files  (planning, review, TDD, refactor, agent patterns)
Package skills:     33 files  (one per package)
────────────────────────────
SKILL.md total:     68 files
~20,500 lines of production Go guidance

Templates:           6 files  (CLAUDE.md variants, .golangci.yml, Makefile, CI)
Commands:            8 files  (.claude/commands/ slash commands)
```
