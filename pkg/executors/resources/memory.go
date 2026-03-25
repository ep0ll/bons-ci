package resources

// memory.go – cgroupv2 memory controller reader.
//
// Reads (in order):
//   - memory.stat            detailed breakdown of memory usage
//   - memory.pressure        PSI stall metrics (CONFIG_PSI=y required)
//   - memory.events          event counters (low/high/max/oom/oom_kill)
//   - memory.peak            high-water-mark usage (kernel ≥ 5.19)
//   - memory.swap.current    current swap usage
//
// All values are in bytes as reported by the kernel.

import (
	"context"
	"path/filepath"

	resourcestypes "github.com/bons/bons-ci/pkg/executors/resources/types"
)

// ─── memory.stat field names ─────────────────────────────────────────────────

const (
	memKeyAnon          = "anon"
	memKeyFile          = "file"
	memKeyKernel        = "kernel"
	memKeyKernelStack   = "kernel_stack"
	memKeyPageTables    = "pagetables"
	memKeySock          = "sock"
	memKeyVmalloc       = "vmalloc"
	memKeyShmem         = "shmem"
	memKeyFileMapped    = "file_mapped"
	memKeyFileDirty     = "file_dirty"
	memKeyFileWriteback = "file_writeback"
	memKeySlab          = "slab"
	memKeyPgscan        = "pgscan"
	memKeyPgsteal       = "pgsteal"
	memKeyPgfault       = "pgfault"
	memKeyPgmajfault    = "pgmajfault"
)

// ─── memory.events field names ───────────────────────────────────────────────

const (
	memEventLow     = "low"
	memEventHigh    = "high"
	memEventMax     = "max"
	memEventOom     = "oom"
	memEventOomKill = "oom_kill"
)

// ─── cgroup file names ────────────────────────────────────────────────────────

const (
	memoryStatFile        = "memory.stat"
	memoryPressureFile    = "memory.pressure"
	memoryPeakFile        = "memory.peak"
	memorySwapCurrentFile = "memory.swap.current"
	memoryEventsFile      = "memory.events"
)

// ─── memoryController ────────────────────────────────────────────────────────

// memoryController implements Controller for the cgroupv2 memory subsystem.
type memoryController struct{}

// Name returns the cgroupv2 controller name.
func (memoryController) Name() string { return "memory" }

// Collect reads all memory controller files and populates dst.MemoryStat.
func (memoryController) Collect(_ context.Context, cgroupPath string, dst *resourcestypes.Sample) error {
	stat, err := collectMemoryStat(cgroupPath)
	if err != nil {
		return err
	}
	dst.MemoryStat = stat
	return nil
}

// ─── Collection logic ─────────────────────────────────────────────────────────

// collectMemoryStat aggregates all memory controller files for cgroupPath.
// Returns (nil, nil) when memory.stat is absent (memory controller disabled).
func collectMemoryStat(cgroupPath string) (*resourcestypes.MemoryStat, error) {
	stat := &resourcestypes.MemoryStat{}
	found := false

	// ── memory.stat ──────────────────────────────────────────────────────────
	if err := parseKVFile(filepath.Join(cgroupPath, memoryStatFile), func(key string, value uint64) {
		found = true
		switch key {
		case memKeyAnon:
			stat.Anon = uint64Ptr(value)
		case memKeyFile:
			stat.File = uint64Ptr(value)
		case memKeyKernel:
			stat.Kernel = uint64Ptr(value)
		case memKeyKernelStack:
			stat.KernelStack = uint64Ptr(value)
		case memKeyPageTables:
			stat.PageTables = uint64Ptr(value)
		case memKeySock:
			stat.Sock = uint64Ptr(value)
		case memKeyVmalloc:
			stat.Vmalloc = uint64Ptr(value)
		case memKeyShmem:
			stat.Shmem = uint64Ptr(value)
		case memKeyFileMapped:
			stat.FileMapped = uint64Ptr(value)
		case memKeyFileDirty:
			stat.FileDirty = uint64Ptr(value)
		case memKeyFileWriteback:
			stat.FileWriteback = uint64Ptr(value)
		case memKeySlab:
			stat.Slab = uint64Ptr(value)
		case memKeyPgscan:
			stat.Pgscan = uint64Ptr(value)
		case memKeyPgsteal:
			stat.Pgsteal = uint64Ptr(value)
		case memKeyPgfault:
			stat.Pgfault = uint64Ptr(value)
		case memKeyPgmajfault:
			stat.Pgmajfault = uint64Ptr(value)
		}
	}); err != nil {
		return nil, err
	}
	if !found {
		return nil, nil // memory controller not active
	}

	// ── memory.pressure ──────────────────────────────────────────────────────
	pressure, err := parsePressureFile(filepath.Join(cgroupPath, memoryPressureFile))
	if err != nil {
		return nil, err
	}
	stat.Pressure = pressure

	// ── memory.events ────────────────────────────────────────────────────────
	if err := parseKVFile(filepath.Join(cgroupPath, memoryEventsFile), func(key string, value uint64) {
		switch key {
		case memEventLow:
			stat.LowEvents = value
		case memEventHigh:
			stat.HighEvents = value
		case memEventMax:
			stat.MaxEvents = value
		case memEventOom:
			stat.OomEvents = value
		case memEventOomKill:
			stat.OomKillEvents = value
		}
	}); err != nil {
		return nil, err
	}

	// ── memory.peak (kernel ≥ 5.19) ──────────────────────────────────────────
	if peak, ok, err := parseSingleUint64File(filepath.Join(cgroupPath, memoryPeakFile)); err != nil {
		return nil, err
	} else if ok {
		stat.Peak = uint64Ptr(peak)
	}

	// ── memory.swap.current ──────────────────────────────────────────────────
	if swap, ok, err := parseSingleUint64File(filepath.Join(cgroupPath, memorySwapCurrentFile)); err != nil {
		return nil, err
	} else if ok {
		stat.SwapBytes = uint64Ptr(swap)
	}

	return stat, nil
}
