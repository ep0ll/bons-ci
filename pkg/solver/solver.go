package solver

import (
	"context"
	"fmt"
	"sync"

	"github.com/bons/bons-ci/pkg/solver/cache"
	"github.com/bons/bons-ci/pkg/solver/schedule"
	"github.com/bons/bons-ci/pkg/solver/signal"
	"github.com/bons/bons-ci/pkg/solver/stream"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
)

// SolverOpts configures the solver.
type SolverOpts struct {
	// ResolveOp converts a vertex's Sys() into an executable Op.
	ResolveOp ResolveOpFunc

	// Cache is the primary cache store. If nil, an in-memory store is used.
	Cache cache.Store

	// Workers is the scheduler concurrency. Defaults to GOMAXPROCS.
	Workers int

	// Policy is the scheduling policy. Defaults to CriticalPathPolicy.
	Policy schedule.Policy
}

// Solver is the main coordinator. It manages solve sessions, integrating
// the cache, scheduler, event bus, and streaming pipeline.
type Solver struct {
	opts  SolverOpts
	cache cache.Store
	bus   *signal.Bus
}

// New creates a new solver with the given options.
func New(opts SolverOpts) *Solver {
	c := opts.Cache
	if c == nil {
		c = cache.NewMemory()
	}
	if opts.Policy == nil {
		opts.Policy = &schedule.CriticalPathPolicy{}
	}
	return &Solver{
		opts:  opts,
		cache: c,
		bus:   signal.NewBus(),
	}
}

// Bus returns the event bus for subscribing to solver events.
func (s *Solver) Bus() *signal.Bus {
	return s.bus
}

// Close shuts down the solver and releases resources.
func (s *Solver) Close() {
	s.bus.Close()
}

// SolveOption configures a single Solve call.
type SolveOption func(*solveConfig)

type solveConfig struct {
	pipeline *stream.Pipeline
}

// WithTransform attaches a streaming transformation pipeline to the solve.
func WithTransform(p *stream.Pipeline) SolveOption {
	return func(cfg *solveConfig) {
		cfg.pipeline = p
	}
}

// Session holds the state and results of a solve operation.
type Session struct {
	mu      sync.Mutex
	results map[digest.Digest]Result
	errors  map[digest.Digest]error
	graph   *Graph
}

// Result returns the result for a specific leaf edge, or an error.
func (sess *Session) Result(e Edge) (Result, error) {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	dgst := e.Vertex.Digest()
	if err, ok := sess.errors[dgst]; ok {
		return nil, err
	}
	if r, ok := sess.results[dgst]; ok {
		return r, nil
	}
	return nil, errors.Errorf("no result for %s", e.Vertex.Name())
}

// Results returns all results keyed by vertex digest.
func (sess *Session) Results() map[digest.Digest]Result {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	out := make(map[digest.Digest]Result, len(sess.results))
	for k, v := range sess.results {
		out[k] = v
	}
	return out
}

// Graph returns the computed DAG for this session.
func (sess *Session) Graph() *Graph {
	return sess.graph
}

// Solve executes the solver for the given leaf edges. It returns a Session
// containing results for each leaf.
//
// The algorithm uses reactive, completion-driven scheduling:
//  1. Build the reachable graph from leaves upward.
//  2. Probe cache for all vertices; mark cached ones as resolved.
//  3. Identify "ready" uncached vertices (all parents resolved) and submit them.
//  4. As each vertex completes, check if its children are now ready and submit them.
//  5. Continue until all vertices are resolved.
func (s *Solver) Solve(ctx context.Context, leaves []Edge, opts ...SolveOption) (*Session, error) {
	cfg := &solveConfig{}
	for _, opt := range opts {
		opt(cfg)
	}

	// Build the DAG.
	g := BuildGraph(leaves)

	sess := &Session{
		results: make(map[digest.Digest]Result, len(leaves)),
		errors:  make(map[digest.Digest]error, len(leaves)),
		graph:   g,
	}

	// resolved tracks completed vertices (cached or executed).
	resolved := &sync.Map{} // digest.Digest → Result
	// resolvedCh is a per-vertex completion signal.
	resolvedCh := make(map[digest.Digest]chan struct{}, g.Size())
	for _, v := range g.Vertices() {
		resolvedCh[v.Digest()] = make(chan struct{})
	}

	markResolved := func(dgst digest.Digest, result Result) {
		resolved.Store(dgst, result)
		ch := resolvedCh[dgst]
		select {
		case <-ch:
		default:
			close(ch)
		}
	}

	// Phase 1: Cache probe — check all vertices.
	s.bus.Publish(signal.Event{Type: signal.VertexQueued, Name: "cache-probe-phase"})

	uncached := make(map[digest.Digest]Vertex)
	for _, v := range g.TopologicalOrder() {
		dgst := v.Digest()
		if !v.Options().IgnoreCache {
			resultID, found, err := s.cache.Probe(ctx, dgst2key(dgst))
			if err != nil {
				return nil, errors.Wrap(err, "cache probe")
			}
			if found {
				s.bus.Publish(signal.Event{
					Type: signal.CacheHit, Vertex: dgst,
					Name: v.Name(), ResultID: resultID,
				})
				markResolved(dgst, &cachedResultRef{id: resultID, vtx: dgst})
				continue
			}
		}
		s.bus.Publish(signal.Event{Type: signal.CacheMiss, Vertex: dgst, Name: v.Name()})
		uncached[dgst] = v
	}

	// If everything was cached, return immediately.
	if len(uncached) == 0 {
		return s.collectLeafResults(sess, leaves, resolved, cfg)
	}

	// Phase 2: Reactive scheduling — submit ready vertices, re-check on completion.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	policy := s.opts.Policy
	if cpp, ok := policy.(*schedule.CriticalPathPolicy); ok {
		cpp.MaxDepth = g.MaxDepth()
	}
	sched := schedule.NewScheduler(s.opts.Workers, policy)
	sched.Start(ctx)
	defer sched.Stop()

	// Track how many uncached vertices remain.
	var remaining sync.WaitGroup
	remaining.Add(len(uncached))
	allDone := make(chan struct{})
	go func() {
		remaining.Wait()
		close(allDone)
	}()

	var solveErr error
	var solveErrOnce sync.Once

	// isReady returns true if all parents of v are resolved.
	isReady := func(v Vertex) bool {
		for _, inp := range v.Inputs() {
			ch := resolvedCh[inp.Vertex.Digest()]
			select {
			case <-ch:
			default:
				return false
			}
		}
		return true
	}

	// submitIfReady submits a vertex to the scheduler if all its parents
	// are resolved. Returns true if submitted.
	var submitIfReady func(v Vertex) bool
	submitted := make(map[digest.Digest]bool)
	var submitMu sync.Mutex

	submitIfReady = func(v Vertex) bool {
		dgst := v.Digest()
		submitMu.Lock()
		if submitted[dgst] {
			submitMu.Unlock()
			return false
		}
		if !isReady(v) {
			submitMu.Unlock()
			return false
		}
		submitted[dgst] = true
		submitMu.Unlock()

		depth := g.Depth(v)
		task := &schedule.Task{
			VertexDigest:  dgst,
			Name:          v.Name(),
			Depth:         depth,
			EstimatedCost: v.Options().EstimatedCost,
			Fn: func() error {
				err := s.executeVertex(ctx, v, resolved, resolvedCh, markResolved)
				if err != nil {
					solveErrOnce.Do(func() {
						solveErr = err
						cancel()
					})
				} else {
					// On success, check if any children are now ready.
					for _, child := range g.ChildrenOf(v) {
						childDgst := child.Digest()
						if _, isUncached := uncached[childDgst]; isUncached {
							submitIfReady(child)
						}
					}
				}
				remaining.Done()
				return err
			},
		}

		sched.Submit(task)
		return true
	}

	// Seed: submit all uncached vertices that are already ready (roots).
	for _, v := range uncached {
		submitIfReady(v)
	}

	// Wait for all uncached vertices to complete.
	select {
	case <-allDone:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	if solveErr != nil {
		return nil, solveErr
	}

	return s.collectLeafResults(sess, leaves, resolved, cfg)
}

// executeVertex resolves a single vertex: its parents are already resolved.
func (s *Solver) executeVertex(
	ctx context.Context,
	v Vertex,
	resolved *sync.Map,
	resolvedCh map[digest.Digest]chan struct{},
	markResolved func(digest.Digest, Result),
) error {
	dgst := v.Digest()

	s.bus.Publish(signal.Event{Type: signal.VertexStarted, Vertex: dgst, Name: v.Name()})

	// Gather parent results (all should already be resolved).
	inputs := v.Inputs()
	inputResults := make([]Result, len(inputs))
	for i, inp := range inputs {
		parentDgst := inp.Vertex.Digest()
		// Wait on parent signal (should be immediate since we only submit ready vertices).
		select {
		case <-ctx.Done():
			s.bus.Publish(signal.Event{Type: signal.VertexCanceled, Vertex: dgst, Name: v.Name()})
			return ctx.Err()
		case <-resolvedCh[parentDgst]:
		}
		val, ok := resolved.Load(parentDgst)
		if !ok {
			return errors.Errorf("dependency %s not resolved for %s", parentDgst, v.Name())
		}
		inputResults[i] = val.(Result)
	}

	// Resolve Op.
	if s.opts.ResolveOp == nil {
		return errors.New("no ResolveOpFunc configured")
	}
	op, err := s.opts.ResolveOp(v)
	if err != nil {
		s.bus.Publish(signal.Event{Type: signal.VertexFailed, Vertex: dgst, Name: v.Name(), Error: err})
		return errors.Wrap(err, "resolve op")
	}

	// Acquire resources.
	release, err := op.Acquire(ctx)
	if err != nil {
		s.bus.Publish(signal.Event{Type: signal.VertexFailed, Vertex: dgst, Name: v.Name(), Error: err})
		return errors.Wrap(err, "acquire")
	}
	defer release()

	// Execute.
	outputs, err := op.Exec(ctx, inputResults)
	if err != nil {
		s.bus.Publish(signal.Event{Type: signal.VertexFailed, Vertex: dgst, Name: v.Name(), Error: err})
		return errors.Wrap(err, "exec")
	}
	if len(outputs) == 0 {
		return errors.Errorf("op for %s returned no outputs", v.Name())
	}

	result := outputs[0]

	// Save to cache (non-fatal on error).
	if cacheErr := s.cache.Save(ctx, dgst2key(dgst), result.ID(), 0); cacheErr != nil {
		s.bus.Publish(signal.Event{
			Type: signal.VertexFailed, Vertex: dgst, Name: v.Name(),
			Error: errors.Wrap(cacheErr, "cache save"),
		})
	}

	markResolved(dgst, result)
	s.bus.Publish(signal.Event{
		Type: signal.VertexCompleted, Vertex: dgst,
		Name: v.Name(), ResultID: result.ID(),
	})
	return nil
}

// collectLeafResults gathers results for the requested leaves and applies transforms.
func (s *Solver) collectLeafResults(
	sess *Session, leaves []Edge, resolved *sync.Map, cfg *solveConfig,
) (*Session, error) {
	for _, leaf := range leaves {
		dgst := leaf.Vertex.Digest()
		val, ok := resolved.Load(dgst)
		if !ok {
			sess.mu.Lock()
			sess.errors[dgst] = errors.Errorf("leaf %s not resolved", leaf.Vertex.Name())
			sess.mu.Unlock()
			continue
		}

		result := val.(Result)

		if cfg.pipeline != nil && cfg.pipeline.Len() > 0 {
			transformed, err := cfg.pipeline.Process(context.Background(), result)
			if err != nil {
				sess.mu.Lock()
				sess.errors[dgst] = errors.Wrap(err, "transform")
				sess.mu.Unlock()
				continue
			}
			if tr, ok := transformed.(Result); ok {
				result = tr
			}
		}

		sess.mu.Lock()
		sess.results[dgst] = result
		sess.mu.Unlock()
	}
	return sess, nil
}

func dgst2key(dgst digest.Digest) cache.Key {
	return cache.Key{Digest: dgst, Output: 0}
}

// cachedResultRef is a lightweight Result reference for cache hits.
type cachedResultRef struct {
	id  string
	vtx digest.Digest
}

func (r *cachedResultRef) ID() string                      { return r.id }
func (r *cachedResultRef) Release(_ context.Context) error { return nil }
func (r *cachedResultRef) Sys() any                        { return nil }
func (r *cachedResultRef) Clone() Result                   { return &cachedResultRef{id: r.id, vtx: r.vtx} }
func (r *cachedResultRef) String() string                  { return fmt.Sprintf("cached:%s", r.id) }
func (r *cachedResultRef) CacheKeys() []ExportableCacheKey { return nil }

// SchedulerStats returns the scheduler's execution statistics.
func (s *Solver) SchedulerStats() (completed, failed int64) {
	return 0, 0 // Per-solve schedulers; stats are transient.
}

// vertexResult is a concrete Result for executed vertices.
type vertexResult struct {
	id    string
	sys   any
	clone func() Result
}

func (r *vertexResult) ID() string                      { return r.id }
func (r *vertexResult) Release(_ context.Context) error { return nil }
func (r *vertexResult) Sys() any                        { return r.sys }
func (r *vertexResult) Clone() Result {
	if r.clone != nil {
		return r.clone()
	}
	return &vertexResult{id: r.id, sys: r.sys}
}

// NewResult creates a simple result for testing and simple ops.
func NewResult(id string, sys any) Result {
	return &vertexResult{id: id, sys: sys}
}
