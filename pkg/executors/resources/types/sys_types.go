package types

import (
	"encoding/json"
	"math"
	"time"
)

// SysSample is a point-in-time snapshot of system-wide (host) resource
// utilisation sampled from /proc. It is produced by the SysSampler and
// accompanies per-cgroup Samples so callers can correlate container load
// against host pressure.
type SysSample struct {
	// Timestamp_ stores the wall-clock sample time.
	// Underscore suffix avoids collision with the Timestamp() method.
	Timestamp_ time.Time `json:"timestamp"`

	// CPUStat is the aggregate CPU utilisation across all logical CPUs.
	CPUStat *SysCPUStat `json:"cpuStat,omitempty"`
	// ProcStat aggregates kernel bookkeeping counters from /proc/stat.
	ProcStat *ProcStat `json:"procStat,omitempty"`
	// MemoryStat is /proc/meminfo fields.
	MemoryStat *SysMemoryStat `json:"memoryStat,omitempty"`

	// PSI stall metrics from /proc/pressure/{cpu,memory,io}.
	// Available when CONFIG_PSI=y and /proc/pressure/ exists.
	CPUPressure    *Pressure `json:"cpuPressure,omitempty"`
	MemoryPressure *Pressure `json:"memoryPressure,omitempty"`
	IOPressure     *Pressure `json:"ioPressure,omitempty"`
}

// Timestamp implements sampler.WithTimestamp so SysSample can be used as
// the generic type parameter of Sampler[*SysSample].
func (s *SysSample) Timestamp() time.Time { return s.Timestamp_ }

// SysCPUStat mirrors the CPUTotal row of /proc/stat. All values are in
// USER_HZ jiffies as reported by the kernel; callers must divide by
// SC_CLK_TCK (typically 100) to obtain seconds.
//
// JSON marshalling rounds values to three decimal places to avoid spurious
// diffs when the same data is serialised twice with floating-point noise.
type SysCPUStat struct {
	User      float64 `json:"user"`
	Nice      float64 `json:"nice"`
	System    float64 `json:"system"`
	Idle      float64 `json:"idle"`
	Iowait    float64 `json:"iowait"`
	IRQ       float64 `json:"irq"`
	SoftIRQ   float64 `json:"softirq"`
	Steal     float64 `json:"steal"`
	Guest     float64 `json:"guest"`
	GuestNice float64 `json:"guestNice"`
}

// sysCPUStatWire is an alias used inside MarshalJSON to avoid infinite recursion.
type sysCPUStatWire SysCPUStat

// MarshalJSON rounds each field to three decimal places before serialising.
// This keeps JSON output stable across repeated measurements of the same
// underlying counter when floating-point arithmetic introduces sub-milliunit noise.
func (s SysCPUStat) MarshalJSON() ([]byte, error) {
	round := func(v float64) float64 { return math.Round(v*1000) / 1000 }
	return json.Marshal(sysCPUStatWire{
		User:      round(s.User),
		Nice:      round(s.Nice),
		System:    round(s.System),
		Idle:      round(s.Idle),
		Iowait:    round(s.Iowait),
		IRQ:       round(s.IRQ),
		SoftIRQ:   round(s.SoftIRQ),
		Steal:     round(s.Steal),
		Guest:     round(s.Guest),
		GuestNice: round(s.GuestNice),
	})
}

// ProcStat captures high-frequency kernel bookkeeping counters from /proc/stat
// that are useful for correlating container activity with system-wide load.
type ProcStat struct {
	// ContextSwitches is the cumulative count of context switches since boot.
	ContextSwitches uint64 `json:"contextSwitches"`
	// ProcessCreated is the cumulative number of processes/threads forked since boot.
	ProcessCreated uint64 `json:"processCreated"`
	// ProcessesRunning is the number of runnable threads (in run queue) right now.
	ProcessesRunning uint64 `json:"processesRunning"`
}

// SysMemoryStat mirrors the most operationally relevant fields from /proc/meminfo.
// All values are in bytes (the kernel reports KiB; the sampler converts them).
// Fields are pointer types so a nil value can indicate "not present in meminfo"
// rather than "zero", though in practice all fields are always populated.
type SysMemoryStat struct {
	// Total is MemTotal: total usable RAM.
	Total *uint64 `json:"total,omitempty"`
	// Free is MemFree: unallocated RAM (not the same as "available").
	Free *uint64 `json:"free,omitempty"`
	// Available is MemAvailable: estimated memory available for new allocations.
	Available *uint64 `json:"available,omitempty"`
	// Buffers is RAM used by the kernel for buffered I/O metadata.
	Buffers *uint64 `json:"buffers,omitempty"`
	// Cached is RAM used for the page cache.
	Cached *uint64 `json:"cached,omitempty"`
	// Active is memory recently used and less likely to be reclaimed.
	Active *uint64 `json:"active,omitempty"`
	// Inactive is memory less recently used and more likely to be reclaimed.
	Inactive *uint64 `json:"inactive,omitempty"`
	// Swap is SwapTotal: total swap space.
	Swap *uint64 `json:"swap,omitempty"`
	// Dirty is memory waiting to be written back to disk.
	Dirty *uint64 `json:"dirty,omitempty"`
	// Writeback is memory actively being written back to disk.
	Writeback *uint64 `json:"writeback,omitempty"`
	// Slab is in-kernel data structures cache.
	Slab *uint64 `json:"slab,omitempty"`
}
