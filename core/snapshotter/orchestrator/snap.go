// Package pipeline provides a concurrent, ordered snapshot pipeline for
// processing OCI image layers using the containerd snapshotter API.
//
// The pipeline is event-driven: [RunSnapshotPipeline] returns a send-only
// channel to which the caller pushes [Event] values as layers become available,
// closing it when no more layers are expected. Internally, N workers call
// Prepare and the user-supplied Action in parallel; a committer goroutine
// re-serialises the results in the original event order and calls Commit —
// preserving the OCI parent-chain invariant while keeping CPU and I/O
// utilisation high.
//
// Data scopes:
//
//	RootFS / chain IDs  →  image scope    →  pipeline (computed once)
//	Prepare / Action    →  layer scope    →  [Event]
//	Commit order        →  pipeline scope →  committer
//
// Typical usage:
//
//	events, errCh := pipeline.RunSnapshotPipeline(ctx, sn, rootFS, 0)
//
//	for i, layer := range layers {
//	    events <- buildEvent(i, layer)
//	}
//	close(events)
//
//	if err := <-errCh; err != nil { /* handle */ }
package pipeline

import (
	"container/heap"
	"context"
	"fmt"
	"runtime"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/containerd/containerd/v2/core/mount"
	"github.com/containerd/containerd/v2/core/snapshots"
	"github.com/containerd/errdefs"
	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/identity"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// ─────────────────────────────────────────────────────────────────────────────
// Public constants
// ─────────────────────────────────────────────────────────────────────────────

// LabelSnapshotterEventIndex is the label key whose value holds the decimal
// string representation of the event's 0-based position in the stream.
// Required on every [Event]; workers parse it to tag results so the committer
// can re-serialise out-of-order completions.
const LabelSnapshotterEventIndex = "containerd.io/snapshot/event-index"

// LabelUncompressed is the conventional containerd label key for the
// uncompressed layer digest (OCI DiffID). The pipeline itself no longer reads
// it — chain IDs and snapshot keys are derived from the rootFS passed to
// [RunSnapshotPipeline]. Callers may still set it on snapshotter options for
// external tooling that inspects snapshot metadata.
const LabelUncompressed = "containerd.io/uncompressed"

// ─────────────────────────────────────────────────────────────────────────────
// Public event types
// ─────────────────────────────────────────────────────────────────────────────

// Event carries the per-layer snapshotter lifecycle options for one layer.
//
// The image-scoped RootFS is intentionally absent: it is passed once to
// [RunSnapshotPipeline], which precomputes a chain table shared across all
// events. Per-event RootFS copies would waste memory, enlarge channel messages,
// increase GC pressure, and risk inconsistent values.
type Event struct {
	// Active describes the Prepare/Commit lifecycle for the layer.
	Active EventSnapshotter
}

// EventSnapshotter encapsulates all snapshotter options and the user-supplied
// action for one layer.
type EventSnapshotter struct {
	// PrepareOptions are forwarded verbatim to snapshots.Snapshotter.Prepare.
	PrepareOptions []snapshots.Opt

	// CommitOptions are forwarded verbatim to snapshots.Snapshotter.Commit.
	// The committer automatically appends snapshots.WithParent for all
	// non-root layers.
	CommitOptions []snapshots.Opt

	// Action is called with the active snapshot's mount points after a
	// successful Prepare. A non-nil return value aborts the whole pipeline.
	Action func(ctx context.Context, mounts []mount.Mount) error

	// DiffID is the uncompressed digest (OCI DiffID) of this layer.
	// Informational only; the pipeline derives snapshot keys from rootFS.
	DiffID digest.Digest

	// Labels holds metadata for the active snapshot.
	// [LabelSnapshotterEventIndex] must be present.
	Labels map[string]string
}

// ─────────────────────────────────────────────────────────────────────────────
// Internal types
// ─────────────────────────────────────────────────────────────────────────────

// chainInfo holds the precomputed OCI chain IDs for one layer, computed once
// by [buildChainTable] and reused for every Commit — O(1) per Commit instead
// of the O(n) DiffID scan the naïve design would perform.
type chainInfo struct {
	diffID        string // active-snapshot key passed to sn.Prepare / sn.Commit
	chainID       string // cumulative chain ID for DiffIDs[:i+1]; committed snapshot name
	parentChainID string // cumulative chain ID for DiffIDs[:i]; empty for the root layer
}

// workerResult carries the outcome of one Prepare+Action execution.
// Workers never call Commit; that is exclusively the committer's job.
type workerResult struct {
	seq   int   // 0-based position parsed from LabelSnapshotterEventIndex
	event Event // originating event; committer forwards CommitOptions to sn.Commit
	err   error
}

// ─────────────────────────────────────────────────────────────────────────────
// resultHeap — min-heap over workerResult ordered by seq
// ─────────────────────────────────────────────────────────────────────────────
//
// Chosen over map[int]workerResult:
//   - no map-bucket allocations or rehashing,
//   - O(log n) drain with excellent cache locality (contiguous backing slice),
//   - pre-allocated to numWorkers capacity → zero steady-state allocations in
//     the committer goroutine.

type resultHeap []workerResult

func (h resultHeap) Len() int           { return len(h) }
func (h resultHeap) Less(i, j int) bool { return h[i].seq < h[j].seq }
func (h resultHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *resultHeap) Push(x any)        { *h = append(*h, x.(workerResult)) }
func (h *resultHeap) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	old[n-1] = workerResult{} // release Action closure and Event refs to GC immediately
	*h = old[:n-1]
	return x
}

// ─────────────────────────────────────────────────────────────────────────────
// Chain table — O(n) upfront, O(1) per Commit
// ─────────────────────────────────────────────────────────────────────────────

// buildChainTable precomputes [chainInfo] for every layer in rootFS.
// Called exactly once per [RunSnapshotPipeline] invocation. For a 100-layer
// image this replaces 100 × O(n) per-Commit DiffID scans with one linear pass.
func buildChainTable(rootFS ocispec.RootFS) []chainInfo {
	chains := make([]chainInfo, len(rootFS.DiffIDs))
	for i, diffID := range rootFS.DiffIDs {
		chains[i].diffID = diffID.String()
		chains[i].chainID = identity.ChainID(rootFS.DiffIDs[:i+1]).String()
		// Skip parent for the root layer (i == 0). identity.ChainID([]) returns
		// a non-empty zero-digest string, so a naive "" guard would incorrectly
		// attach a WithParent option to the very first sn.Commit call.
		if i > 0 {
			chains[i].parentChainID = identity.ChainID(rootFS.DiffIDs[:i]).String()
		}
	}
	return chains
}

// ─────────────────────────────────────────────────────────────────────────────
// Pipeline entry point
// ─────────────────────────────────────────────────────────────────────────────

// RunSnapshotPipeline starts a concurrent, ordered snapshot pipeline and
// returns immediately. All work runs in background goroutines.
//
// Return values:
//   - events (chan<- [Event]): push layer events here in seq-number order,
//     then close to signal end-of-stream.
//   - errc (<-chan error): receives exactly one value — nil on success or the
//     first error. Buffered (capacity 1); the caller need not read immediately.
//
// Pipeline stages:
//
//  1. [buildChainTable] — O(n) upfront pass over rootFS.DiffIDs.
//  2. numWorkers goroutines — read events concurrently; call sn.Prepare and
//     the event's Action; forward [workerResult] to the committer.
//  3. Committer — accumulates out-of-order results in a min-heap; drains
//     strictly in seq order; calls sn.Commit via O(1) chain-table lookups.
//
// Idempotency: sn.Prepare and sn.Commit returning [errdefs.ErrAlreadyExists]
// are treated as success, enabling safe pipeline restarts after partial runs.
// For Prepare, the existing snapshot's mounts are retrieved via sn.Mounts and
// forwarded to Action unchanged.
//
// Cancellation: cancelling ctx or any error cancels a shared pipelineCtx,
// signalling all goroutines to stop. Workers keep draining events after
// cancellation so the caller is never permanently blocked on a send. Close
// events once you observe an error on errc.
//
// Preconditions:
//   - Every event's Labels map must contain [LabelSnapshotterEventIndex].
//   - Sequence numbers must be unique, contiguous from 0, and within
//     [0, len(rootFS.DiffIDs)).
//   - numWorkers ≤ 0 defaults to runtime.NumCPU.
func RunSnapshotPipeline(
	ctx context.Context,
	sn snapshots.Snapshotter,
	rootFS ocispec.RootFS,
	numWorkers int,
) (chan<- Event, <-chan error) {
	errCh := make(chan error, 1)
	chains := buildChainTable(rootFS)

	// Fast path: nothing to process.
	if len(chains) == 0 {
		ch := make(chan Event)
		close(ch)
		errCh <- nil
		return ch, errCh
	}

	if numWorkers <= 0 {
		numWorkers = runtime.NumCPU()
	}

	// eventCh: internal channel; only the send-only end is exposed to the
	// caller. Buffered to numWorkers so the caller can stay one batch ahead.
	eventCh := make(chan Event, numWorkers)

	// pipelineCtx: cancelled by the first goroutine that encounters an error,
	// causing all others to unwind cooperatively.
	pipelineCtx, cancel := context.WithCancel(ctx)

	// firstErr preserves the first real pipeline error (Prepare/Action/Commit
	// failure) so the committer can surface it instead of context.Canceled.
	//
	// Race scenario: a worker errors → storeErr → cancel() → sends result.
	// If the committer's select fires on pipelineCtx.Done() before reading
	// from resultCh, it would return context.Canceled instead of the real
	// error. CompareAndSwap (first-write-wins) guarantees the real error is
	// always visible when resolveErr is called.
	//
	// Rule: storeErr MUST be called BEFORE cancel() so the stored value is
	// visible to goroutines that observe the cancelled context.
	var firstErr atomic.Pointer[error]
	storeErr := func(err error) { firstErr.CompareAndSwap(nil, &err) }
	resolveErr := func() error {
		if p := firstErr.Load(); p != nil {
			return *p
		}
		return pipelineCtx.Err()
	}

	// resultCh: per-event outcomes from workers to the committer.
	// Buffer = numWorkers: at most numWorkers goroutines write concurrently.
	// A larger buffer (e.g. numWorkers*2) wastes memory with no benefit because
	// a goroutine that fills its slot immediately blocks — or takes the
	// pipelineCtx.Done() path — rather than queuing a second result.
	resultCh := make(chan workerResult, numWorkers)

	var wg sync.WaitGroup

	// ── Worker pool ──────────────────────────────────────────────────────────
	//
	// Workers range over eventCh. When the caller closes the returned
	// send-only channel, all range loops exit once the buffer is drained.
	//
	// Drain invariant: after cancellation workers keep consuming eventCh
	// without processing — the caller can always make forward progress on its
	// send loop even after the pipeline has been aborted.
	for range numWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for e := range eventCh {
				if pipelineCtx.Err() != nil {
					continue // drain without processing
				}
				res := processEvent(pipelineCtx, sn, chains, e)
				// Forward to committer. If the context was cancelled while
				// processEvent ran, keep draining rather than blocking.
				select {
				case resultCh <- res:
				case <-pipelineCtx.Done():
					continue
				}
				if res.err != nil {
					storeErr(res.err)
					cancel()
				}
			}
		}()
	}

	// ── Cleanup goroutine ────────────────────────────────────────────────────
	// Waits for all workers to finish, then closes resultCh to tell the
	// committer no further results will ever arrive.
	go func() {
		wg.Wait()
		close(resultCh)
	}()

	// ── Committer ────────────────────────────────────────────────────────────
	//
	// Re-serialises out-of-order results into the correct commit order.
	// sn.Commit for layer N requires layer N-1's snapshot to be committed
	// first (OCI parent-chain invariant), so commits are issued strictly in
	// seq order even though Prepare+Action run in arbitrary order.
	go func() {
		defer cancel() // always release the derived context on exit

		// Pre-allocated to numWorkers: under normal load at most numWorkers
		// results are in-flight simultaneously, so the heap never needs to
		// grow and this goroutine runs with zero steady-state allocations.
		h := make(resultHeap, 0, numWorkers)
		heap.Init(&h)

		next, total := 0, len(chains)

		for {
			select {
			case <-pipelineCtx.Done():
				// resolveErr surfaces the real error over context.Canceled.
				select {
				case errCh <- resolveErr():
				default:
				}
				return

			case res, ok := <-resultCh:
				if !ok {
					// resultCh closed — all workers have exited.
					if h.Len() == 0 {
						errCh <- nil // every seq was committed successfully
						return
					}
					// Non-empty heap: seq gap detected. If a real pipeline
					// error caused the gap, surface that instead of a
					// confusing "seq gap" message.
					if p := firstErr.Load(); p != nil {
						select {
						case errCh <- *p:
						default:
						}
						return
					}
					pending := make([]int, len(h))
					for i, r := range h {
						pending[i] = r.seq
					}
					slices.Sort(pending)
					errCh <- fmt.Errorf(
						"seq gap: expected seq %d but it never arrived; "+
							"pending seqs %v still uncommitted — "+
							"verify LabelSnapshotterEventIndex values are "+
							"unique and contiguous from 0",
						next, pending,
					)
					return
				}

				if res.err != nil {
					storeErr(res.err)
					cancel()
					errCh <- res.err
					return
				}

				heap.Push(&h, res)

				// Drain all consecutive ready seqs in one tight inner loop.
				// h[0] is always the minimum (heap invariant), so a single
				// peek tells us if the next commit is ready. Batching here
				// avoids returning to the outer select for rapid completions
				// (e.g. workers that finished seqs 3,4,5 while we committed
				// seq 2 are all committed in this pass, not across three
				// separate outer iterations).
				for h.Len() > 0 && h[0].seq == next {
					ready := heap.Pop(&h).(workerResult)
					ci := chains[next] // O(1) — no DiffID scan

					// Clone before appending WithParent to avoid mutating
					// the Event's backing array (aliasing hazard if the
					// caller reuses CommitOptions across events).
					commitOpts := cloneOpts(ready.event.Active.CommitOptions)
					if ci.parentChainID != "" {
						commitOpts = append(commitOpts, snapshots.WithParent(ci.parentChainID))
					}

					err := sn.Commit(pipelineCtx, ci.chainID, ci.diffID, commitOpts...)
					if err != nil && !errdefs.IsAlreadyExists(err) {
						// ErrAlreadyExists: this snapshot was committed in a
						// previous (partial) run — treat it as success.
						commitErr := fmt.Errorf("commit seq %d (chainID %s): %w", next, ci.chainID, err)
						storeErr(commitErr)
						cancel()
						errCh <- commitErr
						return
					}

					next++
					if next == total {
						errCh <- nil
						return
					}
				}
			}
		}
	}()

	return eventCh, errCh
}

// ─────────────────────────────────────────────────────────────────────────────
// Worker: Prepare + Action (with idempotency and cleanup)
// ─────────────────────────────────────────────────────────────────────────────

// processEvent executes the Prepare + Action lifecycle for one event.
// It never calls Commit; that is exclusively the committer's job.
//
// Idempotency: if sn.Prepare returns ErrAlreadyExists, the active snapshot
// already exists (pipeline restarted after a partial run). sn.Mounts is called
// to retrieve the existing mounts, and Action runs against them unchanged.
//
// Cleanup: if Action fails after a successful Prepare, the active snapshot is
// removed via sn.Remove to prevent resource leaks. Removal errors are ignored
// (the snapshot will be GC'd eventually); they do not replace the Action error.
func processEvent(ctx context.Context, sn snapshots.Snapshotter, chains []chainInfo, e Event) workerResult {
	rawSeq := e.Active.Labels[LabelSnapshotterEventIndex]
	seq, err := strconv.Atoi(rawSeq)
	if err != nil {
		// seq -1 is a deliberate out-of-band sentinel. Returning 0 on parse
		// failure would silently collide with the legitimate seq-0 result,
		// either displacing it in the heap or being committed in its place —
		// producing a silently corrupted snapshot chain.
		return workerResult{seq: -1, err: fmt.Errorf(
			"event label %q: invalid sequence number: %w", rawSeq, err,
		)}
	}
	if seq < 0 || seq >= len(chains) {
		return workerResult{seq: -1, err: fmt.Errorf(
			"event seq %d out of range [0, %d); check LabelSnapshotterEventIndex",
			seq, len(chains),
		)}
	}

	diffID := chains[seq].diffID

	// sn.Prepare: parent is always "" here. The parent relationship is
	// established at Commit time by the committer via snapshots.WithParent,
	// after it verifies the parent snapshot is already committed.
	mounts, err := sn.Prepare(ctx, diffID, "", e.Active.PrepareOptions...)
	switch {
	case err == nil:
		// Normal path.
	case errdefs.IsAlreadyExists(err):
		// The active snapshot already exists from a previous partial run.
		// Retrieve its mounts so Action can proceed without re-preparation.
		mounts, err = sn.Mounts(ctx, diffID)
		if err != nil {
			return workerResult{seq: seq, event: e, err: fmt.Errorf(
				"mounts seq %d (snapshot existed, could not retrieve mounts): %w", seq, err,
			)}
		}
	default:
		return workerResult{seq: seq, event: e, err: fmt.Errorf("prepare seq %d: %w", seq, err)}
	}

	if err = e.Active.Action(ctx, mounts); err != nil {
		// Remove the active snapshot to avoid leaking it. An orphaned active
		// snapshot is harmless (the GC will reclaim it) but wastes resources
		// and can cause key-collision errors on the next pipeline run if the
		// caller does not handle ErrAlreadyExists in its own Prepare paths.
		// Ignore removal errors — do not replace the Action error.
		_ = sn.Remove(ctx, diffID)
		return workerResult{seq: seq, event: e, err: fmt.Errorf("action seq %d: %w", seq, err)}
	}

	return workerResult{seq: seq, event: e}
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

// cloneOpts returns a shallow copy of opts with capacity len(opts)+1.
//
// The +1 pre-allocates the slot for snapshots.WithParent that the committer
// appends for every non-root layer. Without it, the append inside the
// committer's hot loop would trigger a realloc+copy on 99 of 100 layers for
// a typical image. With it, append writes into the pre-reserved slot for free.
//
// Returns nil for empty input to avoid a zero-length heap allocation.
func cloneOpts(opts []snapshots.Opt) []snapshots.Opt {
	if len(opts) == 0 {
		return nil
	}
	clone := make([]snapshots.Opt, len(opts), len(opts)+1)
	copy(clone, opts)
	return clone
}
