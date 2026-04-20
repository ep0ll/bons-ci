package reactdag

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// MetricsReporter — structured build report
// ---------------------------------------------------------------------------

// ReportOptions controls which sections appear in the report.
type ReportOptions struct {
	ShowPerVertex    bool // per-vertex timing table
	ShowCriticalPath bool // critical path with durations
	ShowCacheStats   bool // cache hit/miss ratios
	ShowFileStats    bool // files read/written per vertex
	ShowTransitions  bool // state machine history per vertex
	MaxVertexRows    int  // 0 = unlimited
}

// DefaultReportOptions returns a sensible default for CI output.
func DefaultReportOptions() ReportOptions {
	return ReportOptions{
		ShowPerVertex:    true,
		ShowCriticalPath: true,
		ShowCacheStats:   true,
		ShowFileStats:    false,
		ShowTransitions:  false,
		MaxVertexRows:    50,
	}
}

// WriteReport writes a structured build report to w.
func WriteReport(w io.Writer, d *DAG, m *BuildMetrics, opts ReportOptions) {
	fmt.Fprintln(w, boxLine("Build Report", 70))
	writeSummary(w, m)

	if opts.ShowCacheStats {
		fmt.Fprintln(w)
		writeCacheStats(w, m)
	}

	if opts.ShowCriticalPath && len(m.CriticalPath) > 0 {
		fmt.Fprintln(w)
		writeCriticalPath(w, d, m)
	}

	if opts.ShowPerVertex && len(m.PerVertex) > 0 {
		fmt.Fprintln(w)
		writePerVertexTable(w, d, m, opts)
	}
}

// ---------------------------------------------------------------------------
// Summary
// ---------------------------------------------------------------------------

func writeSummary(w io.Writer, m *BuildMetrics) {
	fmt.Fprintln(w, "Summary")
	fmt.Fprintln(w, strings.Repeat("─", 40))
	fmt.Fprintf(w, "  %-22s %d\n", "Total vertices:", m.TotalVertices)
	fmt.Fprintf(w, "  %-22s %d\n", "Executed:", m.Executed)
	fmt.Fprintf(w, "  %-22s %d\n", "Fast cache hits:", m.FastCacheHits)
	fmt.Fprintf(w, "  %-22s %d\n", "Slow cache hits:", m.SlowCacheHits)
	fmt.Fprintf(w, "  %-22s %d\n", "Skipped:", m.Skipped)
	fmt.Fprintf(w, "  %-22s %d\n", "Failed:", m.Failed)
	fmt.Fprintf(w, "  %-22s %d\n", "Cached errors:", m.CachedErrors)
	fmt.Fprintf(w, "  %-22s %s\n", "Total duration:", fmtDuration(m.TotalDuration))
}

// ---------------------------------------------------------------------------
// Cache stats
// ---------------------------------------------------------------------------

func writeCacheStats(w io.Writer, m *BuildMetrics) {
	total := m.Executed + m.FastCacheHits + m.SlowCacheHits
	hitRate := 0.0
	if total > 0 {
		hitRate = float64(m.FastCacheHits+m.SlowCacheHits) / float64(total) * 100
	}

	fmt.Fprintln(w, "Cache Statistics")
	fmt.Fprintln(w, strings.Repeat("─", 40))
	fmt.Fprintf(w, "  %-22s %.1f%%\n", "Overall hit rate:", hitRate)
	if total > 0 {
		fastRate := float64(m.FastCacheHits) / float64(total) * 100
		slowRate := float64(m.SlowCacheHits) / float64(total) * 100
		fmt.Fprintf(w, "  %-22s %.1f%%\n", "Fast tier rate:", fastRate)
		fmt.Fprintf(w, "  %-22s %.1f%%\n", "Slow tier rate:", slowRate)
	}
	if m.CachedErrors > 0 {
		fmt.Fprintf(w, "  %-22s %d  ← errors replayed without recompute\n", "Cached errors:", m.CachedErrors)
	}
}

// ---------------------------------------------------------------------------
// Critical path
// ---------------------------------------------------------------------------

func writeCriticalPath(w io.Writer, d *DAG, m *BuildMetrics) {
	fmt.Fprintln(w, "Critical Path")
	fmt.Fprintln(w, strings.Repeat("─", 40))

	var totalMS int64
	for _, id := range m.CriticalPath {
		vm, ok := m.PerVertex[id]
		dur := vm.Duration()
		state := "─"
		if v, found := d.Vertex(id); found {
			state = v.State().String()
		}
		marker := ""
		if ok && dur > 0 {
			totalMS += dur.Milliseconds()
			marker = fmtDuration(dur)
		}
		fmt.Fprintf(w, "  → %-20s %-12s %s\n", id, state, marker)
	}
	if totalMS > 0 {
		fmt.Fprintf(w, "  %s\n", strings.Repeat("─", 38))
		fmt.Fprintf(w, "  %-20s %-12s %dms\n", "TOTAL", "", totalMS)
	}
}

// ---------------------------------------------------------------------------
// Per-vertex table
// ---------------------------------------------------------------------------

func writePerVertexTable(w io.Writer, d *DAG, m *BuildMetrics, opts ReportOptions) {
	fmt.Fprintln(w, "Per-Vertex Metrics")
	fmt.Fprintln(w, strings.Repeat("─", 70))
	fmt.Fprintf(w, "%-20s %-12s %-10s %-10s %-8s %-8s\n",
		"Vertex", "State", "Duration", "Wait", "R Files", "W Files")
	fmt.Fprintln(w, strings.Repeat("─", 70))

	// Sort alphabetically for stable output.
	ids := make([]string, 0, len(m.PerVertex))
	for id := range m.PerVertex {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	rows := 0
	for _, id := range ids {
		if opts.MaxVertexRows > 0 && rows >= opts.MaxVertexRows {
			fmt.Fprintf(w, "  ... (%d more)\n", len(ids)-rows)
			break
		}
		vm := m.PerVertex[id]
		state := "─"
		if v, ok := d.Vertex(id); ok {
			state = v.State().String()
		}
		fmt.Fprintf(w, "%-20s %-12s %-10s %-10s %-8d %-8d\n",
			truncate(id, 19),
			state,
			fmtDuration(vm.Duration()),
			fmtDuration(vm.WaitTime()),
			vm.FilesRead,
			vm.FilesWritten,
		)

		if opts.ShowTransitions && len(vm.Transitions) > 0 {
			for _, t := range vm.Transitions {
				fmt.Fprintf(w, "    %s  %s → %s  (%s)\n",
					t.Timestamp.Format("15:04:05.000"),
					t.From, t.To, t.Cause)
			}
		}
		rows++
	}
	fmt.Fprintln(w, strings.Repeat("─", 70))
}

// ---------------------------------------------------------------------------
// Text helpers
// ---------------------------------------------------------------------------

func boxLine(title string, width int) string {
	pad := width - len(title) - 4
	if pad < 0 {
		pad = 0
	}
	return fmt.Sprintf("┌─ %s %s┐", title, strings.Repeat("─", pad))
}

func fmtDuration(d time.Duration) string {
	if d <= 0 {
		return "─"
	}
	if d < time.Millisecond {
		return fmt.Sprintf("%dµs", d.Microseconds())
	}
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return fmt.Sprintf("%.2fs", d.Seconds())
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}
