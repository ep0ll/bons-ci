# layermerkle

**Deduplication-aware, event-driven Merkle tree engine for stacked overlay filesystem layers.**

`layermerkle` ingests file-access events from a fanotify pipeline (e.g. [fanwatch](../fswatch))
and builds per-ExecOp Merkle trees — computing each file's hash **exactly once** across all
stacked layers and all concurrent ExecOps.

---

## The problem it solves

In a BuildKit / containerd build graph each `ExecOp` runs inside a container whose rootfs is
a union of N read-only layers plus a writable top layer:

```
ExecOp-2  ──► merged view = [ layer-base | layer-app | layer-2-diff ]
ExecOp-1  ──► merged view = [ layer-base | layer-app | layer-1-diff ]
```

When both ExecOps read `/usr/lib/libssl.so` and that file lives unchanged in `layer-base`,
naively hashing it for each ExecOp wastes I/O. `layermerkle` eliminates that waste:

> Hash `(layerID, relPath)` once. Cache forever. Any ExecOp that reads the same file from
> the same layer gets the cached result instantly — **zero disk reads**.

The output is one `MerkleTree` per ExecOp whose root is the content-addressable digest of
every file accessed during that execution. Identical access patterns → identical roots →
solver-level cache reuse.

---

## Features

| | |
|--|--|
| ⚡ **Two-level cache** | `(topLayer, path)` and `(ownerLayer, path)` checked before hashing |
| 🔒 **Singleflight coalescing** | Concurrent requests for the same file collapse into one I/O call |
| 🗂 **256-shard LRU** | Near-zero lock contention under high parallelism |
| 💾 **Hard-link dedup** | Same inode → same hash; `HardLinkAwareHasher` prevents re-reading |
| 🗃 **Persistent WAL** | `PersistentHashCache` survives process restarts via append-only WAL |
| 🔥 **Warm start** | `NewEngineWithWarmStart` pre-populates cache from a prior run's provenance |
| 🧩 **Composable** | Swap FileHasher, HashCache, LayerFileResolver, or add EngineHook without touching the engine |
| 🔭 **Observable** | OTELHook, PrometheusHook, SlogHook — zero SDK import in the core |
| 📄 **Provenance** | Export `OCIProvenance` records and diff them across builds |
| 📊 **Reports** | HTML + GitHub-Flavored Markdown diff reports for PR comments |
| 🛠 **CLI** | `lminspect` — inspect, diff, stats, JSON, vertex filter |
| 🧪 **Test-first** | `testutil` ships FakeHasher, FakeResolver, OverlayFixture, TreeCollector |
| 🐧 **OCI-compliant** | Recursive opaque whiteout, per-file whiteout, metadata whiteout |
| 📦 **stdlib only** | Zero external dependencies — `opencontainers/go-digest` replaced internally |

---

## Quick start

```go
import "github.com/bons/bons-ci/pkg/layermerkle"

// 1. Register layers.
registry := layermerkle.NewLayerRegistry()
_ = registry.Register(&layermerkle.LayerInfo{
    ID:       digest.FromString("sha256:abc..."),
    DiffPath: "/var/lib/containerd/.../snapshots/42/fs",
})

// 2. Build the engine.
engine := layermerkle.NewEngine(
    layermerkle.WithFileHasher(
        layermerkle.NewSingleflightHasher(layermerkle.NewSHA256Hasher()),
    ),
    layermerkle.WithResolver(
        layermerkle.NewOverlayResolver(registry, 200_000),
    ),
    layermerkle.WithPredefinedRegistry(registry),
    layermerkle.WithWorkers(runtime.NumCPU()),
    layermerkle.WithOnTree(func(t *layermerkle.MerkleTree) {
        slog.Info("vertex done", "root", t.Root, "leaves", t.LeafCount,
            "hit_rate", t.CacheHitRate())
    }),
)

ctx, cancel := context.WithCancel(context.Background())
defer cancel()
_ = engine.Start(ctx)

// 3. Feed events from fanwatch.
engine.Feed(ctx, accessEventCh, nil)

// 4. Seal a vertex when the ExecOp finishes.
tree, err := engine.FinalizeVertex(execOpVertexID)

// 5. Export provenance.
p := layermerkle.ExportProvenance(engine.Forest(),
    layermerkle.WithProvenanceBuildID("build-001"),
)
layermerkle.WriteProvenance(os.Stdout, p)

engine.Stop()
```

---

## Architecture

```
AccessEvent (from fanwatch or custom source)
        │
        ▼  Engine.Submit / Engine.Feed / Engine.SubmitBatch
  dispatchLoop ──► bounded worker pool (semaphore)
                         │
                         ▼
             VertexProcessor.Process
                  │              │
                  ▼              ▼
       DeduplicationEngine   MerkleTreeBuilder
            │        │              │
            ▼        ▼              ▼ (on FinalizeVertex)
       ShardedLRU  LayerFileResolver  MerkleTree → MerkleForest
      (2-level,    (stack walk,        │
       256 shards)  Lstat/OCI whiteout)│
            │                         ▼
            ▼                    OCIProvenance
        FileHasher              WriteHTMLReport / WriteMarkdownReport
       (SHA-256 +
        Singleflight +
        HardLinkAware)
```

### Deduplication invariant (always maintained)

```
for each (layerStack, relPath):
  1. cache.Get(topLayer, relPath)           →  O(1) L1 hit
  2. singleflight.Do(stackKey+relPath, fn)  →  coalesce concurrent identical requests
  3. resolver.FindOwnerLayer(stack, relPath) → O(stack_depth) Lstat walk (cached)
  4. cache.Get(ownerLayer, relPath)          →  O(1) L2 hit (cross-ExecOp reuse)
  5. hasher.Hash(diffAbsPath)               →  only on complete miss
  6. cache.Set(ownerLayer, relPath, hash)
     cache.Set(topLayer,   relPath, hash)   →  propagate for next lookup
```

---

## Package inventory

| Package | Files | Lines | Purpose |
|---------|-------|-------|---------|
| `layermerkle` | 30 | 6 250 | Core engine, all interfaces and implementations |
| `cmd/lminspect` | 2 | 320 | CLI: inspect / diff / stats / JSON / Markdown |
| `otel/` | 2 | 200 | OTELHook — spans, counters, histograms |
| `prometheus/` | 2 | 130 | PrometheusHook — Prometheus text format |
| `testutil/` | 1 | 380 | FakeHasher, FakeResolver, builders, OverlayFixture |
| `internal/digest` | 2 | 100 | Stdlib OCI digest type |
| `internal/hash` | 2 | 90 | Pooled SHA-256, SumFile, SumBytes |
| `internal/singleflight` | 2 | 120 | Group + ContextGroup |
| `internal/trie` | 2 | 140 | Path-segment trie for Merkle construction |

---

## Benchmark results (Intel i7-12700, Go 1.22)

| Benchmark | Throughput | Allocs |
|-----------|-----------|--------|
| `RingStatCache.Get` (hit, 0 alloc) | **42 M/sec** | 0 B/op |
| `ClassifyWhiteout` | **1.6 G/sec** | 0 B/op |
| `DeduplicationEngine.Resolve` (L1 hit) | **3.1 M/sec** | 192 B/op |
| `DeduplicationEngine.Resolve` (L2 cross-layer hit) | **3.0 M/sec** | 192 B/op |
| `DeduplicationEngine.Resolve` (parallel, 8 goroutines) | **24 M/sec** | 176 B/op |
| `ShardedLRU.Get` (warm) | **4.4 M/sec** | 96 B/op |
| `ShardedLRU.Set` (parallel, 8 goroutines) | **6.6 M/sec** | 304 B/op |
| `Engine.Submit` (sequential) | **2.2 M/sec** | 50 B/op |
| `MerkleTreeBuilder.Finalize` (1 000 leaves) | **764/sec** | 600 KB/op |
| `ForestDiff` (100 vertices) | **5.3 K/sec** | 17 KB/op |

---

## Built-in components

### FileHashers

| Type | Description |
|------|-------------|
| `SHA256Hasher` | Pooled SHA-256, 1 alloc/op for the hash sum |
| `SingleflightHasher` | Wraps any hasher; coalesces concurrent same-path calls |
| `HardLinkAwareHasher` | Inode+device secondary cache; hashes each unique inode once |
| `HasherChain` | Tries hashers in order; useful for algorithm migration |

### HashCaches

| Type | Description |
|------|-------------|
| `ShardedLRUCache` | 256-shard LRU, size-bounded with clock eviction |
| `PersistentHashCache` | Append-only WAL; survives process restart; auto-compact |
| `TTLHashCache` | Wraps any cache with entry expiry |
| `InstrumentedHashCache` | Hook-based Get/Set instrumentation for OTEL/Prometheus |
| `NoopHashCache` | Always miss; for benchmarks |

### LayerFileResolvers

| Type | Description |
|------|-------------|
| `OverlayResolver` | Lstat-based; bounded `map` stat cache |
| `OverlayResolverWithRingCache` | Same, but clock-eviction RingStatCache (bounded memory) |
| `OCICompliantResolver` | Full OCI: recursive ancestor opaque whiteout detection |
| `StaticResolver` | Pre-populated map; for tests and offline analysis |

### EngineHooks

| Type | Description |
|------|-------------|
| `CountingHook` | Atomic counters for all 6 event types |
| `SlogHook` | `log/slog` output at configurable levels |
| `OTELHook` | Spans, cache counters, latency histogram (zero SDK import) |
| `PrometheusHook` | Atomic counters + `http.Handler` (Prometheus text format) |

---

## OCI whiteout handling

The `OCICompliantResolver` and `WhiteoutIndex` both implement the full OCI image
layer whiteout specification:

| Whiteout type | Filename | What it masks |
|--------------|----------|---------------|
| Per-file | `.wh.<name>` | Specific file `<name>` in the same directory |
| Opaque | `.wh..wh..opq` | All files from lower layers in this directory and all subdirectories |
| Metadata | `.wh..wh.*` | Docker-internal metadata |

`OpaqueWhiteoutPaths(relPath)` returns the full ancestor chain to check,
deepest first, so resolution always respects the most specific mask.

---

## Provenance & reporting

```go
// Export build provenance.
p := layermerkle.ExportProvenance(forest,
    layermerkle.WithProvenanceBuildID("ci-run-42"),
    layermerkle.WithProvenanceFiles(true),
)
layermerkle.WriteProvenance(os.Stdout, p)

// Diff two provenance records.
result := layermerkle.DiffProvenance(prev, curr)

// HTML report for CI dashboard.
layermerkle.WriteHTMLReport(w, forestDiff, layermerkle.HTMLReportConfig{
    Title:             "Build #42 vs #41",
    BuildID:           "ci-42",
    MaxFilesPerVertex: 50,
})

// Markdown for GitHub PR comment.
layermerkle.WriteMarkdownReport(w, forestDiff, "ci-42")
```

---

## Warm start

```go
// Load provenance from prior run.
prevProv, _ := os.ReadFile("prev-run.json")
p, _ := layermerkle.ReadProvenance(bytes.NewReader(prevProv))

engine, warmResult, err := layermerkle.NewEngineWithWarmStart(ctx,
    layermerkle.WarmStartConfig{
        Provenance:    p,
        EngineOptions: []layermerkle.EngineOption{...},
    },
)
slog.Info("warm start", "warmed", warmResult.Warmed, "skipped", warmResult.Skipped)
```

---

## CLI — lminspect

```bash
# Inspect a forest file.
lminspect forest.json

# JSON output.
lminspect -json forest.json

# Per-vertex cache statistics.
lminspect -stats forest.json

# Compare two builds.
lminspect -diff before.json after.json

# Filter to a specific vertex.
lminspect -vertex sha256:abc123 forest.json

# Read from stdin.
cat forest.json | lminspect -stats
```

---

## Testing

```bash
# Unit tests (no root, no filesystem).
go test -race ./...

# Run benchmarks.
go test -bench=. -benchmem -benchtime=1s .

# Integration tests (requires root + Linux + real overlay dirs).
go test -tags integration -count=1 -v ./...
```

---

## License

MIT
