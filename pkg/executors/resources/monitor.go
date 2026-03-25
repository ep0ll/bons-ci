package resources

// monitor.go – central resource monitoring orchestrator.
//
// Monitor owns:
//   - A Controller Registry that drives per-sample collection.
//   - A map of live cgroupRecord values (one per active container namespace).
//   - A map of live OperationRecorder values (pull, push, diff, etc.).
//   - An optional SysSampler for system-wide /proc stats.
//   - A procfs.FS handle for /proc/stat CPU delta computation.
//
// Concurrency model:
//   - mu protects records and opRecords maps.
//   - records are created in RecordNamespace (Monitor goroutine), removed in
//     cgroupRecord.close() (arbitrary goroutine).
//   - closed is a channel: closing it broadcasts "monitor is shutting down"
//     to all in-flight Record calls.
//
// cgroupv2 only: when IsCgroupV2() returns false, all Record methods return nopRecord.

import (
	"sync"

	resourcestypes "github.com/bons/bons-ci/pkg/executors/resources/types"
	"github.com/moby/buildkit/util/bklog"
	"github.com/prometheus/procfs"
)

// ─── Monitor ─────────────────────────────────────────────────────────────────

// Monitor is the top-level resource monitoring handle for one BuildKit daemon
// instance.  Create it once with NewMonitor and pass it to the executor.
//
// All public methods are safe for concurrent use.
type Monitor struct {
	mu        sync.Mutex
	closed    chan struct{}
	records   map[string]*cgroupRecord      // namespace → active cgroupRecord
	opRecords map[string]*OperationRecorder // id → active OperationRecorder

	registry   *Registry                           // pluggable controller registry
	proc       procfs.FS                           // /proc reader
	sysSampler *Sampler[*resourcestypes.SysSample] // nil on non-Linux
}

// ─── RecordNamespaceOptions ───────────────────────────────────────────────────

// RecordNamespaceOptions configures a namespace recording session.
type RecordNamespaceOptions struct {
	// NetworkSampler provides per-sample network counters for the container.
	// If nil, network stats are omitted from Sample.NetStat.
	NetworkSampler NetworkSampler
}

// ─── Constructor ──────────────────────────────────────────────────────────────

// NewMonitor creates a Monitor and performs one-time cgroupv2 setup.
//
// Setup steps (Linux only, cgroupv2 only):
//  1. Detect cgroupv2 via statfs(2) on /sys/fs/cgroup (result cached globally).
//  2. Optionally move the process to the "init" cgroup and enable all available
//     controllers when BUILDKIT_SETUP_CGROUPV2_ROOT=1.
//  3. Start the system-wide SysSampler for host-level /proc metrics.
//
// Returns an error only when /proc cannot be opened (broken system).
func NewMonitor() (*Monitor, error) {
	// One-time cgroupv2 detection and optional controller setup.
	// IsCgroupV2() caches the result via sync.Once.
	if IsCgroupV2() {
		if err := prepareCgroupControllers(); err != nil {
			bklog.L.Warnf("resources: failed to prepare cgroup controllers: %v", err)
		}
	}

	pfs, err := procfs.NewDefaultFS()
	if err != nil {
		return nil, err
	}

	m := &Monitor{
		closed:    make(chan struct{}),
		records:   make(map[string]*cgroupRecord),
		opRecords: make(map[string]*OperationRecorder),
		registry:  NewRegistry(),
		proc:      pfs,
	}

	// Start system-wide sampler (Linux only; returns nil on other platforms).
	sampler, err := newSysSampler()
	if err != nil {
		bklog.L.Warnf("resources: failed to start system sampler: %v", err)
	}
	m.sysSampler = sampler

	return m, nil
}

// ─── Controller extension ────────────────────────────────────────────────────

// RegisterController adds a Controller to the underlying Registry.
// Must be called before any Record methods are invoked.
func (m *Monitor) RegisterController(c Controller) {
	m.registry.Register(c)
}

// ─── Namespace recording ─────────────────────────────────────────────────────

// RecordNamespace creates a Recorder for a container identified by its
// cgroupv2 namespace path (relative to /sys/fs/cgroup).
//
// Returns a nopRecord when:
//   - The system does not use cgroupv2.
//   - The Monitor has been closed.
//
// The caller is responsible for calling Start() then Close()/CloseAsync().
func (m *Monitor) RecordNamespace(ns string, opt RecordNamespaceOptions) (resourcestypes.Recorder, error) {
	select {
	case <-m.closed:
		return &nopRecord{}, nil
	default:
	}
	if !IsCgroupV2() {
		return &nopRecord{}, nil
	}

	r := newCgroupRecord(ns, m, opt.NetworkSampler)

	m.mu.Lock()
	m.records[ns] = r
	m.mu.Unlock()

	return r, nil
}

// ─── Graceful shutdown ────────────────────────────────────────────────────────

// Close shuts down the Monitor, stopping all active records and the system
// sampler.  After Close returns, no new records will be started.
//
// In-flight records complete their close() sequence independently;
// Close does not wait for them.
func (m *Monitor) Close() error {
	close(m.closed)

	// Snapshot active records without holding the lock during teardown.
	m.mu.Lock()
	recs := make([]*cgroupRecord, 0, len(m.records))
	for _, r := range m.records {
		recs = append(recs, r)
	}
	opRecs := make([]*OperationRecorder, 0, len(m.opRecords))
	for _, r := range m.opRecords {
		opRecs = append(opRecs, r)
	}
	m.mu.Unlock()

	for _, r := range recs {
		r.close()
	}
	for _, r := range opRecs {
		r.close()
	}
	if m.sysSampler != nil {
		_ = m.sysSampler.Close()
	}
	return nil
}
