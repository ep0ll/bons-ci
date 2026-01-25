# SCHEDULER
I'll read edge.go, debug.go, and solver.md in parallel to pull low-level details about `unpark`, pipe debug helpers, and documented scheduler behavior; expected outcome: code snippets and doc lines I can reference in the deeper explanation. Task: re-evaluate what the scheduler does and why, using the code plus commit history and related files; I’ll give a deeper, low-level explanation and point out design choices made in commits.

Plan (what I did)
- Read scheduler.go, edge.go, debug.go, solver.md.
- Queried git commits touching scheduler.go to capture rationale from history (merge rules, debug refactors, pipe generics, cancel improvements).
- Synthesized low-level control flow, locking, pipe lifecycle, invariants and merge behavior.

Checklist
- Re-evaluate scheduler responsibilities and runtime flow — Done
- Explain low-level mechanisms (locks, queue, pipes, callbacks) — Done
- Explain merge logic and safety checks with commit rationale — Done
- Point to where debug/commit changes affected behavior (commits) — Done

Concise summary
- The scheduler is the single-threaded event loop that drives edges (units of work). It collects asynchronous events (pipes), wakes (unparks) edges to let them update state and create new requests, deduplicates equivalent work (merge), enforces invariants to avoid deadlocks/leaks, and exposes a blocking `build` API on top of the pipe machinery.
- Commits show incremental hardening: move debug helpers into debug.go (2024-09-18), use generics in `pipe` for stronger typing (2024-09-12), enforce merge direction and dependency checks (2024-01-16, 2021-03-18), and use context cancel causes (2023-12-03 / 2024-11-19).

Low-level flow and why it’s implemented this way

1) Objects / invariants the scheduler maintains
- Queue / FIFO: `next *dispatcher`, `last *dispatcher`, `waitq map[*edge]struct{}` — FIFO dispatch list; `waitq` prevents queue duplicates (efficiency & fairness).
- Two mutexes:
  - `mu sync.Mutex` — main scheduler lock protecting edge state maps (`incoming`, `outgoing`), `cond` (a `StatefulCond` built on `mu`) and most state the dispatch loop uses.
  - `muQ sync.Mutex` — separate, lightweight lock for queue manipulations so enqueue/dequeue can run without holding `mu` for longer than needed (reduces contention, avoids deadlocks).
- Pipe bookkeeping:
  - `incoming map[*edge][]*edgePipe` — pipes where this edge is the target (senders into the edge).
  - `outgoing map[*edge][]*edgePipe` — pipes created by this edge to ask other edges or async functions.
- Stop/close channels: graceful shutdown (`Stop()` closes `stopped` and waits on `closed`).

Why two mutexes? Queue ops happen frequently and must not hold the main `mu` while complex unpark/dispatch runs are happening. `muQ` confines queue-only work.

2) Event loop (`loop()`)
- Single goroutine loop created in `newScheduler`.
- Uses `s.cond` (backed by `s.mu`) to wait until someone calls `signal`.
- Loop pops one dispatcher node (edge) under `muQ` then calls `dispatch(e)`; dispatch runs while `s.mu` is held (this ensures consistent access to maps like `incoming`/`outgoing` and to edges).
- Why single-threaded? Simpler ordering and invariants. Centralizing calls into edges avoids races where multiple goroutines try to modify the same edge bookkeeping concurrently.

3) Enqueueing (`signal(e *edge)`)
- `signal` locks `muQ`: if `e` not already in `waitq`, create a `dispatcher` node, append to FIFO, set `waitq[e]` and call `cond.Signal()` so the loop wakes.
- Avoids duplicate scheduling and thundering restarts.

4) Dispatching an edge (`dispatch(e *edge)`) — the core steps
- Build arrays of current incoming senders and outgoing receivers for the `e` (from `s.incoming[e]` and `s.outgoing[e]`) while holding `s.mu`.
- Scan outgoing receivers and call `p.Receive()` on each to collect updates; `Receive()` returns a boolean if receiver had an update, and `p.Status().Completed` is checked to determine `e.hasActiveOutgoing`.
  - Why: scheduler needs to pass the set of updated outgoing receivers into `e.unpark()` so `edge` can process responses from previous outgoing requests.
- Create `pipeFactory` (pf) that encapsulates `s` and `e` and is passed into `e.unpark` so the edge can create new pipes (`NewInputRequest` / `NewFuncRequest`) that are tied into scheduler maps.
- Call `e.unpark(inc, updates, out, pf)`:
  - unpark is the edge's fast, non-blocking handler that: processes updates, recomputes internal state, decides whether incoming can be completed, and possibly creates outgoing requests or func requests via `pf`.
  - Important: `unpark` must obey invariants (see next).
- After `unpark`, prune `s.incoming` and `s.outgoing` to remove completed pipes; keep only open ones.
- If `e.keysDidChange` (edge created new cache keys), try to dedupe via the `index`: compute `k := e.currentIndexKey()`; call `index.LoadOrStore(k,e)` and if `origEdge` exists, run merge checks:
  - Skip merge if one is a dependency of the other (safety).
  - Use `ef.hasOwner(origEdge.edge, e.edge)` to decide merge direction (owner-based swap) — this reflects the commit that enforces one-way merging.
  - Call `s.mergeTo(dest, src)` and `ef.setEdge(src.edge, dest)` if merge succeeded.
- Validation: If after dispatch there are open incoming but no open outgoing (or vice versa), scheduler marks edge failed via `e.markFailed(...)` and logs a message with `BUILDKIT_SCHEDULER_DEBUG=1` hint. This is defensive: it catches buggy `unpark()` implementations that could deadlock or leak resources.

Why this shape? The scheduler provides a deterministic, well-synchronized environment for edges to run quickly and create new requests. It must (a) handle asynchronous completions, (b) allow edges to spawn new outgoing requests, and (c) detect invariants violations early.

5) Pipes and callback wiring (how events travel)
- `edgePipe` wraps `*pipe.Pipe[*edgeRequest, any]` and tracks `From` and `Target`.
- `newPipe(target, from, req)`:
  - Creates `edgePipe` and appends it to `s.incoming[target]` and, if `from!=nil`, to `s.outgoing[from]`.
  - Sets `OnSendCompletion` on outgoing pipes to signal the `From` edge (so when a sender's send finishes, scheduler gets signaled to wake the source).
  - Sets `OnReceiveCompletion` to signal the `Target` edge (so when a receiver finishes a response, scheduler wakes target).
  - Calls `s.signal(target)` to ensure the target is scheduled.
- `newRequestWithFunc(e, f)` creates a pipe that executes `f` asynchronously and registers it as an outgoing pipe for `e`. `pipe.NewWithFunction` returns a (receiver, start) pair; scheduler calls `start()` to run it.
- `pipe` semantics (used in `dispatch`):
  - `Sender` and `Receiver` have `Send/Receive`, `Status()`, `Cancel()`; `Receive()` returns whether there's an update. Scheduler uses these to pack `updates` and to prune completed pipes.
- Why callbacks? Decouples asynchronous work (IO, cache queries, exec) from the scheduler; when async work completes, pipe callbacks call `s.signal`, which enqueues edges for re-dispatch without blocking workers.

6) Edge `unpark` contract / invariants (critical to correctness)
- Documented in edge.go and enforced in `dispatch`:
  - If `unpark` returns with any incoming requests still open, there must be at least one outgoing request created (otherwise that incoming request will never be completed) — otherwise scheduler marks edge failed.
  - If `unpark` completes all incoming requests it must not leave outgoing requests open.
- These invariants prevent deadlocks and resource leaks in the graph-driven async protocol.
- Practical effects:
  - Edges can process updates, create requests via `pf`, and then return fast. Outgoing requests run asynchronously; their completions re-signal the relevant edges.

7) Execution decision logic inside `edge` (why scheduler organizes work this way)
- Edge maintains multi-phase caching model: initial → cache-fast (selector-based) → cache-slow (content/hash-based) → complete.
- `unpark` phases:
  - process updates (responses from previous outgoing requests)
  - recompute internal state / keys (`recalcCurrentState`)
  - respond to incoming requests: compute `desiredState` and whether requests can be completed (function `respondToIncoming`); it may cancel outgoing requests if inputs can complete
  - create outgoing requests for dependencies or async functions if needed (`createInputRequests`, `computeCacheKeyFromDep`, `execIfPossible`)
- Why staged cache states? To avoid expensive content-based checks unless needed. The scheduler lets edges escalate dependency desired states gradually based on available information so the solver does minimum necessary work while still being correct.

8) Merge and deduplication (what and why)
- When an edge gains new index keys (`keysDidChange`), scheduler computes `k := e.currentIndexKey()` and tries `origEdge := e.index.LoadOrStore(k,e)`.
- Merge preconditions:
  - Skip merge if the two edges are dependencies of each other (would create cycles / unsafe merge).
  - Respect IgnoreCache: if target does not ignore cache but src does, skip merge.
  - Commit history: later commits moved debug logic out and enforced "always merge in one direction" and owner checks; scheduler uses `ef.hasOwner` and `ef.setEdge` to maintain consistent mapping in the shared graph.
- `mergeTo(target, src)`:
  - Moves incoming pipes from `src` to `target` (acquire `inc.mu`), reassigns their `Target`.
  - Moves outgoing pipes from `src` to `target` and cancels their receivers (so redundant outgoing work is stopped).
  - Transfers `secondaryExporters` and other bookkeeping.
  - Deletes `src` maps and signals `target`.
- Why merge? Save work by consolidating identical tasks; sharing results and cache keys reduces duplicate exec/IO and simplifies resource usage.

9) `build(ctx, Edge)` synchronous API (why it’s implemented like this)
- `build` translates a caller request into a one-off pipe on the edge with `desiredState: complete`.
- It uses `s.mu` to look up internal `*edge`, creates a pipe, sets `OnSendCompletion` to close the local `wait` when the receiver becomes completed, unlocks, then awaits.
- It uses `context.WithCancelCause` so cancellation cause stacks are preserved (commit change reflected in history).
- Why this approach? Reuses the same scheduler/pipe logic for synchronous calls; callers get a blocking API that piggybacks on the scheduler's event loop and edge invariants.

10) Cancellation & error handling
- Cancellations propagate via `pipe.Receiver.Cancel()` (e.g., `build` spawns goroutine to cancel on ctx done).
- `dispatch` and `edge` check `Status().Err` and `Status().Completed` to decide pruning or setting `e.err`.
- Defensive validation in `dispatch` marks edges failed on algorithm invariants (provides debug instructions).

11) Observability and debugging (commits related)
- Debugging was modularized into debug.go (2024-09-18). Use `BUILDKIT_SCHEDULER_DEBUG=1` or `BUILDKIT_SCHEDULER_DEBUG_STEPS` for deep per-edge logs.
- Commits added more robust log fields and guardrails (e.g., marking inconsistent graph state as internal).

Concrete micro-sequence of a request (step-by-step)
1. Caller requests a result: `s.build(ctx, Edge)` or an edge calls `pf.NewInputRequest(...)`/`NewFuncRequest`.
2. `newPipe` or `newRequestWithFunc` registers pipe in `s.incoming` and `s.outgoing` and sets callbacks that call `s.signal(...)` on completion.
3. `s.signal` appends edge to FIFO and wakes `s.cond`.
4. `loop()` wakes, pops dispatcher `d`, removes `d.e` from `waitq`.
5. `dispatch(e)`:
   - Collect incoming `inc` senders and outgoing `out` receivers.
   - For each outgoing `p`: `p.Receive()` to collect updates, check `p.Status().Completed`.
   - Build `pf` and call `e.unpark(inc, updates, out, pf)` while `s.mu` is held.
6. `e.unpark`:
   - `processUpdates` stores responses in `e.deps`, `e.execReq`, `e.cacheMap` etc.
   - `recalcCurrentState()` may add new keys -> `keysDidChange`
   - Decide `desiredState` and call `createInputRequests` / `execIfPossible` / `loadCache` as needed (via `pf.New*`).
   - Set up outgoing pipes; ensure invariants (incoming/outgoing consistency).
7. `dispatch` prunes completed pipes from `s.incoming`/`s.outgoing`.
8. If `keysDidChange`, try index dedupe + `mergeTo`.
9. If outgoing async requests were created, their completion triggers pipe callbacks which call `s.signal` and the cycle repeats.

Key reasons behind design choices (TL;DR)
- Single event loop + FIFO + waitq: predictable ordering, simple concurrency model, reduce races.
- Two locks: keep queue lightweight, avoid deadlocks while dispatch/edge work runs.
- Pipe callbacks + `signal`: fully asynchronous IO + deterministic wakeups without busy-waiting.
- Edge invariants (incoming/outgoing rules): avoid silent deadlocks and resource leaks.
- Multi-stage cache states: cheaply avoid expensive content checks until necessary.
- Merge/dedupe logic: avoid duplicate work across edges with identical effective inputs.
- Defensive validation & debug hooks: catch bugs in `unpark()` early and provide actionable debug output (commits moved logging into reusable helpers).

If you want next:
- I can extract and show the exact lines implementing the invariants (e.g., where `dispatch` validates and `edge.unpark` rules are documented).
- Or produce a sequence diagram for the pipe lifecycle and `unpark` contract.
- Or open the recent commit diffs that changed merging behavior (I can show the relevant diffs).