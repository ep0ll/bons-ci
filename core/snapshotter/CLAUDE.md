# CLAUDE.md — Snapshot Pipeline Package

This file orients Claude when working on `core/snapshotter/orchestrator`.
Read it before touching any file in this package.

---

## Package overview

`package pipeline` implements a **concurrent, ordered OCI snapshot pipeline**
on top of containerd's `snapshots.Snapshotter` interface. It processes image
layers in parallel (Prepare + Action) while committing them strictly in OCI
parent-chain order.

The three cooperating goroutine groups are:

| Stage | Goroutines | Responsibility |
|---|---|---|
| Chain table | none (synchronous) | Precompute diffID / chainID / parentChainID once |
| Worker pool | `numWorkers` | `sn.Prepare` → `Action` → send `workerResult` |
| Committer | 1 | Heap-sort results → `sn.Commit` in seq order |

The only public entry point is `RunSnapshotPipeline`. Everything else is
internal.

---

## Critical invariants

**Never violate these; the pipeline's correctness depends on them.**

1. **Commit order.** The committer must call `sn.Commit(layer N)` only after
   `sn.Commit(layer N-1)` has returned successfully. The min-heap enforces
   this. Do not break the sequential drain loop.

2. **`storeErr` before `cancel`.** Whenever a goroutine detects a real error
   it must call `storeErr(err)` *before* `cancel()`. `resolveErr()` relies on
   this ordering to surface the root cause rather than `context.Canceled`.

3. **Workers drain after cancellation.** After `pipelineCtx` is cancelled,
   workers must keep consuming `eventCh` (without processing) so the caller's
   send loop never blocks permanently. Do not add early-return paths that skip
   the drain.

4. **seq=-1 sentinel.** `processEvent` returns `seq: -1` on parse/range errors
   so the committer's `heap.Push` guard (`if res.err != nil`) blocks the
   invalid result from entering the heap. Never push a result with `seq < 0`
   into the heap.

5. **`cloneOpts` before append.** The committer always calls `cloneOpts` on
   `CommitOptions` before appending `WithParent`. Skipping the clone would
   alias into the caller's backing array.

---

## Testing

```bash
# Run all tests with the race detector (required before any PR)
go test -race -count=1 ./core/snapshotter/orchestrator/...

# Run benchmarks
go test -bench=. -benchmem ./core/snapshotter/orchestrator/...

# Run a single test verbosely
go test -race -run TestRunSnapshotPipeline_SeqGap -v ./core/snapshotter/orchestrator/...
```

The test suite is **white-box** (same package). All `mockSnapshotter` hooks
(`onPrepare`, `onCommit`, `onRemove`, `onMounts`) must be set before the
pipeline starts — they are not protected by a mutex.

---

## Mock snapshotter

`mockSnapshotter` in `snap_test.go` is the only test dependency. Key methods:

| Method | Purpose |
|---|---|
| `preCommit(name, parent)` | Simulate a previously completed layer |
| `preActive(key)` | Simulate a partial run (Prepare done, Commit not) |
| `onPrepare / onCommit / onRemove / onMounts` | Inject errors for specific keys |
| `Commits() / Removes() / Prepares() / MountsCalled()` | Thread-safe log accessors |

---

## Adding a new test

1. Create a `Test*` function in `snap_test.go`.
2. Call `t.Parallel()` unless the test intentionally serialises goroutines.
3. Use `requireNoErr` / `requireErr` (never raw channel reads — they have no
   timeout and will hang the test suite).
4. Use `assertCommitOrder` to verify both commit count and parent-chain
   correctness after a successful pipeline run.
5. Send events from a goroutine (use `go sendAll(...)`) when the event count
   may exceed the channel buffer.

---

## Key design decisions

**Why a min-heap instead of `map[int]workerResult`?**
No bucket allocations, O(log n) drain, contiguous backing slice, pre-allocated
to `numWorkers` capacity for zero steady-state allocations in the committer.

**Why `cloneOpts` with `cap = len+1`?**
The committer always appends exactly one `WithParent` option. Pre-allocating
the slot eliminates a realloc+copy on every non-root layer.

**Why does `processEvent` use `seq=-1` on parse failure?**
Returning `seq=0` would silently collide with the legitimate seq-0 result.
The committer guards `heap.Push` with `if res.err != nil`, so `-1` never
enters the heap regardless.

**Why does the committer not call `sn.Commit` on cancellation?**
Partial commits would leave the snapshot store in an inconsistent state.
After cancellation the pipeline stops cleanly; the next run resumes from
where it left off thanks to the idempotency paths.

---

## Dependencies

| Import | Role |
|---|---|
| `container/heap` | Min-heap for out-of-order result serialisation |
| `github.com/containerd/containerd/v2/core/snapshots` | Snapshotter interface |
| `github.com/containerd/containerd/v2/core/mount` | Mount type for Action callbacks |
| `github.com/containerd/errdefs` | `ErrAlreadyExists` idempotency checks |
| `github.com/opencontainers/image-spec/identity` | `ChainID` computation |

---

## What this package does NOT do

- It does not download or decompress layer tarballs — that is the caller's
  responsibility, handled inside each event's `Action` function.
- It does not manage image metadata, content stores, or leases.
- It does not call `sn.Remove` for committed snapshots — only for failed
  active snapshots after an `Action` error.
  