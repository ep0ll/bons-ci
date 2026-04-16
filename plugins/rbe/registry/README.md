# AccelRegistry

[![Build](https://img.shields.io/badge/build-passing-brightgreen)](#)
[![Tests](https://img.shields.io/badge/tests-98%20passing-brightgreen)](#)
[![Go](https://img.shields.io/badge/go-1.22-blue)](#)
[![OCI](https://img.shields.io/badge/OCI_Dist_Spec-1.1.0-blue)](#)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue)](#)

An **OCI Distribution Spec 1.1–compliant container registry** purpose-built for accelerated images. AccelRegistry ingests Nydus, eStargz, SOCI, and OverlayBD images, automatically links them back to their non-accelerated source digests at every level of the OCI DAG, and exposes a rich query API so any client can discover which acceleration variants exist for any image.

---

## Table of Contents

- [Why AccelRegistry?](#why-accelregistry)
- [Acceleration Types](#acceleration-types)
- [Architecture](#architecture)
- [Quick Start](#quick-start)
- [API Reference](#api-reference)
- [accelctl CLI](#accelctl-cli)
- [Source Linkage Model](#source-linkage-model)
- [Configuration](#configuration)
- [Performance](#performance)
- [Extending with New Accel Types](#extending-with-new-accel-types)
- [Production Deployment](#production-deployment)
- [Development](#development)
- [Testing](#testing)

---

## Why AccelRegistry?

Standard OCI registries store accelerated and non-accelerated images as independent, unrelated objects. This forces clients to maintain their own mappings, re-discover variants on every pull, and handle version drift manually.

**AccelRegistry solves this by:**

1. **Automatic detection** — every pushed manifest is analysed by a pluggable detection pipeline. Nydus, eStargz, SOCI, and OverlayBD manifests are recognised without any client-side tagging.

2. **Deep source linkage** — every accel variant stores `SourceRef` pointers back to the original image at the *index*, *manifest*, *config*, and *per-layer* levels. Query by any source artefact digest to find all acceleration variants.

3. **Rich query API** — `GET /accel/v1/query/{sourceDigest}` returns every accel variant in one call, grouped by type, with full metadata.

4. **OCI 1.1 referrers** — SOCI indexes, cosign signatures, and SBOM artefacts are linked via the standard referrers API.

5. **DAG visibility** — `GET /accel/v1/dag/{repo}/{digest}` shows the complete OCI content graph with per-node existence status.

---

## Acceleration Types

| Type | Description | Source Link Mechanism |
|------|-------------|----------------------|
| **Nydus** | RAFS Merkle-tree filesystem, random read access | `containerd.io/snapshot/nydus-source-digest` annotation + bootstrap layer |
| **eStargz** | Extended Stargz TOC, random file access via gzip | `containerd.io/snapshot/stargz/toc.digest` per-layer annotation |
| **SOCI** | Seekable OCI — zTOC indexes as OCI artifacts | OCI 1.1 `subject` field pointing at original manifest |
| **OverlayBD** | Block-layer COW virtual disk images | `containerd.io/snapshot/overlaybd.layer` annotation |

---

## Architecture

```
┌──────────────────────────────────────────────────────────────────┐
│                    HTTP API  (chi router)                        │
│  /v2/*    OCI Distribution Spec 1.1                              │
│  /accel/v1/*   AccelRegistry Query + Pull + DAG + Metadata API   │
│  /healthz  /readyz  /metrics                                     │
└──────────────────────────────┬───────────────────────────────────┘
                               │ PutManifest ingest pipeline
                               ▼
┌──────────────────────────────────────────────────────────────────┐
│                    Registry  (internal/registry)                 │
│                                                                  │
│  ┌──────────────┐  ┌─────────────────┐  ┌────────────────────┐  │
│  │ ContentStore │  │  AccelIndex     │  │  MetadataStore     │  │
│  │ (in-mem/S3)  │  │  256 shards +   │  │  (4 indices)       │  │
│  └──────────────┘  │  Bloom Filter   │  └────────────────────┘  │
│  ┌──────────────┐  └─────────────────┘  ┌────────────────────┐  │
│  │ManifestIndex │  ┌─────────────────┐  │  ReferrersStore    │  │
│  └──────────────┘  │  DAGTraverser   │  │  (OCI 1.1)         │  │
│                    │  (BFS errgroup) │  └────────────────────┘  │
│                    └─────────────────┘                           │
│  ┌─────────────────────────────────────────────────────────────┐ │
│  │              AccelHandler Registry                          │ │
│  │  Nydus ── eStargz ── SOCI ── OverlayBD  ← register more   │ │
│  └─────────────────────────────────────────────────────────────┘ │
└──────────────────────────────────────────────────────────────────┘
              │ periodic                    │ on startup
              ▼                             ▼
     SnapshotManager              GC Collector
     (gzip + atomic rename)       (mark + sweep)
```

---

## Quick Start

### Docker Compose (recommended)

```bash
git clone https://github.com/bons/bons-ci/plugins/rbe/registry
cd accelregistry
docker compose up -d
```

Services start on:
- **AccelRegistry**: `http://localhost:5000`
- **Metrics**: `http://localhost:9090/metrics`  
- **Prometheus**: `http://localhost:9091`
- **Grafana**: `http://localhost:3000` (admin/admin)

### Binary

```bash
# Build
make build

# Run
./bin/accelregistry
# or with env config:
ACCELREG_LOG_LEVEL=debug ACCELREG_ADDR=:5000 ./bin/accelregistry
```

### Push a Nydus image

```bash
# Push source image first
skopeo copy docker://node:20-alpine oci:node-source
docker push localhost:5000/library/node:20-alpine

# Push Nydus variant with source annotation
# The registry auto-detects and links it
nydusify convert \
  --source node:20-alpine \
  --target localhost:5000/library/node:20-alpine-nydus \
  --nydus-image /usr/local/bin/nydus-image

# Query: find all accel variants for the source image
SOURCE_DIGEST=$(skopeo inspect docker://localhost:5000/library/node:20-alpine | jq -r .Digest)
accelctl query $SOURCE_DIGEST
```

---

## API Reference

### OCI Distribution Spec (v2)

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/v2/` | API version check |
| `HEAD/GET` | `/v2/{name}/blobs/{digest}` | Blob existence / download |
| `POST` | `/v2/{name}/blobs/uploads/` | Initiate blob upload |
| `PATCH` | `/v2/{name}/blobs/uploads/{uuid}` | Chunk upload |
| `PUT` | `/v2/{name}/blobs/uploads/{uuid}?digest=…` | Finalize upload |
| `HEAD/GET` | `/v2/{name}/manifests/{reference}` | Manifest existence / download |
| `PUT` | `/v2/{name}/manifests/{reference}` | Push manifest |
| `DELETE` | `/v2/{name}/manifests/{reference}` | Delete manifest |
| `GET` | `/v2/{name}/tags/list` | List tags |
| `GET` | `/v2/{name}/referrers/{digest}` | OCI 1.1 referrers |

### Accel API

#### `GET /accel/v1/query/{digest}`

Returns all acceleration variants for a source (non-accelerated) image digest.

```bash
curl http://localhost:5000/accel/v1/query/sha256:abc123...
```

```json
{
  "sourceDigest": "sha256:abc123...",
  "found": true,
  "totalVariants": 3,
  "supportedTypes": ["nydus", "estargz", "soci"],
  "variants": {
    "nydus": [{
      "accelType": "nydus",
      "manifestDigest": "sha256:def456...",
      "repository": "library/node",
      "tag": "20-alpine-nydus",
      "size": 45678901,
      "visibility": "public",
      "createdAt": "2024-03-15T10:00:00Z",
      "sourceRefs": [
        {"digest": "sha256:abc123...", "kind": "manifest"},
        {"digest": "sha256:111...", "kind": "index"},
        {"digest": "sha256:222...", "kind": "layer"}
      ]
    }],
    "estargz": [...],
    "soci": [...]
  }
}
```

#### `POST /accel/v1/pull`

Resolve a `PullRequest` to a filtered set of `AccelVariant`s.

```bash
curl -X POST http://localhost:5000/accel/v1/pull \
  -H 'Content-Type: application/json' \
  -d '{
    "sourceDigest": "sha256:abc123...",
    "accelTypes": ["nydus", "estargz"],
    "platform": {"os": "linux", "architecture": "amd64"}
  }'
```

#### `GET /accel/v1/dag/{name}/{digest}`

Traverse the full OCI content DAG rooted at `digest`.

```bash
curl http://localhost:5000/accel/v1/dag/library/node/sha256:abc123...
```

```json
{
  "rootDigest": "sha256:abc123...",
  "totalNodes": 5,
  "existingNodes": 5,
  "missingNodes": 0,
  "isComplete": true,
  "accelTypes": ["nydus"],
  "root": {
    "digest": "sha256:abc123...",
    "mediaType": "application/vnd.oci.image.manifest.v1+json",
    "exists": true,
    "depth": 0,
    "children": [
      {"digest": "sha256:cfg...", "mediaType": "...config...", "exists": true, "depth": 1},
      {"digest": "sha256:layer1...", "mediaType": "...layer...", "exists": true, "depth": 1}
    ]
  }
}
```

#### `GET /accel/v1/exists/{digest}`

Fast bloom-filter + definitive existence check. Returns `200` with variant summary if found, `404` if absent.

#### `GET /accel/v1/metadata/{name}/{digest}`

Rich image metadata including visibility, acceleration status, tags, size, and annotations.

#### `GET /accel/v1/stats`

Index statistics: total source digests, variants by type, bloom filter false-positive rate.

#### `GET /accel/v1/types`

List all registered acceleration types.

---

## accelctl CLI

```bash
# Install
go install github.com/bons/bons-ci/plugins/rbe/registry/cmd/accelctl@latest

# Configure
export ACCELREG_URL=http://localhost:5000

# Query all variants
accelctl query sha256:abc123...

# Pull nydus + estargz variants only
accelctl pull sha256:abc123... nydus estargz

# Inspect the OCI DAG
accelctl dag library/node sha256:abc123...

# Show image metadata
accelctl meta library/node sha256:abc123...

# Fast existence check
accelctl exists sha256:abc123...

# Registry stats
accelctl stats

# Health check
accelctl health
```

---

## Source Linkage Model

Every accelerated image pushed to AccelRegistry has its `SourceRef` digests extracted at four levels:

```
Source image (non-accelerated):
  sha256:AAAA  ← index digest   (multi-arch)
    └─ sha256:BBBB  ← manifest digest (single platform)
         ├─ sha256:CCCC  ← config digest
         └─ sha256:DDDD  ← layer digest

Nydus variant stored in registry:
  ManifestDigest: sha256:EEEE
  SourceRefs:
    { digest: sha256:BBBB, kind: "manifest" }  ← from NydusAnnotationSourceDigest
    { digest: sha256:AAAA, kind: "index"    }  ← from AnnotationSourceIndexDigest
    { digest: sha256:DDDD, kind: "layer"    }  ← from per-layer nydus annotation
```

`QueryAccel(sha256:BBBB)` → returns the Nydus variant  
`QueryAccel(sha256:AAAA)` → also returns the Nydus variant (via index digest)  
`QueryAccel(sha256:DDDD)` → also returns the Nydus variant (via layer digest)

---

## Configuration

All configuration via environment variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `ACCELREG_ADDR` | `:5000` | HTTP listen address |
| `ACCELREG_METRICS_ADDR` | `:9090` | Prometheus metrics address |
| `ACCELREG_LOG_LEVEL` | `info` | Log level: debug/info/warn/error |
| `ACCELREG_EXPECTED_SOURCES` | `100000` | Expected unique source images (bloom filter sizing) |
| `ACCELREG_READ_TIMEOUT` | `30s` | HTTP read timeout |
| `ACCELREG_WRITE_TIMEOUT` | `60s` | HTTP write timeout |
| `ACCELREG_IDLE_TIMEOUT` | `120s` | HTTP idle timeout |

---

## Performance

Benchmarks on Intel Xeon Platinum 8581C @ 2.10GHz (2 cores, sandbox):

| Operation | Throughput | Allocations |
|-----------|-----------|-------------|
| Bloom `TestDigestString` hit | **11.5M ops/s** | 0 allocs |
| `ExistsAny` (bloom fast path) | **11.5M ops/s** | 1 alloc |
| `Query` miss (bloom eliminates lock) | **5.0M ops/s** | 3 allocs |
| `Query` hit (shard RLock + map) | **1.28M ops/s** | 7 allocs |
| Concurrent `Query` (parallel) | **1.0M ops/s** | 7 allocs |
| Store `Exists` | **84M ops/s** | 0 allocs |
| Store `Get` | **12M ops/s** | 2 allocs |

**Key design decisions for performance:**

- **256-shard index** — contention drops ~256× under concurrent writers
- **Lock-free bloom filter** — atomic CAS bit operations, no mutex on the hot read path
- **`sync.Pool` buffer recycling** — eliminates per-call allocations in blob ingest
- **Double-hashing** — k virtual hash functions from 2 FNV-1a evaluations vs. k independent hashes
- **Copy-before-return** — `Query()` returns deep copies; callers hold data without lock

---

## Extending with New Accel Types

Adding `zstd-chunked` (hypothetical) in 4 steps:

**1.** Add constant to `pkg/types/types.go`:
```go
const AccelZstdChunked AccelType = "zstdchunked"
var KnownAccelTypes = []AccelType{..., AccelZstdChunked}
```

**2.** Add annotation keys:
```go
const ZstdChunkedManifestChecksum = "io.containers.zstd-chunked.manifest-checksum"
```

**3.** Implement `AccelHandler` in `internal/accel/zstdchunked/`:
```go
type Handler struct{}
func (h *Handler) Name() types.AccelType { return types.AccelZstdChunked }
func (h *Handler) Detect(ctx, manifest, configBlob) (types.AccelType, bool, error) { ... }
func (h *Handler) ExtractSourceRefs(ctx, manifest, configBlob) ([]types.SourceRef, error) { ... }
func (h *Handler) Validate(ctx, manifest) error { ... }
```

**4.** Register in `internal/registry/registry.go`:
```go
func registerBuiltinHandlers(ar *accelreg.Registry) {
    ...
    ar.Register(zstdChunkedHandler{})
}
```

Everything else — indexing, querying, DAG traversal, metadata, API — works automatically.

---

## Production Deployment

### Kubernetes

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: accelregistry
spec:
  replicas: 3
  template:
    spec:
      containers:
      - name: accelregistry
        image: accelregistry:v1.0.0
        ports:
        - containerPort: 5000
        - containerPort: 9090
        env:
        - name: ACCELREG_EXPECTED_SOURCES
          value: "1000000"
        - name: ACCELREG_LOG_LEVEL
          value: "info"
        livenessProbe:
          httpGet: { path: /healthz, port: 5000 }
        readinessProbe:
          httpGet: { path: /readyz, port: 5000 }
        resources:
          requests: { memory: 512Mi, cpu: 500m }
          limits:   { memory: 4Gi,  cpu: 4 }
```

### Production checklist

See [BEST_PRACTICES.md](BEST_PRACTICES.md) for the complete checklist covering:
- Storage backend swap-out (S3/GCS `ContentStore`, PostgreSQL `MetadataStore`)
- Authentication (JWT, OIDC token challenge)
- TLS termination
- Rate limiting
- Distributed locking for multi-node snapshot writes
- Blob GC scheduling
- OpenTelemetry distributed tracing

---

## Development

```bash
# Run all tests
make test

# Run with race detector
make test-race

# Generate coverage report
make coverage

# Benchmarks
make bench

# Start in debug mode
make run

# Build release binaries for all platforms
make release
```

---

## Testing

```
test/
├── unit/         33 tests — bloom filter, sharded index, content store,
│                            metadata store, referrers store, DAG traverser
├── handlers/     44 tests — per-handler: detection, extraction, validation,
│                            cross-contamination for all 4 accel types
├── integration/  14 tests — full HTTP API round-trips over httptest.Server
└── gc/            9 tests — GC mark/sweep, dry run, grace period, snapshot
                             write/restore, atomic write, multi-entry round-trip
                             
Total: 98 tests, 0 failures
```

---

## License

Apache 2.0 — see [LICENSE](LICENSE).
