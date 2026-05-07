---
name: golang-workspace
description: >
  Go workspaces (go.work): multi-module development, local module replacement, shared tooling
  across modules, monorepo patterns, and workspace-aware CI. Use when developing multiple
  Go modules simultaneously (e.g., service + shared library + CLI in same repo).
---

# Go Workspace — Multi-Module Development

## 1. Workspace Setup

```bash
# Initialize workspace at repo root
go work init

# Add modules to workspace
go work use ./services/order-service
go work use ./services/user-service
go work use ./libs/shared
go work use ./tools/cli

# go.work file is generated:
go 1.22

use (
    ./services/order-service
    ./services/user-service
    ./libs/shared
    ./tools/cli
)
```

## 2. go.work Structure

```
monorepo/
├── go.work               ← workspace file (commit to repo)
├── go.work.sum           ← workspace checksum (commit to repo)
├── libs/
│   └── shared/
│       ├── go.mod        ← module: github.com/org/shared
│       └── ...
├── services/
│   ├── order-service/
│   │   ├── go.mod        ← module: github.com/org/order-service
│   │   └── ...
│   └── user-service/
│       ├── go.mod        ← module: github.com/org/user-service
│       └── ...
└── tools/
    └── cli/
        ├── go.mod        ← module: github.com/org/cli
        └── ...
```

## 3. Local Module Development

```go
// Without workspace: use replace directive in go.mod (messy)
// go.mod:
require github.com/org/shared v0.1.0
replace github.com/org/shared => ../shared  // local development hack

// With workspace: no replace needed — workspace handles it
// go.work:
use ./shared
use ./order-service
// order-service imports github.com/org/shared → resolved from ./shared automatically
```

## 4. Workspace Commands

```bash
# Run tests across all modules
go work sync         # sync go.work.sum
go test ./...        # tests all modules in workspace

# Build specific module
cd services/order-service && go build ./...

# Add new dependency to specific module
cd services/order-service && go get github.com/some/package

# Tidy all modules
for dir in libs/shared services/*; do
    (cd "$dir" && go mod tidy)
done

# Check workspace is consistent
go work sync && go build ./...
```

## 5. CI/CD with Workspaces

```yaml
# .github/workflows/ci.yaml
jobs:
  test-order-service:
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version: "1.22" }
      # Workspace allows testing order-service with local shared lib
      - run: go test -race ./services/order-service/...

  test-shared-lib:
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version: "1.22" }
      - run: go test -race ./libs/shared/...

  # Release: ensure modules work with published versions (not workspace)
  release-check:
    steps:
      - run: GOWORK=off go build ./services/order-service/...
      # GOWORK=off disables workspace — tests against published module versions
```

## 6. Makefile for Monorepo

```makefile
MODULES := libs/shared services/order-service services/user-service tools/cli

.PHONY: tidy test build lint

tidy:
	go work sync
	for dir in $(MODULES); do (cd $$dir && go mod tidy); done

test:
	go test -race ./...

lint:
	golangci-lint run ./...

build:
	for dir in services/*; do (cd $$dir && go build ./cmd/...); done

# Release a specific module
release-shared:
	cd libs/shared && go mod tidy && git tag shared/v$(VERSION)
	git push origin shared/v$(VERSION)
```

## 7. Workspace vs Replace Directives

```
USE go.work when:
  ✓ Active development across multiple modules simultaneously
  ✓ Monorepo with multiple services sharing libraries
  ✓ Integration testing across service boundaries locally

USE replace directives when:
  ✓ Single developer, one-off local testing
  ✓ Forking an external dependency for patches
  ✗ Never commit replace pointing to local paths (breaks CI)

GOTCHA: go.work takes precedence over replace directives
GOTCHA: GOWORK=off to test with published module versions
GOTCHA: go.work.sum must be committed (like go.sum)
```

## Workspace Checklist
- [ ] `go.work` and `go.work.sum` committed to repo
- [ ] Each module has its own `go.mod` and `go.sum`
- [ ] `GOWORK=off` used in release CI to verify published module compatibility
- [ ] `go work sync` run before all test/build commands
- [ ] Replace directives removed from `go.mod` when using workspace
- [ ] Module boundaries respect architectural layering (no circular imports)
- [ ] Each module independently buildable with `GOWORK=off`
