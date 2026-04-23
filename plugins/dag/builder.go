package reactdag

import (
	"fmt"
	"time"
)

// ---------------------------------------------------------------------------
// DAGBuilder — fluent construction API
// ---------------------------------------------------------------------------

// VertexSpec describes a vertex before it is materialised into the DAG.
// All fields are set via the fluent methods on DAGBuilder.
type VertexSpec struct {
	id       string
	op       Operation
	parents  []string
	fileDeps []FileDependency
	labels   map[string]string
	timeout  time.Duration
	retry    RetryPolicy
}

// DAGBuilder is a fluent, error-accumulating builder for DAGs.
// Construction errors are collected and returned as a single error from Build().
//
// Usage:
//
//	dag, err := reactdag.NewBuilder().
//	    Add("compile", compileOp).
//	    Add("link",    linkOp,    reactdag.DependsOn("compile")).
//	    Add("test",    testOp,    reactdag.DependsOn("link")).
//	    Build()
type DAGBuilder struct {
	specs []VertexSpec
	errs  []error
}

// NewBuilder constructs an empty DAGBuilder.
func NewBuilder() *DAGBuilder {
	return &DAGBuilder{}
}

// ---------------------------------------------------------------------------
// Vertex configuration options
// ---------------------------------------------------------------------------

// VertexOption configures a single VertexSpec during Add().
type VertexOption func(*VertexSpec)

// DependsOn declares that this vertex depends on the listed parent IDs.
func DependsOn(parentIDs ...string) VertexOption {
	return func(s *VertexSpec) {
		s.parents = append(s.parents, parentIDs...)
	}
}

// ConsumesFiles declares that this vertex reads only specific paths from a
// named parent's output, enabling fine-grained cache invalidation.
func ConsumesFiles(parentID string, paths ...string) VertexOption {
	return func(s *VertexSpec) {
		s.fileDeps = append(s.fileDeps, FileDependency{
			ParentID: parentID,
			Paths:    paths,
		})
	}
}

// WithLabel attaches a key/value label to the vertex.
func WithLabel(key, value string) VertexOption {
	return func(s *VertexSpec) {
		if s.labels == nil {
			s.labels = make(map[string]string)
		}
		s.labels[key] = value
	}
}

// WithTimeout sets a per-vertex execution deadline.
// Zero (default) means no timeout.
func WithTimeout(d time.Duration) VertexOption {
	return func(s *VertexSpec) { s.timeout = d }
}

// WithRetry sets a retry policy for transient failures on this vertex.
func WithRetry(policy RetryPolicy) VertexOption {
	return func(s *VertexSpec) { s.retry = policy }
}

// ---------------------------------------------------------------------------
// Builder methods
// ---------------------------------------------------------------------------

// Add registers a vertex with the given id, operation, and options.
// Duplicate IDs are collected as errors and returned on Build().
func (b *DAGBuilder) Add(id string, op Operation, opts ...VertexOption) *DAGBuilder {
	if id == "" {
		b.errs = append(b.errs, fmt.Errorf("builder: vertex id must not be empty"))
		return b
	}
	if op == nil {
		b.errs = append(b.errs, fmt.Errorf("builder: vertex %q: operation must not be nil", id))
		return b
	}
	for _, s := range b.specs {
		if s.id == id {
			b.errs = append(b.errs, fmt.Errorf("builder: duplicate vertex id %q", id))
			return b
		}
	}
	spec := VertexSpec{id: id, op: op}
	for _, o := range opts {
		o(&spec)
	}
	b.specs = append(b.specs, spec)
	return b
}

// Build materialises all specs into a sealed DAG.
// Returns all accumulated errors if any spec was invalid.
func (b *DAGBuilder) Build() (*DAG, error) {
	if len(b.errs) > 0 {
		return nil, joinErrors(b.errs)
	}

	d := NewDAG()

	// Pass 1: add all vertices.
	vertexMap := make(map[string]*Vertex, len(b.specs))
	for _, spec := range b.specs {
		v := NewVertex(spec.id, spec.op)
		for k, val := range spec.labels {
			v.SetLabel(k, val)
		}
		if spec.timeout > 0 {
			v.SetLabel("timeout", spec.timeout.String())
		}
		if err := d.AddVertex(v); err != nil {
			return nil, fmt.Errorf("builder: %w", err)
		}
		vertexMap[spec.id] = v
	}

	// Pass 2: link edges and file dependencies.
	for _, spec := range b.specs {
		for _, parentID := range spec.parents {
			if _, ok := vertexMap[parentID]; !ok {
				return nil, fmt.Errorf("builder: vertex %q depends on unknown parent %q", spec.id, parentID)
			}
			if err := d.LinkVertices(parentID, spec.id); err != nil {
				return nil, fmt.Errorf("builder: link %q→%q: %w", parentID, spec.id, err)
			}
		}
		for _, dep := range spec.fileDeps {
			if err := d.AddFileDependency(spec.id, dep.ParentID, dep.Paths); err != nil {
				return nil, fmt.Errorf("builder: file dep %q→%q: %w", spec.id, dep.ParentID, err)
			}
		}
	}

	if err := d.Seal(); err != nil {
		return nil, fmt.Errorf("builder: seal: %w", err)
	}
	return d, nil
}

// ---------------------------------------------------------------------------
// RetryPolicy
// ---------------------------------------------------------------------------

// RetryPolicy controls how a vertex is retried after a transient failure.
type RetryPolicy struct {
	// MaxAttempts is the total number of execution attempts (1 = no retry).
	MaxAttempts int
	// IsRetryable, if non-nil, is called to decide whether a given error should
	// trigger a retry. If nil, all errors are treated as retryable.
	IsRetryable func(err error) bool
	// Backoff, if non-nil, returns the delay between attempt i and i+1.
	// If nil, retries happen immediately.
	Backoff func(attempt int) time.Duration
}

// ShouldRetry reports whether a retry should be attempted given the current
// attempt count (1-based) and the error.
func (p RetryPolicy) ShouldRetry(attempt int, err error) bool {
	if p.MaxAttempts <= 1 || attempt >= p.MaxAttempts {
		return false
	}
	if p.IsRetryable != nil {
		return p.IsRetryable(err)
	}
	return err != nil
}

// DelayFor returns the backoff duration before the given attempt.
func (p RetryPolicy) DelayFor(attempt int) time.Duration {
	if p.Backoff == nil {
		return 0
	}
	return p.Backoff(attempt)
}

// ---------------------------------------------------------------------------
// Error helpers
// ---------------------------------------------------------------------------

func joinErrors(errs []error) error {
	if len(errs) == 0 {
		return nil
	}
	msg := ""
	for i, e := range errs {
		if i > 0 {
			msg += "; "
		}
		msg += e.Error()
	}
	return fmt.Errorf("%s", msg)
}
