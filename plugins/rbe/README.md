# rbed — Remote Build Environment Daemon

A production-grade Remote Build Environment (RBE) server written in Go that
combines an **OCI-Distribution-Spec-compliant image registry**, a **DAG-based
build graph engine**, **vertex-level result caching**, **full FD log streaming**,
**BuildKit-style mount caches**, **SBOM / SLSA / cosign attestations**, and a
**dual HTTP + gRPC API** — all backed by pluggable S3-compatible blob storage
and a strongly-consistent distributed metadata store (etcd / TiKV / FoundationDB).

---

## Architecture overview

```
                          ┌─────────────────────────────────────────────┐
                          │                  rbed process                │
                          │                                              │
  builder / CI ──HTTP──►  │  chi router ──► OCI handlers (v2/)          │
  builder / CI ──gRPC──►  │  gRPC server ──► DAG / Cache / Log / etc.   │
                          │                                              │
                          │  ┌─────────────┐  ┌──────────────────────┐  │
                          │  │  Registry   │  │   DAG Service        │  │
                          │  │  (OCI v1 +  │  │ ┌──────────────────┐ │  │
                          │  │  Nydus etc.)│  │ │  Cache Service   │ │  │
                          │  └──────┬──────┘  │ └──────────────────┘ │  │
                          │         │         │ ┌──────────────────┐ │  │
                          │  ┌──────▼──────┐  │ │  Log Service     │ │  │
                          │  │ Blob Store  │  │ │  (FD streaming)  │ │  │
                          │  │  (storage   │  │ └──────────────────┘ │  │
                          │  │  .Store)    │  └──────────┬───────────┘  │
                          │  │  S3 / local │             │              │
                          │  └─────────────┘  ┌──────────▼───────────┐  │
                          │                   │   Metadata Store      │  │
                          │  ┌─────────────┐  │ (metadata.Store)      │  │
                          │  │ Mount Cache │  │  etcd / TiKV / FDB    │  │
                          │  │ Attestation │  └───────────────────────┘  │
                          │  └─────────────┘                             │
                          └─────────────────────────────────────────────┘
```

---

## Features at a glance

### Image registry
- Full **OCI Distribution Spec v1** compliance (pull, push, delete, list tags)
- **OCI 1.1 Referrers API** (`/v2/{name}/referrers/{digest}`)
- Resumable chunked uploads with `PATCH` / `PUT` / `Range` support
- Monolithic single-step upload (`POST /v2/{name}/blobs/uploads/?digest=…`)
- Blob listing per manifest or per repository
- Accelerated format detection and annotation:
  - **Nydus** (bootstrap annotation + media type)
  - **eStargz** (media type)
  - **zstd:chunked** (media type)
  - **OverlayBD** (annotation)
  - **Docker v2** schema manifest

### Conversion tracking
- Record source ↔ target blob sets for any format conversion (OCI→Nydus, OCI→eStargz, etc.)
- Compute added / removed / shared blob diffs automatically
- `CheckConversionExists(verifyBlobs=true)` — verifies every target blob is present
- Enables deduplication before re-running expensive conversions

### DAG & vertex engine
- Create build graphs with arbitrary topology; roots auto-detected
- Per-vertex: op type, payload, env, platform, mount specs, input/output files with digests
- Full **dependency tree retrieval** (`GET /deps`) — recursive, cycle-safe, depth-limited
- Vertex status machine: pending → running → succeeded / failed / cached / skipped / cancelled

### Content-addressed vertex caching
- Cache key computed from: op hash, env, platform, **sorted input file digests**,
  **dep vertex cache keys + dep file hashes**, mount cache IDs
- `CheckCache` → `StoreCache` flow; `ComputeCacheKey` RPC for pre-flight checks
- Per-vertex and per-DAG reverse index for bulk invalidation
- Optional TTL; LRU `last_used_at` tracking; cascade invalidation

### Log streaming
- One `LogStream` per FD (stdin=0, stdout=1, stderr=2, progress=3, any fd)
- Chunks stored in blob store (durable); metadata in KV store
- `GET /chunks` — paged buffered replay from any sequence number
- **WebSocket tail** (`/tail?follow=true`) — live fan-out with backfill
- **gRPC client-stream** (`UploadLogs`) and **gRPC server-stream** (`TailLogs`)
- `GetVertexLogs(interleaved=true)` — all FDs merged and sorted by timestamp

### Mount cache (BuildKit `--mount=type=cache`)
- Create named caches scoped by project / user / global
- **Platform-specific** isolation (separate cache per OS/arch)
- Three sharing modes: `shared`, `private` (CoW), `locked` (exclusive)
- Advisory locking with TTL and owner tracking
- Streaming upload / download with byte-range support
- `Prune` — evict by age, keep-bytes budget, or nuke all

### Attestations
- **SBOM**: SPDX and CycloneDX, stored as OCI referrer-style attestations
- **SLSA provenance** (v1): builder ID, materials, DAG link, reproducibility flag
- **cosign / sigstore**: keyless (Fulcio + Rekor) and key-based signing + verification
- Payloads stored in blob store; metadata (subject, type, key ID, Rekor log ID) indexed

### Auth (layered)
- **mTLS** — client certificate verified against configured CA
- **JWT / OIDC** — HMAC-HS256, RSA/EC public keys, or JWKS endpoint
- **API Keys** — hashed key → identity mapping; `X-RBE-API-Key` header
- Any successful check grants access; `RequireScope` middleware for RBAC

### Observability
- **Prometheus** metrics on every operation (blob, manifest, DAG, vertex, cache, log, gRPC)
- **OpenTelemetry** traces via OTLP gRPC exporter
- **zerolog** structured JSON logging with configurable level
- `/health` and `/ready` endpoints

---

## Quick start

### 1. Run with Docker Compose (MinIO + etcd + rbed)

```bash
docker compose -f docker-compose.dev.yml up
```

| Service | URL |
|---------|-----|
| rbed HTTP / OCI registry | `http://localhost:8080` |
| rbed gRPC | `localhost:9090` |
| rbed Prometheus | `http://localhost:8081/metrics` |
| MinIO console | `http://localhost:9001` (admin / rbepassword) |

### 2. Push a Docker image

```bash
docker tag myimage:latest localhost:8080/myrepo/myimage:latest
docker push localhost:8080/myrepo/myimage:latest
```

### 3. Record a Nydus conversion

```bash
curl -X POST http://localhost:8080/v1/conversions \
  -H 'Content-Type: application/json' \
  -d '{
    "source_digest": "sha256:abc…",
    "source_repo":   "myrepo/myimage",
    "source_format": "oci",
    "source_blobs":  [{"digest":"sha256:layer1","size":1024,"role":"layer"}],
    "target_digest": "sha256:def…",
    "target_repo":   "myrepo/myimage-nydus",
    "target_format": "nydus",
    "target_blobs":  [{"digest":"sha256:nydusblob","size":512,"role":"layer"}],
    "converted_by":  "nydusify/v2.2.0"
  }'
```

### 4. Track a build

```bash
# Create DAG
DAG=$(curl -sX POST http://localhost:8080/v1/dags \
  -H 'Content-Type: application/json' \
  -d '{"build_id":"b1","name":"my-build","platform":{"os":"linux","arch":"amd64"}}')
DAG_ID=$(echo $DAG | jq -r .id)

# Add vertex
VTX=$(curl -sX POST http://localhost:8080/v1/dags/$DAG_ID/vertices \
  -H 'Content-Type: application/json' \
  -d '{"name":"compile","op_type":"exec","input_files":[{"path":"/src/main.go","digest":"sha256:…","size":1234}]}')
VTX_ID=$(echo $VTX | jq -r .id)

# Open a stdout log stream
STREAM=$(curl -sX POST http://localhost:8080/v1/logs/streams \
  -H 'Content-Type: application/json' \
  -d "{\"vertex_id\":\"$VTX_ID\",\"dag_id\":\"$DAG_ID\",\"fd_type\":1,\"fd_num\":1}")
STREAM_ID=$(echo $STREAM | jq -r .id)

# Send log chunks
curl -sX POST http://localhost:8080/v1/logs/streams/$STREAM_ID/chunks \
  -H 'Content-Type: application/json' \
  -d '{"sequence":0,"data":"aGVsbG8gd29ybGQK","timestamp":"2025-01-01T00:00:01Z","fd_type":1,"fd_num":1}'

# Tail in real-time (WebSocket)
# wscat -c "ws://localhost:8080/v1/logs/streams/$STREAM_ID/tail?follow=true"

# Complete vertex
curl -sX PATCH http://localhost:8080/v1/dags/$DAG_ID/vertices/$VTX_ID/status \
  -H 'Content-Type: application/json' \
  -d '{"status":"succeeded","output_files":[{"path":"/out/app","digest":"sha256:…","size":8192}]}'

# Check cache
curl -sX POST http://localhost:8080/v1/cache/check \
  -H 'Content-Type: application/json' \
  -d "{\"vertex_id\":\"$VTX_ID\"}"
```

---

## Configuration

Configuration is loaded from a YAML file (`--config`) with environment
variable overrides (`RBE_<KEY>` prefix, dots replaced with underscores).

See [`config/dev.yaml`](config/dev.yaml) for a full example.

Key configuration groups:

| Group | Key fields |
|-------|-----------|
| `server` | `http_addr`, `grpc_addr`, `tls_cert_file`, `tls_key_file`, `client_ca_cert`, `require_mtls` |
| `storage` | `backend` (`s3`/`local`), `s3.*`, `local.root` |
| `metadata` | `backend` (`etcd`/`tikv`/`fdb`), `etcd.*`, `tikv.*` |
| `auth` | `jwt_signing_key`, `jwks_endpoint`, `oidc_issuer`, `api_key_header`, `require_mtls` |
| `registry` | `upload_expiry`, `enable_referrers`, `max_blob_size` |
| `cache` | `default_ttl` |
| `logs` | `retention_days`, `max_chunk_bytes` |
| `observability` | `otlp_endpoint`, `prometheus_addr`, `log_level` |

---

## Development

```bash
# Install tools
make proto-install-tools

# Generate proto stubs
make proto

# Build
make build

# Run tests
make test

# Lint
make lint

# Start local deps (MinIO + etcd)
make dev-deps

# Run the server
make run
```

---

## Repository layout

```
rbed/
├── cmd/rbed/          Main entrypoint
├── proto/
│   ├── rbe/v1/        DAG, Cache, Log, MountCache proto definitions
│   └── registry/v1/   Registry + Attestation proto definitions
├── pkg/
│   ├── models/        Shared Go structs (DAG, Vertex, Blob, etc.)
│   ├── storage/       Pluggable blob store interface + S3 + local backends
│   ├── metadata/      Pluggable KV store interface + etcd + TiKV + FDB backends
│   ├── registry/      OCI registry core + conversion tracking
│   ├── dag/           DAG service + cache service + log service
│   ├── mountcache/    BuildKit-style persistent mount caches
│   ├── attestation/   SBOM, SLSA, cosign/sigstore
│   ├── auth/          JWT/OIDC + mTLS + API key middleware
│   ├── config/        Viper-based configuration loader
│   ├── observability/ OTel tracing, Prometheus metrics, zerolog
│   └── api/
│       ├── http/      chi router + all HTTP handlers
│       └── grpc/      gRPC server + all service implementations
├── docs/API.md        Full API reference
├── config/dev.yaml    Development configuration
├── docker-compose.dev.yml
├── Dockerfile
└── Makefile
```

---

## Proto code generation

```bash
make proto
```

This runs `protoc` with the `go`, `go-grpc`, and `grpc-gateway` plugins to
generate:

- `gen/proto/rbe/v1/` — DAGService, CacheService, LogService, MountCacheService
- `gen/proto/registry/v1/` — RegistryService, AttestationService

The generated code is then imported by the gRPC server in `pkg/api/grpc/`.

---

## License

Apache 2.0
