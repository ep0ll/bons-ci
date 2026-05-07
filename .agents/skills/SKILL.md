---
name: golang-skills
description: >
  Master index for world-class, production-grade Go development. Load for ANY Go task —
  writing, reviewing, refactoring, architecting, or debugging. Triggers on: Go/Golang,
  .go files, Go packages, Go services, Go CLI, Go microservices, Go performance,
  Go concurrency, Go architecture, Go DSA, Go databases, Go containers, Go Linux,
  Go APIs, Go observability, Go design patterns, Go clean code, Go SOLID, Go event-driven,
  Go CQRS, Go DDD, Go testing, Go security, Go generics, or any Go-related topic.
  ALWAYS read this index before writing any Go code.
---

# Golang Skills — Master Index v3

You are a **principal Go engineer and software architect** with expertise spanning systems
programming, distributed systems, cloud-native infrastructure, security engineering, database
internals, and software craftsmanship. Every Go artifact you produce is: correct, performant
at macro and micro levels, secure by default, fully observable, exhaustively tested,
architecturally principled, cohesive, composable, pluggable, extensible, and robust.

**Zero shortcuts. Zero TODOs in production paths. Zero false assumptions. Zero broken invariants.**

---

## ⚡ Domain Routing Table — Load Sub-Skill Before Writing Code

### Core Engineering
| Concern | Sub-skill path |
|---|---|
| Go idioms, performance, memory, generics, toolchain | `golang-core/SKILL.md` |
| Advanced concurrency: actors, CSP, lock-free, pipelines | `concurrency/SKILL.md` |
| Data structures & algorithms (Go-optimized) | `dsa/SKILL.md` |
| Error handling strategy & custom error taxonomy | `error-handling/SKILL.md` |
| Performance engineering: profiling, SIMD, benchmarks | `performance/SKILL.md` |
| Advanced generics: type params, constraints, collections | `generics/SKILL.md` |

### Architecture & Design
| Concern | Sub-skill path |
|---|---|
| Hexagonal / Clean Architecture / DDD / CQRS / Event Sourcing | `architecture/SKILL.md` |
| Event-driven: sagas, outbox, choreography, orchestration | `event-driven/SKILL.md` |
| REST / gRPC / GraphQL API design & versioning | `api-design/SKILL.md` |
| Code quality: cohesion, composability, extensibility, robustness | `code-quality/SKILL.md` |
| Microservices: boundaries, mesh, contract testing | `microservices/SKILL.md` |
| Caching strategies: L1/L2, stampede, TTL design | `caching/SKILL.md` |
| Resilience: circuit breakers, bulkheads, retry, hedging | `resilience/SKILL.md` |

### Infrastructure
| Concern | Sub-skill path |
|---|---|
| Security: crypto, TLS, auth, OWASP, supply chain | `security/SKILL.md` |
| Linux: syscalls, namespaces, cgroups, eBPF | `linux/SKILL.md` |
| Docker / containerd / OCI runtimes | `docker-containerd/SKILL.md` |
| Networking: HTTP, gRPC, TCP, circuit breakers | `networking/SKILL.md` |
| Database: transactions, migrations, query design | `database/SKILL.md` |
| Configuration management | `configuration/SKILL.md` |
| CLI tool design | `cli/SKILL.md` |
| Code generation (protoc, go:generate) | `code-generation/SKILL.md` |
| Deployment: K8s, Helm, Docker, CI/CD | `deployment/SKILL.md` |
| Go workspaces and monorepo patterns | `workspace/SKILL.md` |

### Quality & Craft
| Concern | Sub-skill path |
|---|---|
| SOLID principles | `solid-principles/SKILL.md` |
| Design patterns (GoF + Go-specific) | `design-patterns/SKILL.md` |
| Clean Code (Uncle Bob) | `clean-code/SKILL.md` |
| Testing: unit, fuzz, bench, integration, contract | `testing/SKILL.md` |
| Observability: logging, tracing, metrics, profiling | `observability/SKILL.md` |

### Agent & Workflow
| Concern | Sub-skill path |
|---|---|
| Multi-agent parallel orchestration | `multi-agent/SKILL.md` |
| Planning with files & iterative development | `planning/SKILL.md` |
| Package-specific skill generation | `meta/package-skill-generator/SKILL.md` |
| Code review workflow | `meta/code-review/SKILL.md` |
| TDD flow | `meta/tdd-flow/SKILL.md` |
| Refactoring workflow | `meta/refactor/SKILL.md` |
| Agent self-verification and parallel patterns | `meta/agent-patterns/SKILL.md` |

### Package-Specific Skills
> Load alongside domain skills. Contain exhaustive API contracts, gotchas, and production patterns.

| Package | Sub-skill |
|---|---|
| `context` | `packages/context/SKILL.md` |
| `sync` + `sync/atomic` | `packages/sync/SKILL.md` |
| `net/http` stdlib | `packages/http-std/SKILL.md` |
| `google.golang.org/grpc` | `packages/grpc/SKILL.md` |
| `jackc/pgx/v5` | `packages/pgx/SKILL.md` |
| `jmoiron/sqlx` | `packages/sqlx/SKILL.md` |
| `redis/go-redis/v9` | `packages/redis/SKILL.md` |
| `segmentio/kafka-go` | `packages/kafka/SKILL.md` |
| `go-chi/chi/v5` | `packages/chi/SKILL.md` |
| `gin-gonic/gin` | `packages/gin/SKILL.md` |
| `spf13/cobra` + `viper` | `packages/cobra-viper/SKILL.md` |
| `google.golang.org/protobuf` | `packages/protobuf/SKILL.md` |
| `google/wire` | `packages/wire/SKILL.md` |
| `uber-go/fx` | `packages/fx/SKILL.md` |
| `testify/mock` + `suite` | `packages/testify-mock/SKILL.md` |
| `testcontainers-go` | `packages/testcontainers/SKILL.md` |
| `go.opentelemetry.io/otel` | `packages/otel/SKILL.md` |
| `log/slog` | `packages/slog/SKILL.md` |
| `uber-go/zap` | `packages/zap/SKILL.md` |
| `golang-migrate/migrate` | `packages/migrate/SKILL.md` |
| `golang.org/x/sync/errgroup` | `packages/errgroup/SKILL.md` |
| `entgo.io/ent` | `packages/ent/SKILL.md` |
| `gorm.io/gorm` | `packages/gorm/SKILL.md` |
| `nats-io/nats.go` | `packages/nats/SKILL.md` |
| `dgraph-io/ristretto` | `packages/ristretto/SKILL.md` |
| `samber/lo` | `packages/lo/SKILL.md` |
| `go-playground/validator/v10` | `packages/validator/SKILL.md` |
| `golang-jwt/jwt/v5` | `packages/jwt/SKILL.md` |
| `google/uuid` + `shopspring/decimal` + `sqlc` | `packages/uuid/SKILL.md` |
| `buf CLI` | `packages/buf/SKILL.md` |

---

## Universal Non-Negotiables — Every .go File

```
QUALITY GATE (must pass before outputting any code):
  ✓ go vet ./...                    zero issues
  ✓ staticcheck ./...               zero issues
  ✓ golangci-lint run               zero issues (strict config below)
  ✓ go test -race ./...             zero races, zero failures
  ✓ govulncheck ./...               zero known CVEs in dependencies
  ✓ All errors handled — never _ without explicit // nolint comment + reason
  ✓ All goroutines have context cancellation path
  ✓ All exported symbols have godoc comments
  ✓ No global mutable state (use DI)
  ✓ No init() functions
  ✓ No panic() in library code
  ✓ No hardcoded credentials, timeouts, sizes
  ✓ All configuration externalized
  ✓ Graceful shutdown implemented for all servers/workers
```

### Canonical Error Wrapping (always)
```go
return fmt.Errorf("ServiceName.MethodName(param=%v): %w", param, err)
```

### Canonical Context Signature (always first)
```go
func (s *Service) Method(ctx context.Context, req Request) (Response, error)
```

### Canonical Project Layout
```
project/
├── cmd/APP/main.go       thin: parse flags → wire → run
├── internal/
│   ├── domain/           entities, VOs, domain events, repo interfaces, domain errors
│   ├── app/              use cases / application services
│   ├── adapters/         HTTP handlers, DB repos, MQ consumers, gRPC servers
│   └── ports/            interface contracts consumed by app/domain
├── pkg/                  exported reusable (minimal — think twice)
├── api/                  .proto / OpenAPI specs
├── configs/              config schema + defaults
├── migrations/           numbered SQL
├── scripts/              build/deploy/generate
├── test/                 integration fixtures, testcontainers setup
├── go.mod  go.sum
└── Makefile
```

### Skill Composition Matrix
```
New HTTP endpoint:   golang-core + api-design + networking + security
                   + clean-code + solid + packages/chi + packages/otel
                   + packages/slog + packages/pgx + error-handling

New microservice:   ALL architecture + event-driven + ALL quality
                  + database + configuration + observability + deployment
                  + packages/* relevant

CLI tool:          golang-core + cli + clean-code + configuration
                 + packages/cobra-viper + packages/slog

Container runtime: golang-core + linux + docker-containerd
                 + security + concurrency

Package-specific:  ALWAYS load packages/PKGNAME/SKILL.md
                   alongside domain skills when using that package
```

### Makefile Template
```makefile
.PHONY: all build test lint vet tidy check proto generate ci

BINARY  := bin/$(notdir $(CURDIR))
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
FLAGS   := -trimpath -ldflags="-s -w -X main.version=$(VERSION) -X main.commit=$(shell git rev-parse --short HEAD 2>/dev/null)"

build:
	CGO_ENABLED=0 go build $(FLAGS) -o $(BINARY) ./cmd/...

test:
	go test -race -count=1 -timeout=5m -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | grep total

test-integration:
	go test -race -tags=integration -count=1 -timeout=10m ./test/...

bench:
	go test -run='^$$' -bench=. -benchmem -count=5 ./... | tee bench.txt

lint:
	golangci-lint run

vet:
	go vet ./...

vuln:
	govulncheck ./...

tidy:
	go mod tidy && go mod verify

proto:
	buf generate

generate:
	go generate ./...

ci: tidy vet lint vuln test

all: ci build
```

### .golangci.yml
```yaml
run:
  timeout: 5m
  go: "1.22"

linters:
  enable:
    - errcheck
    - gosimple
    - govet
    - ineffassign
    - staticcheck
    - unused
    - gofmt
    - goimports
    - misspell
    - gosec
    - gocritic
    - revive
    - wrapcheck
    - contextcheck
    - noctx
    - bodyclose
    - sqlclosecheck
    - rowserrcheck
    - nilerr
    - exhaustive
    - prealloc
    - unparam
    - godot
    - nestif
    - cyclop
    - funlen
    - gocognit
    - maintidx

linters-settings:
  funlen:   { lines: 60, statements: 40 }
  gocognit: { min-complexity: 15 }
  cyclop:   { max-complexity: 10 }
  nestif:   { min-complexity: 4 }
  wrapcheck:
    ignoreSigs: [".Errorf(", "errors.New(", "errors.Unwrap("]
  revive:
    rules:
      - { name: exported }
      - { name: error-return }
      - { name: error-naming }
      - { name: var-naming }
      - { name: receiver-naming }
      - { name: argument-limit, arguments: [4] }

issues:
  exclude-rules:
    - { path: _test\.go, linters: [gosec, exhaustive, funlen, gocognit] }
    - { path: cmd/, linters: [exhaustive] }
```
