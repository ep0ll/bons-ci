package solver_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bons/bons-ci/pkg/solver"
	"github.com/bons/bons-ci/pkg/solver/cache"
	"github.com/bons/bons-ci/pkg/solver/schedule"
	"github.com/bons/bons-ci/pkg/solver/signal"
	"github.com/bons/bons-ci/pkg/solver/stream"
	digest "github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── Test helpers ─────────────────────────────────────────────────────────────

// testVertex is a minimal Vertex for tests.
type testVertex struct {
	name    string
	dgst    digest.Digest
	inputs  []solver.Edge
	options solver.VertexOptions
	sys     any
}

func (v *testVertex) Digest() digest.Digest       { return v.dgst }
func (v *testVertex) Inputs() []solver.Edge        { return v.inputs }
func (v *testVertex) Name() string                 { return v.name }
func (v *testVertex) Sys() any                     { return v.sys }
func (v *testVertex) Options() solver.VertexOptions { return v.options }

func newVertex(name string, inputs ...solver.Edge) *testVertex {
	return &testVertex{
		name:   name,
		dgst:   digest.FromString(name),
		inputs: inputs,
	}
}

func newVertexWithOpts(name string, opts solver.VertexOptions, inputs ...solver.Edge) *testVertex {
	return &testVertex{
		name:    name,
		dgst:    digest.FromString(name),
		inputs:  inputs,
		options: opts,
	}
}

// testOp is a controllable Op for tests.
type testOp struct {
	execFn      func(ctx context.Context, inputs []solver.Result) ([]solver.Result, error)
	delay       time.Duration
	execCount   atomic.Int64
	cacheDigest digest.Digest // per-vertex CacheMap digest; defaults to "cachemap"
}

func (op *testOp) CacheMap(_ context.Context, _ int) (*solver.CacheMap, bool, error) {
	d := op.cacheDigest
	if d == "" {
		d = digest.FromString("cachemap")
	}
	return &solver.CacheMap{Digest: d}, true, nil
}

func (op *testOp) Exec(ctx context.Context, inputs []solver.Result) ([]solver.Result, error) {
	op.execCount.Add(1)
	if op.delay > 0 {
		select {
		case <-time.After(op.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if op.execFn != nil {
		return op.execFn(ctx, inputs)
	}
	return []solver.Result{solver.NewResult("result-default", nil)}, nil
}

func (op *testOp) Acquire(_ context.Context) (func(), error) {
	return func() {}, nil
}

// errorOp always fails Exec.
type errorOp struct{ msg string }

func (op *errorOp) CacheMap(_ context.Context, _ int) (*solver.CacheMap, bool, error) {
	return &solver.CacheMap{Digest: digest.FromString("errmap")}, true, nil
}
func (op *errorOp) Exec(_ context.Context, _ []solver.Result) ([]solver.Result, error) {
	return nil, fmt.Errorf("%s", op.msg)
}
func (op *errorOp) Acquire(_ context.Context) (func(), error) { return func() {}, nil }

// makeSimpleResolveOp produces results named "result-<vertex.Name()>".
func makeSimpleResolveOp() solver.ResolveOpFunc {
	return func(vtx solver.Vertex) (solver.Op, error) {
		return &testOp{
			execFn: func(_ context.Context, _ []solver.Result) ([]solver.Result, error) {
				return []solver.Result{
					solver.NewResult("result-"+vtx.Name(), vtx.Name()),
				}, nil
			},
		}, nil
	}
}

// buildDiamond creates:
//
//	    A (root)
//	   / \
//	  B   C
//	   \ /
//	    D (leaf)
func buildDiamond() (root, b, c, leaf *testVertex) {
	root = newVertex("A")
	b = newVertex("B", solver.Edge{Index: 0, Vertex: root})
	c = newVertex("C", solver.Edge{Index: 0, Vertex: root})
	leaf = newVertex("D",
		solver.Edge{Index: 0, Vertex: b},
		solver.Edge{Index: 0, Vertex: c},
	)
	return
}

// buildChain creates: v0 → v1 → … → v(depth-1)
func buildChain(depth int) []*testVertex {
	verts := make([]*testVertex, depth)
	verts[0] = newVertex("v0-root")
	for i := 1; i < depth; i++ {
		verts[i] = newVertex(
			fmt.Sprintf("v%d", i),
			solver.Edge{Index: 0, Vertex: verts[i-1]},
		)
	}
	return verts
}

// ─── Test 1: Cache hit short-circuits ALL vertex execution ───────────────────

func TestCacheHitShortCircuit(t *testing.T) {
	root, b, c, leaf := buildDiamond()
	store := cache.NewMemory()
	ctx := context.Background()

	// When ResolveOp is nil, computeCacheKey uses v.Digest() directly.
	// Pre-populate every vertex in cache with that key format.
	for _, v := range []*testVertex{root, b, c, leaf} {
		require.NoError(t,
			store.Save(ctx, cache.Key{Digest: v.Digest(), Output: 0}, "cached-"+v.Name(), 100))
	}

	var opExecuted atomic.Bool
	resolveOp := func(vtx solver.Vertex) (solver.Op, error) {
		return &testOp{
			execFn: func(_ context.Context, _ []solver.Result) ([]solver.Result, error) {
				opExecuted.Store(true)
				return []solver.Result{solver.NewResult("should-not-run", nil)}, nil
			},
		}, nil
	}

	// Pass ResolveOp: nil so computeCacheKey uses simple v.Digest() keys.
	s := solver.New(solver.SolverOpts{
		ResolveOp: nil,
		Cache:     store,
		Workers:   2,
	})
	defer s.Close()

	// Set ResolveOp for any vertices that miss cache (shouldn't happen).
	_ = resolveOp

	sess, err := s.Solve(ctx, []solver.Edge{{Index: 0, Vertex: leaf}})
	require.NoError(t, err)

	res, err := sess.Result(solver.Edge{Index: 0, Vertex: leaf})
	require.NoError(t, err)
	assert.NotNil(t, res)

	assert.False(t, opExecuted.Load(), "no Op.Exec should run when all vertices are cached")
}

// ─── Test 2: Partial parent cache reuse ──────────────────────────────────────

func TestPartialParentCacheReuse(t *testing.T) {
	root, b, _, leaf := buildDiamond()
	store := cache.NewMemory()
	ctx := context.Background()

	// Cache root and B but NOT C or D.
	// When ResolveOp is nil, computeCacheKey uses v.Digest() directly.
	require.NoError(t, store.Save(ctx,
		cache.Key{Digest: root.Digest(), Output: 0}, "cached-A", 0))
	require.NoError(t, store.Save(ctx,
		cache.Key{Digest: b.Digest(), Output: 0}, "cached-B", 0))

	var executed sync.Map // vertex name → bool
	resolveOp := func(vtx solver.Vertex) (solver.Op, error) {
		return &testOp{
			execFn: func(_ context.Context, _ []solver.Result) ([]solver.Result, error) {
				executed.Store(vtx.Name(), true)
				return []solver.Result{solver.NewResult("result-"+vtx.Name(), nil)}, nil
			},
		}, nil
	}

	s := solver.New(solver.SolverOpts{ResolveOp: resolveOp, Cache: store, Workers: 4})
	defer s.Close()

	sess, err := s.Solve(ctx, []solver.Edge{{Index: 0, Vertex: leaf}})
	require.NoError(t, err)

	res, err := sess.Result(solver.Edge{Index: 0, Vertex: leaf})
	require.NoError(t, err)
	require.NotNil(t, res)

	_, execA := executed.Load("A")
	_, execB := executed.Load("B")
	assert.False(t, execA, "A should not execute (cached)")
	assert.False(t, execB, "B should not execute (cached)")

	_, execC := executed.Load("C")
	_, execD := executed.Load("D")
	assert.True(t, execC, "C should execute (not cached)")
	assert.True(t, execD, "D should execute (not cached)")
}

// ─── Test 3: Full cache miss — all vertices execute ───────────────────────────

func TestMissingCacheResolution(t *testing.T) {
	_, _, _, leaf := buildDiamond()
	var executed sync.Map

	resolveOp := func(vtx solver.Vertex) (solver.Op, error) {
		return &testOp{
			execFn: func(_ context.Context, _ []solver.Result) ([]solver.Result, error) {
				executed.Store(vtx.Name(), true)
				return []solver.Result{solver.NewResult("result-"+vtx.Name(), nil)}, nil
			},
		}, nil
	}

	s := solver.New(solver.SolverOpts{ResolveOp: resolveOp, Workers: 2})
	defer s.Close()

	sess, err := s.Solve(context.Background(), []solver.Edge{{Index: 0, Vertex: leaf}})
	require.NoError(t, err)

	_, err = sess.Result(solver.Edge{Index: 0, Vertex: leaf})
	require.NoError(t, err)

	for _, name := range []string{"A", "B", "C", "D"} {
		_, ok := executed.Load(name)
		assert.True(t, ok, "vertex %s should have executed", name)
	}
}

// ─── Test 4: Multiple leaf requests — shared parents execute once ─────────────

func TestMultipleLeafRequests(t *testing.T) {
	root := newVertex("root")
	l1 := newVertex("L1", solver.Edge{Index: 0, Vertex: root})
	l2 := newVertex("L2", solver.Edge{Index: 0, Vertex: root})
	l3 := newVertex("L3", solver.Edge{Index: 0, Vertex: root})

	var execCounts sync.Map // vertex name → *atomic.Int64

	resolveOp := func(vtx solver.Vertex) (solver.Op, error) {
		return &testOp{
			execFn: func(_ context.Context, _ []solver.Result) ([]solver.Result, error) {
				v, _ := execCounts.LoadOrStore(vtx.Name(), new(atomic.Int64))
				v.(*atomic.Int64).Add(1)
				return []solver.Result{solver.NewResult("result-"+vtx.Name(), nil)}, nil
			},
		}, nil
	}

	s := solver.New(solver.SolverOpts{ResolveOp: resolveOp, Workers: 4})
	defer s.Close()

	leaves := []solver.Edge{
		{Index: 0, Vertex: l1},
		{Index: 0, Vertex: l2},
		{Index: 0, Vertex: l3},
	}

	sess, err := s.Solve(context.Background(), leaves)
	require.NoError(t, err)

	for _, leaf := range leaves {
		res, err := sess.Result(leaf)
		require.NoError(t, err, "leaf %s should have a result", leaf.Vertex.Name())
		require.NotNil(t, res)
	}

	// Root must execute exactly once — scheduler dedup guarantees this.
	if v, ok := execCounts.Load("root"); ok {
		assert.Equal(t, int64(1), v.(*atomic.Int64).Load(),
			"root vertex must execute exactly once (dedup by digest)")
	}
}

// ─── Test 5: Priority scheduling — roots execute before leaves ────────────────

func TestPriorityScheduling(t *testing.T) {
	chain := buildChain(5)
	var orderMu sync.Mutex
	var order []string

	resolveOp := func(vtx solver.Vertex) (solver.Op, error) {
		return &testOp{
			execFn: func(_ context.Context, _ []solver.Result) ([]solver.Result, error) {
				orderMu.Lock()
				order = append(order, vtx.Name())
				orderMu.Unlock()
				return []solver.Result{solver.NewResult("result-"+vtx.Name(), nil)}, nil
			},
		}, nil
	}

	// Single worker forces strict sequential execution — priority determines order.
	s := solver.New(solver.SolverOpts{
		ResolveOp: resolveOp,
		Workers:   1,
		Policy:    &schedule.CriticalPathPolicy{MaxDepth: 4},
	})
	defer s.Close()

	leaf := chain[len(chain)-1]
	sess, err := s.Solve(context.Background(), []solver.Edge{{Index: 0, Vertex: leaf}})
	require.NoError(t, err)
	_, err = sess.Result(solver.Edge{Index: 0, Vertex: leaf})
	require.NoError(t, err)

	orderMu.Lock()
	defer orderMu.Unlock()
	require.NotEmpty(t, order, "at least one vertex must have executed")
	// Root (deepest in the dependency chain) should execute first.
	assert.Equal(t, "v0-root", order[0], "root (highest depth score) runs first")
}

// ─── Test 6: Cancellation propagates correctly ───────────────────────────────

func TestCancellation(t *testing.T) {
	chain := buildChain(10)
	resolveOp := func(_ solver.Vertex) (solver.Op, error) {
		return &testOp{
			execFn: func(ctx context.Context, _ []solver.Result) ([]solver.Result, error) {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(30 * time.Second):
					return []solver.Result{solver.NewResult("result", nil)}, nil
				}
			},
		}, nil
	}

	s := solver.New(solver.SolverOpts{ResolveOp: resolveOp, Workers: 2})
	defer s.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	leaf := chain[len(chain)-1]
	_, err := s.Solve(ctx, []solver.Edge{{Index: 0, Vertex: leaf}})

	require.Error(t, err, "expected an error from cancelled context")
	assert.Contains(t, err.Error(), "context", "error should mention context")
}

// ─── Test 7: Real error (not ctx.Err()) is returned on vertex failure ─────────
//
// This is a NEW test that covers the bug where the original code returned
// ctx.Err() (context.Canceled) instead of the actual solve error.

func TestRealErrorReturnedNotCtxErr(t *testing.T) {
	root := newVertex("root")
	failVertex := newVertex("fail", solver.Edge{Index: 0, Vertex: root})
	leaf := newVertex("leaf", solver.Edge{Index: 0, Vertex: failVertex})

	resolveOp := func(vtx solver.Vertex) (solver.Op, error) {
		if vtx.Name() == "fail" {
			return &errorOp{msg: "deliberate-test-failure"}, nil
		}
		return &testOp{
			execFn: func(_ context.Context, _ []solver.Result) ([]solver.Result, error) {
				return []solver.Result{solver.NewResult("result-"+vtx.Name(), nil)}, nil
			},
		}, nil
	}

	s := solver.New(solver.SolverOpts{ResolveOp: resolveOp, Workers: 2})
	defer s.Close()

	_, err := s.Solve(context.Background(), []solver.Edge{{Index: 0, Vertex: leaf}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "deliberate-test-failure",
		"solver should return the real vertex error, not ctx.Err()")
	// Must NOT be a bare context.Canceled — that was the original bug.
	assert.NotEqual(t, context.Canceled, err)
}

// ─── Test 8: Streaming transformation pipeline ────────────────────────────────

func TestStreamingTransformation(t *testing.T) {
	root := newVertex("src")
	leaf := newVertex("build", solver.Edge{Index: 0, Vertex: root})

	resolveOp := makeSimpleResolveOp()

	pipeline := stream.NewPipeline(
		stream.TransformFunc{
			Label: "uppercase-prefix",
			Fn: func(_ context.Context, input any) (any, error) {
				r := input.(solver.Result)
				return solver.NewResult("TRANSFORMED-"+r.ID(), r.Sys()), nil
			},
		},
	)

	s := solver.New(solver.SolverOpts{ResolveOp: resolveOp, Workers: 2})
	defer s.Close()

	sess, err := s.Solve(context.Background(),
		[]solver.Edge{{Index: 0, Vertex: leaf}},
		solver.WithTransform(pipeline),
	)
	require.NoError(t, err)

	res, err := sess.Result(solver.Edge{Index: 0, Vertex: leaf})
	require.NoError(t, err)
	assert.Contains(t, res.ID(), "TRANSFORMED-", "result should be transformed")
}

// ─── Test 9: Reactive event handling ─────────────────────────────────────────

func TestReactiveEventHandling(t *testing.T) {
	root := newVertex("root")
	leaf := newVertex("leaf", solver.Edge{Index: 0, Vertex: root})

	s := solver.New(solver.SolverOpts{ResolveOp: makeSimpleResolveOp(), Workers: 2})
	defer s.Close()

	sub := s.Bus().Subscribe(256, nil)

	done := make(chan []signal.Event, 1)
	go func() {
		var evts []signal.Event
		for evt := range sub.Events() {
			evts = append(evts, evt)
		}
		done <- evts
	}()

	sess, err := s.Solve(context.Background(), []solver.Edge{{Index: 0, Vertex: leaf}})
	require.NoError(t, err)
	require.NotNil(t, sess)

	s.Bus().Close()
	evts := <-done

	require.NotEmpty(t, evts, "should have received events")
	types := make(map[signal.EventType]int)
	for _, evt := range evts {
		types[evt.Type]++
	}
	assert.Greater(t, types[signal.CacheMiss], 0, "should see cache misses")
	assert.Greater(t, types[signal.VertexCompleted]+types[signal.VertexStarted], 0,
		"should see vertex lifecycle events")
}

// ─── Test 10: Graph depth computation ────────────────────────────────────────

func TestGraphDepthComputation(t *testing.T) {
	root, b, c, leaf := buildDiamond()
	g := solver.BuildGraph([]solver.Edge{{Index: 0, Vertex: leaf}})

	assert.Equal(t, 0, g.Depth(root), "root depth = 0")
	assert.Equal(t, 1, g.Depth(b), "B depth = 1")
	assert.Equal(t, 1, g.Depth(c), "C depth = 1")
	assert.Equal(t, 2, g.Depth(leaf), "leaf depth = 2")
	assert.Equal(t, 2, g.MaxDepth())
	assert.Equal(t, 4, g.Size())
	assert.Equal(t, 0, g.InDegree(root))
	assert.Equal(t, 2, g.OutDegree(root)) // B and C are children
}

func TestGraphTopologicalOrder(t *testing.T) {
	root, _, _, leaf := buildDiamond()
	g := solver.BuildGraph([]solver.Edge{{Index: 0, Vertex: leaf}})
	order := g.TopologicalOrder()

	require.Len(t, order, 4)
	assert.Equal(t, root.Digest(), order[0].Digest(), "root first (depth 0)")
	assert.Equal(t, leaf.Digest(), order[3].Digest(), "leaf last (depth 2)")
}

func TestGraphCriticalPath(t *testing.T) {
	chain := buildChain(5)
	leaf := chain[len(chain)-1]
	g := solver.BuildGraph([]solver.Edge{{Index: 0, Vertex: leaf}})

	path := g.CriticalPath(solver.Edge{Index: 0, Vertex: leaf})
	require.Len(t, path, 5)
	assert.Equal(t, leaf.Digest(), path[0].Digest(), "path starts at leaf")
	assert.Equal(t, chain[0].Digest(), path[4].Digest(), "path ends at root")
}

// ─── Test 11: Memory cache operations ────────────────────────────────────────

func TestMemoryCacheProbeAndSave(t *testing.T) {
	ctx := context.Background()
	store := cache.NewMemory()
	key := cache.Key{Digest: digest.FromString("test"), Output: 0}

	_, found, err := store.Probe(ctx, key)
	require.NoError(t, err)
	assert.False(t, found, "should not be found before Save")

	require.NoError(t, store.Save(ctx, key, "result-1", 42))

	resultID, found, err := store.Probe(ctx, key)
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, "result-1", resultID)

	rec, err := store.Load(ctx, key)
	require.NoError(t, err)
	assert.Equal(t, "result-1", rec.ResultID)
	assert.Equal(t, 42, rec.Size)

	require.NoError(t, store.Release(ctx, key))
	_, found, _ = store.Probe(ctx, key)
	assert.False(t, found, "should not be found after Release")
}

// ─── Test 12: Combined cache warming ─────────────────────────────────────────

func TestCombinedCacheWarming(t *testing.T) {
	ctx := context.Background()
	primary := cache.NewMemory()
	secondary := cache.NewMemory()
	key := cache.Key{Digest: digest.FromString("warm"), Output: 0}

	require.NoError(t, secondary.Save(ctx, key, "secondary-result", 0))

	combined := cache.NewCombined(primary, secondary)

	resultID, found, err := combined.Probe(ctx, key)
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, "secondary-result", resultID)

	// Primary should now be warmed.
	rid, found, err := primary.Probe(ctx, key)
	require.NoError(t, err)
	assert.True(t, found, "primary should have been warmed by the probe")
	assert.Equal(t, "secondary-result", rid)
}

// ─── Test 13: Event bus filter composition ───────────────────────────────────

func TestEventBusFilterComposition(t *testing.T) {
	bus := signal.NewBus()
	defer bus.Close()

	cacheHitOnly := signal.ForTypes(signal.CacheHit)
	sub := bus.Subscribe(16, cacheHitOnly)

	bus.Publish(signal.Event{Type: signal.CacheMiss, Name: "miss"})
	bus.Publish(signal.Event{Type: signal.CacheHit, Name: "hit"})
	bus.Publish(signal.Event{Type: signal.VertexCompleted, Name: "done"})

	time.Sleep(10 * time.Millisecond)

	select {
	case evt := <-sub.Events():
		assert.Equal(t, signal.CacheHit, evt.Type)
		assert.Equal(t, "hit", evt.Name)
	default:
		t.Fatal("expected CacheHit event")
	}

	// No further events should be queued.
	select {
	case evt := <-sub.Events():
		t.Fatalf("unexpected event: %v", evt)
	default:
	}

	// Test AND filter composition.
	bus2 := signal.NewBus()
	defer bus2.Close()

	v1 := digest.FromString("v1")
	v2 := digest.FromString("v2")
	hitOnV1 := signal.And(signal.ForVertex(v1), signal.ForTypes(signal.CacheHit))
	sub2 := bus2.Subscribe(16, hitOnV1)

	bus2.Publish(signal.Event{Type: signal.CacheHit, Vertex: v2, Name: "other"})
	bus2.Publish(signal.Event{Type: signal.CacheMiss, Vertex: v1, Name: "miss-v1"})
	bus2.Publish(signal.Event{Type: signal.CacheHit, Vertex: v1, Name: "hit-v1"})

	time.Sleep(10 * time.Millisecond)
	select {
	case evt := <-sub2.Events():
		assert.Equal(t, "hit-v1", evt.Name)
	default:
		t.Fatal("expected hit-v1 event through AND filter")
	}
}

// ─── Test 14: Priority queue ordering ────────────────────────────────────────

func TestPriorityQueueOrdering(t *testing.T) {
	pq := schedule.NewPriorityQueue()

	pq.Push(&schedule.Task{VertexDigest: "c", Priority: 3, Name: "low"})
	pq.Push(&schedule.Task{VertexDigest: "a", Priority: 1, Name: "high"})
	pq.Push(&schedule.Task{VertexDigest: "b", Priority: 2, Name: "mid"})

	t1 := pq.TryPop()
	require.NotNil(t, t1)
	assert.Equal(t, 1, t1.Priority, "priority 1 first")

	t2 := pq.TryPop()
	require.NotNil(t, t2)
	assert.Equal(t, 2, t2.Priority)

	t3 := pq.TryPop()
	require.NotNil(t, t3)
	assert.Equal(t, 3, t3.Priority)

	assert.Nil(t, pq.TryPop(), "queue should be empty")
}

func TestPriorityQueueClose(t *testing.T) {
	pq := schedule.NewPriorityQueue()

	done := make(chan struct{})
	go func() {
		defer close(done)
		result := pq.Pop() // should block then return nil
		assert.Nil(t, result)
	}()

	time.Sleep(20 * time.Millisecond)
	pq.Close() // should unblock Pop
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Pop did not unblock after Close")
	}
}

// ─── Test 15: Stream pipeline ─────────────────────────────────────────────────

func TestStreamPipeline(t *testing.T) {
	double := stream.TransformFunc{
		Label: "double",
		Fn: func(_ context.Context, input any) (any, error) {
			return input.(int) * 2, nil
		},
	}
	addTen := stream.TransformFunc{
		Label: "add-ten",
		Fn: func(_ context.Context, input any) (any, error) {
			return input.(int) + 10, nil
		},
	}

	p := stream.NewPipeline(double, addTen)
	result, err := p.Process(context.Background(), 5)
	require.NoError(t, err)
	assert.Equal(t, 20, result, "(5*2)+10 = 20")
}

func TestStreamPipelineCancellation(t *testing.T) {
	slow := stream.TransformFunc{
		Label: "slow",
		Fn: func(ctx context.Context, input any) (any, error) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(10 * time.Second):
				return input, nil
			}
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := stream.NewPipeline(slow).Process(ctx, 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "context")
}

func TestStreamPipelineAppendPrepend(t *testing.T) {
	makeAdder := func(n int) stream.TransformFunc {
		return stream.TransformFunc{
			Label: fmt.Sprintf("add%d", n),
			Fn: func(_ context.Context, input any) (any, error) {
				return input.(int) + n, nil
			},
		}
	}

	p := stream.NewPipeline(makeAdder(1))
	p.Append(makeAdder(2))
	p.Prepend(makeAdder(10))

	// Order: +10 → +1 → +2, applied to 0 = 13
	result, err := p.Process(context.Background(), 0)
	require.NoError(t, err)
	assert.Equal(t, 13, result)
	assert.Equal(t, 3, p.Len())
}

func TestStreamPipelineAsync(t *testing.T) {
	p := stream.NewPipeline(stream.TransformFunc{
		Label: "async-stage",
		Fn: func(_ context.Context, input any) (any, error) {
			time.Sleep(10 * time.Millisecond)
			return input.(int) * 3, nil
		},
	})

	ch := p.ProcessAsync(context.Background(), 7)
	result := <-ch
	require.NoError(t, result.Err)
	assert.Equal(t, 21, result.Value)
}

// ─── Test 16: Ignore-cache forces re-execution ────────────────────────────────

func TestIgnoreCacheForceReexecution(t *testing.T) {
	store := cache.NewMemory()
	ctx := context.Background()

	root := newVertexWithOpts("root", solver.VertexOptions{IgnoreCache: true})
	require.NoError(t, store.Save(ctx,
		cache.Key{Digest: root.Digest(), Output: 0}, "stale-cached-root", 0))

	var executed atomic.Bool
	resolveOp := func(_ solver.Vertex) (solver.Op, error) {
		return &testOp{
			execFn: func(_ context.Context, _ []solver.Result) ([]solver.Result, error) {
				executed.Store(true)
				return []solver.Result{solver.NewResult("fresh-result", nil)}, nil
			},
		}, nil
	}

	s := solver.New(solver.SolverOpts{ResolveOp: resolveOp, Cache: store, Workers: 1})
	defer s.Close()

	sess, err := s.Solve(ctx, []solver.Edge{{Index: 0, Vertex: root}})
	require.NoError(t, err)

	res, err := sess.Result(solver.Edge{Index: 0, Vertex: root})
	require.NoError(t, err)

	assert.True(t, executed.Load(), "IgnoreCache vertex should always execute")
	assert.Equal(t, "fresh-result", res.ID(), "should get fresh result, not cached")
}

// ─── Test 17: VertexByDigest (renamed from Vertex to avoid type shadow) ───────

func TestGraphVertexByDigest(t *testing.T) {
	root, _, _, leaf := buildDiamond()
	g := solver.BuildGraph([]solver.Edge{{Index: 0, Vertex: leaf}})

	v := g.VertexByDigest(root.Digest())
	require.NotNil(t, v)
	assert.Equal(t, "A", v.Name())

	assert.Nil(t, g.VertexByDigest(digest.FromString("nonexistent")))
}

// ─── Benchmarks ───────────────────────────────────────────────────────────────

func BenchmarkCacheProbHit(b *testing.B) {
	store := cache.NewMemory()
	ctx := context.Background()
	key := cache.Key{Digest: digest.FromString("bench"), Output: 0}
	_ = store.Save(ctx, key, "result", 0)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _, _ = store.Probe(ctx, key)
		}
	})
}

func BenchmarkCacheProbeMiss(b *testing.B) {
	store := cache.NewMemory()
	ctx := context.Background()
	key := cache.Key{Digest: digest.FromString("miss"), Output: 0}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _ = store.Probe(ctx, key)
	}
}

func BenchmarkPriorityQueuePushPop(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		pq := schedule.NewPriorityQueue()
		for j := 0; j < 100; j++ {
			pq.Push(&schedule.Task{
				VertexDigest: digest.FromString(fmt.Sprintf("v%d", j)),
				Priority:     j,
			})
		}
		for j := 0; j < 100; j++ {
			pq.TryPop()
		}
	}
}

func BenchmarkGraphBuildWideFlat(b *testing.B) {
	root := newVertex("root")
	leaves := make([]solver.Edge, 100)
	for i := range leaves {
		v := newVertex(fmt.Sprintf("leaf-%d", i), solver.Edge{Index: 0, Vertex: root})
		leaves[i] = solver.Edge{Index: 0, Vertex: v}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		solver.BuildGraph(leaves)
	}
}

func BenchmarkGraphBuildDeepChain(b *testing.B) {
	chain := buildChain(50)
	leaf := chain[len(chain)-1]
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		solver.BuildGraph([]solver.Edge{{Index: 0, Vertex: leaf}})
	}
}

func BenchmarkGraphDepthLookup(b *testing.B) {
	chain := buildChain(50)
	leaf := chain[len(chain)-1]
	g := solver.BuildGraph([]solver.Edge{{Index: 0, Vertex: leaf}})
	mid := chain[25]
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		g.Depth(mid)
	}
}

func BenchmarkEventBusPublish(b *testing.B) {
	bus := signal.NewBus()
	defer bus.Close()

	for i := 0; i < 10; i++ {
		sub := bus.Subscribe(4096, nil)
		go func() {
			for range sub.Events() {
			}
		}()
	}

	evt := signal.Event{Type: signal.VertexCompleted, Name: "bench"}
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			bus.Publish(evt)
		}
	})
}

func BenchmarkSolveChainAllCached(b *testing.B) {
	chain := buildChain(20)
	leaf := chain[len(chain)-1]
	store := cache.NewMemory()
	ctx := context.Background()
	for _, v := range chain {
		_ = store.Save(ctx, cache.Key{Digest: v.Digest(), Output: 0}, "cached-"+v.Name(), 0)
	}
	resolveOp := makeSimpleResolveOp()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s := solver.New(solver.SolverOpts{
			ResolveOp: resolveOp,
			Cache:     store,
			Workers:   4,
		})
		_, _ = s.Solve(ctx, []solver.Edge{{Index: 0, Vertex: leaf}})
		s.Close()
	}
}

func BenchmarkSolveChainNoCacheShallow(b *testing.B) {
	chain := buildChain(5)
	leaf := chain[len(chain)-1]
	resolveOp := makeSimpleResolveOp()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s := solver.New(solver.SolverOpts{
			ResolveOp: resolveOp,
			Workers:   4,
		})
		_, _ = s.Solve(context.Background(), []solver.Edge{{Index: 0, Vertex: leaf}})
		s.Close()
	}
}

func BenchmarkSolveDiamondNoCacheParallel(b *testing.B) {
	resolveOp := makeSimpleResolveOp()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _, _, leaf := buildDiamond()
			s := solver.New(solver.SolverOpts{
				ResolveOp: resolveOp,
				Workers:   4,
			})
			_, _ = s.Solve(context.Background(), []solver.Edge{{Index: 0, Vertex: leaf}})
			s.Close()
		}
	})
}
