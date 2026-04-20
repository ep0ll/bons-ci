package reactdag

import (
	"context"
	"fmt"
)

// ---------------------------------------------------------------------------
// Default Executor
// ---------------------------------------------------------------------------

// defaultExecutor is the built-in Executor. It resolves input files from the
// vertex's parents, calls the Operation, and stores the output file list.
//
// The FileTracker field is an optional hook point: when non-nil, the executor
// starts tracking before the operation and harvests observed files afterward.
// Bind a fanotify-backed FileTracker via WithExecutor(NewTrackedExecutor(...)).
type defaultExecutor struct {
	tracker FileTracker // may be nil
}

// newDefaultExecutor constructs the built-in executor.
func newDefaultExecutor(tracker FileTracker) Executor {
	return &defaultExecutor{tracker: tracker}
}

// NewDefaultExecutorForTest exposes the default executor for use in tests and
// external executor wrappers (RetryExecutor, ResourceLimiter, etc.).
func NewDefaultExecutorForTest() Executor {
	return newDefaultExecutor(nil)
}

// Execute runs the vertex operation and populates its output file list.
func (e *defaultExecutor) Execute(ctx context.Context, v *Vertex) error {
	inputs := v.InputFiles()

	if err := e.startTracking(v); err != nil {
		return fmt.Errorf("executor: start tracking on %q: %w", v.ID(), err)
	}

	outputs, opErr := v.Op().Execute(ctx, inputs)

	observed, trackErr := e.stopTracking()
	if trackErr != nil {
		// Non-fatal: log via context but do not mask the op error.
		_ = trackErr
	}

	e.reconcileFileAccess(v, observed, outputs)

	if opErr != nil {
		return fmt.Errorf("executor: vertex %q: %w", v.ID(), opErr)
	}

	v.SetOutputFiles(outputs)
	return nil
}

// startTracking begins file access monitoring if a tracker is configured.
func (e *defaultExecutor) startTracking(v *Vertex) error {
	if e.tracker == nil {
		return nil
	}
	// PID 0 signals "current process subtree"; real implementations pass the
	// forked process PID obtained just before exec.
	return e.tracker.StartTracking(0)
}

// stopTracking ends file monitoring and returns the observed file list.
func (e *defaultExecutor) stopTracking() ([]FileRef, error) {
	if e.tracker == nil {
		return nil, nil
	}
	return e.tracker.StopTracking()
}

// reconcileFileAccess merges tracker observations with declared outputs and
// updates the vertex's I/O counters.
func (e *defaultExecutor) reconcileFileAccess(v *Vertex, observed []FileRef, declared []FileRef) {
	readCount := len(observed)
	writeCount := len(declared)

	var bytesRead, bytesWritten int64
	for _, f := range observed {
		bytesRead += f.Size
	}
	for _, f := range declared {
		bytesWritten += f.Size
	}

	v.RecordFileAccess(readCount, writeCount, bytesRead, bytesWritten)
}
