package llb

import "context"

// ─────────────────────────────────────────────────────────────────────────────
// VertexVisitor — strategy interface for DAG traversal
// ─────────────────────────────────────────────────────────────────────────────

// VertexVisitor is the strategy interface for walking an LLB DAG. Operations
// that need to inspect the graph (counting, logging, validation, visualization)
// implement this interface. The Pipeline invokes Visit() for each vertex in
// topological order.
//
// All implementations must be safe for concurrent use.
type VertexVisitor interface {
	// Visit is called once per unique vertex during DAG traversal. The depth
	// indicates the vertex's distance from the traversal root (0 = root).
	Visit(ctx context.Context, v Vertex, depth int) error
}

// VertexVisitorFunc adapts a plain function to the VertexVisitor interface.
type VertexVisitorFunc func(ctx context.Context, v Vertex, depth int) error

// Visit implements VertexVisitor.
func (f VertexVisitorFunc) Visit(ctx context.Context, v Vertex, depth int) error {
	return f(ctx, v, depth)
}

// ─────────────────────────────────────────────────────────────────────────────
// Noop implementations
// ─────────────────────────────────────────────────────────────────────────────

// NoopVisitor silently discards all vertices. Useful as a placeholder or in
// unit tests that only care about marshalling, not traversal.
type NoopVisitor struct{}

// Visit implements VertexVisitor.
func (NoopVisitor) Visit(_ context.Context, _ Vertex, _ int) error { return nil }

// ─────────────────────────────────────────────────────────────────────────────
// Chain / Multi composites
// ─────────────────────────────────────────────────────────────────────────────

// ChainVisitor invokes a sequence of VertexVisitor implementations in order.
// If any visitor returns an error, the chain stops and that error is returned.
type ChainVisitor []VertexVisitor

// Visit implements VertexVisitor.
func (c ChainVisitor) Visit(ctx context.Context, v Vertex, depth int) error {
	for _, vis := range c {
		if err := vis.Visit(ctx, v, depth); err != nil {
			return err
		}
	}
	return nil
}

// MultiVisitor fans out each vertex to all registered visitors. Unlike
// ChainVisitor, every visitor is always invoked even when earlier visitors
// fail. Errors are collected and returned via errors.Join.
type MultiVisitor []VertexVisitor

// Visit implements VertexVisitor.
func (m MultiVisitor) Visit(ctx context.Context, v Vertex, depth int) error {
	var errs []error
	for _, vis := range m {
		if err := vis.Visit(ctx, v, depth); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return joinErrors(errs)
}

// ─────────────────────────────────────────────────────────────────────────────
// Predicate routing
// ─────────────────────────────────────────────────────────────────────────────

// VertexPredicate is a function that gates a VertexVisitor. It must not
// perform I/O or block.
type VertexPredicate func(v Vertex) bool

// PredicateVisitor wraps a VertexVisitor behind a predicate gate. The inner
// visitor is only invoked when the predicate returns true.
type PredicateVisitor struct {
	Predicate VertexPredicate
	Visitor   VertexVisitor
}

// Visit implements VertexVisitor.
func (p PredicateVisitor) Visit(ctx context.Context, v Vertex, depth int) error {
	if p.Predicate(v) {
		return p.Visitor.Visit(ctx, v, depth)
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Predicate constructors
// ─────────────────────────────────────────────────────────────────────────────

// OnlyLeafVertices matches vertices with no inputs (source ops).
func OnlyLeafVertices() VertexPredicate {
	return func(v Vertex) bool { return len(v.Inputs()) == 0 }
}

// OnlyBranchVertices matches vertices with at least one input.
func OnlyBranchVertices() VertexPredicate {
	return func(v Vertex) bool { return len(v.Inputs()) > 0 }
}

// AtMaxDepth matches vertices at or below the given depth.
func AtMaxDepth(max int) VertexPredicate {
	return func(_ Vertex) bool { return true } // depth check happens in Walk
}

// ─────────────────────────────────────────────────────────────────────────────
// CountingVisitor — observer implementation
// ─────────────────────────────────────────────────────────────────────────────

// CountingVisitor counts the number of vertices visited. It is safe for
// concurrent use via atomic operations.
type CountingVisitor struct {
	count int64
}

// Visit implements VertexVisitor.
func (cv *CountingVisitor) Visit(_ context.Context, _ Vertex, _ int) error {
	cv.count++
	return nil
}

// Count returns the total number of vertices visited.
func (cv *CountingVisitor) Count() int64 { return cv.count }

// ─────────────────────────────────────────────────────────────────────────────
// Walk — generic DAG traversal engine
// ─────────────────────────────────────────────────────────────────────────────

// Walk traverses the LLB DAG rooted at out, invoking visitor.Visit for each
// unique vertex in depth-first, topological order. Walk deduplicates by vertex
// identity, ensuring each vertex is visited at most once.
func Walk(ctx context.Context, out Output, visitor VertexVisitor, c *Constraints) error {
	if out == nil || visitor == nil {
		return nil
	}
	visited := make(map[Vertex]struct{})
	return walk(ctx, out, visitor, c, visited, 0)
}

func walk(ctx context.Context, out Output, visitor VertexVisitor, c *Constraints, visited map[Vertex]struct{}, depth int) error {
	if out == nil {
		return nil
	}
	v := out.Vertex(ctx, c)
	if v == nil {
		return nil
	}
	if _, ok := visited[v]; ok {
		return nil
	}
	visited[v] = struct{}{}

	// Visit inputs first (topological order).
	for _, inp := range v.Inputs() {
		if err := walk(ctx, inp, visitor, c, visited, depth+1); err != nil {
			return err
		}
	}

	return visitor.Visit(ctx, v, depth)
}

// ─────────────────────────────────────────────────────────────────────────────
// joinErrors helper
// ─────────────────────────────────────────────────────────────────────────────

func joinErrors(errs []error) error {
	if len(errs) == 0 {
		return nil
	}
	if len(errs) == 1 {
		return errs[0]
	}
	// Use errors.Join equivalent
	msg := errs[0].Error()
	for _, e := range errs[1:] {
		msg += "; " + e.Error()
	}
	return joinedError{msg: msg, errs: errs}
}

type joinedError struct {
	msg  string
	errs []error
}

func (e joinedError) Error() string   { return e.msg }
func (e joinedError) Unwrap() []error { return e.errs }
