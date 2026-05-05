# SKILL: layermerkle — Layer-Scoped Merkle Deduplication

## Skill ID
`layermerkle-dedup-v1`

## Domain
Go · Distributed Systems · Content-Addressable Storage · Overlay Filesystems · Merkle Trees · Concurrent Data Structures

---

## What This Skill Does

Generates and maintains production-quality Go code for:

1. **Sharded concurrent hash caches** keyed by `(filePath, layerDigest)` with tombstone support, atomic stats, and FNV-32a shard selection.
2. **Deduplication engines** that walk a layer ancestry stack (top-to-bottom) and reuse cached hashes before computing fresh ones, using `singleflight` to collapse concurrent same-key misses.
3. **Incremental, sealable Merkle trees** with deterministic leaf ordering, length-prefixed leaf hashing, balanced pairing for odd nodes, and O(log N) inclusion proofs.
4. **Per-layer Merkle registries** that lazily create trees on first access and seal them idempotently.
5. **Event-driven processing pipelines** with configurable worker pools, hook chains, backpressure, and typed result channels.
6. **Hook/extension systems** supporting typed filtering, recording, and lenient vs strict error propagation.

---

## Interface Contracts

### HashProvider
```go
type HashProvider interface {
    Hash(ctx context.Context, req HashRequest) (HashResult, error)
    Algorithm() Algorithm
}
```
- Must be goroutine-safe.
- Must honour context cancellation.
- Must be deterministic for identical inputs.
- Dedup engine calls Hash only on total cache misses.

### Cache
```go
type Cache interface {
    Get(key CacheKey) (CacheEntry, bool)
    Set(key CacheKey, entry CacheEntry)
    SetIfAbsent(key CacheKey, entry CacheEntry) (CacheEntry, bool)
    Delete(key CacheKey)
    WalkLayer(layerDigest layer.Digest, fn func(CacheKey, CacheEntry) bool)
    EvictLayer(layerDigest layer.Digest)
    Stats() Stats
}
```
- Use `Set` for mutating ops (write, create, chmod, rename, delete-tombstone).
- Use `SetIfAbsent` for read promotions. Never overwrite a write-entry with a promoted read-entry.

### Hook
```go
type Hook interface {
    OnHook(ctx context.Context, e HookEvent) error
}
```
- Must be goroutine-safe.
- Must not block indefinitely.
- Return non-nil error to fail events (strict mode) or record errors (lenient mode).

---

## Merkle Tree Specification

### Leaf Hash
```
leafHash = SHA256(uint32BE(len(path)) || path || uint32BE(len(contentHash)) || contentHash)
```
Length-prefixed to prevent second-preimage attacks.

### Internal Node Hash
```
nodeHash = SHA256(leftChild.Hash || rightChild.Hash)
```

### Odd Node Handling
If a level has an odd number of nodes, the last node is paired with itself:
```
nodeHash = SHA256(lastNode.Hash || lastNode.Hash)
```

### Empty Tree
```
root = SHA256("")  // hash of empty byte slice
```

### Proof Verification
```
current = leafHash
for each step in proof.Siblings:
    if step.IsLeft: current = nodeHash(step.Hash, current)
    else:           current = nodeHash(current, step.Hash)
assert current == proof.Root
```

---

## Dedup Algorithm (Canonical Reference)

```
Process(event):
  validate(event) or return Error

  outputLayer = event.LayerStack.Top()

  switch event.AccessType:
    DELETE:
      cache.Set((path, outputLayer), tombstone)
      registry.RecordDeletion(outputLayer, path)
      return Tombstone

    MUTATING (write/create/chmod/rename):
      hash = HashProvider.Hash(...)   // always fresh; no cache check
      cache.Set((path, outputLayer), entry)   // overwrite
      registry.AddLeaf(outputLayer, path, hash)
      return CacheMiss

    READ:
      // Phase 1: output layer
      if entry, ok = cache.Get((path, outputLayer)); ok:
        if entry.Tombstone: return Tombstone
        registry.AddLeaf(outputLayer, path, entry.Hash)
        return CacheHit

      // Phase 2: ancestor walk (top-to-bottom)
      ancestors = event.LayerStack.AncestorsOf(outputLayer)
      for i = len(ancestors)-1; i >= 0; i--:
        if entry, ok = cache.Get((path, ancestors[i])); ok:
          if entry.Tombstone: return Tombstone
          actual, _ = cache.SetIfAbsent((path, outputLayer), promoteEntry)
          registry.AddLeaf(outputLayer, path, actual.Hash)
          return CacheHit

      // Phase 3: compute (singleflight)
      sfKey = outputLayer + "\x00" + path
      hash = sf.Do(sfKey, func() { HashProvider.Hash(...) })
      actual, inserted = cache.SetIfAbsent((path, outputLayer), entry)
      registry.AddLeaf(outputLayer, path, actual.Hash)
      return CacheMiss (or CacheHit if racing write won)
```

---

## Code Style Rules

1. **Interfaces first** — define behaviour before implementation.
2. **Functional options** for constructor configuration (`WithXxx() Option`).
3. **Atomic counters** for hot-path stats; never lock stats under cache locks.
4. **No global state** — all state is owned by explicit structs.
5. **Context propagation** — every I/O method takes `context.Context` as first arg.
6. **Error wrapping** — use `fmt.Errorf("%w", err)` for all error wrapping.
7. **Nil-safe** — always check for nil pointers before dereferencing; constructors panic on nil required args.
8. **Compile-time interface assertions** — `var _ Interface = (*Impl)(nil)` in each file.
9. **Clone, don't share** — exported slices and maps must be copied before returning.
10. **Sort for determinism** — any output that affects Merkle roots must be sorted.

---

## Anti-Patterns to Avoid

- ❌ `cache.Set` for read promotions — overwrites concurrent writes
- ❌ Walking ancestors bottom-to-top — misses the nearest override
- ❌ Global singleflight group shared across files/goroutines without key namespacing
- ❌ Unsealed Merkle tree queries — always check sealed flag
- ❌ Non-deterministic leaf ordering — always sort by FilePath
- ❌ Hash the content hash without the path — enables second-preimage attacks
- ❌ Blocking hook implementations — causes worker starvation

---

## Test Checklist

- [ ] Cache: Get/Set/SetIfAbsent/Delete/WalkLayer/EvictLayer round-trips
- [ ] Cache: SetIfAbsent returns existing entry when key present
- [ ] Cache: Tombstone counting via Stats()
- [ ] DedupEngine: phase 1 hit (output layer cache)
- [ ] DedupEngine: phase 2 hit (ancestor promotion)
- [ ] DedupEngine: phase 2 tombstone terminates walk
- [ ] DedupEngine: phase 3 miss + compute
- [ ] DedupEngine: concurrent phase 3 collapses to 1 HashProvider call
- [ ] DedupEngine: mutating overrides SetIfAbsent
- [ ] MerkleTree: empty tree root = sha256("")
- [ ] MerkleTree: single leaf root = leafHash
- [ ] MerkleTree: sorted leaves (path order)
- [ ] MerkleTree: odd-level pairing
- [ ] MerkleTree: Proof.Verify() for every leaf
- [ ] MerkleTree: Seal idempotent
- [ ] MerkleTree: post-seal AddLeaf is no-op
- [ ] Registry: lazy tree creation
- [ ] Registry: SealAll covers all registered layers
- [ ] Pipeline: Run drains eventCh after close
- [ ] Pipeline: worker pool respects ctx cancellation
- [ ] Pipeline: HookChain fires in order
- [ ] Pipeline: result channel backpressure (non-blocking send)
