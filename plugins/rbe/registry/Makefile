# AccelRegistry Makefile
# ──────────────────────────────────────────────────────────────────────────────
# Targets:
#   make build          Build both binaries (server + accelctl)
#   make test           Run all tests
#   make test-unit      Run unit tests only
#   make test-integ     Run integration tests only
#   make test-handlers  Run per-handler tests only
#   make test-gc        Run GC/snapshot tests only
#   make bench          Run all benchmarks (3s per bench)
#   make bench-bloom    Bloom filter benchmarks only
#   make bench-index    Sharded index benchmarks only
#   make coverage       Generate HTML coverage report
#   make lint           Run staticcheck + go vet
#   make docker         Build Docker image
#   make docker-run     Run registry in Docker on :5000
#   make clean          Remove build artefacts
#   make release        Cross-compile release binaries (linux, darwin, windows)
# ──────────────────────────────────────────────────────────────────────────────

MODULE     := github.com/bons/bons-ci/plugins/rbe/registry
BINARY_SRV := bin/accelregistry
BINARY_CTL := bin/accelctl
VERSION    := $(shell git describe --tags --always --dirty 2>/dev/null || echo "v0.0.0-dev")
COMMIT     := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_TIME := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS    := -s -w \
              -X main.version=$(VERSION) \
              -X main.commit=$(COMMIT) \
              -X main.buildTime=$(BUILD_TIME)

GOENV := GONOSUMDB='*' GOPROXY='direct' GOFLAGS='-mod=mod'
GO    := $(GOENV) go

# ── Build ──────────────────────────────────────────────────────────────────

.PHONY: build
build: $(BINARY_SRV) $(BINARY_CTL)

$(BINARY_SRV): $(shell find . -name '*.go' -not -path './vendor/*')
	@mkdir -p bin
	$(GO) build -ldflags="$(LDFLAGS)" -o $(BINARY_SRV) ./cmd/server
	@echo "✓ Built $(BINARY_SRV)"

$(BINARY_CTL): $(shell find . -name '*.go' -not -path './vendor/*')
	@mkdir -p bin
	$(GO) build -ldflags="$(LDFLAGS)" -o $(BINARY_CTL) ./cmd/accelctl
	@echo "✓ Built $(BINARY_CTL)"

.PHONY: build-all
build-all: build

# ── Test ──────────────────────────────────────────────────────────────────

.PHONY: test
test:
	$(GO) test ./test/... -count=1 -timeout=120s -race

.PHONY: test-unit
test-unit:
	$(GO) test ./test/unit/... -count=1 -timeout=60s -v

.PHONY: test-integ
test-integ:
	$(GO) test ./test/integration/... -count=1 -timeout=120s -v

.PHONY: test-handlers
test-handlers:
	$(GO) test ./test/handlers/... -count=1 -timeout=60s -v

.PHONY: test-gc
test-gc:
	$(GO) test ./test/gc/... -count=1 -timeout=60s -v

.PHONY: test-race
test-race:
	$(GO) test ./test/... -count=1 -timeout=120s -race -v

# ── Coverage ──────────────────────────────────────────────────────────────

.PHONY: coverage
coverage:
	@mkdir -p .coverage
	$(GO) test ./test/... -count=1 -timeout=120s \
	    -coverprofile=.coverage/cover.out \
	    -coverpkg=./...
	$(GO) tool cover -html=.coverage/cover.out -o .coverage/coverage.html
	@echo "✓ Coverage report: .coverage/coverage.html"
	@$(GO) tool cover -func=.coverage/cover.out | tail -1

# ── Benchmarks ────────────────────────────────────────────────────────────

.PHONY: bench
bench:
	$(GO) test ./test/unit/... -bench=. -benchmem -benchtime=3s \
	    -count=1 -run='^$$' | tee .bench.txt
	@echo "✓ Benchmark results saved to .bench.txt"

.PHONY: bench-bloom
bench-bloom:
	$(GO) test ./test/unit/... -bench=BenchmarkBloom -benchmem -benchtime=3s \
	    -count=1 -run='^$$'

.PHONY: bench-index
bench-index:
	$(GO) test ./test/unit/... -bench=BenchmarkIndex -benchmem -benchtime=3s \
	    -count=1 -run='^$$'

.PHONY: bench-store
bench-store:
	$(GO) test ./test/unit/... -bench=BenchmarkStore -benchmem -benchtime=3s \
	    -count=1 -run='^$$'

.PHONY: bench-compare
bench-compare: bench
	@if command -v benchstat >/dev/null 2>&1; then \
	    benchstat .bench.txt; \
	else \
	    echo "Install benchstat: go install golang.org/x/perf/cmd/benchstat@latest"; \
	fi

# ── Lint & Vet ────────────────────────────────────────────────────────────

.PHONY: vet
vet:
	$(GO) vet ./...
	@echo "✓ go vet passed"

.PHONY: lint
lint: vet
	@if command -v staticcheck >/dev/null 2>&1; then \
	    staticcheck ./...; \
	    echo "✓ staticcheck passed"; \
	else \
	    echo "staticcheck not installed — run: go install honnef.co/go/tools/cmd/staticcheck@latest"; \
	fi

# ── Fuzz ──────────────────────────────────────────────────────────────────

.PHONY: fuzz-bloom
fuzz-bloom:
	$(GO) test ./pkg/bloom/... -fuzz=FuzzBloomFilter -fuzztime=30s

.PHONY: fuzz-detect
fuzz-detect:
	$(GO) test ./test/unit/... -fuzz=FuzzDetect -fuzztime=30s

# ── Docker ────────────────────────────────────────────────────────────────

IMAGE_NAME := accelregistry
IMAGE_TAG  := $(VERSION)

.PHONY: docker
docker:
	docker build \
	    --build-arg VERSION=$(VERSION) \
	    --build-arg COMMIT=$(COMMIT) \
	    -t $(IMAGE_NAME):$(IMAGE_TAG) \
	    -t $(IMAGE_NAME):latest \
	    .
	@echo "✓ Built Docker image $(IMAGE_NAME):$(IMAGE_TAG)"

.PHONY: docker-run
docker-run:
	docker run --rm -it \
	    -p 5000:5000 \
	    -p 9090:9090 \
	    -e ACCELREG_LOG_LEVEL=debug \
	    $(IMAGE_NAME):latest
	@echo "AccelRegistry running on :5000 (metrics on :9090)"

.PHONY: docker-push
docker-push: docker
	docker push $(IMAGE_NAME):$(IMAGE_TAG)
	docker push $(IMAGE_NAME):latest

# ── Release ───────────────────────────────────────────────────────────────

PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64

.PHONY: release
release:
	@mkdir -p dist
	@for platform in $(PLATFORMS); do \
	    os=$$(echo $$platform | cut -d/ -f1); \
	    arch=$$(echo $$platform | cut -d/ -f2); \
	    ext=""; \
	    if [ "$$os" = "windows" ]; then ext=".exe"; fi; \
	    echo "Building $$os/$$arch..."; \
	    GOOS=$$os GOARCH=$$arch $(GO) build \
	        -ldflags="$(LDFLAGS)" \
	        -o dist/accelregistry-$$os-$$arch$$ext \
	        ./cmd/server; \
	    GOOS=$$os GOARCH=$$arch $(GO) build \
	        -ldflags="$(LDFLAGS)" \
	        -o dist/accelctl-$$os-$$arch$$ext \
	        ./cmd/accelctl; \
	done
	@echo "✓ Release binaries in dist/"
	@ls -lh dist/

# ── Development helpers ───────────────────────────────────────────────────

.PHONY: run
run: build
	ACCELREG_LOG_LEVEL=debug \
	ACCELREG_ADDR=:5000 \
	ACCELREG_METRICS_ADDR=:9090 \
	$(BINARY_SRV)

.PHONY: run-with-snapshot
run-with-snapshot: build
	ACCELREG_LOG_LEVEL=debug \
	ACCELREG_ADDR=:5000 \
	ACCELREG_SNAPSHOT_DIR=/tmp/accelregistry-snapshots \
	$(BINARY_SRV)

.PHONY: tidy
tidy:
	$(GO) mod tidy
	@echo "✓ go mod tidy done"

.PHONY: deps
deps:
	$(GO) mod download
	@echo "✓ dependencies downloaded"

# ── Clean ─────────────────────────────────────────────────────────────────

.PHONY: clean
clean:
	@rm -rf bin/ dist/ .coverage/ .bench.txt
	@echo "✓ cleaned"

# ── CI shortcut (run everything) ──────────────────────────────────────────

.PHONY: ci
ci: vet test coverage bench
	@echo ""
	@echo "═══════════════════════════════════════"
	@echo "  CI complete — all checks passed ✓"
	@echo "═══════════════════════════════════════"

# ── Help ──────────────────────────────────────────────────────────────────

.PHONY: help
help:
	@echo "AccelRegistry Makefile"
	@echo ""
	@echo "Build:"
	@echo "  make build          Build server + accelctl"
	@echo "  make release        Cross-compile for all platforms"
	@echo "  make docker         Build Docker image"
	@echo ""
	@echo "Test:"
	@echo "  make test           Run all tests (with race detector)"
	@echo "  make test-unit      Unit tests only"
	@echo "  make test-integ     Integration tests only"
	@echo "  make test-handlers  Per-handler tests"
	@echo "  make test-gc        GC/snapshot tests"
	@echo "  make coverage       HTML coverage report"
	@echo ""
	@echo "Benchmark:"
	@echo "  make bench          All benchmarks"
	@echo "  make bench-bloom    Bloom filter benchmarks"
	@echo "  make bench-index    Sharded index benchmarks"
	@echo ""
	@echo "Development:"
	@echo "  make run            Start server (debug mode)"
	@echo "  make lint           vet + staticcheck"
	@echo "  make tidy           go mod tidy"
	@echo "  make clean          Remove build artefacts"

.DEFAULT_GOAL := help
