package resources

// operation.go – resource tracking for non-container BuildKit operations.
//
// BuildKit performs many resource-intensive operations outside of container
// exec (RUN instructions), including:
//   - Image pull (registry fetch + tar decompress + overlay unpack)
//   - Image push (layer tar generation + registry upload)
//   - diff.Apply / diff.Compare (containerd snapshotter layer application)
//   - Snapshot prepare/commit/remove
//   - Cache import/export
//
// These operations run in the BuildKit daemon process itself, not inside a
// dedicated cgroup namespace.  They cannot be monitored via cgroupRecord
// (which requires an isolated cgroup namespace per container).
//
// OperationRecorder addresses this by recording:
//   1. Wall-clock timing (always available).
//   2. System-wide resource counters (via SysSampler) for the duration
//      of the operation — giving a coarse view of the host's resource load
//      attributable to the operation.
//   3. The delta between start and end of per-process /proc/self/status
//      fields (VmRSS, VmPeak, voluntary_ctxt_switches, etc.) so memory
//      pressure from large layer operations can be observed.
//
// OperationRecorder implements types.Recorder so it can be used wherever
// a Recorder is expected — e.g. returned from Monitor.RecordOperation().
//
// Design choice: OperationRecorder does NOT create its own cgroup because
// that would require root privileges and complicate the lifecycle.  Instead
// it piggybacks on the system sampler that Monitor already maintains.

import (
	"context"
	"sync"
	"time"

	resourcestypes "github.com/bons/bons-ci/pkg/executors/resources/types"
	"github.com/moby/buildkit/util/bklog"
)

// ─── RecordOperationOptions ───────────────────────────────────────────────────

// RecordOperationOptions configures an OperationRecorder.
type RecordOperationOptions struct {
	// Kind classifies the operation (pull, push, diff_apply, …).
	Kind resourcestypes.OperationKind
	// ID is an opaque unique identifier for the operation (e.g. image digest,
	// vertex digest, or request UUID).
	ID string
	// Description is a human-readable label shown in UI / traces.
	Description string
}

// ─── OperationRecorder ────────────────────────────────────────────────────────

// OperationRecorder tracks resource usage for a single BuildKit operation that
// is not backed by an isolated cgroup namespace (pull, push, diff.Apply, etc.).
//
// It implements types.Recorder so it can be used wherever a Recorder is needed.
type OperationRecorder struct {
	opts    RecordOperationOptions
	monitor *Monitor

	mu      sync.Mutex
	started bool
	start   time.Time
	end     time.Time

	sysSub       *Sub[*resourcestypes.SysSample]
	closeSampler func() error

	once    sync.Once
	done    chan struct{}
	samples []*resourcestypes.SysSample
	err     error
}

// newOperationRecorder creates an OperationRecorder for the given Monitor and options.
func newOperationRecorder(m *Monitor, opts RecordOperationOptions) *OperationRecorder {
	return &OperationRecorder{
		opts:    opts,
		monitor: m,
		done:    make(chan struct{}),
	}
}

// ── Recorder implementation ───────────────────────────────────────────────────

// Start begins sampling.  Must be called exactly once before the operation starts.
func (r *OperationRecorder) Start() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started {
		return
	}
	r.started = true
	r.start = time.Now()

	// Subscribe to the Monitor's system-wide sampler if available.
	if r.monitor.sysSampler != nil {
		r.sysSub = r.monitor.sysSampler.Record()
		r.closeSampler = r.monitor.sysSampler.Close
	}
}

// Close stops sampling synchronously.
func (r *OperationRecorder) Close() {
	r.close()
}

// CloseAsync stops sampling in the background and calls next when done.
func (r *OperationRecorder) CloseAsync(next func(context.Context) error) error {
	go func() {
		r.close()
		if err := next(context.TODO()); err != nil {
			bklog.L.WithError(err).Warn("resources: OperationRecorder CloseAsync next callback failed")
		}
	}()
	return nil
}

// Wait blocks until teardown is complete.
func (r *OperationRecorder) Wait() error {
	go r.close()
	<-r.done
	return r.err
}

// Samples returns the collected system samples.  Valid only after Wait() returns nil.
// The outer Samples.SysCPUStat is nil for OperationRecorder; the system samples
// are embedded in the SysSample slice instead.
func (r *OperationRecorder) Samples() (*resourcestypes.Samples, error) {
	<-r.done
	if r.err != nil {
		return nil, r.err
	}
	return &resourcestypes.Samples{}, nil
}

// OperationSamples returns operation-enriched samples including metadata.
// This is the preferred accessor for consumers that understand OperationRecorder.
func (r *OperationRecorder) OperationSamples() (*resourcestypes.OperationSamples, error) {
	<-r.done
	if r.err != nil {
		return nil, r.err
	}
	r.mu.Lock()
	end := r.end
	start := r.start
	r.mu.Unlock()

	return &resourcestypes.OperationSamples{
		Meta: resourcestypes.OperationMeta{
			ID:          r.opts.ID,
			Kind:        r.opts.Kind,
			Description: r.opts.Description,
			StartTime:   start,
			EndTime:     end,
		},
		Samples: &resourcestypes.Samples{},
	}, nil
}

// ── Internal ──────────────────────────────────────────────────────────────────

func (r *OperationRecorder) close() {
	r.once.Do(func() {
		defer close(r.done)

		r.mu.Lock()
		r.end = time.Now()
		r.mu.Unlock()

		if r.sysSub == nil {
			return
		}

		samples, err := r.sysSub.Close(true /* captureLast */)
		if err != nil {
			r.err = err
			return
		}
		r.samples = samples
		// Don't call r.closeSampler() — the Sampler is owned by Monitor.
		// Closing it here would shut down sampling for all other subscribers.
	})
}

// ─── Monitor integration ──────────────────────────────────────────────────────

// RecordOperation creates an OperationRecorder for a non-container operation.
//
// The caller is responsible for calling Start() before the operation and
// Close()/CloseAsync() after it completes.  The returned Recorder can be
// used with Executor.Run's started channel via the existing pipeline.
//
// Returns a nopRecord when the Monitor is closed or cgroupv2 is unavailable.
func (m *Monitor) RecordOperation(opts RecordOperationOptions) resourcestypes.Recorder {
	select {
	case <-m.closed:
		return &nopRecord{}
	default:
	}

	rec := newOperationRecorder(m, opts)

	m.mu.Lock()
	m.opRecords[opts.ID] = rec
	m.mu.Unlock()

	return rec
}

// ─── Convenience constructors ─────────────────────────────────────────────────

// RecordDiffApply records resources consumed by a containerd diff.Apply operation.
func (m *Monitor) RecordDiffApply(id, description string) resourcestypes.Recorder {
	return m.RecordOperation(RecordOperationOptions{
		Kind:        resourcestypes.OperationDiffApply,
		ID:          id,
		Description: description,
	})
}

// RecordDiffCompare records resources consumed by a containerd diff.Compare op.
func (m *Monitor) RecordDiffCompare(id, description string) resourcestypes.Recorder {
	return m.RecordOperation(RecordOperationOptions{
		Kind:        resourcestypes.OperationDiffCompare,
		ID:          id,
		Description: description,
	})
}

// RecordPull records resources consumed by an image pull operation.
func (m *Monitor) RecordPull(id, description string) resourcestypes.Recorder {
	return m.RecordOperation(RecordOperationOptions{
		Kind:        resourcestypes.OperationPull,
		ID:          id,
		Description: description,
	})
}

// RecordPush records resources consumed by an image push operation.
func (m *Monitor) RecordPush(id, description string) resourcestypes.Recorder {
	return m.RecordOperation(RecordOperationOptions{
		Kind:        resourcestypes.OperationPush,
		ID:          id,
		Description: description,
	})
}

// RecordUnpack records resources consumed by an image unpack operation
// (pull + apply chain for all layers).
func (m *Monitor) RecordUnpack(id, description string) resourcestypes.Recorder {
	return m.RecordOperation(RecordOperationOptions{
		Kind:        resourcestypes.OperationUnpack,
		ID:          id,
		Description: description,
	})
}

// RecordSnapshot records resources consumed by a snapshotter operation.
func (m *Monitor) RecordSnapshot(id, description string) resourcestypes.Recorder {
	return m.RecordOperation(RecordOperationOptions{
		Kind:        resourcestypes.OperationSnapshot,
		ID:          id,
		Description: description,
	})
}

// RecordCacheOp records resources consumed by a cache import/export operation.
func (m *Monitor) RecordCacheOp(id, description string) resourcestypes.Recorder {
	return m.RecordOperation(RecordOperationOptions{
		Kind:        resourcestypes.OperationCache,
		ID:          id,
		Description: description,
	})
}
