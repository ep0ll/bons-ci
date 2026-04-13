# RBE – Remote Build Environment
# ─────────────────────────────────────────────────────────────────────────────

BINARY      := rbed
MAIN        := ./cmd/rbed
MODULE      := github.com/bons/bons-ci/plugins/rbe
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT      ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_TIME  := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS     := -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.buildTime=$(BUILD_TIME)

PROTO_DIR   := proto
GEN_DIR     := gen/proto
PROTOC_GEN_GO := protoc-gen-go
PROTOC_GEN_GO_GRPC := protoc-gen-go-grpc

.PHONY: all build clean test lint proto docker run dev

# ── Build ─────────────────────────────────────────────────────────────────────

all: build

build:
	@echo "→ Building $(BINARY) $(VERSION)"
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -trimpath -o bin/$(BINARY) $(MAIN)

build-race:
	@echo "→ Building $(BINARY) with race detector"
	go build -race -ldflags "$(LDFLAGS)" -o bin/$(BINARY)-race $(MAIN)

# ── Proto code generation ─────────────────────────────────────────────────────

proto:
	@echo "→ Generating proto code"
	@mkdir -p $(GEN_DIR)/rbe/v1 $(GEN_DIR)/registry/v1
	protoc 		--go_out=$(GEN_DIR)/rbe/v1 		--go_opt=paths=source_relative 		--go-grpc_out=$(GEN_DIR)/rbe/v1 		--go-grpc_opt=paths=source_relative 		--grpc-gateway_out=$(GEN_DIR)/rbe/v1 		--grpc-gateway_opt=paths=source_relative 		-I $(PROTO_DIR) 		$(PROTO_DIR)/rbe/v1/rbe.proto
	protoc 		--go_out=$(GEN_DIR)/registry/v1 		--go_opt=paths=source_relative 		--go-grpc_out=$(GEN_DIR)/registry/v1 		--go-grpc_opt=paths=source_relative 		-I $(PROTO_DIR) 		$(PROTO_DIR)/registry/v1/registry.proto
	@echo "✓ Proto generation complete"

proto-install-tools:
	go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
	go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
	go install github.com/grpc-ecosystem/grpc-gateway/v2/protoc-gen-grpc-gateway@latest

# ── Test ──────────────────────────────────────────────────────────────────────

test:
	go test -v -race -count=1 ./...

test-short:
	go test -short -count=1 ./...

test-coverage:
	go test -v -race -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "✓ Coverage report: coverage.html"

# ── Lint & format ─────────────────────────────────────────────────────────────

lint:
	golangci-lint run ./...

fmt:
	gofmt -w -s .
	goimports -w .

vet:
	go vet ./...

# ── Docker ────────────────────────────────────────────────────────────────────

docker:
	docker build -t rbed:$(VERSION) .

docker-push:
	docker push rbed:$(VERSION)

# ── Local dev ─────────────────────────────────────────────────────────────────

# Start dependencies: MinIO (S3), etcd
dev-deps:
	docker compose -f docker-compose.dev.yml up -d

dev-deps-down:
	docker compose -f docker-compose.dev.yml down

run: build
	./bin/$(BINARY) --config config/dev.yaml

clean:
	rm -rf bin/ gen/ coverage.out coverage.html

# ── Tidy ─────────────────────────────────────────────────────────────────────

tidy:
	go mod tidy
	go mod verify
