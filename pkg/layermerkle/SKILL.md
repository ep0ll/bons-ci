# SKILL: layermerkle — deduplication-aware Merkle tree engine for overlay layers

## Identity

**Package path:** `github.com/bons/bons-ci/pkg/layermerkle`
**Language:** Go 1.22+
**Build target:** Any platform (no cgo, no Linux-only syscalls)
**Dependencies:** `opencontainers/go-digest`, `golang.org/x/sync/singleflight`

---

## What this skill does

After reading this file the AI assistant knows:

1. The **full type vocabulary** (AccessEvent → DeduplicationEngine → MerkleTree pipeline)
2. The **deduplication invariant** (two-level cache lookup, singleflight coalescing)
3. The **extension points** (FileHasher, HashCache, LayerFileResolver, EngineHook)
4. The **correct construction pattern** for Engine and its dependencies
5. The **test patterns** (FakeHasher, FakeResolver, AccessEventBuilder, TreeCollector)
6. The **naming rules** and SOLID constraints

---

## Type vocabulary quick reference

```
AccessEvent       — input: vertexID, layerStack, relPath, abspath, mask, pid, timestamp
HashRequest       — internal: vertexID + layerStack + relPath + absPath
HashResult        — output: vertexID + relPath + hash + ownerLayerID + fromCache + err

LayerID           = digest.Digest  (content-addressable layer identifier)
VertexID          = digest.Digest  (ExecOp vertex identifier)
FileHash          = digest.Digest  (content-addressable file hash)
LayerStack        = []LayerID       (bottommost first, topmost last)
LayerStackKey     = string          ("|"-joined digest list for map keys)

LayerInfo         — ID, DiffPath, ChainID, Labels
LayerRegistry     — thread-safe: Register, Unregister, Get, ResolveStack, Len

HashCache         — interface: Get, Set, Delete, DeleteLayer, Len, Stats
CacheStats        — Hits, Misses, Evictions, Capacity, Used, HitRate()

FileHasher        — interface: Hash(ctx, absPath) (FileHash, error), Algorithm() string
LayerFileResolver — interface: FindOwnerLayer(ctx, stack, relPath), DiffAbsPath(ctx, layerID, relPath)
EngineHook        — interface: OnCacheHit, OnHashStart, OnHashComplete, OnHashError,
                              OnEventDropped, OnVertexFinalized

MerkleLeaf        — RelPath, Hash, OwnerLayerID, FromCache
MerkleNode        — Path, Hash []byte, Children, Leaf
MerkleTree        — VertexID, LayerStack, Root digest, RootNode, Leaves, LeafCount, CacheHitCount, FinalizedAt
MerkleForest      — thread-safe: Add, Get, All, Len

Engine            — top-level façade: Start, Stop, Submit, Feed, FinalizeVertex, Registry, Forest, CacheStats
```

---

## Interface signatures (memorise these)

```go
type FileHasher interface {
    Hash(ctx context.Context, absPath string) (FileHash, error)
    Algorithm() string
}

type HashCache interface {
    Get(layerID LayerID, relPath string) (FileHash, bool)
    Set(layerID LayerID, relPath string, h FileHash)
    Delete(layerID LayerID, relPath string)
    DeleteLayer(layerID LayerID)
    Len() int
    Stats() CacheStats
}

type LayerFileResolver interface {
    FindOwnerLayer(ctx context.Context, stack LayerStack, relPath string) (LayerID, error)
    DiffAbsPath(ctx context.Context, layerID LayerID, relPath string) (string, error)
}

type EngineHook interface {
    OnCacheHit(ctx context.Context, req HashRequest, result *HashResult)
    OnHashStart(ctx context.Context, req HashRequest)
    OnHashComplete(ctx context.Context, req HashRequest, h FileHash)
    OnHashError(ctx context.Context, req HashRequest, err error)
    OnEventDropped(ctx context.Context, ev *AccessEvent, reason error)
    OnVertexFinalized(ctx context.Context, tree *MerkleTree)
}
```

---

## Canonical engine construction

```go
registry := layermerkle.NewLayerRegistry()

// Register every layer that may appear in events.
_ = registry.Register(&layermerkle.LayerInfo{
    ID:       digest.FromString("layer-sha256:abc"),
    DiffPath: "/var/lib/containerd/io.containerd.snapshotter.v1.overlayfs/snapshots/42/fs",
})

engine := layermerkle.NewEngine(
    // FileHasher — wraps SHA256Hasher with singleflight coalescing.
    layermerkle.WithFileHasher(
        layermerkle.NewSingleflightHasher(layermerkle.NewSHA256Hasher()),
    ),

    // HashCache — 256-shard LRU, ~128 K entries total.
    layermerkle.WithHashCache(layermerkle.NewShardedLRUCache(128_000)),

    // Resolver — Lstat-based, uses registry for DiffPath lookup.
    layermerkle.WithResolver(
        layermerkle.NewOverlayResolver(registry, 200_000),
    ),

    // Hooks — optional observability.
    layermerkle.WithHook(&myOTELHook{}),

    // Worker pool.
    layermerkle.WithWorkers(runtime.NumCPU()),

    // Called once per finalized vertex.
    layermerkle.WithOnTree(func(t *layermerkle.MerkleTree) {
        slog.Info("vertex finalized",
            "vertex", t.VertexID,
            "root", t.Root,
            "leaves", t.LeafCount,
            "hit_rate", t.CacheHitRate(),
        )
    }),
)

ctx, cancel := context.WithCancel(context.Background())
defer cancel()

if err := engine.Start(ctx); err != nil {
    log.Fatal(err)
}

// Feed from fanwatch pipeline.
engine.Feed(ctx, accessEventCh, func(err error) {
    slog.Error("engine event error", "err", err)
})

// When an ExecOp completes:
tree, err := engine.FinalizeVertex(execOpVertexID)

engine.Stop()
```

---

## Writing a new FileHasher (template)

```go
// <Algo>Hasher hashes file contents using <algorithm>.
// Concurrent-safe; uses internal pool to avoid allocations.
type <Algo>Hasher struct{}

func New<Algo>Hasher() *<Algo>Hasher { return &<Algo>Hasher{} }

func (h *<Algo>Hasher) Hash(ctx context.Context, absPath string) (layermerkle.FileHash, error) {
    if ctx.Err() != nil {
        return "", ctx.Err()
    }
    f, err := os.Open(absPath)
    if err != nil {
        if os.IsNotExist(err) {
            return "", layermerkle.ErrWhiteout
        }
        return "", fmt.Errorf("%w: %s: %v", layermerkle.ErrHashFailed, absPath, err)
    }
    defer f.Close()
    // ... compute hash ...
    return digest.NewDigestFromBytes(digest.<Algo>, sum), nil
}

func (h *<Algo>Hasher) Algorithm() string { return "<algo>" }
```

---

## Writing a new EngineHook (template)

```go
// <Name>Hook observes <what>.
type <Name>Hook struct {
    layermerkle.NoopHook  // embed to inherit no-op defaults
    // config / state
}

func (h *<Name>Hook) OnCacheHit(ctx context.Context, req layermerkle.HashRequest, _ *layermerkle.HashResult) {
    // fast, non-blocking — called on worker goroutine
}

func (h *<Name>Hook) OnHashComplete(ctx context.Context, req layermerkle.HashRequest, hash layermerkle.FileHash) {
    // fast, non-blocking
}

func (h *<Name>Hook) OnVertexFinalized(ctx context.Context, tree *layermerkle.MerkleTree) {
    // fast, non-blocking
}
```

---

## fanwatch integration pattern

```go
// 1. Attach layer context via DynamicAttrTransformer in the fanwatch pipeline.
layerAttrs := fanwatch.DynamicAttrTransformer(func(e *fanwatch.EnrichedEvent) map[string]any {
    return layermerkle.AccessEventAttrs(
        execVertexID,
        currentLayerStack,
        e.Name,   // relative path inside merged dir
    )
})

pipeline := fanwatch.NewPipeline(
    fanwatch.WithFilter(fanwatch.ReadOnlyFilter()),
    fanwatch.WithTransformer(layerAttrs),
    fanwatch.WithHandler(myForwardHandler),
)

// 2. In the handler, decode and submit.
type forwardHandler struct{ engine *layermerkle.Engine }

func (h *forwardHandler) Handle(ctx context.Context, e *fanwatch.EnrichedEvent) error {
    av := &accessEventAdapter{e}
    ev, err := layermerkle.AccessEventFromEnriched(av)
    if err != nil {
        return nil // skip events without layer attrs
    }
    return h.engine.Submit(ctx, ev)
}
```

---

## Test patterns

### Fake dependencies

```go
hasher   := testutil.NewFakeHasher()
resolver := testutil.NewFakeResolver()

// Register file ownership.
layerID := digest.FromString("my-layer")
resolver.AddFile("lib/libssl.so", layerID, "/diffs/layer0")

// Build engine.
engine := layermerkle.NewEngine(
    layermerkle.WithFileHasher(hasher),
    layermerkle.WithResolver(resolver),
    layermerkle.WithWorkers(1),
)
```

### Build events

```go
ev := testutil.NewAccessEvent().
    WithVertexID("exec-op-1").
    WithLayerStack("layer-0", "layer-1").
    WithRelPath("usr/bin/python3").
    WithAbsPath("/merged/usr/bin/python3").
    WithMask(0x00000001). // ACCESS
    Build()
```

### Assert deduplication

```go
// Hash should be called once even for N events for the same file.
for range 100 {
    _ = engine.Submit(ctx, ev)
}
time.Sleep(50 * time.Millisecond)
if hasher.CallCount() > 1 {
    t.Errorf("hasher called %d times, want ≤1", hasher.CallCount())
}
```

### Assert Merkle tree output

```go
tree, _ := engine.FinalizeVertex(testutil.MakeVertexID("exec-op-1"))
if tree.Root == "" {
    t.Error("root should not be empty")
}
if tree.LeafCount == 0 {
    t.Error("tree should have at least one leaf")
}
if tree.CacheHitRate() < 0.5 {
    t.Errorf("low cache hit rate: %.2f", tree.CacheHitRate())
}
```

---

## Naming conventions

| Thing | Pattern | Example |
|-------|---------|---------|
| FileHasher returning impl | `<Algo>Hasher` | `SHA256Hasher`, `BLAKE3Hasher` |
| HashCache returning impl | `<Strategy>HashCache` | `ShardedLRUHashCache`, `TTLHashCache` |
| LayerFileResolver | `<Source>Resolver` | `OverlayResolver`, `StaticResolver` |
| EngineHook impl | `<Concern>Hook` | `CountingHook`, `OTELHook` |
| Engine option | `With<Thing>` | `WithFileHasher`, `WithWorkers`, `WithOnTree` |
| Error sentinels | `Err<Noun>` | `ErrWhiteout`, `ErrLayerNotFound`, `ErrCacheFull` |
| Test fakes | `Fake<Type>` | `FakeHasher`, `FakeResolver` |
| Test builders | `New<Type>` (returns `*<Type>Builder`) | `NewAccessEvent()` |
| Test helpers | descriptive verb + noun | `MakeLayerStack`, `EventBatch`, `MakeFileHash` |

---

## Common mistakes to avoid

| Mistake | Correction |
|---------|------------|
| Hashing the merged-view file | Always use `DiffAbsPath` to hash within the layer diff |
| Walking the layer stack yourself | Use `LayerFileResolver.FindOwnerLayer` |
| Calling the hasher for every event | Always go through `DeduplicationEngine.Resolve` |
| Forgetting to propagate cache to topLayer | The engine does this; don't duplicate it |
| Calling `FinalizeVertex` while workers are still running | Wait with `time.Sleep` or a sync mechanism |
| Using `sync.Map` for the hash cache | Use `shardedLRUCache` for bounded, evicting storage |
| Writing a blocking EngineHook | Hooks run on worker goroutines; blocking stalls the pipeline |
| Comparing digests after string ops | Use `digest.Digest` typed values directly |
| Ignoring `ErrWhiteout` in a resolver | Whiteouts mean the file is deleted; return nil hash, not an error for the caller to wrap |
