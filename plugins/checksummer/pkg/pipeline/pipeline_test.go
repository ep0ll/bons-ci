//go:build linux

package pipeline_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bons/bons-ci/plugins/checksummer/pkg/fanotify"
	"github.com/bons/bons-ci/plugins/checksummer/pkg/pipeline"
)

func noopEvent() *fanotify.Event { return &fanotify.Event{Fd: -1} }

func TestSingleStage(t *testing.T) {
	p := pipeline.New()
	var processed int64
	p.Append(pipeline.NewStage("s1", func(_ context.Context, item *pipeline.Item) error {
		atomic.AddInt64(&processed, 1)
		return nil
	}, 1, 16))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx)

	for i := 0; i < 10; i++ {
		p.Dispatch(&pipeline.Item{Event: noopEvent()}, false)
	}
	time.Sleep(50 * time.Millisecond)
	p.Stop()

	if atomic.LoadInt64(&processed) != 10 {
		t.Errorf("expected 10 processed, got %d", atomic.LoadInt64(&processed))
	}
}

func TestMultiStageChain(t *testing.T) {
	p := pipeline.New()
	var s1, s2, s3 int64

	p.Append(pipeline.NewStage("s1", func(_ context.Context, item *pipeline.Item) error {
		atomic.AddInt64(&s1, 1)
		item.Path = "stage1"
		return nil
	}, 2, 16))
	p.Append(pipeline.NewStage("s2", func(_ context.Context, item *pipeline.Item) error {
		atomic.AddInt64(&s2, 1)
		if item.Path != "stage1" {
			return errors.New("s2: wrong path")
		}
		item.Path = "stage2"
		return nil
	}, 2, 16))
	p.Append(pipeline.NewStage("s3", func(_ context.Context, item *pipeline.Item) error {
		atomic.AddInt64(&s3, 1)
		if item.Path != "stage2" {
			return errors.New("s3: wrong path")
		}
		return nil
	}, 1, 16))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx)

	const n = 20
	for i := 0; i < n; i++ {
		p.Dispatch(&pipeline.Item{Event: noopEvent()}, false)
	}
	time.Sleep(100 * time.Millisecond)
	p.Stop()

	for _, count := range []struct {
		name string
		v    int64
	}{
		{"s1", s1}, {"s2", s2}, {"s3", s3},
	} {
		if count.v != n {
			t.Errorf("%s: want %d, got %d", count.name, n, count.v)
		}
	}
}

func TestStageErrorStopsItem(t *testing.T) {
	p := pipeline.New()
	var s2Reached int64

	p.Append(pipeline.NewStage("fail", func(_ context.Context, _ *pipeline.Item) error {
		return errors.New("intentional failure")
	}, 1, 16))
	p.Append(pipeline.NewStage("after", func(_ context.Context, _ *pipeline.Item) error {
		atomic.AddInt64(&s2Reached, 1)
		return nil
	}, 1, 16))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx)

	p.Dispatch(&pipeline.Item{Event: noopEvent()}, false)
	time.Sleep(50 * time.Millisecond)
	p.Stop()

	if atomic.LoadInt64(&s2Reached) != 0 {
		t.Error("stage after error should not be reached")
	}
}

func TestStageDisable(t *testing.T) {
	p := pipeline.New()
	var called int64

	stage := pipeline.NewStage("s", func(_ context.Context, _ *pipeline.Item) error {
		atomic.AddInt64(&called, 1)
		return nil
	}, 1, 32)
	p.Append(stage)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx)

	stage.Disable()
	p.Dispatch(&pipeline.Item{Event: noopEvent()}, false)
	time.Sleep(30 * time.Millisecond)

	if atomic.LoadInt64(&called) != 0 {
		t.Error("disabled stage should not call Fn")
	}

	stage.Enable()
	p.Dispatch(&pipeline.Item{Event: noopEvent()}, false)
	time.Sleep(30 * time.Millisecond)
	p.Stop()

	if atomic.LoadInt64(&called) != 1 {
		t.Errorf("re-enabled stage: want 1 call, got %d", atomic.LoadInt64(&called))
	}
}

func TestDispatchDrop(t *testing.T) {
	p := pipeline.New()
	var processed int64
	// Tiny buffer – will fill up quickly.
	stage := pipeline.NewStage("slow", func(_ context.Context, _ *pipeline.Item) error {
		time.Sleep(10 * time.Millisecond)
		atomic.AddInt64(&processed, 1)
		return nil
	}, 1, 1)
	p.Append(stage)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx)

	dropped := 0
	for i := 0; i < 100; i++ {
		if !p.Dispatch(&pipeline.Item{Event: noopEvent()}, true) {
			dropped++
		}
	}
	time.Sleep(200 * time.Millisecond)
	p.Stop()

	if dropped == 0 {
		t.Error("expected some drops with tiny buffer and drop=true")
	}
	t.Logf("dropped=%d processed=%d", dropped, atomic.LoadInt64(&processed))
}

func TestAppendInsertRemove(t *testing.T) {
	p := pipeline.New()

	p.Append(pipeline.NewStage("a", func(_ context.Context, _ *pipeline.Item) error { return nil }, 1, 8))
	p.Append(pipeline.NewStage("c", func(_ context.Context, _ *pipeline.Item) error { return nil }, 1, 8))

	if err := p.Insert(1, pipeline.NewStage("b", func(_ context.Context, _ *pipeline.Item) error { return nil }, 1, 8)); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	stats := p.StageStats()
	if len(stats) != 3 {
		t.Fatalf("expected 3 stages, got %d", len(stats))
	}
	if stats[0].Name != "a" || stats[1].Name != "b" || stats[2].Name != "c" {
		t.Errorf("wrong order: %v", stats)
	}

	if !p.Remove("b") {
		t.Error("Remove should return true")
	}
	stats = p.StageStats()
	if len(stats) != 2 {
		t.Fatalf("expected 2 stages after remove, got %d", len(stats))
	}
	if p.Remove("nonexistent") {
		t.Error("Remove of nonexistent should return false")
	}
}

func TestInsertOutOfRange(t *testing.T) {
	p := pipeline.New()
	p.Append(pipeline.NewStage("x", func(_ context.Context, _ *pipeline.Item) error { return nil }, 1, 8))
	if err := p.Insert(99, pipeline.NewStage("y", func(_ context.Context, _ *pipeline.Item) error { return nil }, 1, 8)); err == nil {
		t.Error("expected error for out-of-range insert")
	}
}

func TestStageStats(t *testing.T) {
	p := pipeline.New()
	p.Append(pipeline.NewStage("s1", func(_ context.Context, _ *pipeline.Item) error { return nil }, 2, 64))
	p.Append(pipeline.NewStage("s2", func(_ context.Context, _ *pipeline.Item) error { return nil }, 4, 128))

	stats := p.StageStats()
	if len(stats) != 2 {
		t.Fatalf("expected 2, got %d", len(stats))
	}
	if stats[0].BufCap != 64 || stats[1].BufCap != 128 {
		t.Errorf("wrong buf caps: %d %d", stats[0].BufCap, stats[1].BufCap)
	}
	if !stats[0].Enabled || !stats[1].Enabled {
		t.Error("stages should be enabled by default")
	}
}

func TestContextCancellationDrainsItems(t *testing.T) {
	p := pipeline.New()
	p.Append(pipeline.NewStage("s", func(_ context.Context, _ *pipeline.Item) error {
		time.Sleep(5 * time.Millisecond)
		return nil
	}, 1, 16))

	ctx, cancel := context.WithCancel(context.Background())
	p.Start(ctx)

	for i := 0; i < 5; i++ {
		p.Dispatch(&pipeline.Item{Event: noopEvent()}, false)
	}
	cancel()
	p.Stop() // must not block
}
