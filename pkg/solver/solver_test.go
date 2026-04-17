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

// ---------------------------------------------------------------------------
// Test Helpers
// ---------------------------------------------------------------------------

// testVertex is a minimal Vertex implementation for testing.
type testVertex struct {
	name    string
	dgst    digest.Digest
	inputs  []solver.Edge
	options solver.VertexOptions
	sys     any
}

func (v *testVertex) Digest() digest.Digest         { return v.dgst }
func (v *testVertex) Inputs() []solver.Edge          { return v.inputs }
func (v *testVertex) Name() string                   { return v.name }
func (v *testVertex) Sys() any                       { return v.sys }
func (v *testVertex) Options() solver.VertexOptions   { return v.options }

func newVertex(name string, inputs ...solver.Edge) *testVertex {
	return &testVertex{
		name:   name,
		dgst:   digest.FromString(name),
		inputs: inputs,
	}
}

// testOp is a minimal Op implementation for testing.
type testOp struct {
	execFn   func(ctx context.Context, inputs []solver.Result) ([]solver.Result, error)
	delay    time.Duration
	execCount atomic.Int64
}

func (op *testOp) CacheMap(_ context.Context, _ int) (*solver.CacheMap, bool, error) {
	return &solver.CacheMap{Digest: digest.FromString("cachemap")}, true, nil
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

// makeSimpleResolveOp creates a ResolveOpFunc that returns a testOp which
// produces a result with the vertex name.
func makeSimpleResolveOp() solver.ResolveOpFunc {
	return func(vtx solver.Vertex) (solver.Op, error) {
		return &testOp{
			execFn: func(_ context.Context, _ []solver.Result) ([]solver.Result, error) {
				return []solver.Result{solver.NewResult("result-" + vtx.Name(), vtx.Name())}, nil
			},
		}, nil
	}
}

// buildDiamond creates a diamond DAG:
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

// buildChain creates a linear chain: root → v1 → v2 → ... → leaf
func buildChain(depth int) (vertices []*testVertex) {
	vertices = make([]*testVertex, depth)
	vertices[0] = newVertex(fmt.Sprintf("v0-root"))
	for i := 1; i < depth; i++ {
		vertices[i] = newVertex(
			fmt.Sprintf("v%d", i),
			solver.Edge{Index: 0, Vertex: vertices[i-1]},
		)
	}
	return
}

// ---------------------------------------------------------------------------
// Test 1: Cache Hit Short-Circuiting
// ---------------------------------------------------------------------------

func TestCacheHitShortCircuit(t *testing.T) {
	root, b, c, leaf := buildDiamond()
	cacheStore := cache.NewMemory()

	// Pre-populate ALL vertices in cache.
	ctx := context.Background()
	for _, v := range []*testVertex{root, b, c, leaf} {
		key := cache.Key{Digest: v.Digest(), Output: 0}
		require.NoError(t, cacheStore.Save(ctx, key, "cached-"+v.Name(), 100))
	}

	// Track if any op was executed — it should NOT be.
	var opExecuted atomic.Bool
	resolveOp := func(vtx solver.Vertex) (solver.Op, error) {
		return &testOp{
			execFn: func(_ context.Context, _ []solver.Result) ([]solver.Result, error) {
				opExecuted.Store(true)
				return []solver.Result{solver.NewResult("should-not-run", nil)}, nil
			},
		}, nil
	}

	s := solver.New(solver.SolverOpts{
		ResolveOp: resolveOp,
		Cache:     cacheStore,
		Workers:   2,
	})
	defer s.Close()

	sess, err := s.Solve(context.Background(), []solver.Edge{
		{Index: 0, Vertex: leaf},
	})
	require.NoError(t, err)
	require.NotNil(t, sess)

	result, err := sess.Result(solver.Edge{Index: 0, Vertex: leaf})
	require.NoError(t, err)
	assert.Equal(t, "cached-D", result.ID())
	assert.False(t, opExecuted.Load(), "no op should have been executed when cache hit")
}

// ---------------------------------------------------------------------------
// Test 2: Partial Parent Cache Reuse
// ---------------------------------------------------------------------------

func TestPartialParentCacheReuse(t *testing.T) {
	root, b, _, leaf := buildDiamond()

	cacheStore := cache.NewMemory()

	// Pre-populate root and B in cache, but NOT C or leaf.
	ctx := context.Background()
	require.NoError(t, cacheStore.Save(ctx, cache.Key{Digest: root.Digest(), Output: 0}, "cached-root", 0))
	require.NoError(t, cacheStore.Save(ctx, cache.Key{Digest: b.Digest(), Output: 0}, "cached-B", 0))

	// Track which vertices get executed.
	var executedVertices sync.Map

	resolveOp := func(vtx solver.Vertex) (solver.Op, error) {
		return &testOp{
			execFn: func(_ context.Context, _ []solver.Result) ([]solver.Result, error) {
				executedVertices.Store(vtx.Name(), true)
				return []solver.Result{solver.NewResult("result-" + vtx.Name(), nil)}, nil
			},
		}, nil
	}

	s := solver.New(solver.SolverOpts{
		ResolveOp: resolveOp,
		Cache:     cacheStore,
		Workers:   4,
	})
	defer s.Close()

	sess, err := s.Solve(ctx, []solver.Edge{{Index: 0, Vertex: leaf}})
	require.NoError(t, err)

	result, err := sess.Result(solver.Edge{Index: 0, Vertex: leaf})
	require.NoError(t, err)
	require.NotNil(t, result)

	// A (root) and B should NOT have been executed (cache hit).
	_, executedA := executedVertices.Load("A")
	_, executedB := executedVertices.Load("B")
	assert.False(t, executedA, "A should not have been executed (cached)")
	assert.False(t, executedB, "B should not have been executed (cached)")

	// C and D should have been executed.
	_, executedC := executedVertices.Load("C")
	_, executedD := executedVertices.Load("D")
	assert.True(t, executedC, "C should have been executed (not cached)")
	assert.True(t, executedD, "D should have been executed (not cached)")
}

// ---------------------------------------------------------------------------
// Test 3: Missing Cache Resolution
// ---------------------------------------------------------------------------

func TestMissingCacheResolution(t *testing.T) {
	_, _, _, leaf := buildDiamond()

	// Empty cache — everything should execute.
	var executedVertices sync.Map

	resolveOp := func(vtx solver.Vertex) (solver.Op, error) {
		return &testOp{
			execFn: func(_ context.Context, _ []solver.Result) ([]solver.Result, error) {
				executedVertices.Store(vtx.Name(), true)
				return []solver.Result{solver.NewResult("result-" + vtx.Name(), nil)}, nil
			},
		}, nil
	}

	s := solver.New(solver.SolverOpts{
		ResolveOp: resolveOp,
		Workers:   2,
	})
	defer s.Close()

	sess, err := s.Solve(context.Background(), []solver.Edge{{Index: 0, Vertex: leaf}})
	require.NoError(t, err)

	_, err = sess.Result(solver.Edge{Index: 0, Vertex: leaf})
	require.NoError(t, err)

	// All vertices should have been executed.
	for _, name := range []string{"A", "B", "C", "D"} {
		_, executed := executedVertices.Load(name)
		assert.True(t, executed, "%s should have been executed", name)
	}
}

// ---------------------------------------------------------------------------
// Test 4: Multiple Leaf Requests
// ---------------------------------------------------------------------------

func TestMultipleLeafRequests(t *testing.T) {
	// Create a fan-out DAG:
	//     root
	//    / | \
	//   L1 L2 L3
	root := newVertex("root")
	l1 := newVertex("L1", solver.Edge{Index: 0, Vertex: root})
	l2 := newVertex("L2", solver.Edge{Index: 0, Vertex: root})
	l3 := newVertex("L3", solver.Edge{Index: 0, Vertex: root})

	// Track execution counts — root should only execute once.
	execCounts := &sync.Map{}

	resolveOp := func(vtx solver.Vertex) (solver.Op, error) {
		return &testOp{
			execFn: func(_ context.Context, _ []solver.Result) ([]solver.Result, error) {
				val, _ := execCounts.LoadOrStore(vtx.Name(), new(atomic.Int64))
				val.(*atomic.Int64).Add(1)
				return []solver.Result{solver.NewResult("result-" + vtx.Name(), nil)}, nil
			},
		}, nil
	}

	s := solver.New(solver.SolverOpts{
		ResolveOp: resolveOp,
		Workers:   4,
	})
	defer s.Close()

	leaves := []solver.Edge{
		{Index: 0, Vertex: l1},
		{Index: 0, Vertex: l2},
		{Index: 0, Vertex: l3},
	}

	sess, err := s.Solve(context.Background(), leaves)
	require.NoError(t, err)

	// All leaves should have results.
	for _, leaf := range leaves {
		result, err := sess.Result(leaf)
		require.NoError(t, err, "leaf %s should have result", leaf.Vertex.Name())
		require.NotNil(t, result)
	}

	// Root should have been executed exactly once (dedup by vertex digest).
	if val, ok := execCounts.Load("root"); ok {
		assert.Equal(t, int64(1), val.(*atomic.Int64).Load(),
			"root should execute exactly once via deduplication")
	}
}

// ---------------------------------------------------------------------------
// Test 5: Priority Scheduling
// ---------------------------------------------------------------------------

func TestPriorityScheduling(t *testing.T) {
	// Build a chain of depth 5: v0 → v1 → v2 → v3 → v4
	chain := buildChain(5)

	// Track execution order.
	var executionOrder []string
	var orderMu sync.Mutex

	resolveOp := func(vtx solver.Vertex) (solver.Op, error) {
		return &testOp{
			execFn: func(_ context.Context, _ []solver.Result) ([]solver.Result, error) {
				orderMu.Lock()
				executionOrder = append(executionOrder, vtx.Name())
				orderMu.Unlock()
				return []solver.Result{solver.NewResult("result-" + vtx.Name(), nil)}, nil
			},
		}, nil
	}

	// Use single worker to enforce sequential execution → priority order visible.
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

	// With critical-path scheduling and 1 worker, the root (deepest)
	// should execute first, then progressively toward the leaf.
	orderMu.Lock()
	require.NotEmpty(t, executionOrder, "at least one vertex should have executed")
	assert.Equal(t, "v0-root", executionOrder[0], "root should execute first (deepest)")
	orderMu.Unlock()
}

// ---------------------------------------------------------------------------
// Test 6: Cancellation
// ---------------------------------------------------------------------------

func TestCancellation(t *testing.T) {
	chain := buildChain(10)

	// Create a slow op that respects cancellation.
	resolveOp := func(vtx solver.Vertex) (solver.Op, error) {
		return &testOp{
			delay: 5 * time.Second, // Intentionally slow.
			execFn: func(ctx context.Context, _ []solver.Result) ([]solver.Result, error) {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(5 * time.Second):
					return []solver.Result{solver.NewResult("result", nil)}, nil
				}
			},
		}, nil
	}

	s := solver.New(solver.SolverOpts{
		ResolveOp: resolveOp,
		Workers:   2,
	})
	defer s.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	leaf := chain[len(chain)-1]
	_, err := s.Solve(ctx, []solver.Edge{{Index: 0, Vertex: leaf}})

	// Should fail with context deadline exceeded or canceled.
	require.Error(t, err)
	assert.Contains(t, err.Error(), "context")
}

// ---------------------------------------------------------------------------
// Test 7: Streaming Transformation
// ---------------------------------------------------------------------------

func TestStreamingTransformation(t *testing.T) {
	root := newVertex("src")
	leaf := newVertex("build", solver.Edge{Index: 0, Vertex: root})

	resolveOp := makeSimpleResolveOp()

	// Create a transform pipeline that uppercases the result ID.
	pipeline := stream.NewPipeline(
		stream.TransformFunc{
			Fn: func(_ context.Context, input any) (any, error) {
				r := input.(solver.Result)
				return solver.NewResult("TRANSFORMED-"+r.ID(), r.Sys()), nil
			},
			Label: "uppercase",
		},
	)

	s := solver.New(solver.SolverOpts{
		ResolveOp: resolveOp,
		Workers:   2,
	})
	defer s.Close()

	sess, err := s.Solve(context.Background(),
		[]solver.Edge{{Index: 0, Vertex: leaf}},
		solver.WithTransform(pipeline),
	)
	require.NoError(t, err)

	result, err := sess.Result(solver.Edge{Index: 0, Vertex: leaf})
	require.NoError(t, err)
	assert.Contains(t, result.ID(), "TRANSFORMED-", "result should be transformed")
}

// ---------------------------------------------------------------------------
// Test 8: Reactive Event Handling
// ---------------------------------------------------------------------------

func TestReactiveEventHandling(t *testing.T) {
	root := newVertex("root")
	leaf := newVertex("leaf", solver.Edge{Index: 0, Vertex: root})

	resolveOp := makeSimpleResolveOp()

	s := solver.New(solver.SolverOpts{
		ResolveOp: resolveOp,
		Workers:   2,
	})
	defer s.Close()

	// Subscribe to all events before solving.
	sub := s.Bus().Subscribe(256, nil)

	done := make(chan struct{})
	var events []signal.Event
	var eventMu sync.Mutex

	go func() {
		defer close(done)
		for evt := range sub.Events() {
			eventMu.Lock()
			events = append(events, evt)
			eventMu.Unlock()
		}
	}()

	sess, err := s.Solve(context.Background(), []solver.Edge{{Index: 0, Vertex: leaf}})
	require.NoError(t, err)
	require.NotNil(t, sess)

	// Close the bus to unblock the event reader.
	s.Bus().Close()
	<-done

	eventMu.Lock()
	defer eventMu.Unlock()

	// Verify we received some events.
	require.NotEmpty(t, events, "should have received events")

	// Check that we got the expected event types.
	eventTypes := make(map[signal.EventType]int)
	for _, evt := range events {
		eventTypes[evt.Type]++
	}

	// We should see cache misses (both vertices uncached) and completions.
	assert.Greater(t, eventTypes[signal.CacheMiss], 0, "should have cache miss events")
	assert.Greater(t, eventTypes[signal.VertexCompleted]+eventTypes[signal.VertexStarted], 0,
		"should have vertex lifecycle events")
}

// ---------------------------------------------------------------------------
// Graph Tests
// ---------------------------------------------------------------------------

func TestGraphDepthComputation(t *testing.T) {
	root, b, c, leaf := buildDiamond()
	g := solver.BuildGraph([]solver.Edge{{Index: 0, Vertex: leaf}})

	assert.Equal(t, 0, g.Depth(root), "root depth should be 0")
	assert.Equal(t, 1, g.Depth(b), "B depth should be 1")
	assert.Equal(t, 1, g.Depth(c), "C depth should be 1")
	assert.Equal(t, 2, g.Depth(leaf), "leaf depth should be 2")
	assert.Equal(t, 2, g.MaxDepth())
	assert.Equal(t, 4, g.Size())
}

func TestGraphTopologicalOrder(t *testing.T) {
	root, _, _, leaf := buildDiamond()
	g := solver.BuildGraph([]solver.Edge{{Index: 0, Vertex: leaf}})
	order := g.TopologicalOrder()

	require.Len(t, order, 4)
	// Root should be first (depth 0).
	assert.Equal(t, root.Digest(), order[0].Digest())
	// Leaf should be last (depth 2).
	assert.Equal(t, leaf.Digest(), order[3].Digest())
}

func TestGraphCriticalPath(t *testing.T) {
	chain := buildChain(5)
	leaf := chain[len(chain)-1]
	g := solver.BuildGraph([]solver.Edge{{Index: 0, Vertex: leaf}})

	path := g.CriticalPath(solver.Edge{Index: 0, Vertex: leaf})
	require.Len(t, path, 5, "critical path should include all vertices in chain")
	assert.Equal(t, leaf.Digest(), path[0].Digest(), "first element should be the leaf")
	assert.Equal(t, chain[0].Digest(), path[4].Digest(), "last element should be the root")
}

// ---------------------------------------------------------------------------
// Cache Tests
// ---------------------------------------------------------------------------

func TestMemoryCacheProbeAndSave(t *testing.T) {
	ctx := context.Background()
	store := cache.NewMemory()

	key := cache.Key{Digest: digest.FromString("test"), Output: 0}

	// Should not be found initially.
	_, found, err := store.Probe(ctx, key)
	require.NoError(t, err)
	assert.False(t, found)

	// Save and probe again.
	require.NoError(t, store.Save(ctx, key, "result-1", 42))
	resultID, found, err := store.Probe(ctx, key)
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, "result-1", resultID)

	// Load full record.
	rec, err := store.Load(ctx, key)
	require.NoError(t, err)
	assert.Equal(t, "result-1", rec.ResultID)
	assert.Equal(t, 42, rec.Size)

	// Release.
	require.NoError(t, store.Release(ctx, key))
	_, found, _ = store.Probe(ctx, key)
	assert.False(t, found)
}

func TestCombinedCacheWarming(t *testing.T) {
	ctx := context.Background()
	primary := cache.NewMemory()
	secondary := cache.NewMemory()

	key := cache.Key{Digest: digest.FromString("warm"), Output: 0}

	// Only secondary has the entry.
	require.NoError(t, secondary.Save(ctx, key, "secondary-result", 0))

	combined := cache.NewCombined(primary, secondary)

	// Probe: should find in secondary and warm primary.
	resultID, found, err := combined.Probe(ctx, key)
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, "secondary-result", resultID)

	// Primary should now have it (warmed).
	resultID, found, err = primary.Probe(ctx, key)
	require.NoError(t, err)
	assert.True(t, found, "primary should have been warmed")
	assert.Equal(t, "secondary-result", resultID)
}

// ---------------------------------------------------------------------------
// Event Bus Tests
// ---------------------------------------------------------------------------

func TestEventBusSubscriptionFilter(t *testing.T) {
	bus := signal.NewBus()
	defer bus.Close()

	// Subscribe only to CacheHit events.
	sub := bus.Subscribe(16, signal.ForTypes(signal.CacheHit))

	bus.Publish(signal.Event{Type: signal.CacheMiss, Name: "miss"})
	bus.Publish(signal.Event{Type: signal.CacheHit, Name: "hit"})
	bus.Publish(signal.Event{Type: signal.VertexCompleted, Name: "done"})

	// Give events time to be delivered.
	time.Sleep(10 * time.Millisecond)

	// Only the CacheHit event should be received.
	select {
	case evt := <-sub.Events():
		assert.Equal(t, signal.CacheHit, evt.Type)
		assert.Equal(t, "hit", evt.Name)
	default:
		t.Fatal("expected to receive CacheHit event")
	}

	// No more events should be available.
	select {
	case evt := <-sub.Events():
		t.Fatalf("unexpected event: %v", evt)
	default:
		// Good.
	}
}

// ---------------------------------------------------------------------------
// Priority Queue Tests
// ---------------------------------------------------------------------------

func TestPriorityQueueOrdering(t *testing.T) {
	pq := schedule.NewPriorityQueue()

	pq.Push(&schedule.Task{VertexDigest: "c", Priority: 3, Name: "low"})
	pq.Push(&schedule.Task{VertexDigest: "a", Priority: 1, Name: "high"})
	pq.Push(&schedule.Task{VertexDigest: "b", Priority: 2, Name: "mid"})

	// Should come out in priority order: 1, 2, 3.
	t1 := pq.TryPop()
	require.NotNil(t, t1)
	assert.Equal(t, 1, t1.Priority)

	t2 := pq.TryPop()
	require.NotNil(t, t2)
	assert.Equal(t, 2, t2.Priority)

	t3 := pq.TryPop()
	require.NotNil(t, t3)
	assert.Equal(t, 3, t3.Priority)
}

// ---------------------------------------------------------------------------
// Stream Pipeline Tests
// ---------------------------------------------------------------------------

func TestStreamPipeline(t *testing.T) {
	double := stream.TransformFunc{
		Fn: func(_ context.Context, input any) (any, error) {
			return input.(int) * 2, nil
		},
		Label: "double",
	}
	addTen := stream.TransformFunc{
		Fn: func(_ context.Context, input any) (any, error) {
			return input.(int) + 10, nil
		},
		Label: "add-ten",
	}

	pipeline := stream.NewPipeline(double, addTen)
	result, err := pipeline.Process(context.Background(), 5)
	require.NoError(t, err)
	assert.Equal(t, 20, result) // (5*2) + 10
}

func TestStreamPipelineCancellation(t *testing.T) {
	slow := stream.TransformFunc{
		Fn: func(ctx context.Context, input any) (any, error) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(10 * time.Second):
				return input, nil
			}
		},
		Label: "slow",
	}

	pipeline := stream.NewPipeline(slow)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := pipeline.Process(ctx, 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "context")
}

// ---------------------------------------------------------------------------
// Benchmarks
// ---------------------------------------------------------------------------

func BenchmarkCacheProbe(b *testing.B) {
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

func BenchmarkGraphBuild(b *testing.B) {
	// Build a wide fan-out graph: 1 root → 100 leaves.
	root := newVertex("root")
	leaves := make([]solver.Edge, 100)
	for i := 0; i < 100; i++ {
		v := newVertex(fmt.Sprintf("leaf-%d", i), solver.Edge{Index: 0, Vertex: root})
		leaves[i] = solver.Edge{Index: 0, Vertex: v}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		solver.BuildGraph(leaves)
	}
}

func BenchmarkGraphDepth(b *testing.B) {
	chain := buildChain(50)
	leaf := chain[len(chain)-1]
	g := solver.BuildGraph([]solver.Edge{{Index: 0, Vertex: leaf}})

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		g.Depth(chain[25])
	}
}

func BenchmarkEventBusPublish(b *testing.B) {
	bus := signal.NewBus()
	defer bus.Close()

	// 10 subscribers.
	for i := 0; i < 10; i++ {
		sub := bus.Subscribe(1024, nil)
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

func BenchmarkSolveChainCached(b *testing.B) {
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

func BenchmarkSolveChainUncached(b *testing.B) {
	chain := buildChain(10)
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
