package reactdag

import (
	"context"
	"fmt"
	"time"
)

// ---------------------------------------------------------------------------
// Build-level deadline
// ---------------------------------------------------------------------------

// WithBuildDeadline returns a context that expires at deadline.
// Pass the returned context to Scheduler.Build to enforce a hard build timeout.
//
//	ctx, cancel := dag.WithBuildDeadline(context.Background(), 5*time.Minute)
//	defer cancel()
//	metrics, err := sched.Build(ctx, "target", nil)
func WithBuildDeadline(parent context.Context, deadline time.Time) (context.Context, context.CancelFunc) {
	return context.WithDeadline(parent, deadline)
}

// WithBuildTimeout returns a context that expires after d.
// Convenience wrapper around context.WithTimeout.
func WithBuildTimeout(parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, d)
}

// ---------------------------------------------------------------------------
// PerVertexTimeoutExecutor — apply per-vertex timeouts from vertex labels
// ---------------------------------------------------------------------------

// PerVertexTimeoutExecutor wraps an Executor and applies a per-vertex timeout
// derived from the "timeout" label (set via dag.WithTimeout in DAGBuilder).
// If the label is absent or unparseable, the defaultTimeout is used (0 = no timeout).
//
// This is a thin wrapper; prefer composing it via Chain:
//
//	exec := dag.Chain(
//	    dag.NewDefaultExecutorForTest(),
//	    dag.PerVertexTimeoutMiddleware(30*time.Second),
//	)
func PerVertexTimeoutMiddleware(defaultTimeout time.Duration) ExecutorMiddleware {
	return func(next Executor) Executor {
		return executorFn(func(ctx context.Context, v *Vertex) error {
			timeout := defaultTimeout
			if label, ok := v.Label("timeout"); ok {
				if d, err := time.ParseDuration(label); err == nil {
					timeout = d
				}
			}
			if timeout <= 0 {
				return next.Execute(ctx, v)
			}
			timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			err := next.Execute(timeoutCtx, v)
			if err != nil && timeoutCtx.Err() == context.DeadlineExceeded {
				return fmt.Errorf("vertex %q timed out after %s", v.ID(), timeout)
			}
			return err
		})
	}
}

// ---------------------------------------------------------------------------
// StallDetector — detects vertices that haven't progressed within a window
// ---------------------------------------------------------------------------

// StallReport describes a vertex that appears stalled.
type StallReport struct {
	VertexID  string
	StartedAt time.Time
	Stalled   time.Duration // how long it has been running
}

// DetectStalls returns vertices that have been in a non-terminal, non-initial
// state for longer than stallAfter.
func DetectStalls(d *DAG, stallAfter time.Duration) []StallReport {
	now := time.Now()
	var stalls []StallReport
	for _, v := range d.All() {
		st := v.State()
		if st.IsTerminal() || st == StateInitial {
			continue
		}
		m := v.Metrics()
		if m.StartedAt.IsZero() {
			continue
		}
		elapsed := now.Sub(m.StartedAt)
		if elapsed > stallAfter {
			stalls = append(stalls, StallReport{
				VertexID:  v.ID(),
				StartedAt: m.StartedAt,
				Stalled:   elapsed,
			})
		}
	}
	return stalls
}
