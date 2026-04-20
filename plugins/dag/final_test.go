package reactdag_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	dag "github.com/bons/bons-ci/plugins/dag"
)

// =========================================================================
// Observer tests
// =========================================================================

func TestObserver_ReceivesEvents(t *testing.T) {
	d, _ := buildLinearDAG(t)
	s := dag.NewScheduler(d, dag.WithWorkerCount(2))

	obs := s.Observe(dag.WithBufferSize(64))
	defer obs.Unsubscribe()

	var received []dag.Event
	done := make(chan struct{})
	go func() {
		defer close(done)
		for e := range obs.Events() {
			received = append(received, e)
		}
	}()

	if _, err := s.Build(context.Background(), "A", nil); err != nil {
		t.Fatalf("Build: %v", err)
	}
	obs.Unsubscribe()
	<-done

	if len(received) == 0 {
		t.Error("Observer received no events")
	}
}

func TestObserver_FilterByEventType(t *testing.T) {
	d, _ := buildLinearDAG(t)
	s := dag.NewScheduler(d, dag.WithWorkerCount(2))

	obs := s.Observe(
		dag.WithFilter(dag.ForEventTypes(dag.EventStateChanged)),
		dag.WithBufferSize(32),
	)
	defer obs.Unsubscribe()

	var received []dag.Event
	done := make(chan struct{})
	go func() {
		defer close(done)
		for e := range obs.Events() {
			received = append(received, e)
		}
	}()

	s.Build(context.Background(), "A", nil) //nolint:errcheck
	obs.Unsubscribe()
	<-done

	for _, e := range received {
		if e.Type != dag.EventStateChanged {
			t.Errorf("received unexpected event type %q through StateChanged filter", e.Type)
		}
	}
}

func TestObserver_FilterByVertex(t *testing.T) {
	d, _ := buildLinearDAG(t)
	s := dag.NewScheduler(d, dag.WithWorkerCount(2))

	obs := s.Observe(
		dag.WithFilter(dag.ForVertices("A")),
		dag.WithBufferSize(16),
	)
	defer obs.Unsubscribe()

	var received []dag.Event
	done := make(chan struct{})
	go func() {
		defer close(done)
		for e := range obs.Events() {
			received = append(received, e)
		}
	}()

	s.Build(context.Background(), "A", nil) //nolint:errcheck
	obs.Unsubscribe()
	<-done

	for _, e := range received {
		if e.VertexID != "A" {
			t.Errorf("received event for vertex %q through vertex-A filter", e.VertexID)
		}
	}
	if len(received) == 0 {
		t.Error("expected at least one event for vertex A")
	}
}

func TestObserver_Drain_CompletesAfterUnsubscribe(t *testing.T) {
	d, _ := buildLinearDAG(t)
	s := dag.NewScheduler(d, dag.WithWorkerCount(2))
	obs := s.Observe(dag.WithBufferSize(8))

	s.Build(context.Background(), "A", nil) //nolint:errcheck
	obs.Unsubscribe()

	done := make(chan struct{})
	go func() {
		obs.Drain()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Error("Drain did not complete after Unsubscribe")
	}
}

func TestObserver_OverflowDrop_DoesNotBlock(t *testing.T) {
	d, _ := buildLinearDAG(t)
	// Very small buffer → should drop events rather than blocking.
	s := dag.NewScheduler(d, dag.WithWorkerCount(2))
	obs := s.Observe(
		dag.WithBufferSize(1),
		dag.WithOverflowPolicy(dag.OverflowDrop),
	)
	defer func() {
		obs.Unsubscribe()
		obs.Drain()
	}()

	done := make(chan struct{})
	go func() {
		defer close(done)
		// Intentionally slow consumer.
		time.Sleep(50 * time.Millisecond)
		for range obs.Events() {
		}
	}()

	// Build should not block even though the consumer is slow.
	buildDone := make(chan error, 1)
	go func() {
		_, err := s.Build(context.Background(), "A", nil)
		buildDone <- err
	}()
	select {
	case <-buildDone:
	case <-time.After(500 * time.Millisecond):
		t.Error("Build blocked on slow Observer consumer")
	}
	obs.Unsubscribe()
	<-done
}

// =========================================================================
// EventStream tests
// =========================================================================

func TestEventStream_RoutesEventsByType(t *testing.T) {
	d, _ := buildLinearDAG(t)
	s := dag.NewScheduler(d, dag.WithWorkerCount(2))
	obs := s.Observe(dag.WithBufferSize(64))
	defer obs.Unsubscribe()

	stream := dag.NewEventStream(obs)

	var stateChanges, execEnds int
	stream.On(dag.EventStateChanged, func(_ context.Context, _ dag.Event) {
		stateChanges++
	})
	stream.On(dag.EventExecutionEnd, func(_ context.Context, _ dag.Event) {
		execEnds++
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	buildDone := make(chan struct{})
	go func() {
		s.Build(context.Background(), "A", nil) //nolint:errcheck
		obs.Unsubscribe()
		close(buildDone)
	}()

	stream.Run(ctx)
	<-buildDone

	if stateChanges == 0 {
		t.Error("expected state change events")
	}
	if execEnds == 0 {
		t.Error("expected execution end events")
	}
}

func TestEventStream_DeregisterHandler(t *testing.T) {
	d, _ := buildLinearDAG(t)
	s := dag.NewScheduler(d, dag.WithWorkerCount(2))
	obs := s.Observe(dag.WithBufferSize(32))
	stream := dag.NewEventStream(obs)

	count := 0
	deregister := stream.On(dag.EventStateChanged, func(_ context.Context, _ dag.Event) {
		count++
	})
	deregister() // remove immediately

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go func() {
		s.Build(context.Background(), "A", nil) //nolint:errcheck
		obs.Unsubscribe()
	}()
	stream.Run(ctx)

	if count != 0 {
		t.Errorf("deregistered handler was called %d times", count)
	}
}

// =========================================================================
// JSONLogger tests
// =========================================================================

func TestJSONLogger_WritesNDJSON(t *testing.T) {
	d, _ := buildLinearDAG(t)
	bus := dag.NewEventBus()

	var buf bytes.Buffer
	logger := dag.NewJSONLogger(&buf)
	logger.Subscribe(bus)
	defer logger.Unsubscribe()

	s := dag.NewScheduler(d, dag.WithEventBus(bus), dag.WithWorkerCount(2))
	if _, err := s.Build(context.Background(), "A", nil); err != nil {
		t.Fatalf("Build: %v", err)
	}

	output := buf.String()
	if output == "" {
		t.Fatal("JSONLogger produced no output")
	}

	lines := strings.Split(strings.TrimSpace(output), "\n")
	for _, line := range lines {
		if !strings.HasPrefix(line, "{") {
			t.Errorf("non-JSON line: %s", line)
		}
		if !strings.Contains(line, `"event"`) {
			t.Errorf("line missing 'event' field: %s", line)
		}
		if !strings.Contains(line, `"ts"`) {
			t.Errorf("line missing 'ts' field: %s", line)
		}
	}
}

func TestJSONLogger_IncludesVertexID(t *testing.T) {
	d, _ := buildLinearDAG(t)
	bus := dag.NewEventBus()

	var buf bytes.Buffer
	logger := dag.NewJSONLogger(&buf)
	logger.Subscribe(bus)
	defer logger.Unsubscribe()

	s := dag.NewScheduler(d, dag.WithEventBus(bus), dag.WithWorkerCount(2))
	s.Build(context.Background(), "A", nil) //nolint:errcheck

	output := buf.String()
	// At least one of A, B, C should appear as a vertex_id.
	found := strings.Contains(output, `"A"`) ||
		strings.Contains(output, `"B"`) ||
		strings.Contains(output, `"C"`)
	if !found {
		t.Errorf("JSONLogger output does not contain any vertex IDs: %s", output)
	}
}

// =========================================================================
// Timeout middleware tests
// =========================================================================

func TestPerVertexTimeoutMiddleware_AppliesLabelTimeout(t *testing.T) {
	d := dag.NewDAG()
	// slow op that respects context cancellation
	slowV := dag.NewVertex("slow", ctxSlowOp{id: "slow", sleep: 500 * time.Millisecond})
	slowV.SetLabel("timeout", "20ms")
	if err := d.AddVertex(slowV); err != nil {
		t.Fatal(err)
	}
	if err := d.Seal(); err != nil {
		t.Fatal(err)
	}

	exec := dag.Chain(
		dag.NewDefaultExecutorForTest(),
		dag.PerVertexTimeoutMiddleware(0), // per-label only
	)
	s := dag.NewScheduler(d, dag.WithExecutor(exec), dag.WithWorkerCount(1))

	start := time.Now()
	_, err := s.Build(context.Background(), "slow", nil)
	elapsed := time.Since(start)

	if err == nil {
		t.Error("expected timeout error")
	}
	if elapsed > 100*time.Millisecond {
		t.Errorf("elapsed %v; expected timeout at ~20ms", elapsed)
	}
}

func TestPerVertexTimeoutMiddleware_DefaultTimeout(t *testing.T) {
	d := dag.NewDAG()
	slow := dag.NewVertex("slow", ctxSlowOp{id: "slow", sleep: 500 * time.Millisecond})
	// no label — should use defaultTimeout
	if err := d.AddVertex(slow); err != nil {
		t.Fatal(err)
	}
	if err := d.Seal(); err != nil {
		t.Fatal(err)
	}

	exec := dag.Chain(
		dag.NewDefaultExecutorForTest(),
		dag.PerVertexTimeoutMiddleware(20*time.Millisecond),
	)
	s := dag.NewScheduler(d, dag.WithExecutor(exec), dag.WithWorkerCount(1))

	start := time.Now()
	_, err := s.Build(context.Background(), "slow", nil)
	elapsed := time.Since(start)

	if err == nil {
		t.Error("expected timeout error from default timeout")
	}
	if elapsed > 150*time.Millisecond {
		t.Errorf("elapsed %v; expected timeout at ~20ms", elapsed)
	}
}

func TestWithBuildTimeout_ExpiresCorrectly(t *testing.T) {
	d := dag.NewDAG()
	slow := dag.NewVertex("slow", ctxSlowOp{id: "slow", sleep: 500 * time.Millisecond})
	if err := d.AddVertex(slow); err != nil {
		t.Fatal(err)
	}
	if err := d.Seal(); err != nil {
		t.Fatal(err)
	}

	s := dag.NewScheduler(d, dag.WithWorkerCount(1))
	ctx, cancel := dag.WithBuildTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := s.Build(ctx, "slow", nil)
	elapsed := time.Since(start)

	if err == nil {
		t.Error("expected context deadline exceeded")
	}
	if elapsed > 200*time.Millisecond {
		t.Errorf("elapsed %v; expected cancellation at ~20ms", elapsed)
	}
}

// =========================================================================
// Stall detection tests
// =========================================================================

func TestDetectStalls_IdentifiesLongRunning(t *testing.T) {
	d := dag.NewDAG()
	v := dag.NewVertex("long", noopOp{id: "long"})
	if err := d.AddVertex(v); err != nil {
		t.Fatal(err)
	}
	if err := d.Seal(); err != nil {
		t.Fatal(err)
	}

	// Manually start the vertex to simulate it running.
	v.SetState(dag.StateFastCache, "test") //nolint:errcheck
	v.RecordStart()
	// Wait past the stall threshold.
	time.Sleep(10 * time.Millisecond)

	stalls := dag.DetectStalls(d, 5*time.Millisecond)
	if len(stalls) == 0 {
		t.Error("expected at least one stall report")
	}
	if stalls[0].VertexID != "long" {
		t.Errorf("stall vertex = %q; want long", stalls[0].VertexID)
	}
}

func TestDetectStalls_IgnoresTerminalVertices(t *testing.T) {
	d, _ := buildLinearDAG(t)
	s := dag.NewScheduler(d, dag.WithWorkerCount(2))
	if _, err := s.Build(context.Background(), "A", nil); err != nil {
		t.Fatalf("Build: %v", err)
	}

	// All vertices are terminal; none should be stalled.
	stalls := dag.DetectStalls(d, 0)
	if len(stalls) != 0 {
		t.Errorf("expected no stalls for completed build; got %v", stalls)
	}
}
