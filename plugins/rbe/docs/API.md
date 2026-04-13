# RBE API Reference

## Base URLs

| Protocol | Default address       | Purpose                         |
|----------|-----------------------|---------------------------------|
| HTTP     | `:8080`               | OCI registry + REST API         |
| gRPC     | `:9090`               | All services, streaming RPCs    |
| Metrics  | `:8081/metrics`       | Prometheus                      |

---

## Authentication

All endpoints accept credentials through **any** of the following mechanisms
(tried in order):

| Method | How to supply |
|--------|--------------|
| **mTLS** | Present a valid client certificate signed by the configured CA |
| **JWT / OIDC Bearer** | `Authorization: Bearer <token>` |
| **API Key** | `X-RBE-API-Key: <key>` (header name configurable) |

---

## 1 · OCI Distribution Spec v1 (Image Registry)

Compliant with [opencontainers/distribution-spec](https://github.com/opencontainers/distribution-spec/blob/main/spec.md).

### Base path: `/v2/`

| Method | Path | Description |
|--------|------|-------------|
| GET | `/v2/` | API version check |
| HEAD | `/v2/{name}/blobs/{digest}` | Check blob exists |
| GET | `/v2/{name}/blobs/{digest}` | Pull blob (supports `Range:`) |
| DELETE | `/v2/{name}/blobs/{digest}` | Delete blob |
| POST | `/v2/{name}/blobs/uploads/` | Initiate chunked upload |
| GET | `/v2/{name}/blobs/uploads/{uuid}` | Upload session status |
| PATCH | `/v2/{name}/blobs/uploads/{uuid}` | Send chunk |
| PUT | `/v2/{name}/blobs/uploads/{uuid}?digest=sha256:…` | Complete upload |
| DELETE | `/v2/{name}/blobs/uploads/{uuid}` | Abort upload |
| HEAD | `/v2/{name}/manifests/{reference}` | Check manifest |
| GET | `/v2/{name}/manifests/{reference}` | Pull manifest |
| PUT | `/v2/{name}/manifests/{reference}` | Push manifest |
| DELETE | `/v2/{name}/manifests/{reference}` | Delete manifest |
| GET | `/v2/{name}/tags/list` | List tags |
| GET | `/v2/{name}/referrers/{digest}` | OCI 1.1 referrers |
| GET | `/v2/{name}/blobs` | **Extension** – list all blobs for a repo/manifest |

#### Supported accelerated formats

The registry understands the following layer media types and annotates blobs
accordingly:

| Format | Media type / annotation |
|--------|------------------------|
| OCI | `application/vnd.oci.image.layer.v1.tar+gzip` |
| Docker | `application/vnd.docker.image.rootfs.diff.tar.gzip` |
| Nydus | `application/vnd.oci.image.layer.v1.tar` + `containerd.io/snapshot/nydus-bootstrap` annotation |
| eStargz | `application/vnd.oci.image.layer.v1.tar+estargz` |
| zstd:chunked | `application/vnd.oci.image.layer.v1.tar+zstd` |
| OverlayBD | any + `containerd.io/snapshot/overlaybd` annotation |

---

## 2 · DAG API

All endpoints under `/v1/dags`.

### DAG lifecycle

```
POST   /v1/dags                      Create DAG
GET    /v1/dags                      List DAGs  ?build_id= &status= &limit=
GET    /v1/dags/{dag_id}             Get DAG
DELETE /v1/dags/{dag_id}             Delete DAG
PATCH  /v1/dags/{dag_id}/status      Update DAG status
```

**Create DAG body:**
```json
{
  "build_id": "build-xyz",
  "name": "frontend-build",
  "labels": {"team": "platform"},
  "description": "CI build for PR #42",
  "platform": {"os": "linux", "arch": "amd64"},
  "created_by": "ci-bot"
}
```

### Vertex lifecycle

```
POST   /v1/dags/{dag_id}/vertices                    Add vertex
GET    /v1/dags/{dag_id}/vertices                    List vertices ?status= &limit=
GET    /v1/dags/{dag_id}/vertices/{vertex_id}        Get vertex
PATCH  /v1/dags/{dag_id}/vertices/{vertex_id}/status Update vertex status
GET    /v1/dags/{dag_id}/vertices/{vertex_id}/deps   Full dependency tree  ?max_depth=
GET    /v1/dags/{dag_id}/vertices/{vertex_id}/cache  List cache entries for vertex
```

**Add vertex body:**
```json
{
  "name": "compile",
  "op_type": "exec",
  "op_payload": "<base64-encoded op definition>",
  "inputs": [
    {
      "vertex_id": "dep-vertex-id",
      "output_idx": 0,
      "files": [
        {"path": "/lib/foo.a", "digest": "sha256:…", "size": 4096}
      ]
    }
  ],
  "input_files": [
    {"path": "/src/main.go", "digest": "sha256:…", "size": 1234}
  ],
  "env": {"CGO_ENABLED": "0"},
  "platform": {"os": "linux", "arch": "amd64"},
  "mounts": [
    {"type": "cache", "target": "/root/.cache/go", "cache_id": "go-mod-cache", "sharing": "shared"}
  ]
}
```

**Dependency tree response** — returns a recursive `DependencyNode`:
```json
{
  "vertex": { "id": "…", "name": "link", … },
  "provided_files": [{"path": "/out/app", "digest": "sha256:…"}],
  "deps": [
    {
      "vertex": { "id": "…", "name": "compile", … },
      "provided_files": [{"path": "/obj/main.o", "digest": "sha256:…"}],
      "deps": []
    }
  ]
}
```

---

## 3 · Cache API

```
POST /v1/cache/check         Check cache hit by key
POST /v1/cache/store         Store a cache entry
GET  /v1/cache/{cache_key}   Fetch a cache entry
DELETE /v1/cache/{cache_key} Delete a cache entry
POST /v1/cache/compute-key   Compute content-addressed key from inputs
POST /v1/cache/invalidate    Invalidate entries  ?cascade=true
```

**Check cache body:**
```json
{
  "cache_key": "sha256:…",
  "vertex_id": "v42",
  "with_files": true
}
```

**Compute key body:**
```json
{
  "op_digest":         "sha256:…",
  "input_file_hashes": ["sha256:…", "sha256:…"],
  "dep_cache_keys":    ["sha256:…"],
  "platform":          {"os": "linux", "arch": "amd64"},
  "selector":          ""
}
```

**Cache key algorithm** (deterministic sha256):

```
sha256(
  "op:<op_type>
"
  + op_payload_bytes
  + sorted(env key=value pairs)
  + "platform:<os>/<arch>/<variant>
"
  + sorted(input_files by path: "file:<path>=<digest>
")
  + sorted(dep_vertex_ids + dep_files: "dep:<…>
")
  + sorted(mount cache_ids: "mount:cache:<id>:<sharing>
")
)
```

---

## 4 · Log Streaming API

```
POST   /v1/logs/streams                                  Create log stream
GET    /v1/logs/streams/{stream_id}                      Stream metadata
POST   /v1/logs/streams/{stream_id}/chunks               Ingest chunk (JSON)
GET    /v1/logs/streams/{stream_id}/chunks               Fetch buffered  ?from_seq= &to_seq= &limit=
POST   /v1/logs/streams/{stream_id}/close                Close stream
WS     /v1/logs/streams/{stream_id}/tail                 Live tail (WebSocket)  ?from_seq= &follow=true
GET    /v1/logs/vertex/{dag_id}/{vertex_id}              All vertex logs  ?fd=1&fd=2&interleaved=true
GET    /v1/logs/vertex/{dag_id}/{vertex_id}/streams      List streams for vertex
```

**Create stream body:**
```json
{
  "vertex_id": "v42",
  "dag_id":    "dag-1",
  "fd_type":   2,
  "fd_num":    2,
  "metadata":  {"container": "builder"}
}
```

**FD type values:**

| fd_type | Meaning |
|---------|---------|
| 0 | stdin |
| 1 | stdout |
| 2 | stderr |
| 3 | progress / buildkit events |
| 99 | other / custom FD |

**WebSocket tail message** — each message is a JSON `LogChunk`:
```json
{
  "stream_id": "…",
  "sequence":  42,
  "data":      "<base64>",
  "timestamp": "2025-01-01T00:00:00.000Z",
  "fd_type":   1,
  "fd_num":    1
}
```

**gRPC streaming alternatives:**

| gRPC method | Direction | Description |
|-------------|-----------|-------------|
| `LogService.UploadLogs` | client stream | Push chunks from builder |
| `LogService.TailLogs` | server stream | Live tail with backfill |

---

## 5 · Mount Cache API  (BuildKit `--mount=type=cache`)

```
POST   /v1/mountcache            Create / get-or-create cache
GET    /v1/mountcache            List  ?scope= &os= &arch= &limit=
GET    /v1/mountcache/{id}       Get by ID
DELETE /v1/mountcache/{id}       Delete
POST   /v1/mountcache/{id}/lock  Acquire exclusive lock
POST   /v1/mountcache/{id}/unlock Release lock
PUT    /v1/mountcache/{id}/data  Upload cache blob (streaming)
GET    /v1/mountcache/{id}/data  Download  ?offset= &length=
POST   /v1/mountcache/prune      Prune stale caches
```

**Sharing modes:**

| mode | Behaviour |
|------|-----------|
| `shared` | Concurrent reads + writes (best for read-heavy caches) |
| `private` | Copy-on-write snapshot per build |
| `locked` | Exclusive single-writer, other builders wait / retry |

---

## 6 · Attestation API

```
POST /v1/attestations                         Attach raw attestation
GET  /v1/attestations/{subject_digest}        Get attestations  ?type=sbom-spdx
POST /v1/attestations/sbom                    Attach SBOM (SPDX or CycloneDX)
GET  /v1/attestations/{subject_digest}/sbom   Get SBOM  ?format=sbom-spdx
POST /v1/attestations/slsa                    Record SLSA provenance
GET  /v1/attestations/{subject_digest}/slsa   Get SLSA provenance
POST /v1/attestations/sign                    Sign artifact (cosign/sigstore)
POST /v1/attestations/verify                  Verify signature
```

**SLSA provenance body:**
```json
{
  "subject_digest": "sha256:…",
  "subject_repo":   "myrepo/myimage",
  "provenance": {
    "build_type":   "https://rbe.example.com/build/v1",
    "builder_id":   "rbed/v0.1.0",
    "materials":    ["https://github.com/org/repo@sha256:…"],
    "started_at":   "2025-01-01T00:00:00Z",
    "finished_at":  "2025-01-01T00:05:00Z",
    "reproducible": false,
    "dag_id":       "dag-1"
  },
  "sign":      true,
  "keyless":   true,
  "oidc_token": "<fulcio-compatible OIDC token>"
}
```

---

## 7 · Conversion Tracking API

```
POST /v1/conversions          Record a conversion
GET  /v1/conversions/{id}     Get conversion record
GET  /v1/conversions          List  ?source_digest= &target_format=nydus
POST /v1/conversions/check    Check if conversion exists + verify all blobs
GET  /v1/conversions/diff     Blob diff  ?src=sha256:…&dst=sha256:…
```

**Check conversion body:**
```json
{
  "source_digest": "sha256:…",
  "target_format": "nydus",
  "verify_blobs":  true
}
```

**Response:**
```json
{
  "exists":          true,
  "record":          { "id": "…", "source_blobs": [], "target_blobs": [] },
  "missing_blobs":   [],
  "fully_available": true
}
```

---

## 8 · gRPC API

All HTTP endpoints have exact gRPC equivalents in:

| Proto service | File |
|---------------|------|
| `DAGService` | `proto/rbe/v1/rbe.proto` |
| `CacheService` | `proto/rbe/v1/rbe.proto` |
| `LogService` | `proto/rbe/v1/rbe.proto` |
| `MountCacheService` | `proto/rbe/v1/rbe.proto` |
| `RegistryService` | `proto/registry/v1/registry.proto` |
| `AttestationService` | `proto/registry/v1/registry.proto` |

gRPC-specific advantages:

| Feature | gRPC |
|---------|------|
| Log ingest | `LogService.UploadLogs` — client-streaming, minimal overhead |
| Live tail | `LogService.TailLogs` — server-streaming, no WebSocket upgrade |
| Mount cache data | `MountCacheService.UploadMountCacheData` / `DownloadMountCacheData` — bidirectional streaming |
| Multiplexed conns | HTTP/2 framing, single connection per builder |

**Use `grpc-gateway`** to expose the gRPC API over HTTP/JSON automatically
(see `Makefile: proto` target).

---

## 9 · Observability

| Endpoint | Description |
|----------|-------------|
| `GET /health` | Liveness check → `{"status":"ok"}` |
| `GET /ready` | Readiness check |
| `GET /metrics` | Prometheus metrics (or separate `:8081/metrics` if configured) |

**Key metrics:**

| Metric | Type | Labels |
|--------|------|--------|
| `rbe_blob_uploads_total` | counter | `repo`, `status` |
| `rbe_blob_downloads_total` | counter | `repo`, `status` |
| `rbe_blob_size_bytes` | histogram | `direction` |
| `rbe_manifest_operations_total` | counter | `op`, `repo`, `status` |
| `rbe_dags_created_total` | counter | — |
| `rbe_vertices_total` | counter | `status`, `op_type` |
| `rbe_vertex_duration_seconds` | histogram | `op_type`, `status` |
| `rbe_cache_hits_total` | counter | `result` (hit/miss) |
| `rbe_log_chunks_ingested_total` | counter | `fd_type` |
| `rbe_log_bytes_ingested_total` | counter | — |
| `rbe_http_request_duration_seconds` | histogram | `method`, `path`, `status` |
| `rbe_grpc_request_duration_seconds` | histogram | `method`, `status` |

OpenTelemetry traces are exported via OTLP gRPC to the endpoint configured in
`observability.otlp_endpoint`.
