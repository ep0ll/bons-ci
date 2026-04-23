package reactdag

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"time"
)

// ---------------------------------------------------------------------------
// BuildProfiler — wraps a Scheduler.Build call with pprof profiling
// ---------------------------------------------------------------------------

// ProfileConfig controls which profiles are collected.
type ProfileConfig struct {
	// OutputDir is where profile files are written. Created if absent.
	OutputDir string
	// CPU enables CPU profiling.
	CPU bool
	// Memory enables heap profiling after the build.
	Memory bool
	// Goroutine dumps goroutine stacks at build start and end.
	Goroutine bool
	// Label is appended to profile filenames (e.g. a build ID or timestamp).
	Label string
}

// BuildProfiler wraps a Scheduler and collects pprof profiles around Build().
type BuildProfiler struct {
	sched *Scheduler
	cfg   ProfileConfig
}

// NewBuildProfiler constructs a BuildProfiler.
func NewBuildProfiler(sched *Scheduler, cfg ProfileConfig) *BuildProfiler {
	return &BuildProfiler{sched: sched, cfg: cfg}
}

// Build runs the build with profiling enabled according to cfg.
// Profile files are written to cfg.OutputDir on completion.
func (p *BuildProfiler) Build(
	ctx context.Context,
	targetID string,
	changedFiles []FileRef,
) (*BuildMetrics, error) {
	if err := os.MkdirAll(p.cfg.OutputDir, 0o755); err != nil {
		return nil, fmt.Errorf("profiler: create output dir: %w", err)
	}

	// CPU profile.
	var cpuStop func()
	if p.cfg.CPU {
		stop, err := p.startCPUProfile(targetID)
		if err != nil {
			return nil, fmt.Errorf("profiler: start CPU profile: %w", err)
		}
		cpuStop = stop
	}

	// Goroutine snapshot before.
	if p.cfg.Goroutine {
		p.writeGoroutineProfile(targetID, "before")
	}

	metrics, buildErr := p.sched.Build(ctx, targetID, changedFiles)

	// Stop CPU profiling.
	if cpuStop != nil {
		cpuStop()
	}

	// Heap snapshot.
	if p.cfg.Memory {
		runtime.GC()
		if err := p.writeMemoryProfile(targetID); err != nil {
			// Non-fatal: log and continue.
			fmt.Fprintf(os.Stderr, "profiler: memory profile error: %v\n", err)
		}
	}

	// Goroutine snapshot after.
	if p.cfg.Goroutine {
		p.writeGoroutineProfile(targetID, "after")
	}

	return metrics, buildErr
}

// ---------------------------------------------------------------------------
// Profile helpers
// ---------------------------------------------------------------------------

func (p *BuildProfiler) startCPUProfile(targetID string) (stop func(), err error) {
	path := p.profilePath(targetID, "cpu", "prof")
	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("create %s: %w", path, err)
	}
	if err := pprof.StartCPUProfile(f); err != nil {
		f.Close()
		return nil, fmt.Errorf("start cpu profile: %w", err)
	}
	return func() {
		pprof.StopCPUProfile()
		f.Close()
	}, nil
}

func (p *BuildProfiler) writeMemoryProfile(targetID string) error {
	path := p.profilePath(targetID, "mem", "prof")
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	defer f.Close()
	return pprof.WriteHeapProfile(f)
}

func (p *BuildProfiler) writeGoroutineProfile(targetID, tag string) {
	path := p.profilePath(targetID, fmt.Sprintf("goroutine_%s", tag), "prof")
	f, err := os.Create(path)
	if err != nil {
		return
	}
	defer f.Close()
	pprof.Lookup("goroutine").WriteTo(f, 0) //nolint:errcheck
}

func (p *BuildProfiler) profilePath(targetID, kind, ext string) string {
	ts := time.Now().Format("20060102_150405")
	label := p.cfg.Label
	if label == "" {
		label = sanitizeLabel(targetID)
	}
	filename := fmt.Sprintf("%s_%s_%s.%s", label, ts, kind, ext)
	return filepath.Join(p.cfg.OutputDir, filename)
}

// ---------------------------------------------------------------------------
// MemStats snapshot — lightweight alternative to heap profiling
// ---------------------------------------------------------------------------

// MemStatsSnapshot is a point-in-time snapshot of Go runtime memory.
type MemStatsSnapshot struct {
	Alloc      uint64        // bytes currently allocated
	TotalAlloc uint64        // cumulative bytes allocated
	Sys        uint64        // total memory from OS
	NumGC      uint32        // number of GC cycles
	PauseTotal time.Duration // cumulative GC pause
	Goroutines int
	CapturedAt time.Time
}

// CaptureMemStats returns a snapshot of current runtime memory statistics.
func CaptureMemStats() MemStatsSnapshot {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return MemStatsSnapshot{
		Alloc:      ms.Alloc,
		TotalAlloc: ms.TotalAlloc,
		Sys:        ms.Sys,
		NumGC:      ms.NumGC,
		PauseTotal: time.Duration(ms.PauseTotalNs),
		Goroutines: runtime.NumGoroutine(),
		CapturedAt: time.Now(),
	}
}

// Delta returns the difference between this snapshot and a later one.
func (s MemStatsSnapshot) Delta(later MemStatsSnapshot) MemStatsSnapshot {
	return MemStatsSnapshot{
		Alloc:      later.Alloc,
		TotalAlloc: later.TotalAlloc - s.TotalAlloc,
		Sys:        later.Sys,
		NumGC:      later.NumGC - s.NumGC,
		PauseTotal: later.PauseTotal - s.PauseTotal,
		Goroutines: later.Goroutines,
		CapturedAt: later.CapturedAt,
	}
}

// String returns a compact one-line summary of the snapshot.
func (s MemStatsSnapshot) String() string {
	return fmt.Sprintf(
		"alloc=%s total_alloc=%s sys=%s gc_cycles=%d gc_pause=%s goroutines=%d",
		formatBytes(int64(s.Alloc)),
		formatBytes(int64(s.TotalAlloc)),
		formatBytes(int64(s.Sys)),
		s.NumGC,
		s.PauseTotal.Round(time.Microsecond),
		s.Goroutines,
	)
}
