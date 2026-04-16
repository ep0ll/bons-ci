# AccelRegistry — Best Practices, Design Rationale & Extensibility Guide

## Table of Contents
1. [Architecture Overview](#architecture-overview)
2. [Data Structures & Algorithms](#data-structures--algorithms)
3. [Concurrency Patterns](#concurrency-patterns)
4. [Performance — Macro Level](#performance--macro-level)
5. [Performance — Micro/Instruction Level](#performance--microinstruction-level)
6. [OCI Spec Compliance](#oci-spec-compliance)
7. [Accel Type Linking Model](#accel-type-linking-model)
8. [Extensibility — Adding a New Accel Type](#extensibility--adding-a-new-accel-type)
9. [Production Hardening Checklist](#production-hardening-checklist)
10. [Security](#security)
11. [Observability](#observability)
12. [Testing Strategy](#testing-strategy)
13. [Storage Backend Swap-Out](#storage-backend-swap-out)

---

## Architecture Overview

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                        HTTP API (chi router)                                │
│  /v2/* (OCI Dist Spec)   /accel/v1/* (Accel API)   /healthz  /metrics      │
└────────────────────────────────┬────────────────────────────────────────────┘
                                 │
┌────────────────────────────────▼────────────────────────────────────────────┐
│                         Registry (internal/registry)                        │
│                                                                             │
│  ┌──────────────────┐  ┌──────────────────┐  ┌──────────────────────────┐  │
│  │   ContentStore   │  │   AccelIndex     │  │    MetadataStore         │  │
│  │ (memory / S3 /   │  │ (ShardedIndex +  │  │ (multi-index, in-mem /  │  │
│  │  filesystem)     │  │  Bloom Filter)   │  │  BadgerDB / PostgreSQL)  │  │
│  └──────────────────┘  └──────────────────┘  └──────────────────────────┘  │
│  ┌──────────────────┐  ┌──────────────────┐  ┌──────────────────────────┐  │
│  │  ManifestIndex   │  │  ReferrersStore  │  │     DAGTraverser         │  │
│  │ (repo → digest)  │  │ (OCI 1.1 subj.)  │  │ (concurrent BFS)         │  │
│  └──────────────────┘  └──────────────────┘  └──────────────────────────┘  │
│  ┌──────────────────────────────────────────┐                              │
│  │           AccelHandler Registry          │                              │
│  │  Nydus | eStargz | SOCI | OverlayBD      │ ← plug in new types here    │
│  └──────────────────────────────────────────┘                              │
└─────────────────────────────────────────────────────────────────────────────┘
```

---

## Data Structures & Algorithms

### 1. Sharded Index (256 shards)

The `ShardedIndex` divides the keyspace across 256 independent `sync.RWMutex`
shards keyed by the first byte of the hex-encoded digest. For SHA-256 digests:

```
sha256:abc123...  →  shard[0xAB]
sha256:ff0000...  →  shard[0xFF]
```

**Why 256?** SHA-256 digests are uniformly distributed. With 256 shards, each
shard holds ~1/256 of all entries. Under 256 concurrent writers, the probability
that any two writers contend the same shard is ~1/256. Lock contention approaches
zero at scale.

**Why not `sync.Map`?** `sync.Map` is optimised for read-heavy stable maps.
AccelRegistry has frequent writes (every push) and range-query patterns
(`Query` must return all variants for a source). A sharded mutex map gives
better write throughput and enables the type-index secondary lookup.

### 2. Bloom Filter (lock-free, atomic bit array)

The bloom filter provides an O(k) probabilistic "definitely not present" check
where k = 7 (default). Each `testBit` call is a single `atomic.LoadUint64` with
a bitmask — no lock, no cache line sharing between test goroutines.

**Double-hashing:** Instead of k independent hash functions (expensive), we use:
```
h_i(x) = (h1(x) + i * h2(x)) mod m
```
This generates k virtual hash functions from two FNV-1a evaluations with O(1) arithmetic.

**False-positive rate target:** `p ≈ 0.01` (1%) at the configured capacity. At
this rate, 99% of absent-digest lookups skip the shard lock entirely.

**No deletions:** The filter does not support deletion. Entries removed from the
index leave phantom bits in the filter, causing false positives. Since `Query()`
is definitive, these are harmless — just a small extra lock acquisition.

### 3. Secondary Indices in MetadataStore

The metadata store maintains four indices:
- **Primary**: `(repo, digest) → ImageMetadata`
- **byAccelType**: `AccelType → set<(repo,digest)>`
- **bySourceDigest**: `sourceDigest → set<(repo,digest)>`
- **byRepo**: `repo → set<(repo,digest)>`

All under a single `sync.RWMutex`. For production at scale, replace with
[BadgerDB](https://github.com/dgraph-io/badger) LSM-tree (OLTP) or
[PostgreSQL](https://www.postgresql.org/) with partial indices.

### 4. DAG Traversal — Bounded Concurrent BFS

The DAG traverser uses `errgroup` with a semaphore channel (`chan struct{}` of
capacity 16) to limit concurrency. Each node in the DAG is resolved exactly once
via a `sync.Map` visited set (lock-free CAS).

**Why `sync.Map` for visited?** The visited set is written once per node
(during first resolution) and read many times (dedup check). `sync.Map` is
optimised for this write-once / read-many pattern.

---

## Concurrency Patterns

| Pattern | Used Where | Rationale |
|---------|-----------|-----------|
| Sharded mutex | ShardedIndex, ReferrersStore | Reduce lock contention 256× |
| `sync/atomic` | Counters (total sources, type counts) | Zero-cost reads from Stats() |
| `sync.Map` | DAG visited set | Lock-free read-after-write |
| `sync.Pool` | Buffer recycling in ContentStore | Reduce GC pressure on hot path |
| `errgroup` | DAG child resolution | Bounded concurrency + error propagation |
| CAS loop | Bloom filter bit-set | Lock-free multi-writer safety |
| `sync.RWMutex` | MetadataStore, ManifestIndex | Read-heavy workload |
| Write dedup | ContentStore.Put | Prevent duplicate concurrent puts |
| Graceful shutdown | HTTP server | `signal.NotifyContext` + 30s drain |

### Critical Concurrency Rules

1. **Never hold a shard lock across I/O** — all shard operations are pure
   in-memory map operations. I/O (content store) is done before/after.

2. **Copy before returning** — `Query()` returns a deep copy of the variant
   slice, not a reference to the internal slice. Callers can hold the copy
   indefinitely without racing the index.

3. **Bloom filter is advisory** — always confirm with `Query()` before acting
   on a bloom hit. Never skip the definitive lookup.

---

## Performance — Macro Level

### Hot Path Optimisations

**`/accel/v1/query/{digest}` — the most frequent Accel API call:**

```
1. Bloom filter test         → O(k) atomics, no lock   ← ~30ns
2. Shard RLock               → 1/256 shards             ← ~10ns uncontended
3. Map lookup                → O(1) by digest key        ← ~50ns
4. Slice copy                → O(n) where n ≈ 4          ← ~5ns
5. Shard RUnlock             → release                   ← ~5ns
Total "hit" path:            ~100ns
Total "miss" path (bloom):   ~30ns (no lock acquired)
```

**Avoid these anti-patterns:**

```go
// ❌ BAD: JSON encode inside shard lock
sh.mu.Lock()
json.Marshal(sh.variants[src]) // heap alloc + CPU inside lock
sh.mu.Unlock()

// ✅ GOOD: copy under lock, marshal outside
sh.mu.RLock()
cp := make([]AccelVariant, len(sh.variants[src]))
copy(cp, sh.variants[src])
sh.mu.RUnlock()
json.Marshal(cp) // lock free
```

### Connection Handling

- HTTP server uses Go's built-in connection pooling with `IdleTimeout=120s`
- Large blob transfers use `io.Copy` with a 32 KiB stack-allocated read buffer
  to avoid one allocation per read call
- `sync.Pool` for upload session buffers (pooled `bytes.Buffer`)

### Index Snapshot/Restore

- `Snapshot()` acquires each shard `RLock` briefly in sequence (never holds all
  256 simultaneously) — background goroutines can still serve queries
- Restore at startup: populate bloom filter + shards in a single pass

### Scale-Out Architecture

For multi-node deployments:
1. Use **Redis** or **etcd** as a distributed AccelIndex backend
2. Use **S3/GCS** as the ContentStore with content-addressable keys
3. Use **PostgreSQL** with JSONB for MetadataStore (GIN indices on annotations)
4. Deploy multiple Registry replicas behind a load balancer
5. Use a CDN (CloudFront, Fastly) in front of the blob download path

---

## Performance — Micro/Instruction Level

### FNV-1a Hash (Bloom Filter)

The double-hash functions use FNV-1a — 2 multiplications and 1 XOR per byte.
This is intentionally simple: SHA-256 is cryptographically strong but
10× slower than FNV-1a for non-crypto use cases.

**Stack allocation:** The 32 KiB read buffer in `ContentStore.Put` is declared
as `var readBuf [32*1024]byte` on the stack, avoiding a heap allocation per read
call. The Go compiler will allocate this on the goroutine stack (which grows
dynamically) — no GC pressure.

**Bitmask arithmetic (setBit/testBit):**
```go
word := pos / 64    // compiler turns this into SHR instruction
mask := 1 << (pos % 64)  // compiler turns this into SHL
atomic.LoadUint64(&bits[word]) & mask != 0  // single cache-line read
```
No branching, one cache-line access per bit test.

**`hexNibble`:** The shard selector function uses a branchless lookup:
```go
// Inlined by compiler; the three comparisons typically resolve
// via CMOV (conditional move) on amd64 — no branch mispredictions
func hexNibble(c byte) byte { ... }
```

### Memory Layout

- `blobEntry.data []byte` — the content slice points directly into a
  `make([]byte, n)` allocation. No copying on reads (`bytes.NewReader` shares
  the backing array). The GC sees a single allocation.
- `shard.variants map[digest.Digest][]AccelVariant` — digest keys are 72 bytes
  (algorithm string + encoded hash). Stored inline in the map bucket, not as
  pointers, reducing pointer chasing.

---

## OCI Spec Compliance

AccelRegistry implements:

| Spec | Coverage |
|------|----------|
| OCI Distribution Spec 1.1 | Full (blobs, manifests, tags, referrers) |
| OCI Image Spec 1.1 | Manifest + Index parsing |
| OCI Artifact spec (via referrers) | Full OCI 1.1 referrers API |
| Docker Registry API v2 | Compatible manifest/blob endpoints |

**Digest verification:** Every `Put` verifies the content digest after reading.
The digester is fed via `io.TeeReader` — single pass, no re-read.

**Referrers fallback:** The `GET /v2/{name}/referrers/{digest}` endpoint
returns an OCI Image Index format as required by OCI 1.1 §10.

---

## Accel Type Linking Model

Every accel variant stored in the registry carries a `[]SourceRef` slice that
links it back to the original non-accelerated image at every OCI artefact level:

```
Source image:
  sha256:AAAA  (index digest)
    └─ sha256:BBBB  (manifest digest)
         ├─ sha256:CCCC  (config digest)
         └─ sha256:DDDD  (layer digest)

Nydus variant of node:20:
  ManifestDigest: sha256:EEEE
  SourceRefs:
    [{Digest: sha256:BBBB, Kind: manifest}]   ← from NydusAnnotationSourceDigest
    [{Digest: sha256:AAAA, Kind: index}]       ← from AnnotationSourceIndexDigest
    [{Digest: sha256:DDDD, Kind: layer}]       ← from per-layer annotation
```

**Lookup by any source artefact:** Push the source manifest's index digest,
manifest digest, or any layer digest and `QueryAccel` will find all variants.

---

## Extensibility — Adding a New Accel Type

To add `AccelZstdChunked` (a hypothetical new type):

### Step 1: Register the constant
```go
// pkg/types/types.go
const AccelZstdChunked AccelType = "zstdchunked"
var KnownAccelTypes = []AccelType{..., AccelZstdChunked}
```

### Step 2: Add annotation constants
```go
const ZstdChunkedTOCAnnotation = "io.containers.zstd-chunked.manifest-checksum"
```

### Step 3: Implement `AccelHandler`
```go
// internal/accel/zstdchunked/zstdchunked.go
type Handler struct{}
func (h *Handler) Name() types.AccelType { return types.AccelZstdChunked }
func (h *Handler) Detect(...) (types.AccelType, bool, error) { ... }
func (h *Handler) ExtractSourceRefs(...) ([]types.SourceRef, error) { ... }
func (h *Handler) Validate(...) error { ... }
```

### Step 4: Register the handler
```go
// internal/registry/registry.go → registerBuiltinHandlers()
ar.Register(zstdChunkedHandler{})
```

**That's all.** No other code changes required. The index, query, DAG, and
metadata paths all handle the new type automatically.

---

## Production Hardening Checklist

### Storage
- [ ] Replace `memory.Store` with S3/GCS `ContentStore` implementation
- [ ] Replace `metadata.Store` with PostgreSQL/BadgerDB backend
- [ ] Add WAL-backed `AccelIndex` (badger LSM or Redis with AOF)
- [ ] Configure object storage lifecycle policies for blob GC

### Network
- [ ] Add TLS termination (Let's Encrypt / cert-manager)
- [ ] Enable HTTP/2 for multiplexed blob streaming
- [ ] Configure rate limiting (token bucket per client IP)
- [ ] Add request size limits (currently 4 MiB for manifests)

### Auth & AuthZ
- [ ] Implement JWT bearer token verification middleware
- [ ] Add OCI Distribution Spec token auth challenge (WWW-Authenticate)
- [ ] Integrate with OIDC provider (Keycloak, Dex, GitHub OAuth)
- [ ] Add image-level ACL (namespace/team/public scoping)

### Reliability
- [ ] Add circuit breaker on external storage calls
- [ ] Implement retry with exponential backoff on blob store errors
- [ ] Add blob garbage collection (reference counting)
- [ ] Implement snapshot restore at startup with integrity check
- [ ] Add distributed locking (Redis) for multi-node snapshot writes

### Operations
- [ ] Kubernetes Deployment with PodDisruptionBudget
- [ ] HorizontalPodAutoscaler on HTTP request rate
- [ ] Network policy restricting egress to storage backends only
- [ ] Kubernetes ServiceMonitor for Prometheus scrape

---

## Security

### Content Integrity
- All blobs are verified by content digest on ingest (SHA-256 by default)
- Manifests reference blobs by digest — impossible to serve altered layers
- The registry only stores blobs; it never executes them

### Supply Chain
- Support cosign signature artefacts via the OCI 1.1 referrers API
- SLSA provenance attestations stored as referrers
- SBOM (SPDX, CycloneDX) attachable via referrers

### DoS Mitigations
- Manifest size limited to 4 MiB per push
- Upload sessions expire after 10 minutes
- Prometheus metrics exposed on separate port (not routable from internet)
- `ReadTimeout` and `WriteTimeout` set on all HTTP servers

---

## Observability

### Metrics (Prometheus)

| Metric | Type | Labels |
|--------|------|--------|
| `accelregistry_http_requests_total` | Counter | method, route, status |
| `accelregistry_http_request_duration_seconds` | Histogram | method, route, status |
| `accelregistry_http_request_body_bytes_total` | Counter | method, route |
| `accelregistry_http_response_body_bytes_total` | Counter | method, route |

Add domain metrics via the `types.Metrics` interface:
```go
type Metrics interface {
    IncPush(accelType AccelType)
    IncPull(accelType AccelType)
    IncQuery(found bool)
    ObserveIndexLatency(op string, d time.Duration)
    ...
}
```

### Structured Logging (Zap)

Every request logs: method, path, status, latency, requestId, remoteAddr.
Every manifest push logs: repo, ref, digest, accelType detected.
Every accel index operation logs at Debug level.

### Distributed Tracing

Add OpenTelemetry spans by wrapping the Registry interface:
```go
type TracingRegistry struct {
    inner  types.Registry
    tracer trace.Tracer
}
func (t *TracingRegistry) QueryAccel(ctx context.Context, d digest.Digest) (*AccelQueryResult, error) {
    ctx, span := t.tracer.Start(ctx, "registry.QueryAccel")
    defer span.End()
    return t.inner.QueryAccel(ctx, d)
}
```

---

## Testing Strategy

### Unit Tests (`test/unit/`)
- Every data structure tested in isolation with table-driven cases
- Concurrency tests using `t.Parallel()` and large goroutine counts
- Benchmark suite covering all hot paths

### Integration Tests (`test/integration/`)
- Full round-trip tests over `httptest.Server`
- All four accel types pushed and queried
- OCI 1.1 referrers verified
- DAG traversal verified end-to-end

### Property-Based Tests (recommended addition)
```go
// Use github.com/leanovate/gopter or github.com/nicholasgasior/go-fuzz
// to generate random manifests and verify detection invariants:
// - detect(nydus_manifest) == AccelNydus
// - extractSourceRefs(manifest).sourceDigest ∈ manifest.annotations
```

### Fuzz Targets (recommended addition)
```go
func FuzzDetect(f *testing.F) {
    f.Fuzz(func(t *testing.T, data []byte) {
        var m ocispec.Manifest
        _ = json.Unmarshal(data, &m)
        _, _, _ = registry.Detect(context.Background(), m, nil)
        // Must not panic
    })
}
```

---

## Storage Backend Swap-Out

The `types.ContentStore`, `types.AccelIndex`, `types.MetadataStore`, and
`types.ReferrersStore` interfaces are the seams for storage backend replacement.

**Minimal S3 ContentStore:**
```go
type S3Store struct{ client *s3.Client; bucket string }
func (s *S3Store) Get(ctx context.Context, dgst digest.Digest) (io.ReadCloser, error) {
    out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
        Bucket: &s.bucket, Key: aws.String(dgst.String()),
    })
    return out.Body, err
}
// Put, Exists, Delete, Info, Walk follow the same pattern
```

Replace `memory.New()` in `registry.New()` with `s3store.New(cfg)` — no other
code changes needed.
