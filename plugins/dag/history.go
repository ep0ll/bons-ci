package reactdag

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// BuildRecord — one completed build in the history
// ---------------------------------------------------------------------------

// BuildRecord is a serialisable snapshot of a single completed build.
type BuildRecord struct {
	ID            string            `json:"id"`
	TargetID      string            `json:"target_id"`
	StartedAt     time.Time         `json:"started_at"`
	EndedAt       time.Time         `json:"ended_at"`
	DurationMS    int64             `json:"duration_ms"`
	Succeeded     bool              `json:"succeeded"`
	Error         string            `json:"error,omitempty"`
	Executed      int               `json:"executed"`
	FastCacheHits int               `json:"fast_cache_hits"`
	SlowCacheHits int               `json:"slow_cache_hits"`
	CachedErrors  int               `json:"cached_errors"`
	Failed        int               `json:"failed"`
	Skipped       int               `json:"skipped"`
	CriticalPath  []string          `json:"critical_path,omitempty"`
	Labels        map[string]string `json:"labels,omitempty"`
}

// Duration returns the build's total wall-clock duration.
func (r BuildRecord) Duration() time.Duration {
	return time.Duration(r.DurationMS) * time.Millisecond
}

// HitRate returns the cache hit ratio (0–1) for this build.
func (r BuildRecord) HitRate() float64 {
	total := r.Executed + r.FastCacheHits + r.SlowCacheHits
	if total == 0 {
		return 0
	}
	return float64(r.FastCacheHits+r.SlowCacheHits) / float64(total)
}

// ---------------------------------------------------------------------------
// BuildHistory — append-only audit log with trend analysis
// ---------------------------------------------------------------------------

// BuildHistory accumulates BuildRecords and provides trend queries.
// It is safe for concurrent use and optionally persists to a JSON-lines file.
type BuildHistory struct {
	mu      sync.RWMutex
	records []BuildRecord
	maxSize int    // 0 = unlimited
	logPath string // "" = in-memory only
}

// NewBuildHistory constructs a BuildHistory.
// maxSize caps the in-memory record count (0 = unlimited).
// logPath is a JSON-lines file for persistence ("" = disabled).
func NewBuildHistory(maxSize int, logPath string) *BuildHistory {
	return &BuildHistory{maxSize: maxSize, logPath: logPath}
}

// Record appends a BuildRecord derived from the given metrics and optional
// error. Labels are arbitrary key-value metadata (e.g., git SHA, branch).
func (h *BuildHistory) Record(
	targetID string,
	start time.Time,
	m *BuildMetrics,
	buildErr error,
	labels map[string]string,
) BuildRecord {
	rec := BuildRecord{
		ID:            fmt.Sprintf("%d", start.UnixNano()),
		TargetID:      targetID,
		StartedAt:     start,
		EndedAt:       start.Add(m.TotalDuration),
		DurationMS:    m.TotalDuration.Milliseconds(),
		Succeeded:     buildErr == nil,
		Executed:      m.Executed,
		FastCacheHits: m.FastCacheHits,
		SlowCacheHits: m.SlowCacheHits,
		CachedErrors:  m.CachedErrors,
		Failed:        m.Failed,
		Skipped:       m.Skipped,
		CriticalPath:  m.CriticalPath,
		Labels:        labels,
	}
	if buildErr != nil {
		rec.Error = buildErr.Error()
	}

	h.mu.Lock()
	h.records = append(h.records, rec)
	if h.maxSize > 0 && len(h.records) > h.maxSize {
		h.records = h.records[len(h.records)-h.maxSize:]
	}
	h.mu.Unlock()

	if h.logPath != "" {
		h.appendToLog(rec)
	}
	return rec
}

// All returns a snapshot of all records, most recent last.
func (h *BuildHistory) All() []BuildRecord {
	h.mu.RLock()
	defer h.mu.RUnlock()
	cp := make([]BuildRecord, len(h.records))
	copy(cp, h.records)
	return cp
}

// Last returns the N most recent records (or fewer if history is short).
func (h *BuildHistory) Last(n int) []BuildRecord {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if n >= len(h.records) {
		cp := make([]BuildRecord, len(h.records))
		copy(cp, h.records)
		return cp
	}
	cp := make([]BuildRecord, n)
	copy(cp, h.records[len(h.records)-n:])
	return cp
}

// ---------------------------------------------------------------------------
// Trend analysis
// ---------------------------------------------------------------------------

// TrendStats is a statistical summary computed over a window of BuildRecords.
type TrendStats struct {
	Count         int
	PassRate      float64 // fraction of builds that succeeded (0–1)
	AvgDurationMS int64
	P50DurationMS int64
	P95DurationMS int64
	AvgHitRate    float64 // average cache hit ratio (0–1)
	AvgExecuted   float64 // average executed vertices per build
	AvgCachedErr  float64 // average cached error replays per build
	Trend         DurationTrend
}

// DurationTrend compares recent builds to older ones.
type DurationTrend string

const (
	TrendImproving    DurationTrend = "improving"
	TrendDegrading    DurationTrend = "degrading"
	TrendStable       DurationTrend = "stable"
	TrendInsufficient DurationTrend = "insufficient_data"
)

// Trend computes statistics over the last n records for the given targetID.
// Pass targetID="" to include all targets.
func (h *BuildHistory) Trend(targetID string, n int) TrendStats {
	records := h.filtered(targetID, n)
	if len(records) == 0 {
		return TrendStats{Trend: TrendInsufficient}
	}

	stats := TrendStats{Count: len(records)}

	// Pass rate.
	passed := 0
	for _, r := range records {
		if r.Succeeded {
			passed++
		}
	}
	stats.PassRate = float64(passed) / float64(len(records))

	// Duration percentiles.
	durs := make([]int64, 0, len(records))
	for _, r := range records {
		durs = append(durs, r.DurationMS)
	}
	sort.Slice(durs, func(i, j int) bool { return durs[i] < durs[j] })
	var sum int64
	for _, d := range durs {
		sum += d
	}
	stats.AvgDurationMS = sum / int64(len(durs))
	stats.P50DurationMS = percentile(durs, 50)
	stats.P95DurationMS = percentile(durs, 95)

	// Cache and execution averages.
	var hitSum, execSum, cachedErrSum float64
	for _, r := range records {
		hitSum += r.HitRate()
		execSum += float64(r.Executed)
		cachedErrSum += float64(r.CachedErrors)
	}
	n64 := float64(len(records))
	stats.AvgHitRate = hitSum / n64
	stats.AvgExecuted = execSum / n64
	stats.AvgCachedErr = cachedErrSum / n64

	// Trend: compare first half vs second half average duration.
	stats.Trend = computeTrend(durs)

	return stats
}

// WriteTrendReport writes a human-readable trend report to w.
func WriteTrendReport(w interface{ WriteString(string) (int, error) }, h *BuildHistory, targetID string, n int) {
	stats := h.Trend(targetID, n)
	records := h.Last(min(n, 5))

	var b strings.Builder
	fmt.Fprintln(&b, "Build History Trend")
	fmt.Fprintln(&b, strings.Repeat("─", 50))
	fmt.Fprintf(&b, "  %-24s %d\n", "Builds analysed:", stats.Count)
	fmt.Fprintf(&b, "  %-24s %.1f%%\n", "Pass rate:", stats.PassRate*100)
	fmt.Fprintf(&b, "  %-24s %dms\n", "Avg duration:", stats.AvgDurationMS)
	fmt.Fprintf(&b, "  %-24s %dms / %dms\n", "P50 / P95:", stats.P50DurationMS, stats.P95DurationMS)
	fmt.Fprintf(&b, "  %-24s %.1f%%\n", "Avg cache hit rate:", stats.AvgHitRate*100)
	fmt.Fprintf(&b, "  %-24s %.1f\n", "Avg vertices executed:", stats.AvgExecuted)
	fmt.Fprintf(&b, "  %-24s %.1f\n", "Avg cached errors:", stats.AvgCachedErr)
	fmt.Fprintf(&b, "  %-24s %s\n", "Duration trend:", stats.Trend)

	if len(records) > 0 {
		fmt.Fprintln(&b)
		fmt.Fprintln(&b, "Recent builds:")
		fmt.Fprintf(&b, "  %-20s %-8s %-12s %-8s\n", "ID", "Status", "Duration", "Cache%")
		fmt.Fprintln(&b, "  "+strings.Repeat("─", 52))
		for _, r := range records {
			status := "PASS"
			if !r.Succeeded {
				status = "FAIL"
			}
			fmt.Fprintf(&b, "  %-20s %-8s %-12s %.0f%%\n",
				truncate(r.ID, 19), status, fmtDuration(r.Duration()), r.HitRate()*100)
		}
	}

	w.WriteString(b.String())
}

// ---------------------------------------------------------------------------
// Persistence
// ---------------------------------------------------------------------------

// LoadHistory reads a JSON-lines log file and returns all records.
func LoadHistory(logPath string) ([]BuildRecord, error) {
	data, err := os.ReadFile(logPath)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("build history: read %q: %w", logPath, err)
	}
	var records []BuildRecord
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var r BuildRecord
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			continue // skip malformed lines
		}
		records = append(records, r)
	}
	return records, nil
}

// appendToLog appends a single record as a JSON line to the log file.
func (h *BuildHistory) appendToLog(rec BuildRecord) {
	data, err := json.Marshal(rec)
	if err != nil {
		return
	}
	f, err := os.OpenFile(h.logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	f.Write(append(data, '\n'))
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// filtered returns up to n records matching targetID (empty = all).
func (h *BuildHistory) filtered(targetID string, n int) []BuildRecord {
	h.mu.RLock()
	defer h.mu.RUnlock()
	var out []BuildRecord
	for i := len(h.records) - 1; i >= 0 && (n <= 0 || len(out) < n); i-- {
		if targetID == "" || h.records[i].TargetID == targetID {
			out = append([]BuildRecord{h.records[i]}, out...)
		}
	}
	return out
}

// percentile computes the p-th percentile of a sorted int64 slice.
func percentile(sorted []int64, p int) int64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(math.Ceil(float64(p)/100*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// computeTrend compares the average of the first half vs second half of durations.
func computeTrend(sortedDurs []int64) DurationTrend {
	if len(sortedDurs) < 4 {
		return TrendInsufficient
	}
	mid := len(sortedDurs) / 2
	first := avgInt64(sortedDurs[:mid])
	second := avgInt64(sortedDurs[mid:])
	delta := float64(second-first) / float64(first)
	switch {
	case delta < -0.05:
		return TrendImproving
	case delta > 0.05:
		return TrendDegrading
	default:
		return TrendStable
	}
}

func avgInt64(s []int64) int64 {
	if len(s) == 0 {
		return 0
	}
	var sum int64
	for _, v := range s {
		sum += v
	}
	return sum / int64(len(s))
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
