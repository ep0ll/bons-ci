# CLAUDE.md — layermerkle codegen guidance

> This file is read by AI code-generation tools before generating or modifying
> any code in this package. **Reading this file first is mandatory.**

---

## Package purpose

`layermerkle` is a **deduplication-aware, event-driven engine** for building
per-vertex Merkle trees from file-access events across stacked overlay
filesystem layers. It solves one precise problem:

> Given a stream of `(vertexID, layerStack, relPath)` access events, compute
> the content-addressable hash of every accessed file exactly once — reusing
> cached results across identical `(layerID, relPath)` pairs regardless of
> which ExecOp triggered the access — and assemble per-vertex Merkle trees
> from the results.

It does **not** manage overlays, perform container lifecycle operations, or
interact with fanotify directly. It consumes `AccessEvent` values that callers
produce (typically from a fanwatch pipeline) and emits `MerkleTree` values.

---

## Architecture — the deduplication pipeline

```
AccessEvent (from fanwatch / custom source)
         │
         ▼
   Engine.Submit / Engine.Feed
         │
         ▼
   dispatchLoop  ──► worker goroutines (bounded by semaphore)
                           │
                           ▼
               VertexProcessor.Process
                    │            │
                    ▼            ▼
         DeduplicationEngine   MerkleTreeBuilder
              │     │
              ▼     ▼
         HashCache  LayerFileResolver
         (check)    (FindOwnerLayer → top-to-bottom stack walk)
              │
              ▼ (cache miss only)
         FileHasher → HashResult
              │
              ▼
         cache.Set(ownerLayer, relPath, hash)
         cache.Set(topLayer, relPath, hash)    ← propagation
              │
              ▼
         MerkleTreeBuilder.Add(result)
                    │
                    ▼  (on FinalizeVertex)
              MerkleTree → MerkleForest + OnTree callback
```

**The two-level cache lookup is the core deduplication invariant:**

1. Check `(topLayer, relPath)` — O(1), covers the most common repeat case.
2. `FindOwnerLayer` walks the stack top-to-bottom via `LayerFileResolver`.
3. Check `(ownerLayer, relPath)` — O(1), covers cross-ExecOp reuse.
4. Hasher is called only on complete cache miss.
5. Result is stored at both `ownerLayer` and `topLayer` keys.

---

## Key types (never break these contracts)

| Type | File | Role |
|------|------|------|
| `AccessEvent` | `event.go` | One file access: vertexID, layerStack, relPath, mask, pid |
| `HashRequest` | `event.go` | Internal: resolved request sent to DeduplicationEngine |
| `HashResult` | `event.go` | Resolved hash + attribution + cache/miss flag |
| `LayerStack` | `event.go` | Ordered slice of LayerIDs, bottommost first |
| `LayerInfo` | `layer.go` | Layer metadata: ID, DiffPath, Labels |
| `LayerRegistry` | `layer.go` | Thread-safe catalogue of registered layers |
| `HashCache` | `cache.go` | Interface: Get/Set/Delete/DeleteLayer/Len/Stats |
| `FileHasher` | `hasher.go` | Interface: Hash(ctx, absPath) FileHash |
| `LayerFileResolver` | `resolver.go` | Interface: FindOwnerLayer + DiffAbsPath |
| `EngineHook` | `hooks.go` | Observer interface for engine internals |
| `MerkleTreeBuilder` | `merkle.go` | Accumulates HashResults into a MerkleTree |
| `MerkleTree` | `merkle.go` | Per-vertex result: Root digest, Leaves, stats |
| `MerkleForest` | `merkle.go` | Thread-safe collection of MerkleTrees |
| `DeduplicationEngine` | `deduplicator.go` | Core two-level cache + singleflight |
| `VertexProcessor` | `deduplicator.go` | Routes results to per-vertex builders |
| `Engine` | `engine.go` | Top-level façade wiring all components |

---

## Coding conventions — strictly enforced

### SOLID
- **S** — each file has one concern (cache.go = caching only, resolver.go = resolution only)
- **O** — extend via new `FileHasher`/`HashCache`/`LayerFileResolver` implementations
- **L** — all interface implementations are substitutable (FakeHasher, NoopHashCache, etc.)
- **I** — interfaces are small (3–6 methods each, no fat interfaces)
- **D** — Engine depends on interfaces, never on concrete implementations

### Clean Code
1. Functions under 20 lines. If it needs more, extract helpers.
2. Names describe intent: `FindOwnerLayer`, not `resolve`; `propagateCacheToTopLayer`, not `updateCache`.
3. No boolean parameters — use named types or functional options.
4. No magic numbers — constants in `event.go` or `cache.go`.
5. Errors are values — wrap with context using `fmt.Errorf("%w", err)`.
6. Never `panic` in production paths.
7. Every public function has a godoc comment and a test.

### Concurrency patterns
- All public types safe for concurrent use unless documented otherwise.
- `shardedLRUCache` uses 256 shards to minimize lock contention.
- `singleflight.Group` in `DeduplicationEngine` coalesces identical concurrent hash requests.
- `sync.RWMutex` for read-heavy stores (registry, resolver stat cache).
- `atomic.Int64` for hot-path counters (cache hits, misses).
- Worker pool uses a semaphore channel `chan struct{}` of size `workers`.

---

## File layout — one concern per file

```
layermerkle/
├── doc.go                package-level godoc
├── errors.go             sentinel errors
├── event.go              AccessEvent, HashRequest, HashResult, LayerStack, Attr constants
├── layer.go              LayerInfo, LayerRegistry, LayerStackKey
├── hasher.go             FileHasher interface + SHA256Hasher, SingleflightHasher, HasherChain
├── cache.go              HashCache interface + shardedLRUCache, NoopHashCache, InstrumentedHashCache, TTLHashCache
├── resolver.go           LayerFileResolver interface + OverlayResolver, StaticResolver
├── hooks.go              EngineHook interface + NoopHook, HookChain, CountingHook
├── merkle.go             MerkleLeaf, MerkleNode, MerkleTree, MerkleTreeBuilder, MerkleForest
├── deduplicator.go       DeduplicationEngine, VertexProcessor
├── engine.go             Engine, EngineOption, lifecycle (Start/Stop/Submit/Feed/FinalizeVertex)
├── fanwatch.go           EnrichedEventSource adapter, AccessEventFromEnriched, EncodeLayerStack
├── layermerkle_test.go   comprehensive test suite
├── internal/
│   ├── hash/
│   │   └── pool.go       pooled hash.Hash, SumFile, SumBytes, copyBufPool
│   └── trie/
│       ├── trie.go       path-segment trie (Insert, Walk, SortedChildren, Len)
│       └── trie_test.go
└── testutil/
    └── testutil.go       FakeHasher, FakeResolver, AccessEventBuilder, LayerFixture, TreeCollector
```

---

## Adding a new FileHasher

```go
// <Algo>Hasher hashes file contents with <algorithm>.
// Must be safe for concurrent use.
type <Algo>Hasher struct{ /* immutable config */ }

func New<Algo>Hasher() *<Algo>Hasher { return &<Algo>Hasher{} }

func (h *<Algo>Hasher) Hash(ctx context.Context, absPath string) (layermerkle.FileHash, error) {
    if ctx.Err() != nil {
        return "", ctx.Err()
    }
    // open file, hash, return digest.NewDigestFromBytes(digest.<Algo>, sum)
}

func (h *<Algo>Hasher) Algorithm() string { return "<algo>" }
```

Always wrap the hasher with `NewSingleflightHasher` before passing to `NewEngine`.

---

## Adding a new HashCache implementation

Implement all six methods of the `HashCache` interface:

```go
type MyCache struct { /* ... */ }

func (c *MyCache) Get(layerID LayerID, relPath string) (FileHash, bool)  { ... }
func (c *MyCache) Set(layerID LayerID, relPath string, h FileHash)       { ... }
func (c *MyCache) Delete(layerID LayerID, relPath string)                { ... }
func (c *MyCache) DeleteLayer(layerID LayerID)                           { ... }
func (c *MyCache) Len() int                                              { ... }
func (c *MyCache) Stats() CacheStats                                     { ... }
```

Wrap with `NewInstrumentedHashCache` for OTEL/metrics integration.

---

## Adding a new LayerFileResolver

```go
// MyResolver resolves file ownership by <mechanism>.
type MyResolver struct{ /* config */ }

func (r *MyResolver) FindOwnerLayer(
    ctx context.Context,
    stack LayerStack,
    relPath string,
) (LayerID, error) {
    // Walk stack top-to-bottom.
    // Return ("", ErrWhiteout) for deleted files.
    // Return ("", ErrLayerNotFound) when no layer contains the file.
}

func (r *MyResolver) DiffAbsPath(
    ctx context.Context,
    layerID LayerID,
    relPath string,
) (string, error) {
    // Return absolute path in the layer's diff directory.
}
```

---

## Adding a new EngineHook

1. Implement `EngineHook` by embedding `NoopHook` and overriding only needed methods.
2. Register via `layermerkle.WithHook(myHook)` at engine construction.
3. Must not block or perform I/O on the hot path.

```go
type MyHook struct{ layermerkle.NoopHook }

func (h *MyHook) OnHashComplete(ctx context.Context, req layermerkle.HashRequest, hash layermerkle.FileHash) {
    // record metric, emit span attribute, etc.
}
```

---

## Test patterns

### Full engine test with fake dependencies

```go
hasher := testutil.NewFakeHasher()
resolver := testutil.NewFakeResolver()
collector := &testutil.TreeCollector{}

engine := layermerkle.NewEngine(
    layermerkle.WithFileHasher(hasher),
    layermerkle.WithResolver(resolver),
    layermerkle.WithOnTree(collector.Collect()),
    layermerkle.WithWorkers(1),
)

layerID := digest.FromString("layer-base")
resolver.AddFile("bin/sh", layerID, "/diffs/base")

ctx := context.Background()
_ = engine.Start(ctx)

ev := testutil.NewAccessEvent().
    WithVertexID("exec-op-1").
    WithLayerStack("layer-base").
    WithRelPath("bin/sh").
    Build()
_ = engine.Submit(ctx, ev)

time.Sleep(50 * time.Millisecond)

tree, _ := engine.FinalizeVertex(testutil.MakeVertexID("exec-op-1"))
engine.Stop()
```

### Verify deduplication (hasher called once for N identical requests)

```go
// Prime cache for ownerLayer — simulates prior ExecOp result.
cache.Set(layerID, "lib/libssl.so", existingHash)

// Subsequent request must not call hasher.
result := engine.Resolve(ctx, req)
assert(result.FromCache == true)
assert(hasher.CallCount() == 0)
```

### Verify deterministic Merkle root

```go
// Same file set → same root, regardless of event arrival order.
tree1 := buildTree([]string{"a", "b", "c"})
tree2 := buildTree([]string{"c", "a", "b"}) // different order
assert(tree1.Root == tree2.Root)
```

---

## Performance constraints

- The hot path (`DeduplicationEngine.Resolve`) must not allocate on a cache hit.
- `shardedLRUCache.Get` must not allocate on a cache hit (no interface boxing after the map lookup).
- `internal/hash.SumFile` uses pooled `[]byte` and pooled `hash.Hash` — do not replace with `io.ReadAll`.
- `singleflight.Group` key is a pre-computed string; do not build it inside the hot path.
- The trie uses `map[string]*Node` per node — acceptable for build-time use, not for nanosecond paths.

---

## OTEL integration

No core package imports `go.opentelemetry.io/otel`. All instrumentation goes through:

1. `EngineHook` for per-event observability.
2. `InstrumentedHashCache` wrapper for cache hit/miss metrics.
3. A separate `otel/` sub-package (not yet created) may wrap the Engine.

Attribute namespace: `layermerkle.*`

```
layermerkle.cache.hits         counter
layermerkle.cache.misses       counter
layermerkle.hash.duration_ms   histogram
layermerkle.vertex.leaf_count  gauge
layermerkle.vertex.cache_rate  gauge
layermerkle.layer.id           span attribute
layermerkle.vertex.id          span attribute
layermerkle.rel.path           span attribute
```

---

## Do not do these things

| ❌ Bad | ✅ Good |
|--------|---------|
| `panic(err)` in production paths | Return `error` |
| `log.Printf` in library code | Use `EngineHook.OnHashError` |
| Storing `*AccessEvent` without copying | Call `ev` copy via `cp := *ev` |
| Hash the merged-view file directly | Always hash the diff-layer file via `DiffAbsPath` |
| `FindOwnerLayer` inside a `Filter` | Do it in the `DeduplicationEngine` |
| Importing `internal/hash` from outside the package | Use `FileHasher` interface |
| Using `sync.Map` for the hot-path cache | Use `shardedLRUCache` with fixed shards |
| Calling `engine.FinalizeVertex` before workers drain | Sleep or use a WaitGroup / channel sync |
| Skipping singleflight for concurrent requests | Always go through `DeduplicationEngine.Resolve` |
| Comparing `digest.Digest` with `==` after string manipulation | Use `digest.Digest` typed constants directly |
