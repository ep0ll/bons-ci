# fanwatch — Makefile
# All targets use -mod=vendor so no network access is required after `make vendor`.

GOFLAGS := -mod=vendor
GOTEST  := go test $(GOFLAGS) -race -count=1
GOBUILD := go build $(GOFLAGS)
GOVET   := go vet $(GOFLAGS)

.PHONY: all build test test-race test-repeat vet clean vendor help

## all: build + vet + test (default target)
all: build vet test

## build: compile all packages
build:
	$(GOBUILD) ./...

## test: run all tests with race detector
test:
	$(GOTEST) ./...

## test-race: alias for test (race is always enabled)
test-race: test

## test-repeat: run all tests 5 times to catch flaky behaviour
test-repeat:
	go test $(GOFLAGS) -race -count=5 ./...

## vet: run go vet
vet:
	$(GOVET) ./...

## clean: remove build artefacts
clean:
	go clean ./...

## vendor: tidy and sync the vendor directory
vendor:
	GOPROXY="file://$${GOPATH}/pkg/mod/cache/download,direct" \
	GONOSUMDB="*" \
	go mod tidy
	GOFLAGS="-mod=mod" go mod vendor

## help: list all targets with descriptions
help:
	@grep -E '^## ' Makefile | sed 's/## //' | column -t -s ':'
