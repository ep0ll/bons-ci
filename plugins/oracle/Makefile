BINARY    := oci-migrator
CMD       := ./cmd/migrator
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
BUILD_TS  := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS   := -X main.Version=$(VERSION) -X main.BuildTime=$(BUILD_TS) -s -w

.PHONY: build
build:
	CGO_ENABLED=1 GOOS=linux GOARCH=amd64 \
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY) $(CMD)

.PHONY: build-race
build-race:
	CGO_ENABLED=1 GOOS=linux GOARCH=amd64 \
	go build -race -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY)-race $(CMD)

.PHONY: test
test:
	go test -race -timeout 120s -count=1 ./...

.PHONY: bench
bench:
	go test -bench=. -benchmem -benchtime=5s ./internal/criu/... ./internal/dirty/...

.PHONY: profile-cpu
profile-cpu:
	go test -bench=BenchmarkCheckpoint -benchmem -cpuprofile=cpu.prof \
	  -benchtime=30s ./internal/criu/...
	go tool pprof -http=:8080 cpu.prof

.PHONY: profile-mem
profile-mem:
	go test -bench=BenchmarkCheckpoint -benchmem -memprofile=mem.prof \
	  -benchtime=30s ./internal/criu/...
	go tool pprof -http=:8080 mem.prof

.PHONY: lint
lint:
	golangci-lint run --timeout=5m ./...

.PHONY: vet
vet:
	go vet ./...

.PHONY: docker
docker:
	docker build \
	  --build-arg VERSION=$(VERSION) \
	  --build-arg BUILD_TIME=$(BUILD_TS) \
	  -t oci-migrator:$(VERSION) \
	  -f deploy/Dockerfile .

.PHONY: install
install: build
	install -m 0755 bin/$(BINARY) /usr/local/bin/$(BINARY)
	install -m 0644 deploy/oci-migrator.service /etc/systemd/system/
	install -D -m 0644 deploy/config.yaml /etc/oci-migrator/config.yaml
	systemctl daemon-reload

.PHONY: install-criu
install-criu:
	apt-get update -qq
	apt-get install -y criu libprotobuf-dev open-iscsi e2fsprogs
	criu check --all

.PHONY: simulate-preemption
simulate-preemption:
	@echo "Sending preemption simulation signal to migrator daemon..."
	kill -USR1 $$(systemctl show -p MainPID --value oci-migrator)

.PHONY: clean
clean:
	rm -rf bin/ cpu.prof mem.prof

.PHONY: tidy
tidy:
	go mod tidy
	go mod verify

.DEFAULT_GOAL := build
