# layermerkle — TODO & Progress Tracker

## Status: Complete ✅  (160 tests · 9 packages · 0 race conditions · 0 vet warnings)

---

## Completed — full feature set

### Core package (34 production files / 6250 lines)

**Interfaces & Contracts**
- [x] `FileHasher` — SHA256Hasher, SingleflightHasher, HasherChain, HardLinkAwareHasher
- [x] `HashCache` — ShardedLRUCache (256-shard LRU), InstrumentedHashCache, TTLHashCache, NoopHashCache, PersistentHashCache (WAL)
- [x] `LayerFileResolver` — OverlayResolver, OverlayResolverWithRingCache, OCICompliantResolver, StaticResolver
- [x] `EngineHook` — NoopHook, HookChain, CountingHook, SlogHook, OTELHook, PrometheusHook

**Core engine**
- [x] DeduplicationEngine — two-level cache + singleflight Group + ContextGroup
- [x] VertexProcessor — one MerkleTreeBuilder per active vertex, thread-safe
- [x] Engine — Start/Stop/Submit/Feed/FinalizeVertex/SubmitBatch
- [x] WithPredefinedRegistry, all EngineOptions

**Merkle trees**
- [x] MerkleLeaf, MerkleNode, MerkleTreeBuilder — deterministic, sorted, depth-first
- [x] MerkleTree — Root, Leaves, LeafCount, CacheHitCount, CacheHitRate
- [x] MerkleForest — thread-safe, Add/Get/All (sorted)

**Overlay & layer support**
- [x] LayerInfo, LayerRegistry — thread-safe, Register/Unregister/Get/ResolveStack
- [x] LayerStack — Top/Contains/Valid, LayerStackKey, NewLayerStackKey
- [x] OCI whiteout: per-file `.wh.<name>`, opaque `.wh..wh..opq` (recursive ancestor), meta `.wh..wh.*`
- [x] WhiteoutIndex — pre-scanned O(1) lookup from filesystem walk
- [x] StatResult exported for external packages

**Performance**
- [x] RingStatCache — clock-eviction, bounded O(1), InvalidateLayer
- [x] CacheKeyEncoder — zero-alloc SHA-256 binary key
- [x] fixedCacheKey — [4]uint64 comparable, no string boxing
- [x] HardLinkAwareHasher — inode+dev secondary index (linux/other platform split)
- [x] ContextGroup singleflight — cancellation-aware, goroutine safety

**Serialization & export**
- [x] MarshalForest/UnmarshalForest — JSON with version tag
- [x] WriteForest/ReadForest — io.Writer/Reader
- [x] MarshalTree/UnmarshalTree — single-tree JSON
- [x] PrettyPrintTree/PrettyPrintForest — human-readable tree diagram
- [x] OCIProvenance — build provenance in OCI-manifest-compatible format
- [x] WriteProvenance/ReadProvenance — JSON I/O
- [x] DiffProvenance — cross-build vertex comparison
- [x] ExportProvenanceOption — WithProvenanceBuildID, WithProvenanceFiles

**Diff & comparison**
- [x] ForestDiff — Added/Removed/Changed/Unchanged, IsIdentical, Summary
- [x] TreeChange.ComputeFileDiff — lazy O(n log n) leaf-level diff
- [x] FileDiff — Added/Removed/Modified/Unchanged FileChange records

**Reports**
- [x] WriteHTMLReport — self-contained HTML, CSS-styled, HTML-escaped, MaxFilesPerVertex
- [x] WriteMarkdownReport — GFM tables, file-change details, badges

**Cache warming**
- [x] CacheWarmer — WarmFromForest, Warm(OCIProvenance)
- [x] NewEngineWithWarmStart — WarmStartConfig with Forest or Provenance source

**Persistence**
- [x] PersistentHashCache — append-only WAL, gob-encoded entries, WAL replay, compaction

**Observability**
- [x] SlogHook — log/slog, configurable levels per event type
- [x] OTELHook — zero SDK import, Tracer/Meter/Span/Counter/Histogram interfaces
- [x] PrometheusHook — atomic counters + http.Handler serving text/plain exposition format

**Integration**
- [x] PipelineAdapter, DirectEventSource, MergedPathResolver — fanwatch bridge
- [x] AccessEventFromEnriched, EncodeLayerStack, AccessEventAttrs — attr encoding
- [x] SubmitBatch — amortised burst submission

**CLI**
- [x] `cmd/lminspect` — inspect (human/JSON/stats), diff (human/JSON), -vertex filter

**Internal packages**
- [x] `internal/digest` — stdlib OCI digest (FromBytes, FromString, Validate, Algorithm, Hex)
- [x] `internal/hash` — pooled SHA-256 (Pool, SumFile, SumBytes, 32 KiB copy buffer)
- [x] `internal/singleflight` — Group + ContextGroup (context-cancellation-aware)
- [x] `internal/trie` — path-segment trie (Insert, Walk, SortedChildren, Len)

**Testing (160 tests / 3426 lines)**
- [x] All production code tested
- [x] Race detector (-race) passes on all 9 packages
- [x] Benchmarks: ShardedLRU, DeduplicationEngine, MerkleTreeBuilder, Engine.Submit, hash pool
- [x] Concurrency tests: concurrent FinalizeVertex, high-concurrency stress, SubmitBatch
- [x] Example tests with verifiable Output:
- [x] WAL replay: write → close → reopen → verify entries
- [x] WhiteoutIndex: real filesystem walk with temp dirs
- [x] HTML/Markdown report structure and XSS escaping

---

## Phase 3 — Future (Backlog)

- [ ] Real OCI registry push — POST provenance as attestation via ORAS
- [ ] BadgerDB-backed PersistentHashCache — true cross-process shared cache
- [ ] Integration tests (`//go:build integration`) with real fanotify + overlay
- [ ] Histogram buckets for hash latency in PrometheusHook
- [ ] `lminspect -serve` — HTTP server serving live MerkleForest as HTML

---

## Architecture invariants (never break)

1. **Two-level cache**: `(topLayer, relPath)` then `(ownerLayer, relPath)` — always in that order.
2. **Cache propagation**: after resolving ownerLayer, always populate topLayer too.
3. **Singleflight per (stackKey, relPath)**: identical concurrent requests share one I/O call.
4. **No core → SDK imports**: OTEL/Prometheus/slog hooks live outside the core package.
5. **StatResult exported**: external packages (ring cache, OCI resolver) need the type.
6. **Deterministic Merkle root**: same file set → same root, regardless of event arrival order.
7. **Whiteout precedence**: per-file > opaque (per directory). Walk top-to-bottom in stack.
8. **Write-class events are always skipped**: AccessEvent.IsReadAccess() gate in Submit/Feed.
