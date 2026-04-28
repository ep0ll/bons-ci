---
name: snapshot-pipeline
description: >
  Expert guidance for working on the OCI snapshot pipeline package at
  core/snapshotter/orchestrator. Use this skill whenever the user asks to
  add features, fix bugs, write tests, refactor, or understand any part of
  the concurrent snapshot pipeline — including RunSnapshotPipeline,
  buildChainTable, processEvent, the resultHeap committer, mockSnapshotter,
  or anything involving containerd snapshotters, OCI chain IDs, or the
  Prepare/Action/Commit lifecycle. Also trigger for questions about the
  pipeline's concurrency model, idempotency behaviour, or error propagation.
---

# Snapshot Pipeline Skill

Always read `CLAUDE.md` at the project root before making any changes.
It defines the critical invariants; violating them silently corrupts the
snapshot store.

---

## Package layout

```
core/snapshotter/orchestrator/
├── snap.go       — production code (RunSnapshotPipeline + helpers)
└── snap_test.go  — white-box tests + mockSnapshotter + benchmarks
```

One public entry point: `RunSnapshotPipeline`. Everything else is unexported.

---

## Goroutine architecture

```
caller
  │  chan<- Event (buffered, numWorkers)
  ▼
[worker 0]──┐
[worker 1]──┤  chan workerResult (buffered, numWorkers)
  ...       ├──────────────────────────────────────────► [committer]
[worker N]──┘                                                │
                                                       sn.Commit (in seq order)
```

Workers run `processEvent` (Prepare + Action) concurrently.
The committer holds a min-heap and drains results strictly in seq order.
Only the committer calls `sn.Commit`.

---

## The three immovable rules

Before any edit, confirm it doesn't break these:

1. **`storeErr` before `cancel`** — in every error path in the worker pool.
   Wrong order → `resolveErr()` returns `context.Canceled` instead of the
   real error.

2. **Drain invariant** — workers must `continue` (not `return`) after
   cancellation so the caller's send loop never deadlocks.

3. **Heap guard** — the committer checks `if res.err != nil` before
   `heap.Push`. Never push `seq < 0` into the heap.

---

## Common tasks

### Adding a new test

```go
func TestRunSnapshotPipeline_YourScenario(t *testing.T) {
    t.Parallel()
    sn := newMock()
    rootFS := newRootFS(n)

    // Optionally inject hooks before starting the pipeline:
    // sn.onPrepare = func(key string) error { ... }
    // sn.preCommit(chains[i].chainID, parent)

    eventCh, errCh := RunSnapshotPipeline(context.Background(), sn, rootFS, workers)
    go sendAll(eventCh, buildEvents(n))  // always use a goroutine

    requireNoErr(t, errCh)              // or requireErr for failure cases
    assertCommitOrder(t, sn, rootFS)    // always verify after success
}
```

Key test helpers:

| Helper | Use |
|---|---|
| `newMock()` | Fresh mock snapshotter |
| `newRootFS(n)` | Deterministic n-layer RootFS |
| `buildEvents(n)` | n events with noopAction, seqs 0..n-1 |
| `makeEvent(seq, action)` | Single event with custom Action |
| `sendAll(ch, evs)` | Send all events then close (call in goroutine) |
| `requireNoErr(t, errCh)` | Assert success with 10s timeout |
| `requireErr(t, errCh)` | Assert failure with 10s timeout |
| `assertCommitOrder(t, sn, rootFS)` | Verify commit count + parent chain |
| `waitResult(t, errCh)` | Read result without asserting (for nil-or-cancel cases) |

Never read `errCh` directly — it has no timeout and will hang the test suite.

### Injecting a snapshotter error

Set hooks before calling `RunSnapshotPipeline`. Hooks are read-only after
the pipeline starts; setting them concurrently is a data race.

```go
chains := buildChainTable(rootFS)
wantErr := errors.New("disk full")

// Fail Prepare for a specific layer:
sn.onPrepare = func(key string) error {
    if key == chains[2].diffID {
        return wantErr
    }
    return nil
}

// Pre-simulate a partial run:
sn.preActive(chains[1].diffID)   // Prepare done, Commit not
sn.preCommit(chains[0].chainID, "") // already committed
```

### Modifying error propagation

Error flow: `processEvent` → `workerResult.err` → worker calls `storeErr` +
`cancel` → sends result → committer receives on `resultCh` → checks
`res.err != nil` → calls `storeErr` + `cancel` → writes to `errCh`.

If adding a new error site, follow the same pattern:
```go
storeErr(err)   // MUST come first
cancel()
// then either: errCh <- err (committer) or: resultCh <- res (worker)
```

### Extending Event or EventSnapshotter

Both types are in the public API. Adding a field is non-breaking. Removing or
renaming a field requires a deprecation cycle. New fields that the pipeline
reads internally must be documented in the field comment and covered by a test.

### Changing the heap

`resultHeap` is a standard `container/heap` implementation. The committer
holds the only reference — no mutex needed. If you change capacity pre-allocation
(`make(resultHeap, 0, numWorkers)`), benchmark with `BenchmarkResultHeap_PushPop`
to verify zero allocs under steady load.

---

## Running tests and benchmarks

```bash
# Full test suite with race detector (required before any change)
go test -race -count=1 ./core/snapshotter/orchestrator/...

# Single test, verbose
go test -race -run TestRunSnapshotPipeline_SeqGap -v ./core/snapshotter/orchestrator/...

# Benchmarks with allocation reporting
go test -bench=. -benchmem -count=3 ./core/snapshotter/orchestrator/...
```

Expected benchmark characteristics (noopAction, in-process mock):

| Benchmark | Notes |
|---|---|
| `BenchmarkPipeline_100Layers_NumCPU` | Should show 0 allocs/op in the committer hot path |
| `BenchmarkBuildChainTable_100` | Linear in layer count; one allocation per layer |
| `BenchmarkResultHeap_PushPop` | Should be 0 allocs/op after initial make |

If any benchmark shows allocations in the committer loop, check whether
`cloneOpts` is returning nil for empty opts (avoiding a zero-len alloc) and
whether `resultHeap` still has the `numWorkers` pre-allocated capacity.

---

## OCI chain ID refresher

For a layer stack `[d0, d1, d2]`:

```
chainID(layer 0) = digest(d0)               == d0   (single-element identity)
chainID(layer 1) = digest("sha256:" + d0 + " " + d1)
chainID(layer 2) = digest("sha256:" + chainID(1) + " " + d2)
```

This is computed by `identity.ChainID(rootFS.DiffIDs[:i+1])` from
`github.com/opencontainers/image-spec/identity`.

The pipeline uses:
- `diffID` (= `d0`, `d1`, `d2`) as the **active snapshot key** for Prepare/Mounts/Remove.
- `chainID` as the **committed snapshot name** for Commit/Stat.
- `parentChainID` (= `chainID` of the preceding layer) as the `WithParent` argument.

---

## What to check after any change

- [ ] `go test -race -count=1` passes with no failures or race reports.
- [ ] `assertCommitOrder` still passes for multi-layer happy-path tests.
- [ ] Idempotency tests (`AllAlreadyCommitted`, `PartiallyAlreadyCommitted`,
      `PrepareAlreadyExists`, `CommitAlreadyExists`) still pass.
- [ ] `CancelNoDeadlock` passes (drain invariant).
- [ ] Benchmarks show no new allocations in the committer hot path.
- [ ] `CLAUDE.md` invariants are not violated.
