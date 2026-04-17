package solver

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/bons/bons-ci/pkg/solver/cache"
	"github.com/bons/bons-ci/pkg/solver/schedule"
	"github.com/bons/bons-ci/pkg/solver/signal"
	"github.com/bons/bons-ci/pkg/solver/stream"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
)

// ─── Solver options ───────────────────────────────────────────────────────────

// SolverOpts configures a Solver instance.
type SolverOpts struct {
	// ResolveOp converts a vertex into an executable Op. Required.
	ResolveOp ResolveOpFunc

	// Cache is the primary cache store. Defaults to in-memory if nil.
	Cache cache.Store

	// Workers controls scheduler concurrency. 0 → GOMAXPROCS.
	Workers int

	// Policy is the scheduling policy. Defaults to CriticalPathPolicy.
	Policy schedule.Policy
}

// ─── Solver ───────────────────────────────────────────────────────────────────

// Solver is the top-level coordinator. It integrates the graph builder, cache,
// scheduler, event bus, and streaming pipeline into a single Solve() call.
//
// A Solver instance is safe for concurrent Solve calls; each call creates its
// own independent session state.
type Solver struct {
	opts  SolverOpts
	cache cache.Store
	bus   *signal.Bus
}

// New creates a Solver. The returned solver is ready to use; call Close()
// when the solver's lifetime ends.
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

// Bus returns the event bus. Subscribe before calling Solve to receive all
// vertex lifecycle events.
func (s *Solver) Bus() *signal.Bus { return s.bus }

// Close releases resources associated with the Solver.
func (s *Solver) Close() { s.bus.Close() }

// ─── Solve options ────────────────────────────────────────────────────────────

// SolveOption configures a single Solve call.
type SolveOption func(*solveConfig)

type solveConfig struct {
	pipeline *stream.Pipeline
}

// WithTransform attaches a streaming transformation pipeline. The pipeline is
// applied to each leaf result after the solve completes, enabling
// export-while-solving patterns.
func WithTransform(p *stream.Pipeline) SolveOption {
	return func(cfg *solveConfig) { cfg.pipeline = p }
}

// ─── Session ─────────────────────────────────────────────────────────────────

// Session holds the results and graph from one Solve call.
type Session struct {
	mu      sync.Mutex
	results map[digest.Digest]Result
	errors  map[digest.Digest]error
	graph   *Graph
}

// Result returns the result for the given leaf edge, or an error.
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
	return nil, errors.Errorf("no result for vertex %s", e.Vertex.Name())
}

// Results returns all successfully resolved leaf results keyed by vertex digest.
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
func (sess *Session) Graph() *Graph { return sess.graph }

// ─── Solve ────────────────────────────────────────────────────────────────────

// Solve executes the solver for the given leaf edges and returns a Session
// with results for each leaf. It is the primary entry point.
//
// Algorithm (mirrors BuildKit's two-phase cache model):
//
//  1. Build the reachable graph from leaves upward.
//  2. For each vertex in topological order, call Op.CacheMap to obtain the
//     stable cache descriptor, then probe the cache store using the combined
//     key (CacheMap.Digest + input cache keys). Mark cache-hit vertices as
//     resolved immediately.
//  3. Identify "ready" uncached vertices (all parents resolved) and submit
//     them to the priority scheduler.
//  4. As each vertex completes, check whether its children are now ready and
//     submit them (reactive, event-driven scheduling).
//  5. Continue until all vertices are resolved or an error / cancellation
//     occurs.
//
// Error handling: if any vertex fails, the context is cancelled (stopping
// further work) and the REAL error — not ctx.Err() — is returned.
func (s *Solver) Solve(ctx context.Context, leaves []Edge, opts ...SolveOption) (*Session, error) {
	cfg := &solveConfig{}
	for _, opt := range opts {
		opt(cfg)
	}

	// ── Step 1: build graph ──────────────────────────────────────────────────
	g := BuildGraph(leaves)

	sess := &Session{
		results: make(map[digest.Digest]Result, len(leaves)),
		errors:  make(map[digest.Digest]error),
		graph:   g,
	}

	// ── Step 2: per-vertex completion signals ─────────────────────────────────
	// resolvedCh[dgst] is closed once that vertex has a result (cached or
	// executed). Children wait on this channel before submitting themselves.
	resolvedCh := make(map[digest.Digest]chan struct{}, g.Size())
	for _, v := range g.Vertices() {
		resolvedCh[v.Digest()] = make(chan struct{})
	}

	// resolved stores the final Result for each vertex.
	var resolved sync.Map // digest.Digest → Result

	// markResolved stores the result and closes the completion signal.
	// Idempotent: safe to call multiple times (subsequent calls are no-ops
	// because channels can only be closed once — guard with sync.Once per
	// vertex).
	closedOnce := make(map[digest.Digest]*sync.Once, g.Size())
	for _, v := range g.Vertices() {
		closedOnce[v.Digest()] = &sync.Once{}
	}
	markResolved := func(dgst digest.Digest, result Result) {
		resolved.Store(dgst, result)
		closedOnce[dgst].Do(func() {
			close(resolvedCh[dgst])
		})
	}

	// ── Step 3: cache probe phase ─────────────────────────────────────────────
	// Walk vertices in topological order (roots first) so that when we build
	// a child's cache key we can already access its parents' resolved keys.
	s.bus.Publish(signal.Event{
		Type: signal.VertexQueued,
		Name: "cache-probe-phase",
	})

	uncached := make(map[digest.Digest]Vertex, g.Size())

	for _, v := range g.TopologicalOrder() {
		dgst := v.Digest()

		if v.Options().IgnoreCache {
			uncached[dgst] = v
			s.bus.Publish(signal.Event{Type: signal.CacheMiss, Vertex: dgst, Name: v.Name()})
			continue
		}

		// Compute the stable cache key for this vertex. For root vertices
		// (no inputs) the key is just rootKeyDigest(CacheMap.Digest, output).
		// For non-root vertices it incorporates input cache keys, matching
		// BuildKit's CacheMap + dependency-key combination.
		cacheKey := s.computeCacheKey(ctx, v, &resolved)
		if cacheKey == (cache.Key{}) {
			// CacheMap computation failed or timed out — treat as cache miss.
			uncached[dgst] = v
			s.bus.Publish(signal.Event{Type: signal.CacheMiss, Vertex: dgst, Name: v.Name()})
			continue
		}

		resultID, found, err := s.cache.Probe(ctx, cacheKey)
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

		uncached[dgst] = v
		s.bus.Publish(signal.Event{Type: signal.CacheMiss, Vertex: dgst, Name: v.Name()})
	}

	// Fast path: everything was cached.
	if len(uncached) == 0 {
		return s.collectLeafResults(sess, leaves, &resolved, cfg)
	}

	// ── Step 4: reactive scheduling ───────────────────────────────────────────
	// We use a cancellable sub-context so that a single vertex failure stops
	// all remaining work. We track the real error separately so we can return
	// it instead of ctx.Err() (a critical distinction for callers).
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Update the MaxDepth on the CriticalPathPolicy now that the graph is built.
	if cpp, ok := s.opts.Policy.(*schedule.CriticalPathPolicy); ok {
		cpp.MaxDepth = g.MaxDepth()
	}
	sched := schedule.NewScheduler(s.opts.Workers, s.opts.Policy)
	sched.Start(ctx)
	defer sched.Stop()

	// solveErr is the first non-context error from any vertex. Protected by
	// solveErrOnce + the cancel() mechanism.
	var (
		solveErr     error
		solveErrOnce sync.Once
	)
	failSolve := func(err error) {
		solveErrOnce.Do(func() {
			solveErr = err
			cancel() // stop all remaining work
		})
	}

	// ── Vertex submission logic ───────────────────────────────────────────────
	// pending tracks vertices that have been submitted or are ready to be
	// submitted (to prevent double-submission races).
	var submitMu sync.Mutex
	submitted := make(map[digest.Digest]bool, len(uncached))

	// allParentsResolved returns true if every input of v is already resolved.
	allParentsResolved := func(v Vertex) bool {
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

	// remaining tracks the number of uncached vertices yet to complete.
	// Using atomic int64 instead of WaitGroup so we can handle partial failure:
	// if a parent fails, its children are never submitted and we don't need to
	// call Done() for them — we just check for ctx.Done().
	var remaining atomic.Int64
	remaining.Store(int64(len(uncached)))
	allDone := make(chan struct{})
	go func() {
		// Poll: we can't use WaitGroup because we don't pre-add children.
		// Instead we watch remaining and ctx together.
		for {
			if remaining.Load() == 0 {
				close(allDone)
				return
			}
			select {
			case <-ctx.Done():
				close(allDone)
				return
			default:
			}
			// Tiny yield; in practice this loop only runs a handful of
			// iterations (once per vertex completion event).
			// A cond-var would be more elegant; this keeps the code simple.
		}
	}()

	// submitVertex enqueues v for execution if not already submitted.
	// Called from the seed pass and from post-completion child checks.
	// This function is NOT recursive — children are submitted from within
	// the task Fn after the parent completes.
	var submitVertex func(v Vertex)
	submitVertex = func(v Vertex) {
		dgst := v.Digest()
		submitMu.Lock()
		if submitted[dgst] {
			submitMu.Unlock()
			return
		}
		if !allParentsResolved(v) {
			submitMu.Unlock()
			return
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
				err := s.executeVertex(ctx, v, &resolved, resolvedCh, markResolved)
				if err != nil {
					failSolve(err)
					remaining.Add(-1)
					return err
				}

				// On success, unblock any children that are now fully ready.
				for _, child := range g.ChildrenOf(v) {
					childDgst := child.Digest()
					if _, isUncached := uncached[childDgst]; isUncached {
						submitVertex(child)
					}
				}

				remaining.Add(-1)
				return nil
			},
		}
		s.bus.Publish(signal.Event{
			Type:   signal.VertexQueued,
			Vertex: dgst,
			Name:   v.Name(),
		})
		sched.Submit(task)
	}

	// ── Seed: submit all uncached vertices whose parents are already resolved ─
	// This covers all root-level uncached vertices and any whose parents were
	// cache hits.
	for _, v := range uncached {
		submitVertex(v)
	}

	// ── Wait for completion or cancellation ───────────────────────────────────
	<-allDone

	// If ctx was cancelled by failSolve, return the real error.
	if solveErr != nil {
		return nil, solveErr
	}
	// If ctx was cancelled externally (timeout, client cancel), return that.
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	return s.collectLeafResults(sess, leaves, &resolved, cfg)
}

// ─── CacheMap-based cache key computation ────────────────────────────────────

// computeCacheKey calls Op.CacheMap to get the stable operation digest, then
// combines it with the resolved input cache keys to build a Store key.
//
// This mirrors BuildKit's two-phase cache key computation:
//  1. Fast path: CacheMap.Digest alone (definition-based, no content scan).
//  2. Slow path: content-based digest from ComputeDigestFunc (not implemented
//     here — it requires the input to be fully resolved first, which happens
//     inside executeVertex if needed).
//
// Returns the zero Key{} if CacheMap cannot be computed.
func (s *Solver) computeCacheKey(ctx context.Context, v Vertex, resolved *sync.Map) cache.Key {
	if s.opts.ResolveOp == nil {
		// Fallback to vertex digest when no op resolver is set (tests).
		return cache.Key{Digest: v.Digest(), Output: 0}
	}

	op, err := s.opts.ResolveOp(v)
	if err != nil {
		return cache.Key{}
	}

	cm, _, err := op.CacheMap(ctx, 0)
	if err != nil || cm == nil {
		// CacheMap failed — use vertex digest as a best-effort key.
		return cache.Key{Digest: v.Digest(), Output: 0}
	}

	// For root vertices: key = cm.Digest.
	if len(v.Inputs()) == 0 {
		return cache.Key{Digest: rootKeyDigest(cm.Digest, 0), Output: 0}
	}

	// For dependent vertices: combine op digest with input result IDs.
	// This makes the cache key stable: if inputs produce the same results
	// (same content), the combined key is the same regardless of session.
	h := digest.Canonical.Hash()
	fmt.Fprintf(h, "op:%s", cm.Digest)
	for i, inp := range v.Inputs() {
		parentDgst := inp.Vertex.Digest()
		var parentResultID string
		if val, ok := resolved.Load(parentDgst); ok {
			parentResultID = val.(Result).ID()
		} else {
			// Parent not yet resolved — can't compute stable key.
			// Fall back to vertex digest (cache miss is safe; re-probe later).
			return cache.Key{Digest: v.Digest(), Output: 0}
		}
		sel := ""
		if i < len(cm.Deps) {
			sel = cm.Deps[i].Selector.String()
		}
		fmt.Fprintf(h, "|dep[%d]:%s+sel:%s", i, parentResultID, sel)
	}
	combined := digest.NewDigest(digest.Canonical, h)
	return cache.Key{Digest: combined, Output: int(0)}
}

// ─── Vertex execution ────────────────────────────────────────────────────────

// executeVertex resolves a single vertex whose parents are already resolved.
// It emits lifecycle events, gathers parent results, resolves the Op, acquires
// resources, executes, and saves the result to cache.
func (s *Solver) executeVertex(
	ctx context.Context,
	v Vertex,
	resolved *sync.Map,
	resolvedCh map[digest.Digest]chan struct{},
	markResolved func(digest.Digest, Result),
) error {
	dgst := v.Digest()
	s.bus.Publish(signal.Event{Type: signal.VertexStarted, Vertex: dgst, Name: v.Name()})

	// ── Gather parent results ──────────────────────────────────────────────────
	// All parents should already be resolved (we only submit when ready), but
	// we wait on the channel defensively to handle any TOCTOU window.
	inputs := v.Inputs()
	inputResults := make([]Result, len(inputs))
	for i, inp := range inputs {
		parentDgst := inp.Vertex.Digest()
		select {
		case <-ctx.Done():
			s.bus.Publish(signal.Event{
				Type: signal.VertexCanceled, Vertex: dgst, Name: v.Name(),
			})
			return ctx.Err()
		case <-resolvedCh[parentDgst]:
		}
		val, ok := resolved.Load(parentDgst)
		if !ok {
			err := errors.Errorf("internal: dependency %s not in resolved map for %s",
				inp.Vertex.Name(), v.Name())
			s.bus.Publish(signal.Event{
				Type: signal.VertexFailed, Vertex: dgst, Name: v.Name(), Error: err,
			})
			return err
		}
		inputResults[i] = val.(Result)
	}

	// ── Resolve Op ────────────────────────────────────────────────────────────
	if s.opts.ResolveOp == nil {
		return errors.New("solver: ResolveOpFunc is nil")
	}
	op, err := s.opts.ResolveOp(v)
	if err != nil {
		err = errors.Wrap(err, "resolve op")
		s.bus.Publish(signal.Event{
			Type: signal.VertexFailed, Vertex: dgst, Name: v.Name(), Error: err,
		})
		return err
	}

	// ── Acquire resources ─────────────────────────────────────────────────────
	release, err := op.Acquire(ctx)
	if err != nil {
		err = errors.Wrap(err, "acquire op resources")
		s.bus.Publish(signal.Event{
			Type: signal.VertexFailed, Vertex: dgst, Name: v.Name(), Error: err,
		})
		return err
	}
	defer release()

	// ── Execute ───────────────────────────────────────────────────────────────
	outputs, err := op.Exec(ctx, inputResults)
	if err != nil {
		s.bus.Publish(signal.Event{
			Type: signal.VertexFailed, Vertex: dgst, Name: v.Name(), Error: err,
		})
		return errors.Wrap(err, "exec op")
	}
	if len(outputs) == 0 {
		err = errors.Errorf("op for vertex %s returned no outputs", v.Name())
		s.bus.Publish(signal.Event{
			Type: signal.VertexFailed, Vertex: dgst, Name: v.Name(), Error: err,
		})
		return err
	}

	result := outputs[0]

	// ── Save to cache ─────────────────────────────────────────────────────────
	// Recompute the stable cache key now that all inputs are resolved.
	cacheKey := s.computeCacheKey(ctx, v, resolved)
	if cacheKey != (cache.Key{}) {
		if cacheErr := s.cache.Save(ctx, cacheKey, result.ID(), 0); cacheErr != nil {
			// Cache save failure is non-fatal: the vertex result is still usable.
			s.bus.Publish(signal.Event{
				Type:  signal.VertexFailed,
				Vertex: dgst, Name: v.Name(),
				Error: errors.Wrap(cacheErr, "cache save (non-fatal)"),
			})
		}
	}

	markResolved(dgst, result)
	s.bus.Publish(signal.Event{
		Type:     signal.VertexCompleted,
		Vertex:   dgst,
		Name:     v.Name(),
		ResultID: result.ID(),
	})
	return nil
}

// ─── Result collection ────────────────────────────────────────────────────────

// collectLeafResults gathers final results for all requested leaves,
// applying any configured transformation pipeline.
func (s *Solver) collectLeafResults(
	sess *Session,
	leaves []Edge,
	resolved *sync.Map,
	cfg *solveConfig,
) (*Session, error) {
	for _, leaf := range leaves {
		dgst := leaf.Vertex.Digest()
		val, ok := resolved.Load(dgst)
		if !ok {
			sess.mu.Lock()
			sess.errors[dgst] = errors.Errorf(
				"leaf vertex %s was not resolved", leaf.Vertex.Name())
			sess.mu.Unlock()
			continue
		}

		result := val.(Result)

		// Apply transformation pipeline if configured.
		if cfg.pipeline != nil && cfg.pipeline.Len() > 0 {
			transformed, err := cfg.pipeline.Process(context.Background(), result)
			if err != nil {
				sess.mu.Lock()
				sess.errors[dgst] = errors.Wrap(err, "transform pipeline")
				sess.mu.Unlock()
				continue
			}
			if r, ok := transformed.(Result); ok {
				result = r
			}
		}

		sess.mu.Lock()
		sess.results[dgst] = result
		sess.mu.Unlock()
	}
	return sess, nil
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

// dgst2key is a convenience for the simple vertex-digest → cache.Key mapping
// used in places where CacheMap integration is not required (e.g. tests).
func dgst2key(dgst digest.Digest) cache.Key {
	return cache.Key{Digest: dgst, Output: 0}
}
