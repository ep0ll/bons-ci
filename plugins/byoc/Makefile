.PHONY: all build test test-unit test-integration lint fmt docker-build docker-push \
        infra-init infra-plan infra-apply infra-destroy dev clean help

BINARY      := byoc-server
IMAGE_NAME  := byoc-oci-runners
IMAGE_TAG   ?= latest
REGISTRY    ?= us-ashburn-1.ocir.io/your-namespace
TF_ENV      ?= dev

# ── Build ─────────────────────────────────────────────────────────────────────

all: lint test build

## build: Compile the control-plane binary.
build:
	CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/$(BINARY) ./cmd/server

## run: Run the server locally (requires MySQL via docker-compose).
run:
	BYOC_LOG_PRETTY=true BYOC_LOG_LEVEL=debug go run ./cmd/server

# ── Quality ───────────────────────────────────────────────────────────────────

## test: Run all unit tests with race detector.
test: test-unit

## test-unit: Run unit tests only (no external dependencies).
test-unit:
	go test -race -count=1 -timeout=60s ./tests/unit/...

## test-integration: Run integration tests (requires MySQL; set TEST_DB_DSN).
test-integration:
	go test -race -tags=integration -count=1 -timeout=120s ./tests/integration/...

## test-e2e: Run end-to-end tests (requires OCI + GitHub credentials).
test-e2e:
	go test -race -tags=e2e -count=1 -timeout=300s ./tests/e2e/...

## test-cover: Run unit tests and open HTML coverage report.
test-cover:
	go test -race -coverprofile=coverage.out ./internal/...
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

## lint: Run golangci-lint (install from https://golangci-lint.run).
lint:
	golangci-lint run ./...

## fmt: Format all Go source files.
fmt:
	gofmt -w -s .
	goimports -w .

## vet: Run go vet.
vet:
	go vet ./...

# ── Docker ────────────────────────────────────────────────────────────────────

## docker-build: Build the production Docker image.
docker-build:
	docker build -t $(IMAGE_NAME):$(IMAGE_TAG) .

## docker-push: Push to OCI Container Registry.
docker-push: docker-build
	docker tag $(IMAGE_NAME):$(IMAGE_TAG) $(REGISTRY)/$(IMAGE_NAME):$(IMAGE_TAG)
	docker push $(REGISTRY)/$(IMAGE_NAME):$(IMAGE_TAG)

# ── Local Dev ─────────────────────────────────────────────────────────────────

## dev: Start all local dependencies via Docker Compose.
dev:
	docker compose up -d mysql prometheus
	@echo "MySQL ready on :3306 | Prometheus on :9090"
	@echo "Run 'make run' to start the server."

## dev-down: Stop all local dependencies.
dev-down:
	docker compose down

# ── Terraform ─────────────────────────────────────────────────────────────────

TF_DIR := infra/terraform/environments/$(TF_ENV)

## infra-init: Initialise Terraform for the target environment.
infra-init:
	terraform -chdir=$(TF_DIR) init

## infra-plan: Preview Terraform changes.
infra-plan:
	terraform -chdir=$(TF_DIR) plan -out=plan.out

## infra-apply: Apply Terraform changes.
infra-apply:
	terraform -chdir=$(TF_DIR) apply plan.out

## infra-destroy: Destroy all Terraform-managed resources (DANGEROUS).
infra-destroy:
	terraform -chdir=$(TF_DIR) destroy

## infra-validate: Validate all Terraform modules.
infra-validate:
	find infra/terraform/modules -name "*.tf" -exec dirname {} \; | sort -u | \
	  xargs -I{} terraform -chdir={} validate

## infra-fmt: Format all Terraform files.
infra-fmt:
	terraform fmt -recursive infra/terraform/

# ── Utilities ─────────────────────────────────────────────────────────────────

## clean: Remove build artifacts.
clean:
	rm -rf bin/ coverage.out coverage.html

## tidy: Run go mod tidy.
tidy:
	go mod tidy

## help: Show this help.
help:
	@grep -E '^## ' Makefile | sed 's/## /  /'
