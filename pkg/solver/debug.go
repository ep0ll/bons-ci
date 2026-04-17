package solver

import (
	"fmt"
	"os"
	"strings"
	"sync"
)

// Debug instrumentation for the solver. Controlled by environment variables:
//   - SOLVER_DEBUG_SCHEDULER=1: enable verbose scheduler logging
//   - SOLVER_DEBUG_STEPS=step1,step2: filter debug to specific vertex names
//
// This matches BuildKit's debug.go.

var (
	debugScheduler      = false
	debugSchedulerSteps = sync.OnceValue(parseSchedulerDebugSteps)
)

func init() {
	if v := os.Getenv("SOLVER_DEBUG_SCHEDULER"); v != "" && v != "0" && v != "false" {
		debugScheduler = true
	}
}

func parseSchedulerDebugSteps() []string {
	v := os.Getenv("SOLVER_DEBUG_STEPS")
	if v == "" {
		return nil
	}
	return strings.Split(v, ",")
}

// debugCheckVertex returns true if this vertex should emit debug output.
func debugCheckVertex(name string) bool {
	if !debugScheduler {
		return false
	}
	steps := debugSchedulerSteps()
	if len(steps) == 0 {
		return true // debug all
	}
	for _, step := range steps {
		if strings.Contains(name, step) {
			return true
		}
	}
	return false
}

// debugf prints a debug message if debugging is enabled for the vertex.
func debugf(name, format string, args ...any) {
	if debugCheckVertex(name) {
		fmt.Fprintf(os.Stderr, "[solver:debug] [%s] %s\n", name, fmt.Sprintf(format, args...))
	}
}

// ─── Debug event functions ──────────────────────────────────────────────────

// DebugVertexStarted logs when a vertex begins execution.
func DebugVertexStarted(name string) {
	debugf(name, "started")
}

// DebugVertexCompleted logs when a vertex finishes execution.
func DebugVertexCompleted(name, resultID string) {
	debugf(name, "completed result=%s", resultID)
}

// DebugVertexCanceled logs when a vertex is canceled.
func DebugVertexCanceled(name string) {
	debugf(name, "canceled")
}

// DebugVertexFailed logs when a vertex fails.
func DebugVertexFailed(name string, err error) {
	debugf(name, "failed: %v", err)
}

// DebugCacheHit logs a cache hit.
func DebugCacheHit(name, resultID string) {
	debugf(name, "cache hit result=%s", resultID)
}

// DebugCacheMiss logs a cache miss.
func DebugCacheMiss(name string) {
	debugf(name, "cache miss")
}

// DebugSchedulerSubmit logs when a vertex is submitted to the scheduler.
func DebugSchedulerSubmit(name string, depth, priority int) {
	debugf(name, "submitted depth=%d priority=%d", depth, priority)
}

// DebugSchedulerReady logs when a vertex becomes ready for execution.
func DebugSchedulerReady(name string) {
	debugf(name, "all parents resolved, ready")
}
