---
name: byoc-oci-github-runners
description: >
  Build a production-grade BYOC (Bring Your Own Cloud) SaaS platform that provisions and manages
  self-hosted GitHub Actions runners on Oracle Cloud Infrastructure (OCI). Use this skill whenever
  the user asks to design, implement, extend, or debug any component of a multi-tenant GitHub
  Actions runner platform on OCI — including control-plane services (Go), autoscaling logic,
  GitHub App integration, Terraform infrastructure, observability, or tenant lifecycle management.
  Trigger even when the user only mentions one part (e.g., "how do I autoscale runners", "write
  the webhook handler", "create the Terraform VCN module") — the skill provides the architectural
  context needed to keep every piece consistent with the whole.
---

# BYOC OCI GitHub Actions Runner SaaS — Build Skill

This skill encodes the architecture decisions, coding standards, module contracts, and
implementation patterns for the BYOC runner platform. **Read this skill completely before
writing a single line of code.** Then implement strictly in accordance with it.

---

## 1. Project Anatomy

```
byoc-oci-runners/
├── cmd/
│   └── server/          # main.go — wire everything together
├── internal/
│   ├── api/             # HTTP layer only; no business logic
│   │   ├── handler/     # one file per resource group
│   │   ├── middleware/  # auth, logging, tracing, recovery
│   │   └── router.go
│   ├── orchestrator/    # job queue + dispatch decisions
│   ├── runner/          # runner FSM + lifecycle operations
│   ├── provisioner/     # OCI resource creation (pluggable)
│   │   ├── interface.go
│   │   ├── compute/     # OCI Compute VM provisioner
│   │   └── container/   # OCI Container Instance provisioner
│   ├── tenant/          # tenant CRUD + config validation
│   ├── github/          # GitHub App auth + webhook parsing
│   ├── scaler/          # autoscaling algorithm + rate limiter
│   ├── reconciler/      # background leak detection
│   ├── vault/           # OCI Vault secret fetch + cache
│   ├── store/           # database models + repository pattern
│   │   ├── interface.go
│   │   └── mysql/
│   └── observability/   # zerolog setup, OTel, Prometheus
├── infra/
│   └── terraform/
│       ├── modules/
│       │   ├── network/
│       │   ├── compute/
│       │   ├── iam/
│       │   ├── database/
│       │   ├── vault/
│       │   └── monitoring/
│       ├── environments/
│       │   ├── dev/
│       │   └── prod/
│       └── README.md
├── docs/
│   ├── architecture.md
│   ├── setup.md
│   ├── operations.md
│   └── tenant-onboarding.md
├── tests/
│   ├── unit/            # mirrors internal/ structure
│   ├── integration/     # requires live OCI + GitHub creds
│   └── e2e/             # full round-trip tests
├── docker-compose.yml   # local dev with mock services
├── .github/workflows/ci.yml
├── go.mod
├── go.sum
└── Makefile
```

---

## 2. Language & Framework Decisions

| Concern | Choice | Rationale |
|---|---|---|
| Language | Go 1.22+ | Native concurrency, fast startup, single binary |
| HTTP framework | `github.com/gin-gonic/gin` | Minimal, testable, good middleware ecosystem |
| Database ORM | `gorm.io/gorm` + MySQL driver | Structured queries, easy migrations |
| Logging | `github.com/rs/zerolog` | Zero-alloc structured JSON |
| Metrics | `github.com/prometheus/client_golang` | Standard; scraped by OCI Monitoring |
| Tracing | `go.opentelemetry.io/otel` | Vendor-neutral; export to OCI APM |
| Config | `github.com/spf13/viper` | Env + file, hot-reload capable |
| Testing | stdlib `testing` + `testify` + `gomock` | No heavy frameworks |
| IaC | Terraform 1.7+ | Reproducible, OCI provider mature |
| OCI SDK | `github.com/oracle/oci-go-sdk/v65` | Official |

---

## 3. Architecture Principles (NON-NEGOTIABLE)

### 3.1 Hexagonal / Ports-and-Adapters

Every domain package defines its own **port interfaces** in `interface.go`.
Concrete adapters live in sub-packages. The domain never imports adapters.

```
internal/provisioner/interface.go  ← port (interface)
internal/provisioner/compute/      ← OCI Compute adapter
internal/provisioner/container/    ← OCI Container Instance adapter
```

### 3.2 Dependency Injection (Constructor Injection)

No global state except the logger and tracer provider.
Every struct receives its dependencies via constructor:

```go
// CORRECT
func NewOrchestrator(store store.Store, provisioner provisioner.Provisioner,
    scaler scaler.Scaler, logger zerolog.Logger) *Orchestrator { ... }

// FORBIDDEN
var globalStore *mysql.Store // ← never
```

### 3.3 Context Propagation

Every function that does I/O must accept `context.Context` as its first parameter.
Derived contexts carry tenant_id, trace_id via `context.WithValue` using typed keys (not strings).

```go
type contextKey string
const TenantIDKey contextKey = "tenant_id"
```

### 3.4 Error Handling

- Never discard errors silently.
- Wrap errors with context: `fmt.Errorf("provision runner: %w", err)`.
- Define sentinel errors in each package: `var ErrRunnerNotFound = errors.New("runner not found")`.
- HTTP handlers translate domain errors to HTTP status via an explicit error map — not `switch err.Error()`.

### 3.5 Concurrency Safety

- Use `sync.Mutex` or `sync/atomic` for shared mutable state; document which fields are guarded.
- Channel-based work queues with explicit buffer sizes and documented backpressure behaviour.
- Never call `goroutine` without a corresponding lifecycle (context cancellation or WaitGroup).

### 3.6 Idempotency

GitHub sends duplicate webhook events. The orchestrator must deduplicate on `workflow_job.id`
using an idempotency table in MySQL with a unique index. Duplicate processing returns 200 OK
immediately (do not re-provision).

---

## 4. Key Domain Models

```go
// tenant/model.go
type Tenant struct {
    ID            string
    Name          string
    GitHubAppID   int64
    GitHubOrgName string
    OCICompartmentID string
    MaxRunners    int
    MinWarmPool   int
    IdleTimeoutSec int
    Labels        []string
    CreatedAt     time.Time
    Status        TenantStatus // active | suspended | offboarding
}

// runner/model.go
type Runner struct {
    ID         string
    TenantID   string
    JobID      int64   // GitHub workflow_job.id; 0 if warm-pool
    OCIInstanceID string
    GitHubRunnerID int64
    Status     RunnerStatus // provisioning | registering | idle | busy | terminating | terminated
    Labels     []string
    ProvisionedAt time.Time
    IdleSince  *time.Time
}

type RunnerStatus string
const (
    StatusProvisioning RunnerStatus = "provisioning"
    StatusRegistering  RunnerStatus = "registering"
    StatusIdle         RunnerStatus = "idle"
    StatusBusy         RunnerStatus = "busy"
    StatusTerminating  RunnerStatus = "terminating"
    StatusTerminated   RunnerStatus = "terminated"
)
```

---

## 5. Runner Finite State Machine

```
[queued event]
      │
      ▼
 Provisioning ──(OCI instance up)──▶ Registering ──(GitHub ACK)──▶ Idle
      │                                   │                           │
 (OCI error)                       (GitHub error)               (job picked up)
      │                                   │                           │
      ▼                                   ▼                           ▼
 Terminated ◀──────────────────── Terminated                       Busy
                                                                      │
                                                          (job done / idle_timeout)
                                                                      │
                                                                      ▼
                                                                 Terminating
                                                                      │
                                                          (OCI terminated + deregistered)
                                                                      │
                                                                      ▼
                                                                 Terminated
```

State transitions must be validated; illegal transitions return `ErrInvalidTransition`.

---

## 6. Autoscaler Algorithm

```
on workflow_job.queued(tenant_id, job_id):
  1. Acquire idempotency lock for job_id (MySQL upsert)
  2. If lock already existed → return (duplicate)
  3. Check tenant.Status == active
  4. idle_runners = count runners where tenant=T and status=idle
  5. If idle_runners > 0 → assign first idle runner to job → return
  6. active_count = count runners where tenant=T and status IN (provisioning,registering,idle,busy)
  7. If active_count >= tenant.MaxRunners → enqueue job to tenant wait queue → return
  8. Apply rate limiter (token bucket, 10 provisions/min per tenant default)
  9. Dispatch ProvisionRunner(tenant, job_id, labels) → async
 10. Record metric: job_queue_depth, provision_latency_start

on workflow_job.completed(tenant_id, runner_id):
  1. Transition runner → Terminating
  2. Deregister from GitHub (best-effort, timeout 10s)
  3. Terminate OCI instance
  4. Transition runner → Terminated
  5. Check tenant wait queue; if non-empty → dequeue + goto step 9 above
```

---

## 7. GitHub Integration Contract

File: `internal/github/interface.go`

```go
type GitHubClient interface {
    // CreateRegistrationToken returns a short-lived token for runner registration.
    // It fetches a fresh installation access token from OCI Vault each call.
    CreateRegistrationToken(ctx context.Context, tenantID string) (token string, expiresAt time.Time, err error)

    // RemoveRunner de-registers a runner from the GitHub org/repo.
    RemoveRunner(ctx context.Context, tenantID string, runnerID int64) error

    // ValidateWebhookSignature validates the HMAC-SHA256 signature header.
    ValidateWebhookSignature(payload []byte, sigHeader string, secret string) error

    // ParseWorkflowJobEvent parses a workflow_job webhook payload.
    ParseWorkflowJobEvent(payload []byte) (*WorkflowJobEvent, error)
}
```

Token lifecycle:
- GitHub App JWT is generated in-process from the App's private key (fetched from OCI Vault).
- Installation access token is fetched per registration; **never cached longer than 55 minutes**.
- Private key PEM is cached in memory with a 5-minute TTL from OCI Vault.

---

## 8. Provisioner Interface

File: `internal/provisioner/interface.go`

```go
type Provisioner interface {
    // Provision creates a new runner instance and returns its OCI resource ID.
    // It must be idempotent: calling with the same RunnerSpec returns the same resource.
    Provision(ctx context.Context, spec RunnerSpec) (ociInstanceID string, err error)

    // Terminate destroys the OCI resource. Idempotent on already-terminated resources.
    Terminate(ctx context.Context, ociInstanceID string) error

    // Describe returns current OCI resource state (for reconciler).
    Describe(ctx context.Context, ociInstanceID string) (*InstanceState, error)
}

type RunnerSpec struct {
    TenantID      string
    RunnerID      string  // used as OCI display name + runner name on GitHub
    CompartmentID string
    SubnetID      string
    Shape         string  // e.g. "VM.Standard.E4.Flex"
    OCPUs         float32
    MemoryGB      float32
    ImageID       string  // OCI image OCID with runner pre-installed
    UserData      string  // cloud-init script (base64)
    Labels        []string
    Tags          map[string]string // OCI freeform tags
}
```

---

## 9. Store / Repository Interface

File: `internal/store/interface.go`

```go
type Store interface {
    // Tenants
    CreateTenant(ctx context.Context, t *tenant.Tenant) error
    GetTenant(ctx context.Context, id string) (*tenant.Tenant, error)
    UpdateTenant(ctx context.Context, t *tenant.Tenant) error
    ListTenants(ctx context.Context, filter TenantFilter) ([]*tenant.Tenant, error)

    // Runners
    CreateRunner(ctx context.Context, r *runner.Runner) error
    GetRunner(ctx context.Context, id string) (*runner.Runner, error)
    UpdateRunnerStatus(ctx context.Context, id string, status runner.RunnerStatus, opts ...RunnerUpdateOpt) error
    ListRunners(ctx context.Context, filter RunnerFilter) ([]*runner.Runner, error)
    DeleteRunner(ctx context.Context, id string) error

    // Idempotency
    AcquireIdempotencyLock(ctx context.Context, key string, ttl time.Duration) (acquired bool, err error)
}
```

---

## 10. Observability Standards

### Structured Log Fields (REQUIRED on every log line in business logic)

```go
log.Info().
    Str("tenant_id", tenantID).
    Str("runner_id", runnerID).
    Int64("job_id", jobID).
    Str("trace_id", traceID).  // extracted from ctx
    Str("action", "provision_runner").
    Msg("runner provisioned successfully")
```

### Prometheus Metrics (register in `observability/metrics.go`)

```go
var (
    RunnersActive = promauto.NewGaugeVec(prometheus.GaugeOpts{
        Name: "byoc_runners_active_total",
        Help: "Number of active runners (provisioning+registering+idle+busy)",
    }, []string{"tenant_id"})

    ProvisionLatency = promauto.NewHistogramVec(prometheus.HistogramOpts{
        Name:    "byoc_provision_latency_seconds",
        Help:    "Time from webhook received to runner registered",
        Buckets: []float64{5, 10, 20, 30, 60, 90, 120},
    }, []string{"tenant_id", "provisioner_type"})

    JobQueueDepth = promauto.NewGaugeVec(prometheus.GaugeOpts{
        Name: "byoc_job_queue_depth",
        Help: "Jobs waiting for a runner",
    }, []string{"tenant_id"})

    WebhookTotal = promauto.NewCounterVec(prometheus.CounterOpts{
        Name: "byoc_webhook_total",
        Help: "Total webhook events received",
    }, []string{"event_type", "action", "status"})
)
```

### OpenTelemetry Spans

Wrap every external call (OCI SDK, GitHub API, MySQL) in a span:

```go
ctx, span := tracer.Start(ctx, "provisioner.compute.launch_instance")
defer span.End()
span.SetAttributes(
    attribute.String("tenant.id", spec.TenantID),
    attribute.String("runner.id", spec.RunnerID),
    attribute.String("oci.shape", spec.Shape),
)
```

---

## 11. API Design

### Tenant API

```
POST   /v1/tenants                    Create tenant + provision OCI compartment
GET    /v1/tenants/:id                Get tenant config
PATCH  /v1/tenants/:id               Update config (labels, pool size, etc.)
DELETE /v1/tenants/:id               Offboard tenant (drain first)
GET    /v1/tenants/:id/runners        List runners for tenant
GET    /v1/tenants/:id/metrics        Runner metrics snapshot
```

### Webhook API (no auth — validated by HMAC signature)

```
POST   /webhooks/github/:tenant_id   Receive GitHub workflow_job events
```

### Internal / Health

```
GET    /healthz                       Liveness probe
GET    /readyz                        Readiness probe (checks DB + OCI connectivity)
GET    /metrics                       Prometheus scrape endpoint
```

### Response Envelope

```json
{
  "data": { ... },
  "error": null,
  "request_id": "uuid"
}
```

Errors:

```json
{
  "data": null,
  "error": {
    "code": "RUNNER_LIMIT_EXCEEDED",
    "message": "Tenant has reached its maximum runner limit of 50",
    "details": {}
  },
  "request_id": "uuid"
}
```

---

## 12. Terraform Module Contracts

### `modules/network`

**Inputs:** `compartment_id`, `vcn_cidr`, `private_subnet_cidr`, `public_subnet_cidr`, `tenant_id`  
**Outputs:** `vcn_id`, `private_subnet_id`, `public_subnet_id`, `nat_gateway_id`

Resources: VCN, internet gateway, NAT gateway, 2 route tables, 2 security lists,
2 subnets, NSG for runners (egress-only: 443, 80).

### `modules/iam`

**Inputs:** `compartment_id`, `runner_group_name`  
**Outputs:** `instance_principal_policy_id`, `runner_dynamic_group_id`

Creates: Dynamic group matching runner instances by compartment tag,
IAM policy granting Vault read + Object Storage write.

### `modules/compute` (runner image bake)

**Inputs:** `compartment_id`, `subnet_id`, `base_image_id`, `runner_version`  
**Outputs:** `custom_image_id` (OCI custom image with GitHub runner pre-installed)

### `modules/database`

**Inputs:** `compartment_id`, `subnet_id`, `db_name`, `admin_password_vault_secret_id`  
**Outputs:** `db_endpoint`, `db_port`

### `modules/vault`

**Inputs:** `compartment_id`, `github_app_private_key_pem`  
**Outputs:** `vault_id`, `github_app_key_secret_id`

### `modules/monitoring`

**Inputs:** `compartment_id`, `notification_topic_id`  
**Outputs:** alarm ARNs

---

## 13. Security Checklist (verify before every PR)

- [ ] No secrets in source code or Terraform state; all from OCI Vault.
- [ ] Webhook signature validated before any processing.
- [ ] Runner instance principal has no write access to control-plane compartment.
- [ ] All inter-service calls use context with timeout ≤ 30 s.
- [ ] SQL queries use parameterized statements (GORM default — never raw string concat).
- [ ] Rate limiter applied before OCI API calls.
- [ ] Token TTL checked before use; refresh if < 5 min remaining.

---

## 14. Testing Standards

- **Unit tests**: Mock all external dependencies using `gomock`; cover all state transitions,
  error branches, and the autoscaler algorithm. Target ≥ 80% on `internal/`.
- **Integration tests** (`//go:build integration`): Use real MySQL (Docker), mock OCI SDK.
- **E2E tests** (`//go:build e2e`): Require `TEST_OCI_*` and `TEST_GITHUB_*` env vars;
  spin up a real runner and verify it picks a queued job.
- Test file naming: `<file>_test.go` in the same package for white-box; `<pkg>_test` package
  for black-box integration.
- Every test function must clean up after itself (defer cleanup, use `t.Cleanup`).

---

## 15. Implementation Order (follow exactly)

1. `go.mod` + dependency setup
2. `internal/observability` — logger, metrics registry, tracer provider
3. `internal/store/interface.go` + `internal/store/mysql` — models + migrations
4. `internal/tenant` — domain model + service
5. `internal/github` — GitHub App client
6. `internal/vault` — OCI Vault adapter
7. `internal/provisioner` — interface + OCI Compute adapter
8. `internal/runner` — FSM + lifecycle service
9. `internal/scaler` — token bucket + algorithm
10. `internal/orchestrator` — job queue + dispatch loop
11. `internal/reconciler` — background leak detection
12. `internal/api` — router + handlers + middleware
13. `cmd/server/main.go` — wire + serve
14. `infra/terraform` — all modules + environments
15. `tests/` — unit tests, integration tests
16. `docs/` — all documentation
17. `.github/workflows/ci.yml` + `docker-compose.yml` + `Makefile`

---

## 16. File-by-File Coding Rules

- One primary exported type per file; helpers in the same file are fine if < 50 lines.
- `var _ InterfaceName = (*ConcreteType)(nil)` compile-time interface check in every adapter.
- Always run `gofmt` and `golangci-lint`; CI must pass lint before merge.
- Every exported function has a godoc comment.
- No `init()` functions; no package-level side effects.
- Prefer table-driven tests; use `t.Run` for subtests.

---

## Reference Files

- `references/oci-sdk-patterns.md` — OCI Go SDK common patterns (pagination, retry, instance principal)
- `references/github-app-auth.md` — GitHub App JWT flow + installation token refresh
- `references/terraform-oci-examples.md` — OCI Terraform resource reference snippets
