# PROGRESS — layermerkle

Last updated: 2025-05 (final)

---

## Status Legend

| Symbol | Meaning |
|--------|---------|
| ✅ | Complete and tested |
| 🚧 | Work in progress |
| ❌ | Not started |

---

## Core Package Files

| File | Status | Notes |
|------|--------|-------|
| `doc.go` | ✅ | Package-level godoc with architecture diagram |
| `layer/stack.go` | ✅ | Stack, Digest, New/MustNew, AncestorsOf, Below, Push |
| `event/event.go` | ✅ | FileAccessEvent, AccessType, IsMutating, Validate, Clone |
| `hash/provider.go` | ✅ | HashProvider, HashRequest, HashResult, SHA256Provider, SyntheticProvider |
| `cache/entry.go` | ✅ | CacheKey, CacheEntry, Stats, tombstone semantics |
| `cache/sharded.go` | ✅ | ShardedCache, 256 shards, FNV-32a, atomic stats |
| `cache/bounded.go` | ✅ | BoundedCache, per-shard LRU, TTL expiry, background GC |
| `cache/snapshot.go` | ✅ | ShardedCache.Snapshot/Restore for cross-build persistence |
| `dedup/result.go` | ✅ | ResultKind, Result, IsSuccess |
| `dedup/engine.go` | ✅ | DedupEngine, 3-phase algorithm, singleflight, tombstone-after-SetIfAbsent fix |
| `merkle/leaf.go` | ✅ | Leaf, leafHash (length-prefixed), Proof, SiblingStep, Verify |
| `merkle/tree.go` | ✅ | MerkleTree, buildLevels, Seal (idempotent), binary-search Proof |
| `merkle/registry.go` | ✅ | Registry, lazy creation, parallel SealAll, double-checked locking |
| `hook/hook.go` | ✅ | Hook, HookChain, TypedHook, RecordingHook, NoopHook |
| `hook/metrics.go` | ✅ | MetricsHook with atomic counters, Snapshot, Reset |
| `pipeline/pipeline.go` | ✅ | Pipeline, Option, worker pool, Submit batch API, CrossLayerProof |
| `internal/singleflight/singleflight.go` | ✅ | stdlib-only singleflight (DoChan, Forget, panic propagation) |
| `examples/simulate/main.go` | ✅ | Full simulation, cross-layer promotion, tombstone, proof verification |

---

## Test Coverage

| Package | Tests | Status |
|---------|-------|--------|
| `layer` | 12 | ✅ |
| `event` | 8 | ✅ |
| `hash` | 12 | ✅ |
| `cache` (sharded) | 18 | ✅ |
| `cache` (bounded) | 9 | ✅ |
| `cache` (snapshot) | 3 | ✅ |
| `dedup` | 15 | ✅ |
| `merkle` | 24 | ✅ |
| `hook` | 10 + 4 metrics | ✅ |
| `pipeline` (core) | 21 | ✅ |
| `pipeline` (features) | 7 | ✅ |
| `internal/singleflight` | 7 | ✅ |
| **Total** | **142** | **✅ all pass** |

**Race detector: CLEAN — verified across 30-run stress test**

---

## Benchmark Results (Xeon Platinum 8581C @ 2.10GHz, 2 vCPU)

### ShardedCache
| Benchmark | ops/sec | ns/op | allocs |
|-----------|---------|-------|--------|
| Set (parallel) | ~345K | 2,897 | 2 |
| Get/hit (parallel) | ~2.3M | 436 | 1 |
| Get/miss (parallel) | ~3.1M | 324 | 1 |
| SetIfAbsent (parallel) | ~2.2M | 447 | 1 |

### MerkleTree.Seal
| Leaves | ns/op | allocs |
|--------|-------|--------|
| 10 | 17,472 | 46 |
| 100 | 139,142 | 320 |
| 1,000 | 865,367 | 3,023 |
| 10,000 | 19,895,501 | 30,031 |

### MerkleTree.Proof (O(log N))
| Leaves | ns/op | allocs |
|--------|-------|--------|
| 10 | 1,343 | 10 |
| 100 | 2,144 | 14 |
| 1,000 | 3,936 | 18 |

### Pipeline.Submit (100-event batch)
| Workers | ns/op |
|---------|-------|
| 1 | 487,074 |
| 4 | 585,408 |
| 8 | 546,344 |

---

## Architecture Decisions (final)

All ADRs from the previous PROGRESS.md apply, plus:

### ADR-007: BoundedCache uses exclusive mutex per shard
**Decision**: BoundedCache uses `sync.Mutex` (not RWMutex) per shard because LRU list mutation on every Get makes RWMutex ineffective (all "reads" become writes to the LRU list).
**Alternative**: Separate read path that only promotes atomically; rejected for complexity.

### ADR-008: Parallel SealAll
**Decision**: Each tree seals independently in its own goroutine. No global lock needed because each MerkleTree has its own RWMutex.
**Benefit**: For builds with hundreds of layers, sealing is O(N/P) where P = parallelism.

### ADR-009: Submit blocks until all events processed
**Decision**: Submit launches one goroutine per event and waits for all via WaitGroup. This matches the fanotify replay use case where callers need synchronous completion guarantees.
**Alternative**: Channel-based queuing — rejected because Submit is designed for ordered, synchronous batches not asynchronous streams.

---

## Known Issues

None open. All P0 and P1 items complete.
