package reactdag

import (
	"context"
	"fmt"
	"log"
	"runtime/debug"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// ExecutorMiddleware — composable executor pipeline
// ---------------------------------------------------------------------------

// ExecutorMiddleware wraps an Executor with additional behaviour.
// Middlewares are composed left-to-right:
//
//	Chain(base, a, b, c) → a wraps b wraps c wraps base
type ExecutorMiddleware func(next Executor) Executor

// Chain composes middlewares around a base Executor.
// The first middleware listed is the outermost (called first).
func Chain(base Executor, middlewares ...ExecutorMiddleware) Executor {
	e := base
	for i := len(middlewares) - 1; i >= 0; i-- {
		e = middlewares[i](e)
	}
	return e
}

// ---------------------------------------------------------------------------
// Built-in middlewares
// ---------------------------------------------------------------------------

// Logger is a minimal interface for log output accepted by LoggingMiddleware.
// Both *log.Logger and any custom logger satisfying Printf work.
type Logger interface {
	Printf(format string, args ...interface{})
}

// LoggingMiddleware logs the start, end, and duration of every vertex execution.
// Pass nil to use the standard log package.
func LoggingMiddleware(logger Logger) ExecutorMiddleware {
	logf := log.Printf
	if logger != nil {
		logf = logger.Printf
	}
	return func(next Executor) Executor {
		return executorFn(func(ctx context.Context, v *Vertex) error {
			logf("[dag] execute start  vertex=%s op=%s", v.ID(), v.OpID())
			start := time.Now()
			err := next.Execute(ctx, v)
			dur := time.Since(start)
			if err != nil {
				logf("[dag] execute error  vertex=%s dur=%s err=%v", v.ID(), dur, err)
			} else {
				logf("[dag] execute done   vertex=%s dur=%s", v.ID(), dur)
			}
			return err
		})
	}
}

// MetricsMiddleware records execution samples to a MetricsSink after each run.
func MetricsMiddleware(sink MetricsSink) ExecutorMiddleware {
	return func(next Executor) Executor {
		return executorFn(func(ctx context.Context, v *Vertex) error {
			start := time.Now()
			err := next.Execute(ctx, v)
			dur := time.Since(start)
			m := v.Metrics()
			sink.RecordExecution(ctx, ExecutionSample{
				VertexID:     v.ID(),
				OpID:         v.OpID(),
				Duration:     dur,
				FilesRead:    m.FilesRead,
				FilesWritten: m.FilesWritten,
				BytesRead:    m.BytesRead,
				BytesWritten: m.BytesWritten,
				Error:        err,
			})
			return err
		})
	}
}

// TracingMiddleware wraps each vertex execution in a Tracer span.
func TracingMiddleware(tracer Tracer) ExecutorMiddleware {
	return func(next Executor) Executor {
		return executorFn(func(ctx context.Context, v *Vertex) error {
			spanCtx, span := tracer.Start(ctx, "vertex.execute", map[string]interface{}{
				"dag.vertex.id": v.ID(),
				"dag.vertex.op": v.OpID(),
			})
			defer span.End()
			err := next.Execute(spanCtx, v)
			if err != nil {
				span.RecordError(err)
				span.SetAttribute("error", true)
			}
			span.SetAttribute("dag.vertex.state", v.State().String())
			return err
		})
	}
}

// PanicRecoveryMiddleware converts panics inside an executor into errors.
// This prevents a single misbehaving Operation from killing the whole process.
func PanicRecoveryMiddleware() ExecutorMiddleware {
	return func(next Executor) Executor {
		return executorFn(func(ctx context.Context, v *Vertex) (retErr error) {
			defer func() {
				if r := recover(); r != nil {
					stack := debug.Stack()
					retErr = fmt.Errorf("vertex %q panicked: %v\n%s", v.ID(), r, stack)
				}
			}()
			return next.Execute(ctx, v)
		})
	}
}

// DryRunMiddleware replaces real execution with a no-op. Useful for validating
// graph structure and caching logic without running actual operations.
func DryRunMiddleware() ExecutorMiddleware {
	return func(_ Executor) Executor {
		return executorFn(func(_ context.Context, v *Vertex) error {
			v.SetOutputFiles(nil)
			return nil
		})
	}
}

// ContextValueMiddleware injects a key-value pair into the context passed to the
// inner executor. Useful for propagating build-level metadata such as trace IDs.
func ContextValueMiddleware(key, value interface{}) ExecutorMiddleware {
	return func(next Executor) Executor {
		return executorFn(func(ctx context.Context, v *Vertex) error {
			return next.Execute(context.WithValue(ctx, key, value), v)
		})
	}
}

// ---------------------------------------------------------------------------
// MetricsSink — pluggable metrics receiver
// ---------------------------------------------------------------------------

// ExecutionSample carries a single vertex execution measurement.
type ExecutionSample struct {
	VertexID     string
	OpID         string
	Duration     time.Duration
	FilesRead    int
	FilesWritten int
	BytesRead    int64
	BytesWritten int64
	Error        error
}

// MetricsSink receives execution samples. Implement this interface to integrate
// with Prometheus, DataDog, OTel metrics, or any other backend.
type MetricsSink interface {
	RecordExecution(ctx context.Context, s ExecutionSample)
}

// ---------------------------------------------------------------------------
// InMemoryMetricsSink — for testing and local aggregation
// ---------------------------------------------------------------------------

// InMemoryMetricsSink accumulates samples in memory.
type InMemoryMetricsSink struct {
	mu      sync.RWMutex
	samples []ExecutionSample
}

// NewInMemoryMetricsSink constructs an empty in-memory sink.
func NewInMemoryMetricsSink() *InMemoryMetricsSink { return &InMemoryMetricsSink{} }

// RecordExecution appends the sample to the internal list.
func (s *InMemoryMetricsSink) RecordExecution(_ context.Context, sample ExecutionSample) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.samples = append(s.samples, sample)
}

// Samples returns a snapshot of all recorded samples (in arrival order).
func (s *InMemoryMetricsSink) Samples() []ExecutionSample {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cp := make([]ExecutionSample, len(s.samples))
	copy(cp, s.samples)
	return cp
}

// TotalDuration returns the sum of all sample durations.
func (s *InMemoryMetricsSink) TotalDuration() time.Duration {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var total time.Duration
	for _, sam := range s.samples {
		total += sam.Duration
	}
	return total
}

// ErrorCount returns the number of samples where Error is non-nil.
func (s *InMemoryMetricsSink) ErrorCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := 0
	for _, sam := range s.samples {
		if sam.Error != nil {
			n++
		}
	}
	return n
}

// ---------------------------------------------------------------------------
// executorFn — function adapter satisfying the Executor interface
// ---------------------------------------------------------------------------

type executorFn func(ctx context.Context, v *Vertex) error

func (f executorFn) Execute(ctx context.Context, v *Vertex) error { return f(ctx, v) }

// NewExecutorFn wraps a plain function as an Executor. Useful in tests and
// middleware composition.
func NewExecutorFn(fn func(ctx context.Context, v *Vertex) error) Executor {
	return executorFn(fn)
}
