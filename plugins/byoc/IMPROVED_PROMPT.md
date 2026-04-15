# Improved Prompt: BYOC Self-Hosted GitHub Actions Runner SaaS on OCI

## Context & Goal

Design and implement a **production-grade, multi-tenant SaaS platform** that provisions and manages
ephemeral and persistent **self-hosted GitHub Actions runners** on **Oracle Cloud Infrastructure (OCI)**.
The platform must be operable as a "Bring Your Own Cloud" (BYOC) model — tenants run runners inside
their own OCI tenancy, but management, orchestration, and billing remain centralised.

## Clarifications & Constraints Added

| Area | Clarification |
|---|---|
| Language | Control-plane in **Go 1.22+**; IaC in **Terraform ≥ 1.7** |
| Runner OS | Ubuntu 22.04 LTS on OCI Compute (VM.Standard.E4.Flex); container runners via OCI Container Instances |
| GitHub API | Use **GitHub App** (not PAT) for runner registration — short-lived JWTs, scoped per repo/org |
| Autoscaler target | ≤ 90 s from `workflow_job` webhook → runner ready |
| Isolation model | Each tenant gets a dedicated OCI compartment + VCN; no cross-tenant network reachability |
| Persistence | Control-plane state in **OCI MySQL HeatWave** (managed); runner ephemeral state in OCI Object Storage |
| Observability | Structured JSON logs → OCI Logging; metrics → OCI Monitoring; traces → OCI APM (OpenTelemetry) |
| Cost | Runners are terminated within 5 min of job completion (grace period configurable per tenant) |
| Secret management | GitHub App private keys + OCI API keys stored in **OCI Vault** |
| Scale target | ≥ 500 concurrent runners across all tenants; single-region MVP, multi-region path documented |

## Functional Requirements

### 1. Tenant Lifecycle
- Onboard a new tenant via REST API (create compartment, VCN, subnet, IAM policies).
- Suspend / offboard tenant — drain runners, revoke credentials, delete OCI resources.
- Tenant configuration: runner pool limits (min/max), runner labels, OCI shape preferences, timeout policies.

### 2. Runner Provisioning
- **Ephemeral runners**: Launched on demand, registered with GitHub, destroyed after one job.
- **Warm pool**: Optionally pre-provision N idle runners per tenant to reduce cold-start latency.
- **Persistent runners**: Long-lived for tenants that need shared caches (e.g. Docker layer cache).
- Provisioner is pluggable (OCI Compute, OCI Container Instances, OKE Pod — swappable via Strategy pattern).

### 3. Autoscaling
- Consume GitHub `workflow_job.queued` webhook events.
- Scale up: spawn runner(s) immediately on `queued`; respect per-tenant `max_runners` ceiling.
- Scale down: mark runner idle after job completes; terminate after `idle_timeout` (default 300 s).
- Burst protection: token-bucket rate limiter per tenant on scale-up calls.

### 4. GitHub Integration
- Register runner using **GitHub App installation token** (not stored; fetched fresh per registration).
- De-register runner on termination (best-effort; GitHub auto-expires stale runners after 30 d).
- Webhook signature validation (HMAC-SHA256, `X-Hub-Signature-256`).
- Runner labels: tenant-defined labels + system labels (`oci`, `ephemeral`, shape name).

### 5. Security
- OCI IAM — least-privilege instance principal for each runner; no long-lived keys on runner.
- Network — runners in private subnet; NAT gateway for outbound GitHub connectivity; no inbound except health port.
- Secrets — all credentials in OCI Vault; rotated automatically via Vault rotation policies.
- API authentication — HMAC signed requests (tenants) or JWT (internal services).

### 6. Observability
- **Structured logging** (zerolog): `tenant_id`, `runner_id`, `job_id`, `trace_id` in every log line.
- **Metrics** (Prometheus-compatible, scraped by OCI Monitoring): `runners_active`, `runners_idle`, `job_queue_depth`, `provision_latency_seconds`.
- **Distributed tracing**: OpenTelemetry spans exported to OCI APM.
- **Health endpoints**: `/healthz` (liveness), `/readyz` (readiness) on all services.

### 7. Fault Tolerance
- Idempotent provisioning: duplicate `workflow_job.queued` events must not double-provision.
- Graceful shutdown: drain in-flight requests; complete runner registration before exit.
- Runner leak guard: background reconciliation loop detects orphaned OCI instances and terminates them.

## Architecture

```
┌─────────────────────────────────────────────────────────────────────┐
│                        Control Plane (Go)                           │
│  ┌─────────────┐  ┌──────────────┐  ┌───────────────────────────┐  │
│  │ Webhook API  │  │  Orchestrator│  │   Runner Lifecycle Mgr    │  │
│  │ (Gin/HTTP)   │→ │  (job queue) │→ │  provision / register /   │  │
│  └─────────────┘  └──────────────┘  │  deregister / terminate   │  │
│                                      └───────────────────────────┘  │
│  ┌─────────────┐  ┌──────────────┐  ┌───────────────────────────┐  │
│  │ Tenant API   │  │  Autoscaler  │  │   Reconciler (background) │  │
│  │ (REST/CRUD)  │  │  (scale loop)│  │   leak detection + cleanup│  │
│  └─────────────┘  └──────────────┘  └───────────────────────────┘  │
└─────────────────────────────────────────────────────────────────────┘
         │                    │                      │
    OCI Compute          OCI Vault             OCI MySQL
    OCI Container        OCI Logging           OCI Monitoring
    Instances            OCI APM               OCI Object Storage
```

## Module Boundaries

| Module | Responsibility |
|---|---|
| `internal/api` | HTTP handlers, middleware, routing |
| `internal/orchestrator` | Job queue consumer, scale decisions |
| `internal/runner` | Runner registration, lifecycle FSM |
| `internal/provisioner` | OCI resource creation (pluggable) |
| `internal/tenant` | Tenant CRUD, config validation |
| `internal/github` | GitHub App auth, webhook parsing |
| `internal/scaler` | Autoscaling algorithm, rate limiting |
| `internal/reconciler` | Background leak detection |
| `internal/vault` | Secret fetch/cache (OCI Vault) |
| `internal/observability` | Logger, tracer, metrics setup |
| `infra/terraform` | OCI IaC (modules: network, compute, iam, db) |

## Deliverables (Ordered)

1. **`SKILL.md`** — Codified architecture decisions, patterns, and coding standards.
2. **Go source code** — All modules above with unit tests (≥ 80 % coverage on business logic).
3. **Terraform modules** — `network`, `compute`, `iam`, `database`, `vault`, `monitoring`.
4. **`docs/`** — Architecture overview, setup guide, operational runbook, tenant onboarding example.
5. **`docker-compose.yml`** — Local dev environment (mock OCI, mock GitHub).
6. **GitHub Actions workflow** — CI pipeline: lint, test, build, Docker image publish.

## Non-Goals (explicitly out of scope for MVP)

- Multi-region active-active (documented as future work).
- Billing / metering integration.
- UI dashboard (API-first).
- Windows or macOS runners.
- Self-hosted OCI (always uses OCI public cloud).
