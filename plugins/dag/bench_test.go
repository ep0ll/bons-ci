package reactdag_test

import (
	"context"
	"fmt"
	"testing"

	dag "github.com/bons/bons-ci/plugins/dag"
)

// ---------------------------------------------------------------------------
// Benchmark helpers
// ---------------------------------------------------------------------------

// buildWideDAG builds a two-level DAG: one root feeding N parallel leaves.
//
//	root → leaf0, leaf1, ..., leafN-1
func buildWideDAG(b *testing.B, width int) (*dag.DAG, string) {
	b.Helper()
	d := dag.NewDAG()
	root := dag.NewVertex("root", noopOp{id: "root"})
	if err := d.AddVertex(root); err != nil {
		b.Fatal(err)
	}
	for i := range width {
		id := fmt.Sprintf("leaf%d", i)
		v := dag.NewVertex(id, noopOp{id: id})
		if err := d.AddVertex(v); err != nil {
			b.Fatal(err)
		}
		if err := d.LinkVertices("root", id); err != nil {
			b.Fatal(err)
		}
	}
	// Single target that depends on all leaves
	target := dag.NewVertex("target", noopOp{id: "target"})
	if err := d.AddVertex(target); err != nil {
		b.Fatal(err)
	}
	for i := range width {
		if err := d.LinkVertices(fmt.Sprintf("leaf%d", i), "target"); err != nil {
			b.Fatal(err)
		}
	}
	if err := d.Seal(); err != nil {
		b.Fatal(err)
	}
	return d, "target"
}

// buildDeepDAG builds a linear chain of depth vertices.
func buildDeepDAG(b *testing.B, depth int) (*dag.DAG, string) {
	b.Helper()
	d := dag.NewDAG()
	first := dag.NewVertex("v0", noopOp{id: "v0"})
	if err := d.AddVertex(first); err != nil {
		b.Fatal(err)
	}
	prev := "v0"
	for i := 1; i < depth; i++ {
		id := fmt.Sprintf("v%d", i)
		v := dag.NewVertex(id, noopOp{id: id})
		if err := d.AddVertex(v); err != nil {
			b.Fatal(err)
		}
		if err := d.LinkVertices(prev, id); err != nil {
			b.Fatal(err)
		}
		prev = id
	}
	if err := d.Seal(); err != nil {
		b.Fatal(err)
	}
	return d, prev
}

// ---------------------------------------------------------------------------
// Scheduler benchmarks
// ---------------------------------------------------------------------------

// BenchmarkScheduler_WideDAG measures throughput on a wide, shallow DAG where
// most work is parallelisable (root → N parallel leaves → target).
func BenchmarkScheduler_WideDAG(b *testing.B) {
	widths := []int{8, 32, 128}
	workers := []int{1, 4, 8, 16}

	for _, width := range widths {
		for _, w := range workers {
			b.Run(fmt.Sprintf("width=%d/workers=%d", width, w), func(b *testing.B) {
				d, target := buildWideDAG(b, width)
				s := dag.NewScheduler(d, dag.WithWorkerCount(w))
				ctx := context.Background()
				b.ResetTimer()

				for range b.N {
					// Reset all vertices between iterations.
					for _, v := range d.All() {
						v.Reset()
					}
					if _, err := s.Build(ctx, target, nil); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

// BenchmarkScheduler_DeepDAG measures throughput on a sequential chain where
// no parallelism is possible (worst case for the scheduler).
func BenchmarkScheduler_DeepDAG(b *testing.B) {
	depths := []int{10, 50, 200}

	for _, depth := range depths {
		b.Run(fmt.Sprintf("depth=%d", depth), func(b *testing.B) {
			d, target := buildDeepDAG(b, depth)
			s := dag.NewScheduler(d, dag.WithWorkerCount(4))
			ctx := context.Background()
			b.ResetTimer()

			for range b.N {
				for _, v := range d.All() {
					v.Reset()
				}
				if _, err := s.Build(ctx, target, nil); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkScheduler_CacheHit_Wide measures the fast path: all vertices serve
// from the in-memory fast cache.
func BenchmarkScheduler_CacheHit_Wide(b *testing.B) {
	d, target := buildWideDAG(b, 32)
	fastCache := dag.NewMemoryCacheStore(0)
	s := dag.NewScheduler(d,
		dag.WithWorkerCount(8),
		dag.WithFastCache(fastCache),
	)
	ctx := context.Background()

	// Prime the cache with one warm run.
	if _, err := s.Build(ctx, target, nil); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()

	for range b.N {
		for _, v := range d.All() {
			v.Reset()
		}
		if _, err := s.Build(ctx, target, nil); err != nil {
			b.Fatal(err)
		}
	}
}

// ---------------------------------------------------------------------------
// DAG algorithm benchmarks
// ---------------------------------------------------------------------------

func BenchmarkDAG_TopologicalSort(b *testing.B) {
	d, _ := buildWideDAG(b, 128)
	b.ResetTimer()
	for range b.N {
		if _, err := d.TopologicalSort(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDAG_Ancestors(b *testing.B) {
	d, target := buildDeepDAG(b, 100)
	b.ResetTimer()
	for range b.N {
		if _, err := d.Ancestors(target); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDAG_CriticalPath(b *testing.B) {
	d, target := buildDeepDAG(b, 100)
	b.ResetTimer()
	for range b.N {
		if _, err := d.CriticalPath(target); err != nil {
			b.Fatal(err)
		}
	}
}

// ---------------------------------------------------------------------------
// Cache benchmarks
// ---------------------------------------------------------------------------

func BenchmarkMemoryCacheStore_Get_Hit(b *testing.B) {
	store := dag.NewMemoryCacheStore(0)
	ctx := context.Background()
	key := dag.CacheKey{1, 2, 3}
	_ = store.Set(ctx, key, &dag.CacheEntry{})
	b.ResetTimer()
	for range b.N {
		if _, err := store.Get(ctx, key); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkMemoryCacheStore_Set(b *testing.B) {
	store := dag.NewMemoryCacheStore(0)
	ctx := context.Background()
	entry := &dag.CacheEntry{OutputFiles: []dag.FileRef{{Path: "/out/x"}}}
	b.ResetTimer()
	for i := range b.N {
		key := dag.CacheKey{byte(i), byte(i >> 8), byte(i >> 16)}
		if err := store.Set(ctx, key, entry); err != nil {
			b.Fatal(err)
		}
	}
}

// ---------------------------------------------------------------------------
// Key computer benchmarks
// ---------------------------------------------------------------------------

func BenchmarkDefaultKeyComputer_Compute(b *testing.B) {
	kc := dag.DefaultKeyComputer{}
	v := dag.NewVertex("bench", noopOp{id: "bench"})
	files := make([]dag.FileRef, 20)
	for i := range files {
		files[i] = dag.FileRef{
			Path: fmt.Sprintf("/out/file%d.o", i),
			Hash: [32]byte{byte(i)},
		}
	}
	b.ResetTimer()
	for range b.N {
		if _, err := kc.Compute(v, files); err != nil {
			b.Fatal(err)
		}
	}
}

// ---------------------------------------------------------------------------
// EventBus benchmarks
// ---------------------------------------------------------------------------

func BenchmarkEventBus_Publish_NoSubscribers(b *testing.B) {
	bus := dag.NewEventBus()
	v := dag.NewVertex("x", noopOp{id: "x"})
	ctx := context.Background()
	b.ResetTimer()
	for range b.N {
		bus.PublishStateChanged(ctx, v, dag.StateInitial, dag.StateCompleted)
	}
}

func BenchmarkEventBus_Publish_TenSubscribers(b *testing.B) {
	bus := dag.NewEventBus()
	v := dag.NewVertex("x", noopOp{id: "x"})
	ctx := context.Background()
	for range 10 {
		bus.Subscribe(dag.EventStateChanged, func(_ context.Context, _ dag.Event) {})
	}
	b.ResetTimer()
	for range b.N {
		bus.PublishStateChanged(ctx, v, dag.StateInitial, dag.StateCompleted)
	}
}
