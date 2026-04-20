package reactdag

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// MetricsExporter — Prometheus text exposition format
// ---------------------------------------------------------------------------

// MetricsExporter accumulates build metrics and exposes them in
// Prometheus text format (exposition format 0.0.4). Mount the output of
// Collect() on a /metrics HTTP endpoint.
//
// Compatible with any Prometheus scraper without importing the official client.
type MetricsExporter struct {
	mu sync.Mutex

	// Counters
	buildsTotal      int64
	buildSuccesses   int64
	buildFailures    int64
	verticesExecuted int64
	fastCacheHits    int64
	slowCacheHits    int64
	cacheMisses      int64
	cachedErrors     int64
	invalidations    int64

	// Histograms (milliseconds)
	buildDurations   []float64
	vertexDurations  map[string][]float64 // keyed by op ID

	// Labels attached to all metrics in this exporter.
	constLabels map[string]string
}

// NewMetricsExporter constructs an exporter.
// constLabels are attached to every metric (e.g. {"service": "ci", "env": "prod"}).
func NewMetricsExporter(constLabels map[string]string) *MetricsExporter {
	return &MetricsExporter{
		vertexDurations: make(map[string][]float64),
		constLabels:     constLabels,
	}
}

// RecordBuild incorporates a completed build's metrics into the exporter.
func (e *MetricsExporter) RecordBuild(m *BuildMetrics, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.buildsTotal++
	if err != nil {
		e.buildFailures++
	} else {
		e.buildSuccesses++
	}
	e.verticesExecuted += int64(m.Executed)
	e.fastCacheHits += int64(m.FastCacheHits)
	e.slowCacheHits += int64(m.SlowCacheHits)
	e.cacheMisses += int64(m.TotalVertices - m.FastCacheHits - m.SlowCacheHits - m.Skipped)
	e.cachedErrors += int64(m.CachedErrors)
	e.buildDurations = append(e.buildDurations, float64(m.TotalDuration.Milliseconds()))
}

// RecordVertexExecution adds a per-vertex timing sample.
// opID should be the operation's stable content-addressable ID.
func (e *MetricsExporter) RecordVertexExecution(opID string, dur time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.vertexDurations[opID] = append(e.vertexDurations[opID], float64(dur.Milliseconds()))
}

// RecordInvalidation increments the invalidation counter.
func (e *MetricsExporter) RecordInvalidation(count int) {
	e.mu.Lock()
	e.invalidations += int64(count)
	e.mu.Unlock()
}

// Collect writes all metrics in Prometheus text format to w.
func (e *MetricsExporter) Collect(w io.Writer) {
	e.mu.Lock()
	defer e.mu.Unlock()

	lb := e.labelStr(nil)

	writeCounter(w, "reactdag_builds_total",
		"Total number of builds attempted.", lb, e.buildsTotal)
	writeCounter(w, "reactdag_build_successes_total",
		"Total number of builds that succeeded.", lb, e.buildSuccesses)
	writeCounter(w, "reactdag_build_failures_total",
		"Total number of builds that failed.", lb, e.buildFailures)
	writeCounter(w, "reactdag_vertices_executed_total",
		"Total vertices executed (cache miss, real execution).", lb, e.verticesExecuted)
	writeCounter(w, "reactdag_cache_hits_total",
		"Total fast-tier cache hits.", e.labelStr(map[string]string{"tier": "fast"}), e.fastCacheHits)
	writeCounter(w, "reactdag_cache_hits_total",
		"", e.labelStr(map[string]string{"tier": "slow"}), e.slowCacheHits)
	writeCounter(w, "reactdag_cache_misses_total",
		"Total cache misses (execution required).", lb, e.cacheMisses)
	writeCounter(w, "reactdag_cached_errors_total",
		"Errors replayed from cache without recompute.", lb, e.cachedErrors)
	writeCounter(w, "reactdag_invalidations_total",
		"Total vertices invalidated by file changes.", lb, e.invalidations)

	writeHistogram(w, "reactdag_build_duration_ms",
		"Build wall-clock duration in milliseconds.", lb, e.buildDurations)

	// Per-op vertex histograms.
	ops := make([]string, 0, len(e.vertexDurations))
	for op := range e.vertexDurations {
		ops = append(ops, op)
	}
	sort.Strings(ops)
	for _, op := range ops {
		opLabel := e.labelStr(map[string]string{"op_id": sanitizeLabel(op)})
		writeHistogram(w, "reactdag_vertex_duration_ms",
			"Vertex execution duration in milliseconds.", opLabel, e.vertexDurations[op])
	}
}

// CollectString returns the Prometheus text output as a string.
func (e *MetricsExporter) CollectString() string {
	var sb strings.Builder
	e.Collect(&sb)
	return sb.String()
}

// ---------------------------------------------------------------------------
// Prometheus text format helpers
// ---------------------------------------------------------------------------

var prometheusHistogramBuckets = []float64{
	1, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000,
}

func writeCounter(w io.Writer, name, help, labels string, value int64) {
	if help != "" {
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s counter\n", name, help, name)
	}
	fmt.Fprintf(w, "%s%s %d\n", name, labels, value)
}

func writeHistogram(w io.Writer, name, help, labels string, samples []float64) {
	if len(samples) == 0 {
		return
	}
	if help != "" {
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s histogram\n", name, help, name)
	}
	sort.Float64s(samples)

	var sum float64
	for _, s := range samples {
		sum += s
	}

	for _, b := range prometheusHistogramBuckets {
		count := 0
		for _, s := range samples {
			if s <= b {
				count++
			}
		}
		bl := appendLabel(labels, fmt.Sprintf(`le="%g"`, b))
		fmt.Fprintf(w, "%s_bucket%s %d\n", name, bl, count)
	}
	fmt.Fprintf(w, "%s_bucket%s %d\n", name, appendLabel(labels, `le="+Inf"`), len(samples))
	fmt.Fprintf(w, "%s_sum%s %.3f\n", name, labels, sum)
	fmt.Fprintf(w, "%s_count%s %d\n", name, labels, len(samples))
}

// labelStr builds a Prometheus label set string from the exporter's const labels
// merged with extra labels.
func (e *MetricsExporter) labelStr(extra map[string]string) string {
	merged := make(map[string]string, len(e.constLabels)+len(extra))
	for k, v := range e.constLabels {
		merged[k] = v
	}
	for k, v := range extra {
		merged[k] = v
	}
	return formatLabels(merged)
}

func formatLabels(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf(`%s="%s"`, k, labels[k]))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func appendLabel(existing, newLabel string) string {
	if existing == "" {
		return "{" + newLabel + "}"
	}
	return existing[:len(existing)-1] + "," + newLabel + "}"
}

func sanitizeLabel(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// EventBus hook — wire exporter directly to a bus
// ---------------------------------------------------------------------------

// ExporterHook wires a MetricsExporter to an EventBus so it automatically
// receives build and cache events without manual calls.
type ExporterHook struct {
	exporter *MetricsExporter
	unsub    []func()
}

// NewExporterHook creates an ExporterHook and subscribes it to bus.
func NewExporterHook(exp *MetricsExporter, bus *EventBus) *ExporterHook {
	h := &ExporterHook{exporter: exp}
	h.unsub = []func(){
		bus.Subscribe(EventCacheHit, h.onCacheHit),
		bus.Subscribe(EventCacheMiss, h.onCacheMiss),
		bus.Subscribe(EventExecutionEnd, h.onExecutionEnd),
		bus.Subscribe(EventInvalidated, h.onInvalidated),
	}
	return h
}

// Unsubscribe removes all EventBus subscriptions.
func (h *ExporterHook) Unsubscribe() {
	for _, u := range h.unsub {
		u()
	}
}

func (h *ExporterHook) onCacheHit(_ context.Context, e Event) {
	tier, _ := e.Payload["tier"].(string)
	if tier == "fast" {
		h.exporter.mu.Lock()
		h.exporter.fastCacheHits++
		h.exporter.mu.Unlock()
	} else {
		h.exporter.mu.Lock()
		h.exporter.slowCacheHits++
		h.exporter.mu.Unlock()
	}
}

func (h *ExporterHook) onCacheMiss(_ context.Context, _ Event) {
	h.exporter.mu.Lock()
	h.exporter.cacheMisses++
	h.exporter.mu.Unlock()
}

func (h *ExporterHook) onExecutionEnd(_ context.Context, e Event) {
	if ms, ok := e.Payload["duration_ms"].(int64); ok {
		h.exporter.mu.Lock()
		h.exporter.vertexDurations[e.VertexID] = append(
			h.exporter.vertexDurations[e.VertexID], float64(ms))
		h.exporter.mu.Unlock()
	}
}

func (h *ExporterHook) onInvalidated(_ context.Context, _ Event) {
	h.exporter.mu.Lock()
	h.exporter.invalidations++
	h.exporter.mu.Unlock()
}
