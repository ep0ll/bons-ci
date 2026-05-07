---
name: golang-deployment
description: >
  Go service deployment: Kubernetes manifests, Helm chart patterns, Docker multi-stage builds,
  docker-compose for local development, health probes, resource limits, secrets management,
  graceful shutdown under orchestration, and CI/CD pipeline patterns.
  Cross-references: docker-containerd/SKILL.md, observability/SKILL.md, configuration/SKILL.md.
---

# Go Deployment — Kubernetes, Helm, Docker

## 1. Production Dockerfile

```dockerfile
# ── Build stage ───────────────────────────────────────────────────
FROM golang:1.22-alpine AS builder

WORKDIR /build

# Cache dependency layer separately (faster rebuilds)
COPY go.mod go.sum ./
RUN go mod download && go mod verify

COPY . .

# Build: static binary, trimmed, version injected
ARG VERSION=dev
ARG COMMIT=unknown
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build \
    -trimpath \
    -ldflags="-s -w -extldflags=-static \
              -X main.version=${VERSION} \
              -X main.commit=${COMMIT} \
              -X main.buildDate=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    -o /app/server \
    ./cmd/server

# ── Final stage: distroless (no shell, no package manager) ────────
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /app/server /server

# Metadata
LABEL org.opencontainers.image.title="order-service"
LABEL org.opencontainers.image.source="https://github.com/org/order-service"

USER nonroot:nonroot
EXPOSE 8080 9090
ENTRYPOINT ["/server"]
```

## 2. Kubernetes Manifests

```yaml
# k8s/deployment.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: order-service
  labels:
    app: order-service
    version: "1.0.0"
spec:
  replicas: 3
  selector:
    matchLabels:
      app: order-service
  strategy:
    type: RollingUpdate
    rollingUpdate:
      maxSurge: 1
      maxUnavailable: 0   # zero-downtime rolling update
  template:
    metadata:
      labels:
        app: order-service
      annotations:
        prometheus.io/scrape: "true"
        prometheus.io/port: "9090"
        prometheus.io/path: "/metrics"
    spec:
      terminationGracePeriodSeconds: 60  # must be > server shutdown timeout
      containers:
        - name: order-service
          image: ghcr.io/org/order-service:1.0.0
          imagePullPolicy: IfNotPresent
          ports:
            - name: http
              containerPort: 8080
            - name: metrics
              containerPort: 9090

          # All config from env (12-factor)
          env:
            - name: APP_SERVER_ADDR
              value: ":8080"
            - name: APP_LOG_LEVEL
              value: "info"
            - name: APP_LOG_FORMAT
              value: "json"
            - name: APP_DATABASE_DSN
              valueFrom:
                secretKeyRef:
                  name: order-service-secrets
                  key: database-dsn
            - name: GOMAXPROCS
              valueFrom:
                resourceFieldRef:
                  resource: limits.cpu
            - name: GOMEMLIMIT
              valueFrom:
                resourceFieldRef:
                  resource: limits.memory

          # Resource limits — always set both requests and limits
          resources:
            requests:
              cpu: "100m"
              memory: "128Mi"
            limits:
              cpu: "500m"
              memory: "512Mi"

          # Probes — liveness separate from readiness
          livenessProbe:
            httpGet:
              path: /healthz
              port: http
            initialDelaySeconds: 10
            periodSeconds: 10
            timeoutSeconds: 3
            failureThreshold: 3

          readinessProbe:
            httpGet:
              path: /readyz
              port: http
            initialDelaySeconds: 5
            periodSeconds: 5
            timeoutSeconds: 3
            failureThreshold: 2
            successThreshold: 1

          # Startup probe — gives slow-starting app time without k8s killing it
          startupProbe:
            httpGet:
              path: /healthz
              port: http
            failureThreshold: 30    # 30 × 5s = 150s max startup time
            periodSeconds: 5

          # Graceful shutdown
          lifecycle:
            preStop:
              exec:
                command: ["/bin/sleep", "5"]  # give time for load balancer drain

          # Security context
          securityContext:
            runAsNonRoot: true
            runAsUser: 65532
            readOnlyRootFilesystem: true
            allowPrivilegeEscalation: false
            capabilities:
              drop: ["ALL"]

      # Pod anti-affinity — spread across nodes
      affinity:
        podAntiAffinity:
          preferredDuringSchedulingIgnoredDuringExecution:
            - weight: 100
              podAffinityTerm:
                labelSelector:
                  matchExpressions:
                    - key: app
                      operator: In
                      values: [order-service]
                topologyKey: kubernetes.io/hostname
```

## 3. Graceful Shutdown Under K8s

```go
// K8s sends SIGTERM → preStop sleep → SIGTERM to process → terminationGracePeriod
// Your app must:
// 1. Receive SIGTERM
// 2. Stop accepting new requests (readiness probe fails)
// 3. Drain in-flight requests
// 4. Flush telemetry
// 5. Exit cleanly

func run(ctx context.Context, cfg *Config) error {
    // Setup
    shutdown, err := telemetry.Init(ctx, cfg.ServiceName)
    if err != nil { return err }

    srv := newHTTPServer(cfg)

    // Signal handling
    ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
    defer stop()

    var eg errgroup.Group
    eg.Go(func() error { return srv.ListenAndServe() })

    <-ctx.Done()
    slog.Info("shutdown signal received")

    // Graceful shutdown with timeout from config
    shutCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
    defer cancel()

    if err := srv.Shutdown(shutCtx); err != nil {
        slog.Error("server shutdown error", "err", err)
    }

    // Flush telemetry AFTER server stops accepting requests
    if err := shutdown(shutCtx); err != nil {
        slog.Error("telemetry flush error", "err", err)
    }

    return eg.Wait()
}
```

## 4. docker-compose (Local Development)

```yaml
# docker-compose.yaml
version: "3.9"
services:
  app:
    build:
      context: .
      target: builder           # use builder stage for dev (faster rebuild)
    command: go run ./cmd/server
    ports:
      - "8080:8080"
      - "9090:9090"
    volumes:
      - .:/build                # live code reload
    environment:
      APP_DATABASE_DSN: "postgres://dev:dev@postgres:5432/devdb?sslmode=disable"
      APP_REDIS_URL: "redis://redis:6379"
      APP_LOG_LEVEL: "debug"
      APP_LOG_FORMAT: "text"
    depends_on:
      postgres: { condition: service_healthy }
      redis:    { condition: service_healthy }

  postgres:
    image: postgres:16-alpine
    environment:
      POSTGRES_USER: dev
      POSTGRES_PASSWORD: dev
      POSTGRES_DB: devdb
    ports: ["5432:5432"]
    volumes:
      - postgres_data:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U dev -d devdb"]
      interval: 5s
      timeout: 3s
      retries: 5

  redis:
    image: redis:7-alpine
    ports: ["6379:6379"]
    healthcheck:
      test: ["CMD", "redis-cli", "ping"]
      interval: 5s
      timeout: 3s
      retries: 5

volumes:
  postgres_data:
```

## 5. CI/CD Pipeline (GitHub Actions)

```yaml
# .github/workflows/ci.yaml
name: CI
on: [push, pull_request]

jobs:
  test:
    runs-on: ubuntu-latest
    services:
      postgres:
        image: postgres:16-alpine
        env:
          POSTGRES_USER: test
          POSTGRES_PASSWORD: test
          POSTGRES_DB: testdb
        options: >-
          --health-cmd pg_isready
          --health-interval 5s
          --health-timeout 3s
          --health-retries 5
        ports: ["5432:5432"]

    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version: "1.22" }

      - name: Cache Go modules
        uses: actions/cache@v4
        with:
          path: ~/go/pkg/mod
          key: ${{ runner.os }}-go-${{ hashFiles('**/go.sum') }}

      - run: go mod verify
      - run: go vet ./...
      - run: golangci-lint run
      - run: govulncheck ./...
      - run: go test -race -count=1 -timeout=10m -coverprofile=coverage.out ./...
        env:
          TEST_DATABASE_URL: "postgres://test:test@localhost:5432/testdb?sslmode=disable"

      - name: Build
        run: |
          make build
          docker build --build-arg VERSION=${{ github.sha }} -t order-service:${{ github.sha }} .
```

## Deployment Checklist
- [ ] Distroless or scratch final image — no shell, minimal attack surface
- [ ] `terminationGracePeriodSeconds` > `ShutdownTimeout` + preStop sleep
- [ ] `preStop` sleep gives load balancer time to drain connections
- [ ] `GOMAXPROCS` set from cgroup limit (automaxprocs or env var)
- [ ] `GOMEMLIMIT` set to ~80% of memory limit
- [ ] `readinessProbe` fails before SIGTERM handling starts
- [ ] Resource `requests` and `limits` both set (never only one)
- [ ] `readOnlyRootFilesystem: true` in securityContext
- [ ] Secrets from Kubernetes Secrets — never in ConfigMaps or env literals
- [ ] PodAntiAffinity spreads replicas across nodes
- [ ] Health endpoints: `/healthz` (liveness, cheap) and `/readyz` (readiness, checks deps)
