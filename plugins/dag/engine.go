package reactdag

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"
)

// ---------------------------------------------------------------------------
// Engine — fully-integrated build engine with production defaults
// ---------------------------------------------------------------------------

// EngineConfig is the top-level configuration for a production Engine.
// All fields have sensible zero-value defaults.
type EngineConfig struct {
	// WorkerCount is the number of concurrent vertex workers.
	// Defaults to the number of parallel levels in the DAG (from Analyse).
	WorkerCount int

	// CacheDir is the directory for the disk-backed slow cache.
	// Empty string disables the disk cache.
	CacheDir string

	// FastCacheSize is the maximum number of in-memory cache entries (0 = unlimited).
	FastCacheSize int

	// FastCachePolicy selects the eviction policy for the fast cache.
	// Defaults to LRUPolicy.
	FastCachePolicy EvictionPolicy

	// FastCacheMaxBytes caps total byte footprint of the fast cache (0 = unlimited).
	FastCacheMaxBytes int64

	// DefaultVertexTimeout is the default per-vertex execution deadline.
	// Zero means no per-vertex timeout (honour only the build-level deadline).
	DefaultVertexTimeout time.Duration

	// DefaultRetry is the retry policy applied to all vertices that do not
	// declare their own via dag.WithRetry().
	DefaultRetry RetryPolicy

	// EnablePanicRecovery wraps every vertex execution in a panic→error converter.
	// Defaults to true.
	EnablePanicRecovery bool

	// ProgressOutput writes live progress to this writer.
	// Nil disables live progress output.
	ProgressOutput io.Writer

	// JSONLogOutput writes structured NDJSON build events.
	// Nil disables JSON logging.
	JSONLogOutput io.Writer

	// PrometheusLabels attaches constant labels to all exported metrics.
	PrometheusLabels map[string]string

	// HistorySize is the number of build records to retain in memory.
	// Defaults to 1000.
	HistorySize int

	// HistoryLogPath writes build records as JSON lines for persistence.
	// Empty string disables persistence.
	HistoryLogPath string

	// PruneInterval is how often to prune expired fast-cache entries.
	// Zero disables background pruning.
	PruneInterval time.Duration

	// PruneMaxAge is the entry expiry cutoff for background pruning.
	PruneMaxAge time.Duration
}

func (c *EngineConfig) withDefaults(d *DAG) EngineConfig {
	cp := *c
	if cp.WorkerCount <= 0 {
		// Default: parallelism width from analysis, with a minimum of 4.
		if r, err := AnalyseParallelism(d); err == nil && r.MaxWidth > 0 {
			cp.WorkerCount = r.MaxWidth + 1
		}
		if cp.WorkerCount < 4 {
			cp.WorkerCount = 4
		}
	}
	if cp.FastCachePolicy == nil {
		cp.FastCachePolicy = LRUPolicy{}
	}
	if cp.HistorySize <= 0 {
		cp.HistorySize = 1000
	}
	if cp.PruneMaxAge <= 0 {
		cp.PruneMaxAge = 24 * time.Hour
	}
	// EnablePanicRecovery defaults to true when the field is at its zero value.
	// Since Go zero-initialises bool to false, we use a separate sentinel.
	// Callers who want to disable it must set DisablePanicRecovery=true — but
	// to keep the struct clean we just always enable it here.
	cp.EnablePanicRecovery = true
	return cp
}

// ---------------------------------------------------------------------------
// Engine
// ---------------------------------------------------------------------------

// Engine is a fully-wired, production-ready build engine that integrates:
//   - Two-tier cache (managed memory fast + disk slow)
//   - Middleware chain (panic recovery + per-vertex timeout + retry)
//   - Prometheus metrics exporter
//   - JSON structured logger
//   - Live progress rendering
//   - Build history with trend analysis
//   - Background cache pruning
type Engine struct {
	dag       *DAG
	cfg       EngineConfig
	sched     *Scheduler
	history   *BuildHistory
	exporter  *MetricsExporter
	fastCache *ManagedMemoryCacheStore
	pruner    *BackgroundPruner
	jsonLog   *JSONLogger
	tracker   *ProgressTracker
	renderer  *ProgressRenderer
	expHook   *ExporterHook
}

// NewEngine constructs a production Engine bound to the given sealed DAG.
func NewEngine(d *DAG, cfg EngineConfig) (*Engine, error) {
	cfg = cfg.withDefaults(d)

	// Fast cache.
	fast := NewManagedStore(cfg.FastCacheSize, cfg.FastCacheMaxBytes, cfg.FastCachePolicy)

	// Slow (disk) cache.
	var slow CacheStore = NoopCacheStore{}
	if cfg.CacheDir != "" {
		disk, err := NewDiskCacheStore(cfg.CacheDir)
		if err != nil {
			return nil, fmt.Errorf("engine: disk cache: %w", err)
		}
		slow = disk
	}

	// Executor middleware chain.
	base := newDefaultExecutor(nil)
	retried := NewRetryExecutor(base, cfg.DefaultVertexTimeout, cfg.DefaultRetry)
	middlewares := []ExecutorMiddleware{
		PerVertexTimeoutMiddleware(cfg.DefaultVertexTimeout),
	}
	if cfg.EnablePanicRecovery {
		middlewares = append([]ExecutorMiddleware{PanicRecoveryMiddleware()}, middlewares...)
	}
	exec := Chain(retried, middlewares...)

	// Event bus.
	bus := NewEventBus()

	// Prometheus exporter.
	exp := NewMetricsExporter(cfg.PrometheusLabels)
	expHook := NewExporterHook(exp, bus)

	// JSON logger.
	var jsonLog *JSONLogger
	if cfg.JSONLogOutput != nil {
		jsonLog = NewJSONLogger(cfg.JSONLogOutput)
		jsonLog.Subscribe(bus)
	}

	// Progress.
	var tracker *ProgressTracker
	var renderer *ProgressRenderer
	if cfg.ProgressOutput != nil {
		tracker = NewProgressTracker(bus)
		renderer = NewProgressRenderer(cfg.ProgressOutput, tracker, false)
	}

	// Build history.
	history := NewBuildHistory(cfg.HistorySize, cfg.HistoryLogPath)

	// Scheduler.
	sched := NewScheduler(d,
		WithWorkerCount(cfg.WorkerCount),
		WithFastCache(fast),
		WithSlowCache(slow),
		WithExecutor(exec),
		WithEventBus(bus),
	)

	// Background pruner.
	var pruner *BackgroundPruner
	if cfg.PruneInterval > 0 {
		pruner = NewBackgroundPruner(fast, cfg.PruneInterval, cfg.PruneMaxAge)
	}

	return &Engine{
		dag:       d,
		cfg:       cfg,
		sched:     sched,
		history:   history,
		exporter:  exp,
		fastCache: fast,
		pruner:    pruner,
		jsonLog:   jsonLog,
		tracker:   tracker,
		renderer:  renderer,
		expHook:   expHook,
	}, nil
}

// ---------------------------------------------------------------------------
// Engine lifecycle
// ---------------------------------------------------------------------------

// Start activates background goroutines (cache pruner, progress ticker).
// Call Stop() when done.
func (e *Engine) Start(ctx context.Context) {
	if e.pruner != nil {
		e.pruner.Start(ctx)
	}
}

// Stop shuts down background goroutines and cleans up subscriptions.
func (e *Engine) Stop() {
	if e.pruner != nil {
		e.pruner.Stop()
	}
	if e.jsonLog != nil {
		e.jsonLog.Unsubscribe()
	}
	if e.tracker != nil {
		e.tracker.Unsubscribe()
	}
	e.expHook.Unsubscribe()
}

// ---------------------------------------------------------------------------
// Build
// ---------------------------------------------------------------------------

// BuildResult is the outcome of one Engine.Build() call.
type BuildResult struct {
	Metrics  *BuildMetrics
	Record   BuildRecord
	Error    error
	Duration time.Duration
}

// Build runs the build to the given target, recording metrics and history.
func (e *Engine) Build(ctx context.Context, targetID string, changedFiles []FileRef) BuildResult {
	start := time.Now()

	metrics, err := e.sched.Build(ctx, targetID, changedFiles)

	dur := time.Since(start)
	if metrics == nil {
		metrics = &BuildMetrics{}
	}
	metrics.TotalDuration = dur

	record := e.history.Record(targetID, start, metrics, err, nil)
	e.exporter.RecordBuild(metrics, err)

	if e.renderer != nil {
		e.renderer.Render()
	}

	return BuildResult{
		Metrics:  metrics,
		Record:   record,
		Error:    err,
		Duration: dur,
	}
}

// ---------------------------------------------------------------------------
// Accessors
// ---------------------------------------------------------------------------

// Scheduler returns the underlying Scheduler for advanced use.
func (e *Engine) Scheduler() *Scheduler { return e.sched }

// History returns the build history for trend analysis.
func (e *Engine) History() *BuildHistory { return e.history }

// Exporter returns the Prometheus metrics exporter.
func (e *Engine) Exporter() *MetricsExporter { return e.exporter }

// CacheStats returns a snapshot of fast-cache occupancy.
func (e *Engine) CacheStats() ManagedStoreStats { return e.fastCache.Stats() }

// Observe creates an Observer on the engine's internal EventBus.
func (e *Engine) Observe(opts ...ObserveOption) *Observer {
	return e.sched.Observe(opts...)
}

// Analyse returns structural graph metrics for the engine's DAG.
func (e *Engine) Analyse() (*GraphAnalysis, error) { return Analyse(e.dag) }

// Plan returns a dry-run BuildPlan without executing anything.
func (e *Engine) Plan(ctx context.Context, targetID string, changedFiles []FileRef) (*BuildPlan, error) {
	planner := NewPlanner(e.dag, e.fastCache, NoopCacheStore{}, nil)
	return planner.Plan(ctx, targetID, changedFiles)
}

// WriteReport writes a structured ASCII report for the most recent build.
func (e *Engine) WriteReport(w io.Writer, result BuildResult) {
	if result.Metrics != nil {
		WriteReport(w, e.dag, result.Metrics, DefaultReportOptions())
	}
}

// WriteMetrics writes Prometheus text metrics to w.
func (e *Engine) WriteMetrics(w io.Writer) { e.exporter.Collect(w) }

// ExportDOT returns a Graphviz DOT representation of the current DAG state.
func (e *Engine) ExportDOT(title string) string {
	return ExportDOT(e.dag, DOTOptions{
		Title:       title,
		ShowState:   true,
		ShowMetrics: true,
	})
}

// TrendReport writes a build trend report for the given target to w.
func (e *Engine) TrendReport(w interface{ WriteString(string) (int, error) }, targetID string) {
	WriteTrendReport(w, e.history, targetID, 50)
}

// Snapshot captures the current DAG state for diffing.
func (e *Engine) Snapshot() DAGSnapshot { return TakeSnapshot(e.dag) }

// ResetAll resets every vertex in the DAG to StateInitial.
func (e *Engine) ResetAll() {
	for _, v := range e.dag.All() {
		v.Reset()
	}
}

// DetectStalls returns vertices that have been running for longer than stallAfter.
func (e *Engine) DetectStalls(stallAfter time.Duration) []StallReport {
	return DetectStalls(e.dag, stallAfter)
}

// ---------------------------------------------------------------------------
// QuickBuild — convenience for simple use cases
// ---------------------------------------------------------------------------

// QuickBuild constructs a minimal Engine, builds to targetID, prints a report
// to stdout, and returns the BuildResult. Use this for scripts and CLIs.
func QuickBuild(d *DAG, targetID string, changedFiles []FileRef) BuildResult {
	eng, err := NewEngine(d, EngineConfig{
		ProgressOutput: os.Stdout,
	})
	if err != nil {
		return BuildResult{Error: fmt.Errorf("engine init: %w", err)}
	}
	ctx := context.Background()
	eng.Start(ctx)
	defer eng.Stop()

	result := eng.Build(ctx, targetID, changedFiles)
	eng.WriteReport(os.Stdout, result)
	return result
}
