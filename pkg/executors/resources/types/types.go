// Package types defines the data contracts for BuildKit's resource monitoring
// subsystem. All concrete monitoring code in the parent package depends only on
// these types, keeping the wire format decoupled from the collection machinery.
//
// Design notes
//
//   - Every numeric field that may legitimately be absent (e.g. a cgroup
//     controller that is not enabled) is expressed as *T so consumers can
//     distinguish "zero" from "not collected".
//   - Event counters (OOM, etc.) are non-pointer uint64 because zero is a
//     meaningful and common value.
//   - JSON tags are camelCase to match the existing BuildKit API surface.
//   - Pressure fields mirror the Linux PSI ABI exactly (avg10/avg60/avg300/total).
package types

import (
	"context"
	"time"
)

// ─── Recorder ─────────────────────────────────────────────────────────────────

// Recorder tracks resource consumption for a single BuildKit operation
// (container exec, image pull/push, snapshot diff, etc.).
//
// Lifecycle contract (must be followed by every implementation):
//
//	Start()   – called once, immediately before the operation begins.
//	           Initialises baseline counters and starts the background sampler.
//	Close()   – called once when the operation ends (synchronous variant).
//	CloseAsync() – asynchronous variant; calls next() when teardown is done.
//	Wait()    – blocks until all background work (sampling, hashing, …) is done.
//	Samples() – returns collected data; only valid after Wait() returns nil.
//
// Implementations must be safe to call Close/CloseAsync from any goroutine.
// Start and Wait are always called from the same goroutine as the operation.
type Recorder interface {
	// Start begins resource sampling. Must be called before the operation starts.
	Start()
	// Close stops sampling and blocks until all goroutines have exited.
	Close()
	// CloseAsync stops sampling in the background and invokes next when done.
	// It returns immediately; next is guaranteed to be called exactly once.
	CloseAsync(next func(context.Context) error) error
	// Wait blocks until sampling and cleanup are fully complete.
	Wait() error
	// Samples returns the collected resource samples.
	// Callers must call Wait() first; behaviour is undefined otherwise.
	Samples() (*Samples, error)
}

// ─── Top-level aggregates ──────────────────────────────────────────────────────

// Samples is the final output produced by a Recorder after the operation ends.
type Samples struct {
	// Samples is the time-series of per-cgroup snapshots taken during the op.
	Samples []*Sample `json:"samples,omitempty"`
	// SysCPUStat is the delta of system-wide /proc/stat CPU counters across
	// the operation. Useful for correlating container CPU with host load.
	SysCPUStat *SysCPUStat `json:"sysCPUStat,omitempty"`
}

// Sample is a single point-in-time snapshot of all enabled cgroup v2
// controllers for one namespace/container.
type Sample struct {
	// Timestamp_ is the wall-clock time at which this sample was taken.
	// The underscore suffix prevents collision with the Timestamp() method.
	Timestamp_ time.Time `json:"timestamp"`

	CPUStat    *CPUStat    `json:"cpuStat,omitempty"`
	MemoryStat *MemoryStat `json:"memoryStat,omitempty"`
	IOStat     *IOStat     `json:"ioStat,omitempty"`
	PIDsStat   *PIDsStat   `json:"pidsStat,omitempty"`
	NetStat    *NetworkSample `json:"netStat,omitempty"`
}

// Timestamp implements sampler.WithTimestamp so Sample can be used as the
// generic type parameter of Sampler[*Sample].
func (s *Sample) Timestamp() time.Time { return s.Timestamp_ }

// ─── Per-controller stats ─────────────────────────────────────────────────────

// CPUStat mirrors the cgroupv2 cpu.stat file plus the optional cpu.pressure PSI
// stall metrics. All time values are converted from microseconds to nanoseconds
// so they are directly comparable to Go's time.Duration.
type CPUStat struct {
	// UsageNanos is total CPU time consumed (user + system) in nanoseconds.
	UsageNanos *uint64 `json:"usageNanos,omitempty"`
	// UserNanos is user-mode CPU time in nanoseconds.
	UserNanos *uint64 `json:"userNanos,omitempty"`
	// SystemNanos is kernel-mode CPU time in nanoseconds.
	SystemNanos *uint64 `json:"systemNanos,omitempty"`
	// NrPeriods is the number of CFS bandwidth enforcement periods elapsed.
	NrPeriods *uint32 `json:"nrPeriods,omitempty"`
	// NrThrottled is the number of periods in which this cgroup was throttled.
	NrThrottled *uint32 `json:"nrThrottled,omitempty"`
	// ThrottledNanos is the total duration the cgroup spent throttled, in ns.
	ThrottledNanos *uint64 `json:"throttledNanos,omitempty"`
	// Pressure contains PSI stall metrics (requires CONFIG_PSI=y).
	Pressure *Pressure `json:"pressure,omitempty"`
}

// MemoryStat aggregates all cgroupv2 memory controller files for a cgroup:
// memory.stat, memory.peak, memory.swap.current, memory.events, memory.pressure.
type MemoryStat struct {
	// SwapBytes is the current swap usage in bytes (memory.swap.current).
	SwapBytes *uint64 `json:"swapBytes,omitempty"`
	// Anon is anonymous memory (stack, heap) in bytes.
	Anon *uint64 `json:"anon,omitempty"`
	// File is file-backed memory (page cache) in bytes.
	File *uint64 `json:"file,omitempty"`
	// Kernel is total kernel memory attributed to this cgroup.
	Kernel *uint64 `json:"kernel,omitempty"`
	// KernelStack is kernel stack memory in bytes.
	KernelStack *uint64 `json:"kernelStack,omitempty"`
	// PageTables is page table memory in bytes.
	PageTables *uint64 `json:"pageTables,omitempty"`
	// Sock is socket buffer memory in bytes.
	Sock *uint64 `json:"sock,omitempty"`
	// Vmalloc is vmalloc memory in bytes.
	Vmalloc *uint64 `json:"vmalloc,omitempty"`
	// Shmem is shared memory (tmpfs, IPC) in bytes.
	Shmem *uint64 `json:"shmem,omitempty"`
	// FileMapped is memory-mapped file pages in bytes.
	FileMapped *uint64 `json:"fileMapped,omitempty"`
	// FileDirty is dirty page cache waiting for writeback, in bytes.
	FileDirty *uint64 `json:"fileDirty,omitempty"`
	// FileWriteback is page cache currently under writeback, in bytes.
	FileWriteback *uint64 `json:"fileWriteback,omitempty"`
	// Slab is kernel slab-allocator memory in bytes.
	Slab *uint64 `json:"slab,omitempty"`
	// Pgscan is the count of pages scanned by the memory reclaimer.
	Pgscan *uint64 `json:"pgscan,omitempty"`
	// Pgsteal is the count of pages successfully reclaimed.
	Pgsteal *uint64 `json:"pgsteal,omitempty"`
	// Pgfault is the total minor page fault count.
	Pgfault *uint64 `json:"pgfault,omitempty"`
	// Pgmajfault is the total major page fault count.
	Pgmajfault *uint64 `json:"pgmajfault,omitempty"`
	// Peak is the high-water-mark memory usage in bytes (memory.peak).
	// Available on kernel ≥ 5.19.
	Peak *uint64 `json:"peak,omitempty"`

	// Event counters from memory.events (not pointers; zero is meaningful).
	LowEvents     uint64 `json:"lowEvents,omitempty"`
	HighEvents    uint64 `json:"highEvents,omitempty"`
	MaxEvents     uint64 `json:"maxEvents,omitempty"`
	OomEvents     uint64 `json:"oomEvents,omitempty"`
	OomKillEvents uint64 `json:"oomKillEvents,omitempty"`

	// Pressure contains PSI stall metrics from memory.pressure.
	Pressure *Pressure `json:"pressure,omitempty"`
}

// IOStat aggregates block device I/O counters from the cgroupv2 io.stat file.
// Because a cgroup may have processes using multiple block devices, all per-device
// values are summed into a single total. Per-device breakdowns are not preserved
// because BuildKit's workloads are single-root-device by design.
type IOStat struct {
	// ReadBytes is the total bytes read across all block devices.
	ReadBytes *uint64 `json:"readBytes,omitempty"`
	// WriteBytes is the total bytes written across all block devices.
	WriteBytes *uint64 `json:"writeBytes,omitempty"`
	// DiscardBytes is the total bytes discarded (TRIM) across all block devices.
	DiscardBytes *uint64 `json:"discardBytes,omitempty"`
	// ReadIOs is the total number of read I/O operations.
	ReadIOs *uint64 `json:"readIOs,omitempty"`
	// WriteIOs is the total number of write I/O operations.
	WriteIOs *uint64 `json:"writeIOs,omitempty"`
	// DiscardIOs is the total number of discard (TRIM) I/O operations.
	DiscardIOs *uint64 `json:"discardIOs,omitempty"`
	// Pressure contains PSI stall metrics from io.pressure.
	Pressure *Pressure `json:"pressure,omitempty"`
}

// PIDsStat reflects the cgroupv2 pids controller state.
type PIDsStat struct {
	// Current is the live process count in this cgroup (pids.current).
	Current *uint64 `json:"current,omitempty"`
	// Limit is the configured maximum (pids.max); nil means unlimited.
	Limit *uint64 `json:"limit,omitempty"`
}

// NetworkSample captures delta counters from a container's network namespace.
// These are the change in interface counters since the previous sample, not
// cumulative totals, so consumers can sum them to get total transfer.
type NetworkSample struct {
	RxBytes   int64 `json:"rxBytes,omitempty"`
	RxPackets int64 `json:"rxPackets,omitempty"`
	RxErrors  int64 `json:"rxErrors,omitempty"`
	RxDropped int64 `json:"rxDropped,omitempty"`
	TxBytes   int64 `json:"txBytes,omitempty"`
	TxPackets int64 `json:"txPackets,omitempty"`
	TxErrors  int64 `json:"txErrors,omitempty"`
	TxDropped int64 `json:"txDropped,omitempty"`
}

// ─── Pressure Stall Information (PSI) ────────────────────────────────────────

// Pressure mirrors the Linux PSI interface exposed through both
// /proc/pressure/{cpu,memory,io} (system-wide) and the per-cgroup
// {cpu,memory,io}.pressure files.
//
// See: https://docs.kernel.org/accounting/psi.html
type Pressure struct {
	// Some measures time during which at least one task was stalled waiting
	// for this resource. Not available for CPU (kernel limitation).
	Some *PressureValues `json:"some,omitempty"`
	// Full measures time during which ALL non-idle tasks were stalled waiting
	// for this resource (i.e. the system made no forward progress).
	Full *PressureValues `json:"full,omitempty"`
}

// PressureValues holds the rolling-window exponential moving averages and the
// total cumulative stall time for one stall category (some or full).
type PressureValues struct {
	// Avg10 is the 10-second exponential moving average, 0.0–100.0.
	Avg10 *float64 `json:"avg10,omitempty"`
	// Avg60 is the 60-second exponential moving average.
	Avg60 *float64 `json:"avg60,omitempty"`
	// Avg300 is the 300-second exponential moving average.
	Avg300 *float64 `json:"avg300,omitempty"`
	// Total is cumulative stall time in microseconds since boot.
	Total *uint64 `json:"total,omitempty"`
}
