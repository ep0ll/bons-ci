# signing-service

Production-ready, event-driven container image signing service built on the
[Sigstore](https://www.sigstore.dev) ecosystem (Cosign, Fulcio, Rekor).

---

## Architecture

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                          signing-service process                             │
│                                                                              │
│  HTTP :8080          Metrics :9090                                           │
│  ┌──────────┐        ┌─────────┐                                            │
│  │ /livez   │        │/metrics │◄── Prometheus scrape                       │
│  │ /readyz  │        └─────────┘                                            │
│  └────┬─────┘                                                               │
│       │  Publish(SigningRequestedEvent)                                      │
│       ▼                                                                      │
│  ┌──────────────────────────────────────────────────────────────────┐       │
│  │                        EventBus (MemoryBus)                       │       │
│  │                                                                    │       │
│  │  topic: signing.requested  ─────────────────────────────────────┐│       │
│  │  topic: signing.started    ◄────────────────────────────────────┤│       │
│  │  topic: signing.succeeded  ◄────────────────────────────────────┤│       │
│  │  topic: signing.failed     ◄────────────────────────────────────┤│       │
│  │  topic: signing.duplicate  ◄────────────────────────────────────┤│       │
│  │  topic: signing.dead_letter◄────────────────────────────────────┘│       │
│  │                                                                    │       │
│  │  Each topic: buffered channel(N) + worker pool(M)                │       │
│  └──────────────────────┬───────────────────────────────────────────┘       │
│                         │ fan-out to subscribers                             │
│                         ▼                                                    │
│  ┌──────────────────────────────────────────────────────────────────┐       │
│  │                      SigningService                               │       │
│  │                                                                    │       │
│  │  1. Idempotency gate ─── IdempotencyStore.TryClaim()             │       │
│  │        │ duplicate? ──────────────────────► publish duplicate     │       │
│  │        │ claimed                                                  │       │
│  │        ▼                                                          │       │
│  │  2. publish signing.started                                       │       │
│  │        │                                                          │       │
│  │        ▼                                                          │       │
│  │  3. ResiliencePolicy.Execute()                                    │       │
│  │     ┌──────────────────────────────────┐                         │       │
│  │     │ CircuitBreaker (outer)           │                         │       │
│  │     │   └── RetryPolicy (inner)        │                         │       │
│  │     │         └── Signer.Sign()        │                         │       │
│  │     └──────────────────────────────────┘                         │       │
│  │        │ error? ──────────────────────► MarkFailed + publish fail │       │
│  │        │ success                                                  │       │
│  │        ▼                                                          │       │
│  │  4. MarkSucceeded + publish signing.succeeded                     │       │
│  └──────────────────────────────────────────────────────────────────┘       │
│                                                                              │
│  Signer implementations (swap at bootstrap — zero code changes):            │
│  ┌────────────────────────────────────────────────────────────────┐         │
│  │  KeylessSigner          StaticKeySigner         KMSSigner      │         │
│  │  ┌───────────┐          ┌──────────────┐        ┌──────────┐  │         │
│  │  │1. Ephemeral│         │1. KeyProvider│        │1. KMS    │  │         │
│  │  │   keygen  │         │   .GetSigner │        │   handle │  │         │
│  │  │2. OIDC    │         │2. Sign digest│        │2. Sign   │  │         │
│  │  │   token   │         │3. Rekor log  │        │3. Rekor  │  │         │
│  │  │3. Fulcio  │         └──────────────┘        └──────────┘  │         │
│  │  │   cert    │                                                 │         │
│  │  │4. Sign    │         KeyProvider implementations:           │         │
│  │  │5. Rekor   │         ┌────────────────────────────────────┐ │         │
│  │  └───────────┘         │ StaticKeyProvider  KMSKeyProvider  │ │         │
│  └────────────────────────┴────────────────────────────────────┴─┘         │
│                                                                              │
│  External broker extension (no core changes):                               │
│  MemoryBus ─────► KafkaBus ─────► NATSBus ─────► PubSubBus                │
│  (pkg/transport/brokers.go — implement EventBus, swap in bootstrap/wire.go)│
└─────────────────────────────────────────────────────────────────────────────┘

Signal flow for one image signing request:
═══════════════════════════════════════════
API Call ──► SigningRequestedEvent
         ──► EventBus.Publish(signing.requested)
         ──► SigningService.handleSigningRequested
               ├─ IdempotencyStore.TryClaim()    ← duplicate? → signing.duplicate
               ├─ EventBus.Publish(signing.started)
               ├─ ResiliencePolicy.Execute()
               │    ├─ CircuitBreaker.allow()    ← open? → ErrCircuitOpen
               │    └─ RetryPolicy.Execute()
               │         └─ Signer.Sign()
               │              ├─ Keyless: ephemeral key → Fulcio cert → Rekor
               │              └─ Static/KMS: KeyProvider → sign → Rekor
               ├─ (success) → IdempotencyStore.MarkSucceeded
               │            → EventBus.Publish(signing.succeeded)
               └─ (failure) → IdempotencyStore.MarkFailed
                            → EventBus.Publish(signing.failed)
                            → (exhausted) → signing.dead_letter
```

---

## Package Structure

```
signing-service/
├── cmd/
│   └── signing-service/
│       └── main.go             # OS signal handling, graceful shutdown
├── internal/
│   ├── bootstrap/
│   │   └── wire.go             # dependency wiring — only place knowing concrete types
│   ├── config/
│   │   └── config.go           # YAML load, env overrides, strict validation
│   ├── domain/
│   │   ├── events.go           # immutable domain events
│   │   └── models.go           # value objects
│   ├── eventbus/
│   │   ├── bus.go              # EventBus, Publisher, Subscriber interfaces
│   │   └── memory.go           # MemoryBus: per-topic channels + worker pools
│   ├── health/
│   │   └── handler.go          # /livez /readyz /healthz handlers
│   ├── idempotency/
│   │   └── store.go            # IdempotencyStore interface + MemoryStore
│   ├── keyprovider/
│   │   └── provider.go         # KeyProvider interface + Static + KMS
│   ├── observability/
│   │   └── metrics.go          # Prometheus, slog, OTel SDK wiring
│   ├── resilience/
│   │   └── policy.go           # ResiliencePolicy, RetryPolicy, CircuitBreaker
│   └── service/
│       └── signing_service.go  # orchestrator: events → idempotency → sign → events
└── pkg/
    └── transport/
        └── brokers.go          # Kafka/NATS/PubSub extension skeletons
```

---

## Quick Start

```bash
# Local development (static key backend)
export SIGNING_BACKEND=static_key
export SIGNING_KEY_PATH=/tmp/test-key.pem
export CONFIG_PATH=config.yaml

# Generate a test key
openssl ecparam -name prime256v1 -genkey -noout | \
  openssl pkcs8 -topk8 -nocrypt -out /tmp/test-key.pem

go run ./cmd/signing-service

# Health check
curl http://localhost:8080/readyz

# Metrics
curl http://localhost:9090/metrics | grep signing_service
```

---

## Backend Swap Guide

All backends are swapped by changing **one env var** and **zero application code**.

### Signing backend

| Backend       | Set env var                  | Key material source          |
|---------------|------------------------------|------------------------------|
| `keyless`     | `SIGNING_BACKEND=keyless`    | Ephemeral (Fulcio OIDC flow) |
| `static_key`  | `SIGNING_BACKEND=static_key` | PEM file at `SIGNING_KEY_PATH` |
| `kms`         | `SIGNING_BACKEND=kms`        | `KMS_PATH` (gcpkms/awskms)  |

**To add a new signing backend:**
1. Implement `signing.Signer` in `internal/signing/`
2. Add a case in `bootstrap.buildSigner()` — one `switch` branch
3. No other changes

### Event bus backend

The in-memory bus is the default and requires no external infrastructure.

**To add Kafka/NATS/PubSub:**
1. Complete the TODO sections in `pkg/transport/brokers.go`
2. In `bootstrap/wire.go`, replace:
   ```go
   bus := eventbus.NewMemoryBus(...)
   ```
   with:
   ```go
   bus, err := transport.NewKafkaBus(transport.KafkaBusConfig{
       Brokers: strings.Split(os.Getenv("KAFKA_BROKERS"), ","),
       GroupID: "signing-service",
   })
   ```
3. No changes to `SigningService`, domain events, or any other package

### Key provider backend

**To add HashiCorp Vault:**
1. Implement `keyprovider.KeyProvider` using the Vault SDK
2. Add a case in `bootstrap.buildKeyProvider()`

---

## Production Deployment Checklist

### Security
- [ ] Container runs as UID 65532 (nonroot); `runAsNonRoot: true` enforced
- [ ] `readOnlyRootFilesystem: true`; no writable paths needed
- [ ] All capabilities dropped (`capabilities.drop: ["ALL"]`)
- [ ] Seccomp profile: RuntimeDefault or stricter custom profile
- [ ] Image digest pinned in Deployment (never `:latest`)
- [ ] Image signed with this service and verified by admission controller (Kyverno/OPA Gatekeeper)
- [ ] Keyless: Workload Identity configured (GKE SA annotation or IRSA)
- [ ] Static key: mounted from Secret with `defaultMode: 0400`, never in env vars
- [ ] KMS: service account has `cloudkms.cryptoKeyVersions.useToSign` only (least-privilege)
- [ ] NetworkPolicy: egress allow-list to Fulcio, Rekor, registry; deny all ingress except LB
- [ ] RBAC: ServiceAccount has no cluster-wide permissions

### Reliability
- [ ] `minAvailable: 2` in PodDisruptionBudget
- [ ] `terminationGracePeriodSeconds ≥ server.shutdown_timeout`
- [ ] `maxUnavailable: 0` in rolling update strategy
- [ ] `topologySpreadConstraints` across zones (HA)
- [ ] Resource requests and limits tuned from load-test results
- [ ] Circuit breaker thresholds calibrated against Fulcio/Rekor SLA

### Observability
- [ ] `signing_service_signing_duration_seconds` — alert on p99 > SLO
- [ ] `signing_service_signing_total{outcome="failed"}` — alert on error rate > threshold
- [ ] `signing_service_circuit_breaker_state{state="open"}` — alert immediately
- [ ] `signing_service_events_dropped_total` — alert on non-zero (backpressure breach)
- [ ] Distributed traces flowing to OTel collector via `OTLP_ENDPOINT`
- [ ] Structured JSON logs shipped to Datadog/CloudWatch/Loki
- [ ] `/readyz` integrated with load balancer health checks

### CI/CD Supply-Chain
- [ ] `go vet ./...` in CI (zero warnings enforced)
- [ ] `golangci-lint run` with `errcheck`, `staticcheck`, `gosec`, `gocritic` enabled
- [ ] `govulncheck ./...` — fail on high-severity CVEs in dependencies
- [ ] `go mod verify` before build to detect tampered modules
- [ ] Multi-stage Dockerfile: tests run in build stage; failed tests abort image build
- [ ] SBOM generated at build time (e.g. with `syft`) and attested to Rekor
- [ ] The service image itself is signed by this service (bootstrapping complete loop)
- [ ] Admission webhook verifies all images in cluster are signed before scheduling

### Scaling
- [ ] HPA configured on `signing_service_signing_duration_seconds` (latency-based) or CPU
- [ ] `event_bus.buffer_size` tuned: expected_rps × max_handler_latency_seconds × 1.5 safety factor
- [ ] `workers_per_topic` tuned from profiling: I/O-bound → increase; CPU-bound → match GOMAXPROCS
- [ ] For multi-replica: replace MemoryIdempotencyStore with Redis (atomic SETNX)
- [ ] For multi-replica: replace MemoryBus with Kafka/NATS JetStream for cross-replica fan-out
- [ ] Connection pool sizing for Fulcio/Rekor HTTP clients (default: Go's DefaultTransport = 100)

---

## Interface Contracts Summary

```go
// Core interfaces — implement these to extend the service:

type EventBus interface {
    Publish(ctx context.Context, env domain.Envelope) error
    Subscribe(topic domain.EventType, h Handler) (Subscription, error)
    Close() error
}

type Signer interface {
    Sign(ctx context.Context, req SignRequest) (domain.SigningResult, error)
}

type KeyProvider interface {
    GetSigner(ctx context.Context, spec domain.KeySpec) (crypto.Signer, error)
    PublicKey(ctx context.Context, spec domain.KeySpec) (crypto.PublicKey, error)
}

type ResiliencePolicy interface {
    Execute(ctx context.Context, fn func(ctx context.Context) error) error
}

type IdempotencyStore interface {
    TryClaim(ctx context.Context, key string, ttl time.Duration) (bool, error)
    MarkSucceeded(ctx context.Context, key, resultRef string) error
    MarkFailed(ctx context.Context, key string, reason string) error
    Get(ctx context.Context, key string) (Record, error)
}
```

Every interface is satisfied by a stub/mock in tests — no Sigstore network calls
needed for unit testing.
