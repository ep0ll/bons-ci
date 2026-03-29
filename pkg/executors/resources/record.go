package resources

// record.go – Recorder implementations.
//
// Two implementations of types.Recorder:
//
//   cgroupRecord  – real implementation backed by a cgroupv2 namespace.
//                   Drives the Sampler, captures system CPU deltas, and
//                   collects network counters via an optional NetworkSampler.
//
//   nopRecord     – no-op stub returned when cgroupv2 is unavailable or the
//                   Monitor has been closed.  All methods are safe no-ops.
//
// cgroupRecord lifecycle:
//
//   Monitor.RecordNamespace() → cgroupRecord created (Start not yet called)
//   Executor.Run() calls Start()          → sampler starts
//   Executor.Run() returns / CloseAsync() → close() triggered
//   close() waits for sampler             → samples available
//   Samples()                             → caller retrieves results

import (
	"context"
	"path/filepath"
	"sync"
	"time"

	resourcestypes "github.com/bons/bons-ci/pkg/executors/resources/types"
	"github.com/moby/buildkit/util/bklog"
	"github.com/prometheus/procfs"
)

// ─── cgroupRecord ─────────────────────────────────────────────────────────────

// cgroupRecord tracks resource usage for a single cgroupv2 namespace.
// It is owned by the Monitor and is created by RecordNamespace.
type cgroupRecord struct {
	// immutable after creation
	ns         string
	monitor    *Monitor
	netSampler NetworkSampler

	// sampler state — set by Start()
	sub          *Sub[*resourcestypes.Sample]
	closeSampler func() error // closes the underlying Sampler
	startCPUStat *procfs.CPUStat

	// results — set by close()
	once       sync.Once
	done       chan struct{} // closed when close() finishes
	samples    []*resourcestypes.Sample
	sysCPUStat *resourcestypes.SysCPUStat
	err        error
}

// newCgroupRecord creates an uninitialised cgroupRecord.
// Start() must be called before any samples are taken.
func newCgroupRecord(ns string, m *Monitor, net NetworkSampler) *cgroupRecord {
	return &cgroupRecord{
		ns:         ns,
		monitor:    m,
		netSampler: net,
		done:       make(chan struct{}),
	}
}

// ── Recorder implementation ───────────────────────────────────────────────────

// Start begins resource sampling.  Must be called exactly once before the
// monitored operation begins.
func (r *cgroupRecord) Start() {
	// Snapshot the system-wide CPU counters so we can compute a delta in close().
	if stat, err := r.monitor.proc.Stat(); err == nil {
		cp := stat.CPUTotal
		r.startCPUStat = &cp
	}

	// Build a per-record sampler that writes into the shared Registry pipeline.
	sampler := NewSampler(2*time.Second, 10, r.collectSample)
	r.sub = sampler.Record()
	r.closeSampler = sampler.Close
}

// Close stops sampling synchronously.  Safe to call from any goroutine.
func (r *cgroupRecord) Close() {
	r.close()
}

// CloseAsync stops sampling in the background and invokes next when done.
func (r *cgroupRecord) CloseAsync(next func(context.Context) error) error {
	go func() {
		r.close()
		// CloseAsync delegates to the next stage using a background context
		// because the request context might already be cancelled or timed out.
		if err := next(context.Background()); err != nil {
			bklog.L.WithError(err).Warn("resources: CloseAsync next callback failed")
		}
	}()
	return nil
}

// Wait blocks until close() has finished.
func (r *cgroupRecord) Wait() error {
	go r.close() // safe: sync.Once ensures it runs at most once
	<-r.done
	return r.err
}

// Samples returns the collected data.  Valid only after Wait() returns nil.
func (r *cgroupRecord) Samples() (*resourcestypes.Samples, error) {
	<-r.done
	if r.err != nil {
		return nil, r.err
	}
	return &resourcestypes.Samples{
		Samples:    r.samples,
		SysCPUStat: r.sysCPUStat,
	}, nil
}

// ── Internal ──────────────────────────────────────────────────────────────────

// close is the idempotent teardown implementation.  sync.Once guarantees it
// executes exactly once regardless of how many concurrent callers invoke
// Close, CloseAsync, or Wait.
func (r *cgroupRecord) close() {
	r.once.Do(func() {
		defer close(r.done)

		// Unregister from the Monitor's live-record map.
		go func() {
			r.monitor.mu.Lock()
			delete(r.monitor.records, r.ns)
			r.monitor.mu.Unlock()
		}()

		// If Start() was never called (e.g. the container failed immediately),
		// there is nothing to collect.
		if r.sub == nil {
			return
		}

		// Close the subscription, capturing one final sample.
		samples, err := r.sub.Close(true /* captureLast */)
		if err != nil {
			r.err = err
		} else {
			r.samples = samples
		}
		r.closeSampler()

		// Compute system-wide CPU delta.
		r.sysCPUStat = r.computeSysCPUDelta()
	})
}

// collectSample is the per-tick callback passed to NewSampler.
// It drives the Registry to collect all enabled controllers for r.ns.
func (r *cgroupRecord) collectSample(tm time.Time) (*resourcestypes.Sample, error) {
	cgroupPath := filepath.Join(defaultMountpoint, r.ns)
	sample, err := r.monitor.registry.Collect(context.Background(), cgroupPath, tm)
	if err != nil {
		return nil, err
	}

	// Attach network stats if a sampler is registered.
	if r.netSampler != nil {
		net, err := r.netSampler.Sample()
		if err != nil {
			return nil, err
		}
		sample.NetStat = net
	}
	return sample, nil
}

// computeSysCPUDelta calculates the system-wide CPU delta from r.startCPUStat
// to the current /proc/stat reading.  Returns nil if the measurement fails.
func (r *cgroupRecord) computeSysCPUDelta() *resourcestypes.SysCPUStat {
	if r.startCPUStat == nil {
		return nil
	}
	stat, err := r.monitor.proc.Stat()
	if err != nil {
		return nil
	}
	end := stat.CPUTotal
	start := r.startCPUStat
	return &resourcestypes.SysCPUStat{
		User:      end.User - start.User,
		Nice:      end.Nice - start.Nice,
		System:    end.System - start.System,
		Idle:      end.Idle - start.Idle,
		Iowait:    end.Iowait - start.Iowait,
		IRQ:       end.IRQ - start.IRQ,
		SoftIRQ:   end.SoftIRQ - start.SoftIRQ,
		Steal:     end.Steal - start.Steal,
		Guest:     end.Guest - start.Guest,
		GuestNice: end.GuestNice - start.GuestNice,
	}
}

// ─── nopRecord ────────────────────────────────────────────────────────────────

// nopRecord is a no-op Recorder returned when:
//   - The system does not use cgroupv2.
//   - The Monitor has been closed.
//
// All methods are safe to call and return zero-value results.
type nopRecord struct{}

func (*nopRecord) Start()                                           {}
func (*nopRecord) Close()                                           {}
func (*nopRecord) Wait() error                                      { return nil }
func (*nopRecord) Samples() (*resourcestypes.Samples, error)        { return nil, nil }
func (*nopRecord) CloseAsync(next func(context.Context) error) error { return next(context.Background()) }
