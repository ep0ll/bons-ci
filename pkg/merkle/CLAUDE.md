# CLAUDE.md — layermerkle

> Instructions for AI assistants (Claude, Copilot, GPT) working on this codebase.

---

## Package Purpose (One Sentence)

`layermerkle` deduplicates file-hash computations across stacked overlay filesystem layers and builds per-layer Merkle trees from the deduplicated results — doing **zero redundant work** even when ExecOps share ancestry chains.

---

## Mental Model

Think of OCI/BuildKit layers as a stack of transparent acetate sheets laid on a projector. Each sheet (layer) can add, modify, or delete marks (files). When you look through the stack you see the merged view. Our job:

- Accept file-access events from fanotify with `(filePath, layerStack, accessType)`.
- For each event, determine the **unique (filePath, layerDigest) pair** it belongs to.
- Compute the file hash **at most once per pair** across all ExecOps.
- Record each pair as a Merkle leaf in the output layer's tree.
- Seal the tree on demand to produce a Merkle root and generate inclusion proofs.

---

## Package Map

```
layermerkle/
├── doc.go                     Package-level godoc
├── layer/
│   └── stack.go               Layer stack type (ordered []Digest). Immutable.
├── event/
│   └── event.go               FileAccessEvent (fanotify data model)
├── hash/
│   └── provider.go            HashProvider interface + SHA256 + Synthetic impls
├── cache/
│   ├── entry.go               CacheKey, CacheEntry, Stats types
│   └── sharded.go             ShardedCache (256-shard, FNV-32a, RWMutex)
├── dedup/
│   ├── result.go              ResultKind, Result types
│   └── engine.go              DedupEngine — the core dedup algorithm
├── merkle/
│   ├── leaf.go                Leaf, Proof, SiblingStep types
│   ├── tree.go                MerkleTree (incremental, sealable, proof-generating)
│   └── registry.go            Registry (per-layer tree manager)
├── hook/
│   └── hook.go                Hook interface, HookChain, built-in hooks
├── pipeline/
│   └── pipeline.go            Pipeline (worker pool, wires everything together)
└── examples/simulate/
    └── main.go                Full simulation demo (no filesystem I/O)
```

---

## Core Invariants (Never Violate)

1. **SetIfAbsent for read promotions** — When promoting a lower-layer hash to the output layer cache, always use `SetIfAbsent`. A concurrent mutating write for the same key must win.

2. **Set (overwrite) for mutating ops** — Write/create/chmod/rename events must always overwrite the cache. Stale read-promoted entries must not survive a write.

3. **Tombstone terminates stack walk** — If a file has a tombstone in layer L, the walk must stop. Do not look in layers below L for this file in the output layer context.

4. **Singleflight scope** — The singleflight key is `outputLayerDigest + "\x00" + filePath`. This deduplicate concurrent reads within one ExecOp. Mutating ops bypass singleflight.

5. **Merkle leaf last-write-wins** — `AddLeaf` for the same path in the same layer always updates to the latest hash. The final state after all events is correct.

6. **Seal is idempotent** — Calling `Seal()` multiple times returns the same root. Once sealed, a tree is immutable.

7. **Sorted leaves** — Leaves are sorted by FilePath before hashing. This ensures identical file sets always produce identical roots regardless of event arrival order.

---

## Dedup Algorithm (handleRead)

```
Phase 1 — Output layer cache hit:
  cache.Get((path, outputLayer))
    hit (non-tombstone) → AddLeaf, return CacheHit
    hit (tombstone)     → return Tombstone

Phase 2 — Ancestor stack walk (top-to-bottom):
  for ancestor from (outputLayer-1) down to base:
    cache.Get((path, ancestor))
      miss          → continue to next ancestor
      tombstone     → return Tombstone (file was deleted in this ancestor)
      hit           → promote: SetIfAbsent((path, outputLayer), entry)
                      AddLeaf, return CacheHit

Phase 3 — Compute (singleflight):
  sf.Do(outputLayer+"\x00"+path, func() {
    hash = HashProvider.Hash(req)
  })
  SetIfAbsent((path, outputLayer), entry)
  AddLeaf, return CacheMiss
```

---

## Adding a New Feature

### New AccessType

1. Add the constant to `event/event.go` `AccessType` iota.
2. Add it to `AccessType.IsMutating()` if it modifies content.
3. Add a `String()` case.
4. Update `dedup/engine.go` if the new type requires special routing.

### New Hook

Implement `hook.Hook`:
```go
type MyHook struct{}
func (h *MyHook) OnHook(ctx context.Context, e hook.HookEvent) error {
    // ...
    return nil
}
```
Register with `pipeline.WithHook(&MyHook{})`.

### New HashProvider

Implement `hash.HashProvider`:
```go
type MyProvider struct{}
func (p *MyProvider) Hash(ctx context.Context, req hash.HashRequest) (hash.HashResult, error) { ... }
func (p *MyProvider) Algorithm() hash.Algorithm { return hash.AlgorithmSHA256 }
```

---

## Testing Patterns

- Use `hash.NewSyntheticProvider()` — no filesystem I/O, deterministic.
- Use `hook.NewRecordingHook()` — assert `CountByType(hook.HookCacheHit)`.
- Use `cache.NewShardedCache()` — real implementation; no mocks needed.
- Send events synchronously in tests: `close(eventCh)` then `wg.Wait()` before asserting.
- Seal before calling `Root()` / `Proof()` / `Leaves()`.

---

## Performance Notes

- The ShardedCache with 256 shards handles >10M lookups/sec on modern hardware.
- Singleflight collapses N concurrent same-key misses into 1 HashProvider.Hash call.
- FNV-32a shard selection: ~2 ns/key. SHA256 leaf hash: ~150 ns/leaf.
- `buildLevels` allocates O(N) for N leaves; sealed trees are fully reusable.
- Hook chains add <50 ns/event when using NoopHook.

---

## Common Pitfalls

| Pitfall | Fix |
|---|---|
| Calling `Root()` before `Seal()` | Call `p.Seal(layerDigest)` or `p.SealAll(ctx)` first |
| Forgetting to close `eventCh` | The pipeline blocks forever; always `close(eventCh)` after all events are sent |
| Using `Set` for read promotions | Use `SetIfAbsent` — `Set` would overwrite a concurrent write |
| Zero workers | `WithWorkers(1)` minimum; default is `runtime.NumCPU()` |
| Race on `MerkleTree.leafMap` after seal | All mutations are no-ops post-seal; concurrent reads are RLock-protected |

---

## Build & Test

```bash
go test ./...                    # run all tests
go test -race ./...              # race detector (always use in CI)
go test -bench=. ./cache/...     # benchmark cache throughput
go run ./examples/simulate/      # run the full simulation demo
go vet ./...                     # static analysis
```

---

## Dependency Policy

This package has **one external dependency**: `golang.org/x/sync` for `singleflight.Group`.
All other functionality uses the Go standard library only. Do not add dependencies without explicit approval.
