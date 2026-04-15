# Architecture Overview

## System Purpose

The BYOC OCI GitHub Actions Runner Platform is a multi-tenant SaaS control plane that
provisions and manages self-hosted GitHub Actions runners on Oracle Cloud Infrastructure.
Tenants bring their own OCI compartments; the platform manages the full runner lifecycle
from webhook receipt to termination.

---

## High-Level Architecture

```
                         ┌──────────────────────────────────────────────────────┐
                         │               GitHub (external)                      │
                         │  workflow_job webhooks ──► POST /webhooks/github/:id │
                         │  runner registration ◄── GitHub App installation     │
                         └─────────────────────────────┬────────────────────────┘
                                                        │ HTTPS + HMAC-SHA256
                                                        ▼
┌───────────────────────────── Control Plane (Go binary) ──────────────────────────────┐
│                                                                                        │
│  ┌────────────────┐   ┌──────────────────┐   ┌──────────────────────────────────┐    │
│  │  Webhook API   │──►│   Orchestrator   │──►│      Runner Lifecycle Service    │    │
│  │  (Gin/HTTP)    │   │  (job dispatch   │   │  Provision → Register → Idle →   │    │
│  │  /webhooks/    │   │   queue + loop)  │   │  Busy → Terminating → Terminated │    │
│  └────────────────┘   └──────────────────┘   └──────────────┬───────────────────┘    │
│                               │                              │                        │
│  ┌────────────────┐   ┌───────▼──────────┐   ┌─────────────▼───────────────────┐    │
│  │  Tenant API    │   │     Scaler       │   │      Provisioner (pluggable)     │    │
│  │  /v1/tenants   │   │  Token bucket +  │   │  OCI Compute / Container Inst.  │    │
│  └────────────────┘   │  FSM decisions   │   └─────────────────────────────────┘    │
│                        └──────────────────┘                                           │
│  ┌────────────────┐   ┌──────────────────┐   ┌──────────────────────────────────┐    │
│  │  Health &      │   │   Reconciler     │   │      GitHub Client               │    │
│  │  /metrics      │   │  (background     │   │  App JWT → IAT → reg token       │    │
│  └────────────────┘   │   orphan sweep)  │   └──────────────────────────────────┘    │
│                        └──────────────────┘                                           │
└──────────────────────────────────────────────────────────────────────────────────────┘
          │                     │                      │                   │
    OCI Compute           OCI MySQL              OCI Vault         OCI Monitoring
    (runner VMs)         HeatWave              (secrets)          + OCI APM (traces)
```

---

## Module Responsibilities

| Module | Package | Responsibility |
|--------|---------|----------------|
| **API** | `internal/api` | HTTP routing, middleware (request ID, logging, recovery, metrics), response envelope |
| **Webhook Handler** | `internal/api/handler` | HMAC validation, event parsing, async dispatch to orchestrator |
| **Tenant Handler** | `internal/api/handler` | Tenant CRUD REST endpoints |
| **Orchestrator** | `internal/orchestrator` | Buffered job channel, dispatch workers, scale-down ticker, wait queue |
| **Scaler** | `internal/scaler` | Idempotency check, idle runner assignment, rate limiting, provision/enqueue/rate-limit decisions |
| **Runner Service** | `internal/runner` | FSM-validated state transitions, CreateAndProvision, Terminate lifecycle |
| **Provisioner** | `internal/provisioner` | OCI Compute adapter; pluggable for Container Instances or OKE pods |
| **GitHub Client** | `internal/github` | GitHub App JWT, installation access token, runner registration/removal |
| **Vault** | `internal/vault` | OCI Vault secret fetch with 5-min in-memory TTL cache |
| **Store** | `internal/store` | Persistence port; MySQL/GORM adapter with migrations |
| **Reconciler** | `internal/reconciler` | Background loop detecting orphaned/stuck runners |
| **Observability** | `internal/observability` | zerolog logger, Prometheus metrics registry, OTel tracer |
| **Tenant Service** | `internal/tenant` | Tenant validation, defaults, lifecycle (create, suspend, offboard) |

---

## Data Flow: `workflow_job.queued`

```
1. GitHub sends POST /webhooks/github/{tenant_id}
   Header: X-Hub-Signature-256: sha256=<hmac>
   Body:   {"action":"queued","workflow_job":{"id":12345,"labels":["self-hosted","oci"]}}

2. WebhookHandler:
   a. Read raw body (1 MB cap)
   b. Fetch tenant from DB (needed for webhook_secret)
   c. ValidateWebhookSignature(body, header, tenant.WebhookSecret)  ← HMAC-SHA256
   d. ParseWorkflowJobEvent(body)
   e. Respond 202 Accepted immediately (async processing below)
   f. orchestrator.EnqueueJob(tenant, jobID, labels)  → non-blocking channel send

3. Orchestrator dispatch worker picks up jobEvent from channel:
   a. scaler.ScaleUp(ctx, tenant, jobID)
      ├─ AcquireIdempotencyLock("job:tenant:12345")  → duplicate? return early
      ├─ ListRunners(idle, limit=1)                  → idle runner? AssignIdle
      ├─ CountRunners(active statuses)               → at max?    Enqueue
      ├─ rateLimiter.Allow()                         → throttled? RateLimited
      └─ all clear                                   → Provision
   b. On DecisionProvision:
      → runnerService.CreateAndProvision(tenant, jobID)
        ├─ github.CreateRegistrationToken(tenantID)  → short-lived token
        ├─ store.CreateRunner(runner{status:Provisioning})
        ├─ provisioner.Provision(RunnerSpec)          → OCI Compute LaunchInstance
        │   cloud-init installs runner binary, calls config.sh --token <token> --ephemeral
        └─ store.UpdateRunnerStatus(Registering)

4. Runner instance self-registers with GitHub and picks up the queued job.

5. GitHub sends workflow_job.completed:
   → orchestrator.HandleJobCompleted(tenantID, runnerID)
     ├─ runnerService.Terminate(runnerID, "job_completed")
     │   ├─ github.RemoveRunner(...)    best-effort, 10s timeout
     │   └─ provisioner.Terminate(ociInstanceID)
     └─ orchestrator.NotifyJobComplete(tenantID) → dequeue waiting jobs
```

---

## Tenant Isolation Model

Each tenant is isolated at three layers:

1. **OCI Compartment** — All runner instances, subnets, and IAM policies live in a
   dedicated OCI compartment. One tenant's instances cannot reach another's via OCI IAM.

2. **VCN / Subnet** — Each tenant gets a private VCN with its own CIDR block.
   No VCN peering between tenants is created.

3. **GitHub App Installation** — Each tenant configures their own GitHub App installation.
   Registration tokens are scoped to that installation and cannot be used cross-tenant.

---

## Runner Finite State Machine

```
                ┌─────────────┐
    (new)       │ Provisioning│──── OCI error ──────────────────────┐
    ────────►   └──────┬──────┘                                      │
                       │ OCI instance launched                        │
                       ▼                                              │
               ┌───────────────┐                                      │
               │  Registering  │──── cloud-init error (timeout) ─────┤
               └──────┬────────┘                                      │
                       │ GitHub confirms registration                  │
                       ▼                                              ▼
               ┌───────────────┐                            ┌──────────────┐
               │     Idle      │──── idle_timeout ─────────►│  Terminating │
               └──────┬────────┘                            └──────┬───────┘
                       │ job assigned                               │ OCI terminated
                       ▼                                            ▼
               ┌───────────────┐                            ┌──────────────┐
               │     Busy      │──── job completed ─────────►  Terminated  │
               └───────────────┘                            └──────────────┘
```

Illegal transitions return `ErrInvalidTransition`. The reconciler detects runners
stuck in `Provisioning` or `Registering` beyond configured timeouts and force-terminates them.

---

## Security Model

### Authentication

| Path | Method |
|------|--------|
| `POST /webhooks/github/:id` | HMAC-SHA256 on raw body (`X-Hub-Signature-256`) |
| `POST /v1/tenants` | Caller-provided bearer token (JWT) — add auth middleware in production |
| Internal services | OCI Instance Principal (no long-lived API keys) |

### Secret Storage

All secrets are stored in **OCI Vault**. The control plane fetches secrets via instance
principal with a 5-minute in-memory cache. No secrets appear in:
- Environment variables
- Terraform state
- Log output (webhook_secret is scrubbed from API responses)
- Git repository

### Network Security

- Runner instances are placed in **private subnets** with no inbound security rules.
- Outbound: NAT gateway for GitHub API (port 443) and runner downloads.
- OCI services (Vault, Object Storage) accessed via **Service Gateway** — no public internet hop.

---

## Scalability Targets

| Metric | Target |
|--------|--------|
| Concurrent runners | ≥ 500 across all tenants |
| Webhook → runner ready | ≤ 90 seconds (p90) |
| Tenants | ≥ 100 active |
| Control plane instances | Stateless; horizontally scalable behind OCI Load Balancer |
| Database | OCI MySQL HeatWave — vertically scalable; read replicas for analytics |

---

## Trade-offs & Assumptions

| Decision | Trade-off |
|----------|-----------|
| In-process job channel (not Kafka/SQS) | Simpler ops; loses in-flight jobs on restart. Acceptable for MVP — add persistent queue for HA. |
| MySQL for idempotency locks | Serialised on DB; sufficient for 500 runners. Replace with Redis for higher throughput. |
| GitHub App per-tenant | Each tenant manages their own App; more ops burden but better isolation. |
| cloud-init registration | Runner registers itself; no polling needed. Risk: cloud-init failures are detected only by reconciler timeout. |
| In-memory rate limiter | Per-instance; does not enforce across multiple control-plane replicas. Use Redis token bucket for distributed rate limiting. |
| OCI Compute (VM) | Cold start ~60s. Container Instances start faster (~15s) but have less disk. |
