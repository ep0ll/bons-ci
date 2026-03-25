//go:build linux

package resources

// sys_linux.go – Linux implementation of the system-wide resource sampler.
//
// Samples /proc/stat (CPU, context switches, process creation),
// /proc/meminfo (memory utilisation), and — when the kernel supports PSI
// (CONFIG_PSI=y) — /proc/pressure/{cpu,memory,io}.
//
// All fields are read through the prometheus/procfs library so we benefit from
// its caching, correct KiB→bytes conversion, and future kernel field additions.

import (
	"os"
	"time"

	resourcestypes "github.com/bons/bons-ci/pkg/executors/resources/types"
	"github.com/prometheus/procfs"
)

// procPressureDir is the PSI pressure directory introduced in kernel 4.20.
const procPressureDir = "/proc/pressure"

// newSysSampler creates a Sampler that collects system-wide resource stats
// from /proc every 2 seconds with a maximum history of 20 samples before
// adaptive interval doubling kicks in.
func newSysSampler() (*Sampler[*resourcestypes.SysSample], error) {
	pfs, err := procfs.NewDefaultFS()
	if err != nil {
		return nil, err
	}

	return NewSampler(2*time.Second, 20, func(tm time.Time) (*resourcestypes.SysSample, error) {
		return collectSysSample(pfs, tm)
	}), nil
}

// collectSysSample reads all available /proc data and assembles a SysSample.
// Errors from individual subsections (e.g. missing pressure dir) are handled
// gracefully: the sample is returned with those fields nil.
func collectSysSample(proc procfs.FS, tm time.Time) (*resourcestypes.SysSample, error) {
	s := &resourcestypes.SysSample{Timestamp_: tm}

	// ── /proc/stat ───────────────────────────────────────────────────────────
	if err := collectProcStat(proc, s); err != nil {
		return nil, err
	}

	// ── /proc/meminfo ────────────────────────────────────────────────────────
	if err := collectMeminfo(proc, s); err != nil {
		return nil, err
	}

	// ── /proc/pressure/* ─────────────────────────────────────────────────────
	// Fail-open: if /proc/pressure does not exist the sample is still valid.
	if _, err := os.Lstat(procPressureDir); err == nil {
		collectProcPressure(s)
	}

	return s, nil
}

// collectProcStat populates s.CPUStat and s.ProcStat from /proc/stat.
func collectProcStat(proc procfs.FS, s *resourcestypes.SysSample) error {
	stat, err := proc.Stat()
	if err != nil {
		return err
	}

	s.CPUStat = &resourcestypes.SysCPUStat{
		User:      stat.CPUTotal.User,
		Nice:      stat.CPUTotal.Nice,
		System:    stat.CPUTotal.System,
		Idle:      stat.CPUTotal.Idle,
		Iowait:    stat.CPUTotal.Iowait,
		IRQ:       stat.CPUTotal.IRQ,
		SoftIRQ:   stat.CPUTotal.SoftIRQ,
		Steal:     stat.CPUTotal.Steal,
		Guest:     stat.CPUTotal.Guest,
		GuestNice: stat.CPUTotal.GuestNice,
	}
	s.ProcStat = &resourcestypes.ProcStat{
		ContextSwitches:  stat.ContextSwitches,
		ProcessCreated:   stat.ProcessCreated,
		ProcessesRunning: stat.ProcessesRunning,
	}
	return nil
}

// collectMeminfo populates s.MemoryStat from /proc/meminfo.
// The procfs library returns values in KiB; we convert to bytes.
func collectMeminfo(proc procfs.FS, s *resourcestypes.SysSample) error {
	mem, err := proc.Meminfo()
	if err != nil {
		return err
	}
	s.MemoryStat = &resourcestypes.SysMemoryStat{
		Total:     kibToBytes(mem.MemTotal),
		Free:      kibToBytes(mem.MemFree),
		Available: kibToBytes(mem.MemAvailable),
		Buffers:   kibToBytes(mem.Buffers),
		Cached:    kibToBytes(mem.Cached),
		Active:    kibToBytes(mem.Active),
		Inactive:  kibToBytes(mem.Inactive),
		Swap:      kibToBytes(mem.SwapTotal),
		Dirty:     kibToBytes(mem.Dirty),
		Writeback: kibToBytes(mem.Writeback),
		Slab:      kibToBytes(mem.Slab),
	}
	return nil
}

// collectProcPressure reads PSI data from /proc/pressure and populates
// the pressure fields in s.  Errors for individual files are silently
// ignored so a single missing pressure file does not fail the whole sample.
func collectProcPressure(s *resourcestypes.SysSample) {
	if cp, err := parsePressureFile(procPressureDir + "/cpu"); err == nil {
		s.CPUPressure = cp
	}
	if mp, err := parsePressureFile(procPressureDir + "/memory"); err == nil {
		s.MemoryPressure = mp
	}
	if ip, err := parsePressureFile(procPressureDir + "/io"); err == nil {
		s.IOPressure = ip
	}
}

// kibToBytes converts a *uint64 value in KiB to a *uint64 in bytes.
// Returns nil when kib is nil.
func kibToBytes(kib *uint64) *uint64 {
	if kib == nil {
		return nil
	}
	v := *kib * 1024
	return &v
}
