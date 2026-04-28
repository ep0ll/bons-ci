// Package pipeline implements a concurrent, ordered snapshot pipeline for
// processing OCI image layers via the containerd snapshotter API.
//
// # Architecture
//
// The pipeline has three cooperating stages:
//
//  1. Chain table — [buildChainTable] makes one O(n) pass over rootFS.DiffIDs
//     and precomputes every layer's diffID, chainID, and parentChainID. All
//     subsequent operations are O(1) lookups into this table.
//
//  2. Worker pool — N goroutines read [Event] values from the caller-owned
//     channel and execute Prepare + Action concurrently. Workers never call
//     Commit; they forward [workerResult] values to the committer.
//
//  3. Committer — a single goroutine accumulates out-of-order results in a
//     min-heap and drains them strictly in sequence-number order, calling
//     sn.Commit for each layer. This preserves the OCI parent-chain invariant
//     (layer N's committed snapshot must exist before layer N+1 is committed)
//     while keeping CPU and I/O utilisation high across the worker pool.
//
// # Data scopes
//
//	RootFS / chain IDs  →  image scope    →  pipeline (computed once)
//	Prepare / Action    →  layer scope    →  [Event]
//	Commit order        →  pipeline scope →  committer
//
// # Typical usage
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
	"errors"
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
// string of the event's 0-based position in the stream. Every [Event] must
// carry this label; workers parse it so the committer can re-serialise
// out-of-order completions.
const LabelSnapshotterEventIndex = "containerd.io/snapshot/event-index"

// LabelUncompressed is the conventional containerd label key for the
// uncompressed layer digest (OCI DiffID). The pipeline derives snapshot keys
// from the rootFS passed to [RunSnapshotPipeline] and does not read this
// label itself; callers may set it on snapshotter options for external tooling
// that inspects snapshot metadata.
const LabelUncompressed = "containerd.io/uncompressed"

// ─────────────────────────────────────────────────────────────────────────────
// Public event types
// ─────────────────────────────────────────────────────────────────────────────

// Event carries per-layer snapshotter lifecycle options for one layer.
//
// The image-scoped RootFS is intentionally absent: it is passed once to
// [RunSnapshotPipeline], which precomputes a chain table shared across all
// events. Embedding per-event RootFS copies would waste memory, enlarge
// channel messages, increase GC pressure, and risk inconsistent values across
// concurrent workers.
type Event struct {
	// Active describes the Prepare/Commit lifecycle for this layer.
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

// chainInfo holds the precomputed OCI chain IDs for one layer. Built once by
// [buildChainTable] and shared read-only across all workers and the committer.
type chainInfo struct {
	diffID        string // OCI DiffID string; used as active-snapshot key
	chainID       string // cumulative chain ID for DiffIDs[:i+1]; committed snapshot name
	parentChainID string // cumulative chain ID for DiffIDs[:i]; empty for the root layer
}

// workerResult carries the outcome of one Prepare+Action execution.
// Workers never call Commit; that is exclusively the committer's responsibility.
type workerResult struct {
	seq              int   // 0-based position parsed from LabelSnapshotterEventIndex
	event            Event // originating event; committer forwards CommitOptions to sn.Commit
	err              error
	alreadyCommitted bool // true when chainID already exists as a committed snapshot
}

// ─────────────────────────────────────────────────────────────────────────────
// resultHeap — min-heap over workerResult ordered by seq
// ─────────────────────────────────────────────────────────────────────────────
//
// A heap is preferred over map[int]workerResult because:
//   - No map-bucket allocations or rehashing.
//   - O(log n) drain with excellent cache locality (contiguous backing slice).
//   - Pre-allocated to numWorkers capacity → zero steady-state allocations in
//     the committer goroutine under normal load.

type resultHeap []workerResult

func (h resultHeap) Len() int           { return len(h) }
func (h resultHeap) Less(i, j int) bool { return h[i].seq < h[j].seq }
func (h resultHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *resultHeap) Push(x any)        { *h = append(*h, x.(workerResult)) }
func (h *resultHeap) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	old[n-1] = workerResult{} // release Action closure and Event refs promptly
	*h = old[:n-1]
	return x
}

// ─────────────────────────────────────────────────────────────────────────────
// Chain table — O(n) upfront, O(1) per Commit
// ─────────────────────────────────────────────────────────────────────────────

// buildChainTable precomputes [chainInfo] for every layer in rootFS.
//
// Called exactly once per [RunSnapshotPipeline] invocation. For a 100-layer
// image this replaces 100 × O(n) per-Commit DiffID scans with one linear pass.
//
// Root-layer special case: identity.ChainID of an empty slice returns a
// non-empty zero-digest string, so parentChainID is set only for i > 0.
// Setting it for the root layer would incorrectly attach a WithParent option
// to the very first sn.Commit call.
func buildChainTable(rootFS ocispec.RootFS) []chainInfo {
	chains := make([]chainInfo, len(rootFS.DiffIDs))
	for i, diffID := range rootFS.DiffIDs {
		chains[i].diffID = diffID.String()
		chains[i].chainID = identity.ChainID(rootFS.DiffIDs[:i+1]).String()
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
// # Parameters
//
//   - ctx: Parent context. Cancellation stops all goroutines cooperatively.
//   - sn: Snapshotter implementation. Must be safe for concurrent use.
//   - rootFS: OCI RootFS describing all layers. Must not change after the call.
//   - numWorkers: Number of parallel Prepare+Action goroutines. ≤ 0 defaults
//     to runtime.NumCPU.
//
// # Return values
//
//   - events (chan<- [Event]): push layer events in any order, then close to
//     signal end-of-stream. The channel is buffered to numWorkers.
//   - errc (<-chan error): receives exactly one value — nil on success or the
//     first error encountered. Buffered (capacity 1); safe to read lazily.
//
// # Preconditions
//
//   - Every event's Labels map must contain [LabelSnapshotterEventIndex].
//   - Sequence numbers must be unique, contiguous starting at 0, and within
//     [0, len(rootFS.DiffIDs)).
//   - Close events only after all layers are delivered.
//
// # Idempotency
//
// sn.Prepare and sn.Commit returning [errdefs.ErrAlreadyExists] are treated
// as success, enabling safe pipeline restarts after partial runs. For Prepare,
// the existing snapshot's mounts are retrieved via sn.Mounts and forwarded to
// Action unchanged. Layers whose committed snapshot already exists (detected
// via sn.Stat) bypass Prepare and Action entirely.
//
// # Cancellation and error propagation
//
// The first error stores the real failure then cancels a shared pipelineCtx,
// signalling all goroutines to unwind. Workers keep draining the event channel
// after cancellation so the caller's send loop never blocks permanently. The
// committer always surfaces the real error rather than the derived
// context.Canceled from pipelineCtx.
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

	// eventCh is the internal channel; only the send-only end is returned.
	// Buffered to numWorkers so the caller can stay one batch ahead.
	eventCh := make(chan Event, numWorkers)

	// pipelineCtx is cancelled by the first goroutine that encounters an
	// error, causing all others to unwind cooperatively.
	pipelineCtx, cancel := context.WithCancel(ctx)

	// firstErr preserves the real pipeline error so the committer can surface
	// it instead of the derived context.Canceled. CompareAndSwap ensures
	// first-write-wins across racing goroutines.
	//
	// Ordering requirement: storeErr MUST be called before cancel() so the
	// stored value is visible to goroutines that observe the cancelled context.
	var firstErr atomic.Pointer[error]
	storeErr := func(err error) { firstErr.CompareAndSwap(nil, &err) }
	resolveErr := func() error {
		if p := firstErr.Load(); p != nil {
			return *p
		}
		return pipelineCtx.Err()
	}

	// resultCh carries per-event outcomes from workers to the committer.
	// Capacity numWorkers: at most numWorkers goroutines write concurrently,
	// so this never causes unnecessary blocking under normal load.
	resultCh := make(chan workerResult, numWorkers)

	var wg sync.WaitGroup

	// ── Worker pool ──────────────────────────────────────────────────────────
	//
	// Workers range over eventCh. When the caller closes the send-only
	// channel, all range loops exit once the buffer is drained.
	//
	// Drain invariant: after cancellation, workers keep consuming eventCh
	// without processing so the caller's send loop always makes progress.
	for range numWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for e := range eventCh {
				if pipelineCtx.Err() != nil {
					continue // drain without processing
				}
				res := processEvent(pipelineCtx, sn, chains, e)

				// Record the error before the select so it is always stored
				// regardless of which branch fires. storeErr is a CAS
				// (first-write-wins) and is safe to call from any goroutine.
				if res.err != nil {
					storeErr(res.err)
					cancel()
				}

				// Forward result to committer. If the context was cancelled
				// while processEvent ran, drain rather than blocking.
				select {
				case resultCh <- res:
				case <-pipelineCtx.Done():
					continue
				}
			}
		}()
	}

	// ── Cleanup goroutine ────────────────────────────────────────────────────
	// Waits for all workers then closes resultCh, signalling the committer
	// that no further results will ever arrive.
	go func() {
		wg.Wait()
		close(resultCh)
	}()

	// ── Committer ────────────────────────────────────────────────────────────
	//
	// Re-serialises out-of-order results into commit order. sn.Commit for
	// layer N requires layer N-1's snapshot to be committed first (OCI
	// parent-chain invariant), so commits are issued strictly in seq order
	// even though Prepare+Action run in arbitrary order.
	go func() {
		defer cancel() // always release the derived context on exit

		// Pre-allocated to numWorkers: under normal load at most numWorkers
		// results are in-flight simultaneously, so the heap never grows and
		// this goroutine runs with zero steady-state allocations.
		h := make(resultHeap, 0, numWorkers)
		heap.Init(&h)

		next, total := 0, len(chains)

		for {
			select {
			case <-pipelineCtx.Done():
				select {
				case errCh <- resolveErr():
				default:
				}
				return

			case res, ok := <-resultCh:
				if !ok {
					// resultCh closed — all workers have exited.
					if h.Len() == 0 {
						if next == total {
							errCh <- nil // every layer committed successfully
							return
						}
						// Workers may have failed before producing a result
						// (e.g. observed cancellation on send). Surface the
						// real failure rather than a misleading "early close".
						if p := firstErr.Load(); p != nil {
							errCh <- *p
							return
						}
						if err := pipelineCtx.Err(); err != nil {
							errCh <- err
							return
						}
						// Caller closed the channel before delivering all layers.
						errCh <- fmt.Errorf(
							"early close: committed %d of %d layer(s) before the "+
								"event channel was closed (missing seqs [%d, %d)); "+
								"close the channel only after all layers are delivered",
							next, total, next, total,
						)
						return
					}

					// Non-empty heap with a closed resultCh means a sequence
					// gap: the expected next seq never arrived. If a real
					// pipeline error caused the gap, surface that instead of a
					// confusing gap message.
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

				// Drain all consecutive ready seqs in one inner loop.
				// h[0] is always the minimum (heap invariant), so a single
				// peek tells us whether the next commit is ready. Batching
				// avoids returning to the outer select for rapid completions.
				for h.Len() > 0 && h[0].seq == next {
					ready := heap.Pop(&h).(workerResult)
					ci := chains[next] // O(1) chain-table lookup

					if ready.alreadyCommitted {
						next++
						if next == total {
							errCh <- nil
							return
						}
						continue
					}

					// Clone before appending WithParent to avoid mutating
					// the Event's backing array (aliasing hazard if the
					// caller reuses CommitOptions across events).
					commitOpts := cloneOpts(ready.event.Active.CommitOptions)
					if ci.parentChainID != "" {
						commitOpts = append(commitOpts, snapshots.WithParent(ci.parentChainID))
					}

					err := sn.Commit(pipelineCtx, ci.chainID, ci.diffID, commitOpts...)
					if err != nil && !errdefs.IsAlreadyExists(err) {
						// If cancellation was triggered by an earlier worker
						// error, prefer that original failure over a derived
						// context error from this Commit call.
						if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
							if p := firstErr.Load(); p != nil {
								errCh <- *p
								return
							}
						}
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
// Worker: Prepare + Action
// ─────────────────────────────────────────────────────────────────────────────

// processEvent executes the Prepare + Action lifecycle for one event.
// It never calls Commit; that is exclusively the committer's responsibility.
//
// # Idempotency
//
// If sn.Stat reveals that chainID already exists as a committed snapshot, the
// function returns immediately with alreadyCommitted=true and skips both
// Prepare and Action. If sn.Prepare returns ErrAlreadyExists (the active
// snapshot exists from a partial prior run), sn.Mounts retrieves the existing
// mount points and Action proceeds against them unchanged.
//
// # Cleanup
//
// If Action fails after a successful Prepare, the active snapshot is removed
// via sn.Remove to prevent resource leaks. Removal errors are ignored and do
// not replace the Action error; the snapshot will be collected by the
// snapshotter GC.
//
// # Error sentinel
//
// Parse and range errors return seq=-1 with a non-nil err. The committer
// checks res.err before heap.Push, so seq=-1 never enters the heap. Returning
// seq=0 on parse failure would silently collide with the legitimate seq-0
// result, either displacing it in the heap or being committed in its place.
func processEvent(ctx context.Context, sn snapshots.Snapshotter, chains []chainInfo, e Event) workerResult {
	rawSeq := e.Active.Labels[LabelSnapshotterEventIndex]
	seq, err := strconv.Atoi(rawSeq)
	if err != nil {
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
	chainID := chains[seq].chainID

	// Fast-path: if the committed snapshot already exists from a previous
	// completed pipeline run, bypass Prepare and Action entirely.
	if info, err := sn.Stat(ctx, chainID); err == nil && info.Kind == snapshots.KindCommitted {
		return workerResult{seq: seq, event: e, alreadyCommitted: true}
	}

	// sn.Prepare: parent is always "" here. The parent relationship is
	// established at Commit time by the committer via snapshots.WithParent,
	// after verifying the parent snapshot is already committed.
	mounts, err := sn.Prepare(ctx, diffID, "", e.Active.PrepareOptions...)
	switch {
	case err == nil:
		// Normal path — active snapshot created.
	case errdefs.IsAlreadyExists(err):
		// Active snapshot exists from a previous partial run.
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
		// Best-effort cleanup: remove the active snapshot to avoid leaking
		// resources and to prevent key-collision errors on the next pipeline
		// run. Removal errors are ignored.
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
// The extra capacity pre-allocates the slot for the snapshots.WithParent
// option that the committer appends for every non-root layer. Without it,
// the append would trigger a realloc+copy on almost every Commit call.
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
