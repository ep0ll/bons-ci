# Engineering Prompt: `layermerkle`

> Use this prompt verbatim to regenerate, extend, or review the `layermerkle` package.

---

## Context

You are an expert Go engineer with deep knowledge of:
- Overlay filesystem semantics (overlayfs, OCI layers, BuildKit LLB ExecOp)
- Concurrent data structures (sharded maps, RWMutex, atomic ops, singleflight)
- Merkle tree construction, balanced binary trees, and inclusion proofs
- Event-driven pipeline architecture (worker pools, channels, backpressure)
- Linux fanotify and file access event streams
- SOLID principles, interface-driven design, functional options, and composability

---

## Problem Statement

Design and implement a Go package called `layermerkle` with **one responsibility**:

> Given a stream of file-access events from fanotify (each carrying a file path, an ordered layer stack `[base → ... → outputLayer]`, a vertex digest, and an access type), deduplicate hash computations across stacked overlay filesystem layers and build a per-output-layer Merkle tree from the deduplicated results.

### The Deduplication Problem

When multiple ExecOps execute over a shared layer ancestry:

```
ExecOp-1: [base] → exec1
ExecOp-2: [base, exec1] → exec2
ExecOp-3: [base, exec1, exec2] → exec3
```

ExecOp-3 may access `/bin/sh` which lives in `base`. Without dedup, its hash would be computed three times (once per ExecOp context). With dedup:

- ExecOp-1 computes `hash(/bin/sh)` → stored in `cache[(path, base)]`
- ExecOp-2 reads `/bin/sh` → ancestor walk finds `cache[(path, base)]` → promotes to `cache[(path, exec1)]`, no I/O
- ExecOp-3 reads `/bin/sh` → ancestor walk finds `cache[(path, exec1)]` → promotes to `cache[(path, exec2)]`, no I/O

### Tombstone Propagation

When a file is deleted in layer L, a tombstone is stored at `(path, L)`. Ancestor walks that encounter a tombstone stop immediately — the file is considered non-existent at that point in the stack.

### Singleflight Deduplication

Multiple concurrent goroutines processing events for the same `(path, outputLayer)` on a total cache miss must share one `HashProvider.Hash` call. Use `golang.org/x/sync/singleflight`.

---

## Required Architecture

```
layermerkle/
├── layer/     — Stack type: ordered []Digest, immutable
├── event/     — FileAccessEvent: path, stack, vertexDigest, accessType, timestamp
├── hash/      — HashProvider interface + SHA256Provider + SyntheticProvider
├── cache/     — ShardedCache: 256-shard, FNV-32a, RWMutex, atomic stats, tombstones
├── dedup/     — DedupEngine: 3-phase lookup + singleflight + promotion
├── merkle/    — MerkleTree (sealable, proofs) + Registry (per-layer)
├── hook/      — Hook interface + HookChain + TypedHook + RecordingHook
└── pipeline/  — Pipeline: worker pool + functional options + Seal/Proof/Leaves
```

### Component Responsibilities

| Component | Single Responsibility |
|---|---|
| `layer.Stack` | Represent ordered layer ancestry; provide `AncestorsOf()` |
| `event.FileAccessEvent` | Carry fanotify data; classify access as mutating/non-mutating |
| `hash.HashProvider` | Compute file content hash; single I/O boundary |
| `cache.ShardedCache` | Store (path,layer)→hash with concurrency-safe Get/Set/SetIfAbsent |
| `dedup.Engine` | Route events through 3-phase lookup; never compute hash twice |
| `merkle.MerkleTree` | Accumulate leaves; seal into Merkle root; generate proofs |
| `merkle.Registry` | Own one tree per layer; provide SealAll() |
| `hook.HookChain` | Notify observers of pipeline lifecycle events |
| `pipeline.Pipeline` | Wire components; manage worker pool; expose public API |

---

## Merkle Tree Specification

```
leaf_hash  = SHA256(uint32BE(len(path)) || path || uint32BE(len(hash)) || hash)
node_hash  = SHA256(left || right)
odd_node   = SHA256(node || node)   // balanced convention
empty_root = SHA256("")
leaf_order = lexicographic by FilePath (deterministic)
```

Proofs: sibling path from leaf to root. Each step: `{siblingHash, isLeft}`.
Verification: recompute root from leaf hash using sibling steps; compare to stored root.

---

## Performance Requirements

- Cache: support >1M concurrent Get/Set operations per second per core.
- Dedup: O(1) cache hit; O(depth) ancestor walk; O(1) amortised singleflight.
- Merkle: O(N log N) seal time for N leaves; O(log N) proof generation.
- Workers: default to `runtime.NumCPU()`, configurable via `WithWorkers(n)`.
- Memory: O(entries) for cache; O(N) for sealed tree with N leaves.

---

## Quality Requirements

- Fully decoupled interfaces; all components replaceable without modifying others.
- No global state anywhere.
- All exported types have godoc comments.
- All error paths are tested.
- Race-detector clean (`go test -race ./...`).
- Zero external dependencies except `golang.org/x/sync`.
- `var _ Interface = (*Impl)(nil)` compile-time assertions in every implementation file.

---

## Deliverables

1. All Go source files as described in the architecture.
2. `CLAUDE.md` — AI assistant instructions, invariants, algorithm reference.
3. `SKILL.md` — domain skill specification for code generation.
4. `PROMPT.md` — this file.
5. `TODO.md` — prioritised backlog.
6. `PROGRESS.md` — implementation tracker.
7. `examples/simulate/main.go` — runnable demo with no filesystem I/O.

---

## Gotchas to Handle

- `SetIfAbsent` vs `Set`: read promotions must use `SetIfAbsent` to avoid overwriting concurrent writes.
- Tombstone + read in same layer: a read after a delete in the same outputLayer should return Tombstone, not try to re-hash.
- Empty layer stack: `event.Validate()` rejects empty stacks.
- Concurrent `SealAll`: two goroutines calling `SealAll` must both see identical roots (idempotent).
- Proof for single-leaf tree: no siblings; verifier must handle `len(Siblings)==0`.
- Proof index off-by-one: binary search uses `sort.Search` with `>=` comparison.
- FNV shard separator: write a zero byte between layerDigest and filePath to prevent key collision where (digest="ab", path="c") == (digest="a", path="bc").
