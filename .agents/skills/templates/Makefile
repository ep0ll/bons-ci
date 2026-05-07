# ─── Variables ────────────────────────────────────────────────────────────────
BINARY      := bin/$(notdir $(CURDIR))
MODULE      := $(shell go list -m)
VERSION     := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT      := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_DATE  := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS     := -trimpath -ldflags="-s -w \
	-X $(MODULE)/internal/version.Version=$(VERSION) \
	-X $(MODULE)/internal/version.Commit=$(COMMIT) \
	-X $(MODULE)/internal/version.BuildDate=$(BUILD_DATE)"

# ─── Build ────────────────────────────────────────────────────────────────────
.PHONY: build
build:
	CGO_ENABLED=0 go build $(LDFLAGS) -o $(BINARY) ./cmd/...

.PHONY: build-linux
build-linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o $(BINARY)-linux ./cmd/...

# ─── Test ─────────────────────────────────────────────────────────────────────
.PHONY: test
test:
	go test -race -count=1 -timeout=5m \
		-coverprofile=coverage.out \
		-covermode=atomic \
		./...
	@go tool cover -func=coverage.out | grep total | awk '{print "Coverage: "$$3}'

.PHONY: test-integration
test-integration:
	go test -race -tags=integration -count=1 -timeout=10m ./test/...

.PHONY: test-short
test-short:
	go test -short -race -count=1 ./...

.PHONY: bench
bench:
	go test -run='^$$' -bench=. -benchmem -count=5 ./... | tee bench.txt

.PHONY: cover
cover: test
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

# ─── Code Quality ─────────────────────────────────────────────────────────────
.PHONY: vet
vet:
	go vet ./...

.PHONY: lint
lint:
	golangci-lint run --fix

.PHONY: lint-strict
lint-strict:
	golangci-lint run

.PHONY: vuln
vuln:
	govulncheck ./...

.PHONY: staticcheck
staticcheck:
	staticcheck ./...

# ─── Dependencies ─────────────────────────────────────────────────────────────
.PHONY: tidy
tidy:
	go mod tidy
	go mod verify

.PHONY: deps
deps:
	go mod download

# ─── Code Generation ──────────────────────────────────────────────────────────
.PHONY: generate
generate:
	go generate ./...

.PHONY: mocks
mocks:
	mockery

.PHONY: wire
wire:
	wire ./...

.PHONY: proto
proto:
	buf generate

.PHONY: proto-lint
proto-lint:
	buf lint

.PHONY: proto-breaking
proto-breaking:
	buf breaking --against '.git#branch=main'

# ─── Database ─────────────────────────────────────────────────────────────────
DATABASE_URL ?= postgres://dev:dev@localhost:5432/devdb?sslmode=disable

.PHONY: migrate-up
migrate-up:
	migrate -path ./migrations -database "$(DATABASE_URL)" up

.PHONY: migrate-down
migrate-down:
	migrate -path ./migrations -database "$(DATABASE_URL)" down 1

.PHONY: migrate-version
migrate-version:
	migrate -path ./migrations -database "$(DATABASE_URL)" version

.PHONY: migrate-create
migrate-create:
	@test -n "$(NAME)" || (echo "Usage: make migrate-create NAME=create_users"; exit 1)
	migrate create -ext sql -dir ./migrations -seq $(NAME)

# ─── Docker ───────────────────────────────────────────────────────────────────
IMAGE_NAME  ?= $(notdir $(CURDIR))
IMAGE_TAG   ?= $(VERSION)
REGISTRY    ?= ghcr.io/org

.PHONY: docker-build
docker-build:
	docker build \
		--build-arg VERSION=$(VERSION) \
		--build-arg COMMIT=$(COMMIT) \
		-t $(IMAGE_NAME):$(IMAGE_TAG) \
		-t $(IMAGE_NAME):latest \
		.

.PHONY: docker-push
docker-push:
	docker tag $(IMAGE_NAME):$(IMAGE_TAG) $(REGISTRY)/$(IMAGE_NAME):$(IMAGE_TAG)
	docker push $(REGISTRY)/$(IMAGE_NAME):$(IMAGE_TAG)

.PHONY: docker-up
docker-up:
	docker compose up -d

.PHONY: docker-down
docker-down:
	docker compose down

# ─── CI Pipeline ──────────────────────────────────────────────────────────────
.PHONY: ci
ci: tidy vet lint-strict vuln test

.PHONY: ci-full
ci-full: ci bench

# ─── Development ──────────────────────────────────────────────────────────────
.PHONY: run
run:
	go run ./cmd/server

.PHONY: run-dev
run-dev:
	APP_LOG_FORMAT=text APP_LOG_LEVEL=debug go run ./cmd/server

.PHONY: clean
clean:
	rm -rf bin/ coverage.out coverage.html bench.txt

# ─── Help ─────────────────────────────────────────────────────────────────────
.PHONY: help
help:
	@echo "Available targets:"
	@echo ""
	@echo "  Build:       build build-linux docker-build docker-push"
	@echo "  Test:        test test-integration test-short bench cover"
	@echo "  Quality:     vet lint lint-strict vuln staticcheck"
	@echo "  Deps:        tidy deps"
	@echo "  Generate:    generate mocks wire proto proto-lint"
	@echo "  Database:    migrate-up migrate-down migrate-version migrate-create"
	@echo "  Dev:         run run-dev docker-up docker-down"
	@echo "  CI:          ci ci-full"
	@echo ""
	@echo "  Variables:"
	@echo "    DATABASE_URL  $(DATABASE_URL)"
	@echo "    IMAGE_NAME    $(IMAGE_NAME)"
	@echo "    REGISTRY      $(REGISTRY)"

.DEFAULT_GOAL := help
