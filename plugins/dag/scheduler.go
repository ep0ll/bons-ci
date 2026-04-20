package reactdag

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// ---------------------------------------------------------------------------
// Scheduler — the reactive build engine
// ---------------------------------------------------------------------------

// Scheduler drives the DAG from StateInitial toward completion using a
// concurrent worker pool and a reactive coordinator. Core invariant:
//
//   A vertex may begin execution only when ALL of its parents are terminal.
//
// Optimisations implemented:
//  1. Fine-grained file invalidation — only vertices consuming changed files reset.
//  2. Two-tier cache (fast+slow)    — results looked up before executing.
//  3. Cached-error replay           — failed results replayed without recompute.
//  4. Target short-circuit          — terminal target propagates instantly.
//  5. Concurrent workers            — bounded pool, reactive dispatch.
//  6. Failure propagation           — downstream vertices failed immediately.
//  7. Per-vertex execution gate     — prevents duplicate concurrent execution
//     when multiple group-member builds share the same Scheduler instance.
type Scheduler struct {
	dag         *DAG
	invalidator *InvalidationEngine
	cfg         schedulerConfig
	// vertexGate guards against concurrent execution of the same vertex from
	// independent concurrent Build calls (e.g. GroupScheduler members).
	vertexGate sync.Map // key: vertexID (string) → *vertexOnce
}

// vertexOnce ensures a vertex is executed at most once per build epoch.
// Each Reset() call on the Vertex increments its generation, changing the
// gate key and automatically invalidating the previous gate.
type vertexOnce struct {
	once       sync.Once
	done       chan struct{}
	err        error
	cacheState State
}

// gateKey is the composite key for the vertex gate map.
type gateKey struct {
	id         string
	generation uint64
}

// gateFor retrieves or creates a vertexOnce gate for the current epoch of v.
// Because the key includes v.Generation(), a Reset() call on v automatically
// routes subsequent builds to a fresh gate — no explicit invalidation needed.
func (s *Scheduler) gateFor(v *Vertex) *vertexOnce {
	key := gateKey{id: v.ID(), generation: v.Generation()}
	if existing, ok := s.vertexGate.Load(key); ok {
		return existing.(*vertexOnce)
	}
	fresh := &vertexOnce{done: make(chan struct{})}
	actual, _ := s.vertexGate.LoadOrStore(key, fresh)
	return actual.(*vertexOnce)
}

// invalidateGate removes the gate for a vertex's current epoch.
// After calling this, the next gateFor call for that vertex creates a new gate.
// NOTE: with generation-keyed gates this is only needed for explicit external resets
// that bypass v.Reset() — call it from ForceState if needed.
func (s *Scheduler) invalidateGate(v *Vertex) {
	s.vertexGate.Delete(gateKey{id: v.ID(), generation: v.Generation()})
}

// NewScheduler constructs a Scheduler bound to the given DAG.
// Call dag.Seal() before the first Build.
func NewScheduler(dag *DAG, opts ...Option) *Scheduler {
	cfg := defaultConfig()
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.executor == nil {
		cfg.executor = newDefaultExecutor(nil)
	}
	return &Scheduler{
		dag:         dag,
		cfg:         cfg,
		invalidator: NewInvalidationEngine(dag, cfg.eventBus),
	}
}

// ---------------------------------------------------------------------------
// Build entry-point
// ---------------------------------------------------------------------------

// Build executes the DAG up to targetID.
// changedFiles contains files modified since the last build; nil means clean.
// Returns aggregate BuildMetrics, or an error if the target fails.
func (s *Scheduler) Build(ctx context.Context, targetID string, changedFiles []FileRef) (*BuildMetrics, error) {
	buildStart := time.Now()

	target, err := s.resolveTarget(targetID)
	if err != nil {
		return nil, err
	}

	s.cfg.eventBus.PublishBuildStart(ctx, targetID)
	s.cfg.hooks.Execute(ctx, HookOnBuildStart, target, HookPayload{}) //nolint:errcheck

	if err := s.applyInvalidations(ctx, changedFiles); err != nil {
		return nil, err
	}

	if target.State().IsTerminal() {
		return s.shortCircuit(ctx, target, buildStart)
	}

	sorted, err := s.dag.TopologicalSortFrom(targetID)
	if err != nil {
		return nil, fmt.Errorf("scheduler: topo sort: %w", err)
	}

	metrics, buildErr := s.runLoop(ctx, sorted)
	metrics.TotalDuration = time.Since(buildStart)

	if path, pathErr := s.dag.CriticalPath(targetID); pathErr == nil {
		metrics.CriticalPath = path
	}

	s.cfg.eventBus.PublishBuildEnd(ctx, targetID, metrics)
	s.cfg.hooks.Execute(ctx, HookOnBuildEnd, target, HookPayload{}) //nolint:errcheck

	return metrics, buildErr
}

// ---------------------------------------------------------------------------
// Invalidation
// ---------------------------------------------------------------------------

func (s *Scheduler) applyInvalidations(ctx context.Context, changedFiles []FileRef) error {
	if len(changedFiles) == 0 {
		return nil
	}
	if _, err := s.invalidator.Invalidate(ctx, changedFiles); err != nil {
		return fmt.Errorf("scheduler: invalidation: %w", err)
	}
	// Gates are keyed by (vertexID, generation). Since Invalidate calls v.Reset()
	// which increments each vertex's generation, stale gates are automatically
	// abandoned — no explicit cleanup required.
	return nil
}

// ---------------------------------------------------------------------------
// Short-circuit
// ---------------------------------------------------------------------------

// shortCircuit skips all execution because the target is already terminal.
func (s *Scheduler) shortCircuit(ctx context.Context, target *Vertex, buildStart time.Time) (*BuildMetrics, error) {
	m := &BuildMetrics{PerVertex: make(map[string]VertexMetrics)}

	ancestors, _ := s.dag.Ancestors(target.ID())
	for _, anc := range ancestors {
		s.markAncestorCompleted(ctx, anc)
		m.Skipped++
		m.TotalVertices++
		m.PerVertex[anc.ID()] = anc.Metrics()
	}
	m.TotalVertices++
	m.PerVertex[target.ID()] = target.Metrics()

	if target.State() == StateFailed {
		m.Failed++
		m.CachedErrors++
	} else {
		m.Skipped++
	}

	m.TotalDuration = time.Since(buildStart)
	return m, target.Err()
}

func (s *Scheduler) markAncestorCompleted(ctx context.Context, v *Vertex) {
	if v.State().IsTerminal() {
		return
	}
	prev := v.State()
	if err := v.SetState(StateCompleted, "ancestor of cached target"); err != nil {
		return
	}
	s.cfg.eventBus.PublishStateChanged(ctx, v, prev, StateCompleted)
}

// ---------------------------------------------------------------------------
// vertexResult — per-vertex outcome from worker to coordinator
// ---------------------------------------------------------------------------

type vertexResult struct {
	vertex     *Vertex
	err        error
	cacheState State // state at cache lookup time; StateInitial means cache miss
}

// ---------------------------------------------------------------------------
// Reactive build loop
// ---------------------------------------------------------------------------

// runLoop starts workers, seeds roots, and reacts to completions.
func (s *Scheduler) runLoop(ctx context.Context, sorted []*Vertex) (*BuildMetrics, error) {
	metrics := &BuildMetrics{
		TotalVertices: len(sorted),
		PerVertex:     make(map[string]VertexMetrics, len(sorted)),
	}

	jobs := make(chan *Vertex, len(sorted))
	results := make(chan vertexResult, len(sorted))

	workerCtx, cancelWorkers := context.WithCancel(ctx)
	defer cancelWorkers()

	wg, workerCtx := newWorkgroup(workerCtx)
	for range s.cfg.workerCount {
		wg.Go(func() error {
			return s.workerLoop(workerCtx, jobs, results)
		})
	}

	coordErr := s.coordinate(ctx, sorted, jobs, results, metrics)

	close(jobs)
	wg.Wait() //nolint:errcheck — workers return nil

	return metrics, coordErr
}

// workerLoop processes jobs until the channel closes or context is cancelled.
func (s *Scheduler) workerLoop(ctx context.Context, jobs <-chan *Vertex, results chan<- vertexResult) error {
	for {
		select {
		case v, ok := <-jobs:
			if !ok {
				return nil
			}
			cacheState, err := s.processVertex(ctx, v)
			results <- vertexResult{vertex: v, err: err, cacheState: cacheState}
		case <-ctx.Done():
			return nil
		}
	}
}

// coordinate is the reactive event loop.
//
// It tracks two separate counts to avoid the drainOnError deadlock:
//   - inFlight: vertices currently executing inside workers (will produce results).
//   - pending:  vertices not yet dispatched (will never produce results on their own).
//
// On failure, pending descendants are synchronously failed and removed from the
// pending set; only inFlight results need to be drained.
func (s *Scheduler) coordinate(
	ctx context.Context,
	sorted []*Vertex,
	jobs chan<- *Vertex,
	results <-chan vertexResult,
	metrics *BuildMetrics,
) error {
	pending := buildPendingSet(sorted)
	var inFlight atomic.Int64

	enqueue := func(v *Vertex) {
		v.RecordQueued()
		inFlight.Add(1)
		jobs <- v
		delete(pending, v.ID())
	}

	// Seed: vertices whose parents (within this sub-graph) are already terminal.
	for _, v := range sorted {
		if s.allParentsTerminal(v) {
			enqueue(v)
		}
	}

	for inFlight.Load() > 0 || len(pending) > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case res := <-results:
			inFlight.Add(-1)
			s.recordResult(res, metrics)

			if res.err != nil {
				// Synchronously fail all pending descendants; they won't produce results.
				s.failPendingDescendants(ctx, res.vertex, pending, metrics)
				// Drain only the in-flight results (those already sent to workers).
				return s.drainInFlight(ctx, inFlight.Load(), results, metrics, res)
			}

			// React: newly unblocked children.
			for _, child := range res.vertex.Children() {
				if _, stillPending := pending[child.ID()]; !stillPending {
					continue
				}
				if s.allParentsTerminal(child) {
					enqueue(child)
				}
			}
		}
	}
	return nil
}

// drainInFlight waits for exactly inFlight more results to arrive, then returns.
// Pending descendants have already been failed synchronously and removed.
func (s *Scheduler) drainInFlight(
	ctx context.Context,
	inFlight int64,
	results <-chan vertexResult,
	metrics *BuildMetrics,
	failedRes vertexResult,
) error {
	for i := int64(0); i < inFlight; i++ {
		select {
		case res := <-results:
			s.recordResult(res, metrics)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return fmt.Errorf("vertex %q failed: %w", failedRes.vertex.ID(), failedRes.err)
}

// failPendingDescendants synchronously marks all pending descendants of failed
// as StateFailed. They were never dispatched, so they produce no channel results.
func (s *Scheduler) failPendingDescendants(
	ctx context.Context,
	failed *Vertex,
	pending map[string]struct{},
	metrics *BuildMetrics,
) {
	descs, _ := s.dag.Descendants(failed.ID())
	cause := fmt.Errorf("parent vertex %q failed", failed.ID())
	for _, desc := range descs {
		if _, isPending := pending[desc.ID()]; !isPending {
			continue
		}
		_ = desc.SetFailed(cause, "parent failed")
		s.cfg.eventBus.PublishStateChanged(ctx, desc, StateInitial, StateFailed)
		metrics.Failed++
		metrics.PerVertex[desc.ID()] = desc.Metrics()
		delete(pending, desc.ID())
	}
}

// ---------------------------------------------------------------------------
// Per-vertex processing pipeline
// ---------------------------------------------------------------------------

// processVertex runs the full resolution pipeline, guarded by a per-vertex
// once-gate so that when multiple concurrent Build calls share this Scheduler
// (e.g. via GroupScheduler), a vertex is executed at most once.
//
// The gate works like a coalescing barrier:
//   - The first goroutine to arrive claims the gate via sync.Once and runs
//     the full pipeline, storing the result.
//   - Every subsequent goroutine blocks on the gate's done channel, then
//     returns the stored result without re-executing anything.
func (s *Scheduler) processVertex(ctx context.Context, v *Vertex) (cacheState State, execErr error) {
	gate := s.gateFor(v)

	gate.once.Do(func() {
		gate.cacheState, gate.err = s.processVertexOnce(ctx, v)
		close(gate.done)
	})

	// Wait for whoever won the gate to finish (including ourselves).
	select {
	case <-gate.done:
	case <-ctx.Done():
		return StateInitial, ctx.Err()
	}

	return gate.cacheState, gate.err
}

// processVertexOnce is the actual (un-gated) resolution pipeline.
func (s *Scheduler) processVertexOnce(ctx context.Context, v *Vertex) (cacheState State, err error) {
	// Fast path: already terminal from a previous build epoch.
	if v.State().IsTerminal() {
		return v.State(), v.Err()
	}
	v.RecordStart()
	defer v.RecordFinish()

	if err := s.cfg.hooks.ExecuteAbortOnError(ctx, HookBeforeExecute, v, HookPayload{}); err != nil {
		return StateInitial, s.failVertex(ctx, v, err, "before-execute hook aborted")
	}

	inputFiles := s.resolveInputFiles(v)
	v.SetInputFiles(inputFiles)

	key, err := s.cfg.keyComputer.Compute(v, inputFiles)
	if err != nil {
		return StateInitial, s.failVertex(ctx, v, fmt.Errorf("cache key: %w", err), "key computation")
	}
	v.SetCacheKey(key)

	if hit, tier, err := s.tryFastCache(ctx, v, key); hit || err != nil {
		return tier, err
	}

	if hit, tier, err := s.trySlowCache(ctx, v, key); hit || err != nil {
		return tier, err
	}

	if err := s.executeOp(ctx, v, key); err != nil {
		return StateInitial, err
	}

	s.cfg.hooks.Execute(ctx, HookAfterExecute, v, HookPayload{}) //nolint:errcheck
	return StateInitial, nil
}

// ---------------------------------------------------------------------------
// Cache resolution
// ---------------------------------------------------------------------------

// tryFastCache queries the fast tier.
// Returns (hit, tier, err): tier is StateFastCache on a hit.
func (s *Scheduler) tryFastCache(ctx context.Context, v *Vertex, key CacheKey) (bool, State, error) {
	if err := s.cfg.hooks.ExecuteAbortOnError(ctx, HookBeforeCacheLookup, v, HookPayload{"tier": "fast"}); err != nil {
		return false, StateInitial, err
	}
	entry, err := s.cfg.fastCache.Get(ctx, key)
	if err != nil {
		return false, StateInitial, fmt.Errorf("fast cache get: %w", err)
	}
	if entry == nil {
		s.cfg.eventBus.PublishCacheMiss(ctx, v)
		return false, StateInitial, nil
	}
	s.cfg.eventBus.PublishCacheHit(ctx, v, "fast")
	err = s.applyCacheEntry(ctx, v, entry, StateFastCache)
	return true, StateFastCache, err
}

// trySlowCache queries the slow tier and back-fills the fast tier on hit.
func (s *Scheduler) trySlowCache(ctx context.Context, v *Vertex, key CacheKey) (bool, State, error) {
	if err := s.cfg.hooks.ExecuteAbortOnError(ctx, HookBeforeCacheLookup, v, HookPayload{"tier": "slow"}); err != nil {
		return false, StateInitial, err
	}
	entry, err := s.cfg.slowCache.Get(ctx, key)
	if err != nil {
		return false, StateInitial, fmt.Errorf("slow cache get: %w", err)
	}
	if entry == nil {
		return false, StateInitial, nil
	}
	_ = s.cfg.fastCache.Set(ctx, key, entry)
	s.cfg.eventBus.PublishCacheHit(ctx, v, "slow")
	err = s.applyCacheEntry(ctx, v, entry, StateSlowCache)
	return true, StateSlowCache, err
}

// applyCacheEntry applies a cache result to the vertex.
//
// CACHED ERROR REPLAY OPTIMISATION:
//   If the entry records a past failure with the same cache key (same inputs),
//   the error is returned immediately — no upstream recomputation whatsoever.
//   Vertex B and all its ancestry is completely skipped.
func (s *Scheduler) applyCacheEntry(ctx context.Context, v *Vertex, entry *CacheEntry, tier State) error {
	prev := v.State()
	if err := v.SetState(tier, "cache hit"); err != nil {
		return err
	}
	s.cfg.hooks.Execute(ctx, HookAfterCacheLookup, v, HookPayload{"tier": tier.String()}) //nolint:errcheck

	if entry.IsFailed() {
		cachedErr := entry.CachedError()
		_ = v.SetFailed(cachedErr, "cached error replayed")
		s.cfg.eventBus.PublishStateChanged(ctx, v, prev, StateFailed)
		return cachedErr
	}

	v.SetOutputFiles(entry.OutputFiles)
	if err := v.SetState(StateCompleted, "cache applied"); err != nil {
		return err
	}
	s.cfg.eventBus.PublishStateChanged(ctx, v, prev, StateCompleted)
	return nil
}

// ---------------------------------------------------------------------------
// Execution
// ---------------------------------------------------------------------------

// executeOp runs the Operation and persists the result in both cache tiers.
func (s *Scheduler) executeOp(ctx context.Context, v *Vertex, key CacheKey) error {
	s.cfg.eventBus.PublishExecutionStart(ctx, v)

	execErr := s.cfg.executor.Execute(ctx, v)

	entry := s.buildCacheEntry(key, v, execErr)
	s.persistToCaches(ctx, key, entry)

	if execErr != nil {
		return s.failVertex(ctx, v, execErr, "operation execution")
	}

	prev := v.State()
	if err := v.SetState(StateCompleted, "execution succeeded"); err != nil {
		return err
	}
	s.cfg.eventBus.PublishStateChanged(ctx, v, prev, StateCompleted)
	s.cfg.eventBus.PublishExecutionEnd(ctx, v)
	return nil
}

func (s *Scheduler) buildCacheEntry(key CacheKey, v *Vertex, execErr error) *CacheEntry {
	entry := &CacheEntry{
		Key:         key,
		OutputFiles: v.OutputFiles(),
		CachedAt:    time.Now(),
		DurationMS:  v.Metrics().Duration().Milliseconds(),
	}
	if execErr != nil {
		entry.CachedErr = execErr.Error()
		entry.OutputFiles = nil
	}
	return entry
}

func (s *Scheduler) persistToCaches(ctx context.Context, key CacheKey, entry *CacheEntry) {
	_ = s.cfg.fastCache.Set(ctx, key, entry)
	_ = s.cfg.slowCache.Set(ctx, key, entry)
}

// ---------------------------------------------------------------------------
// State helpers
// ---------------------------------------------------------------------------

func (s *Scheduler) failVertex(ctx context.Context, v *Vertex, err error, cause string) error {
	prev := v.State()
	_ = v.SetFailed(err, cause)
	s.cfg.eventBus.PublishExecutionEnd(ctx, v)
	s.cfg.eventBus.PublishStateChanged(ctx, v, prev, StateFailed)
	return err
}

// ---------------------------------------------------------------------------
// Input file resolution
// ---------------------------------------------------------------------------

// resolveInputFiles collects the files this vertex reads from each parent,
// honouring fine-grained FileDependency declarations.
func (s *Scheduler) resolveInputFiles(v *Vertex) []FileRef {
	var inputs []FileRef
	for _, parent := range v.Parents() {
		inputs = append(inputs, s.filesFromParent(v, parent)...)
	}
	return inputs
}

func (s *Scheduler) filesFromParent(v, parent *Vertex) []FileRef {
	declared, hasDep := v.FileDependencyForParent(parent.ID())
	if !hasDep {
		return parent.OutputFiles()
	}
	return filterFilesByPath(parent.OutputFiles(), declared)
}

func filterFilesByPath(files []FileRef, allowedPaths []string) []FileRef {
	allow := make(map[string]bool, len(allowedPaths))
	for _, p := range allowedPaths {
		allow[p] = true
	}
	out := make([]FileRef, 0, len(allowedPaths))
	for _, f := range files {
		if allow[f.Path] {
			out = append(out, f)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Readiness
// ---------------------------------------------------------------------------

func (s *Scheduler) allParentsTerminal(v *Vertex) bool {
	for _, p := range v.Parents() {
		if !p.State().IsTerminal() {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Metrics
// ---------------------------------------------------------------------------

// recordResult updates aggregate metrics from a single completed vertex.
// cacheState carries the cache tier recorded at lookup time, because by
// result delivery the vertex state has already advanced to completed/failed.
func (s *Scheduler) recordResult(res vertexResult, m *BuildMetrics) {
	v := res.vertex
	m.PerVertex[v.ID()] = v.Metrics()

	switch res.cacheState {
	case StateFastCache:
		m.FastCacheHits++
	case StateSlowCache:
		m.SlowCacheHits++
	default:
		if v.State() == StateCompleted {
			m.Executed++
		} else if v.State() == StateFailed {
			m.Failed++
		}
	}
	if v.State() == StateFailed && res.cacheState.IsCached() {
		m.CachedErrors++
	}
}

// ---------------------------------------------------------------------------
// Utility
// ---------------------------------------------------------------------------

func (s *Scheduler) resolveTarget(id string) (*Vertex, error) {
	v, ok := s.dag.Vertex(id)
	if !ok {
		return nil, fmt.Errorf("scheduler: target vertex %q not found", id)
	}
	return v, nil
}

func buildPendingSet(vs []*Vertex) map[string]struct{} {
	m := make(map[string]struct{}, len(vs))
	for _, v := range vs {
		m[v.ID()] = struct{}{}
	}
	return m
}

// ---------------------------------------------------------------------------
// Exposed accessors
// ---------------------------------------------------------------------------

// EventBus returns the scheduler's event bus for external subscriptions.
func (s *Scheduler) EventBus() *EventBus { return s.cfg.eventBus }

// Hooks returns the hook registry for external registrations.
func (s *Scheduler) Hooks() *HookRegistry { return s.cfg.hooks }

// ---------------------------------------------------------------------------
// Full-control state manipulation API
// ---------------------------------------------------------------------------

// ForceState overrides any vertex's state. Intended for orchestration/testing.
func (s *Scheduler) ForceState(ctx context.Context, vertexID string, state State, cause string) error {
	v, ok := s.dag.Vertex(vertexID)
	if !ok {
		return fmt.Errorf("scheduler: vertex %q not found", vertexID)
	}
	prev := v.State()
	if err := v.SetState(state, cause); err != nil {
		return err
	}
	s.cfg.eventBus.PublishStateChanged(ctx, v, prev, state)
	return nil
}

// MarkTargetAndAncestorsCompleted sets the target and all its ancestors to
// StateCompleted without executing anything.
func (s *Scheduler) MarkTargetAndAncestorsCompleted(ctx context.Context, targetID string) error {
	target, err := s.resolveTarget(targetID)
	if err != nil {
		return err
	}
	ancestors, err := s.dag.Ancestors(targetID)
	if err != nil {
		return fmt.Errorf("scheduler: ancestors of %q: %w", targetID, err)
	}
	for _, anc := range ancestors {
		s.markAncestorCompleted(ctx, anc)
	}
	s.markAncestorCompleted(ctx, target)
	return nil
}

// ResetSubtree resets the target and all its descendants to StateInitial.
// Each call to v.Reset() increments v.Generation(), automatically invalidating
// the scheduler's gate for that vertex without any explicit cleanup.
func (s *Scheduler) ResetSubtree(_ context.Context, targetID string) error {
	target, err := s.resolveTarget(targetID)
	if err != nil {
		return err
	}
	target.Reset()
	descs, err := s.dag.Descendants(targetID)
	if err != nil {
		return fmt.Errorf("scheduler: descendants: %w", err)
	}
	for _, d := range descs {
		d.Reset()
	}
	return nil
}

// Snapshot returns a map of vertex ID → current State.
func (s *Scheduler) Snapshot() map[string]State {
	all := s.dag.All()
	snap := make(map[string]State, len(all))
	for _, v := range all {
		snap[v.ID()] = v.State()
	}
	return snap
}

// AggregateMetrics returns VertexMetrics for every vertex in the DAG.
func (s *Scheduler) AggregateMetrics() map[string]VertexMetrics {
	all := s.dag.All()
	m := make(map[string]VertexMetrics, len(all))
	for _, v := range all {
		m[v.ID()] = v.Metrics()
	}
	return m
}
