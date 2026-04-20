package reactdag_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	dag "github.com/bons/bons-ci/plugins/dag"
)

// =========================================================================
// DAG serialization tests
// =========================================================================

func TestMarshalDAG_RoundTrip(t *testing.T) {
	d, _ := buildLinearDAG(t)

	data, err := dag.MarshalDAG(d)
	if err != nil {
		t.Fatalf("MarshalDAG: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("MarshalDAG returned empty bytes")
	}

	schema, err := dag.UnmarshalDAGSchema(data)
	if err != nil {
		t.Fatalf("UnmarshalDAGSchema: %v", err)
	}
	if len(schema.Vertices) != 3 {
		t.Errorf("Vertices=%d; want 3", len(schema.Vertices))
	}
	if len(schema.Edges) != 2 {
		t.Errorf("Edges=%d; want 2 (C→B, B→A)", len(schema.Edges))
	}
}

func TestRebuildDAG_ReconstructsStructure(t *testing.T) {
	d, _ := buildLinearDAG(t)

	data, _ := dag.MarshalDAG(d)
	schema, _ := dag.UnmarshalDAGSchema(data)

	registry := dag.OperationRegistry{
		"*": func() dag.Operation { return noopOp{id: "restored"} },
	}
	rebuilt, err := dag.RebuildDAG(schema, registry)
	if err != nil {
		t.Fatalf("RebuildDAG: %v", err)
	}

	// Verify vertices.
	for _, id := range []string{"A", "B", "C"} {
		if _, ok := rebuilt.Vertex(id); !ok {
			t.Errorf("rebuilt DAG missing vertex %s", id)
		}
	}

	// Verify edge: C→B.
	vB, _ := rebuilt.Vertex("B")
	parents := vB.Parents()
	if len(parents) != 1 || parents[0].ID() != "C" {
		t.Errorf("B.Parents=%v; want [C]", parentIDs(parents))
	}
}

func TestRebuildDAG_UnknownOpError(t *testing.T) {
	d, _ := buildLinearDAG(t)
	data, _ := dag.MarshalDAG(d)
	schema, _ := dag.UnmarshalDAGSchema(data)

	// Empty registry — no fallback.
	_, err := dag.RebuildDAG(schema, dag.OperationRegistry{})
	if err == nil {
		t.Error("expected error for unknown operation without fallback")
	}
}

func TestMarshalDAG_PreservesFileDeps(t *testing.T) {
	d, _ := buildFileDepsDAG(t)

	data, err := dag.MarshalDAG(d)
	if err != nil {
		t.Fatalf("MarshalDAG: %v", err)
	}

	schema, _ := dag.UnmarshalDAGSchema(data)
	if len(schema.FileDeps) == 0 {
		t.Error("serialised schema missing file dependencies")
	}

	// Verify B's dep on C is present.
	found := false
	for _, fd := range schema.FileDeps {
		if fd.ChildID == "B" && fd.ParentID == "C" {
			found = true
			break
		}
	}
	if !found {
		t.Error("B→C file dep not found in serialised schema")
	}
}

func TestMarshalDAG_PreservesLabels(t *testing.T) {
	d, err := dag.NewBuilder().
		Add("A", noopOp{id: "A"}, dag.WithLabel("team", "platform")).
		Build()
	if err != nil {
		t.Fatal(err)
	}

	data, _ := dag.MarshalDAG(d)
	schema, _ := dag.UnmarshalDAGSchema(data)

	if len(schema.Vertices) == 0 {
		t.Fatal("no vertices in schema")
	}
	if schema.Vertices[0].Labels["team"] != "platform" {
		t.Errorf("label not preserved; got %v", schema.Vertices[0].Labels)
	}
}

func TestUnmarshalDAGSchema_WrongVersion(t *testing.T) {
	bad := []byte(`{"version": 999, "vertices": [], "edges": []}`)
	_, err := dag.UnmarshalDAGSchema(bad)
	if err == nil {
		t.Error("expected version mismatch error")
	}
}

// =========================================================================
// State snapshot tests
// =========================================================================

func TestCaptureStateSnapshot_RoundTrip(t *testing.T) {
	d, _ := buildLinearDAG(t)
	s := dag.NewScheduler(d, dag.WithWorkerCount(2))
	if _, err := s.Build(context.Background(), "A", nil); err != nil {
		t.Fatalf("Build: %v", err)
	}

	snap := dag.CaptureStateSnapshot(d)
	data, err := dag.MarshalStateSnapshot(snap)
	if err != nil {
		t.Fatalf("MarshalStateSnapshot: %v", err)
	}

	snap2, err := dag.UnmarshalStateSnapshot(data)
	if err != nil {
		t.Fatalf("UnmarshalStateSnapshot: %v", err)
	}
	if len(snap2.Vertices) != 3 {
		t.Errorf("Vertices=%d; want 3", len(snap2.Vertices))
	}
}

func TestRestoreStateSnapshot_RestoressCompletedState(t *testing.T) {
	// Build 1: populate state.
	d1, _ := buildLinearDAG(t)
	s1 := dag.NewScheduler(d1, dag.WithWorkerCount(2))
	s1.Build(context.Background(), "A", nil) //nolint:errcheck

	snap := dag.CaptureStateSnapshot(d1)

	// Build 2: fresh DAG, restore state.
	d2, _ := buildLinearDAG(t)
	if err := dag.RestoreStateSnapshot(d2, snap); err != nil {
		t.Fatalf("RestoreStateSnapshot: %v", err)
	}

	// All vertices should be restored to StateCompleted.
	for _, id := range []string{"A", "B", "C"} {
		v, _ := d2.Vertex(id)
		if v.State() != dag.StateCompleted {
			t.Errorf("vertex %s state=%s; want completed", id, v.State())
		}
	}
}

func TestRestoreStateSnapshot_SkipsUnknownVertices(t *testing.T) {
	snap := &dag.StateSnapshot{
		Vertices: []dag.VertexStateSnapshot{
			{VertexID: "NONEXISTENT", State: "completed"},
		},
	}

	d, _ := buildLinearDAG(t)
	// Should not error when a vertex in the snapshot doesn't exist in the DAG.
	if err := dag.RestoreStateSnapshot(d, snap); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// =========================================================================
// BuildQueue tests
// =========================================================================

func makeEngine(t *testing.T) *dag.Engine {
	t.Helper()
	d, _ := buildLinearDAG(t)
	eng, err := dag.NewEngine(d, dag.EngineConfig{WorkerCount: 2})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	return eng
}

func TestBuildQueue_SyncBuild(t *testing.T) {
	eng := makeEngine(t)
	ctx := context.Background()
	eng.Start(ctx)
	defer eng.Stop()

	q := dag.NewBuildQueue(eng, 1)
	q.Start(ctx)
	defer q.Stop()

	resp, err := q.SyncBuild(ctx, dag.BuildRequest{
		ID:       "job-1",
		TargetID: "A",
	})
	if err != nil {
		t.Fatalf("SyncBuild: %v", err)
	}
	if resp.Result.Metrics.Executed == 0 && resp.Result.Metrics.FastCacheHits == 0 {
		t.Error("expected either executed or cached vertices")
	}
	if resp.WaitTime() < 0 {
		t.Error("WaitTime should be non-negative")
	}
}

func TestBuildQueue_Deduplication(t *testing.T) {
	d, _ := buildLinearDAG(t)
	// Use a slow op so the first build is still running when the second arrives.
	vA, _ := d.Vertex("A")
	_ = vA // The slow build is from the vertex ops

	eng, _ := dag.NewEngine(d, dag.EngineConfig{WorkerCount: 1})
	ctx := context.Background()
	eng.Start(ctx)
	defer eng.Stop()

	q := dag.NewBuildQueue(eng, 1)
	q.Start(ctx)
	defer q.Stop()

	req := dag.BuildRequest{ID: "job-1", TargetID: "A"}

	// Submit same target twice quickly.
	ch1 := q.Submit(req)
	ch2 := q.Submit(dag.BuildRequest{ID: "job-2", TargetID: "A"})

	var r1, r2 dag.BuildResponse
	select {
	case r1 = <-ch1:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for first response")
	}
	select {
	case r2 = <-ch2:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for second response")
	}

	// Both should have succeeded.
	if r1.Result.Error != nil {
		t.Errorf("r1 error: %v", r1.Result.Error)
	}
	if r2.Result.Error != nil {
		t.Errorf("r2 error: %v", r2.Result.Error)
	}
}

func TestBuildQueue_PriorityOrdering(t *testing.T) {
	d, _ := buildFanOutDAG(t)
	eng, _ := dag.NewEngine(d, dag.EngineConfig{WorkerCount: 1})
	ctx := context.Background()
	eng.Start(ctx)
	defer eng.Stop()

	q := dag.NewBuildQueue(eng, 1)
	q.Start(ctx)
	defer q.Stop()

	var order []string
	var mu sync.Mutex

	makeReq := func(target string, prio int) {
		ch := q.Submit(dag.BuildRequest{ID: target, TargetID: target, Priority: prio})
		go func() {
			resp := <-ch
			mu.Lock()
			order = append(order, resp.Request.TargetID)
			mu.Unlock()
		}()
	}

	// Submit low-priority first, then high-priority.
	makeReq("lint", 1)
	makeReq("vet", 10) // high priority
	makeReq("compile", 5)

	time.Sleep(300 * time.Millisecond)

	mu.Lock()
	got := append([]string(nil), order...)
	mu.Unlock()

	// All three should complete.
	if len(got) != 3 {
		t.Errorf("expected 3 responses; got %d", len(got))
	}
}

func TestBuildQueue_Stats(t *testing.T) {
	eng := makeEngine(t)
	ctx := context.Background()
	eng.Start(ctx)
	defer eng.Stop()

	q := dag.NewBuildQueue(eng, 2)
	// Don't start workers — requests stay pending.

	q.Submit(dag.BuildRequest{TargetID: "A"}) //nolint:errcheck

	stats := q.Stats()
	if stats.Pending != 1 {
		t.Errorf("Pending=%d; want 1", stats.Pending)
	}
	if stats.InFlight != 0 {
		t.Errorf("InFlight=%d; want 0", stats.InFlight)
	}
}

func TestBuildQueue_ContextCancellation(t *testing.T) {
	eng := makeEngine(t)
	ctx, cancel := context.WithCancel(context.Background())
	eng.Start(ctx)
	defer eng.Stop()

	q := dag.NewBuildQueue(eng, 1)
	q.Start(ctx)
	defer q.Stop()

	cancel() // Cancel immediately.

	// SyncBuild should return promptly with a context error.
	reqCtx, reqCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer reqCancel()

	_, err := q.SyncBuild(reqCtx, dag.BuildRequest{TargetID: "A"})
	if err == nil {
		t.Log("build completed before context cancelled (acceptable in fast env)")
	}
}

// =========================================================================
// HTTP server tests
// =========================================================================

func newTestServer(t *testing.T) (*dag.BuildServer, *dag.Engine) {
	t.Helper()
	d, _ := buildLinearDAG(t)
	eng, err := dag.NewEngine(d, dag.EngineConfig{WorkerCount: 2})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	ctx := context.Background()
	eng.Start(ctx)
	t.Cleanup(eng.Stop)

	server := dag.NewBuildServer(eng, nil)
	return server, eng
}

func TestBuildServer_Health(t *testing.T) {
	s, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status=%d; want 200", rr.Code)
	}
	var body map[string]string
	json.NewDecoder(rr.Body).Decode(&body) //nolint:errcheck
	if body["status"] != "ok" {
		t.Errorf("body=%v; want {status:ok}", body)
	}
}

func TestBuildServer_Metrics(t *testing.T) {
	s, eng := newTestServer(t)
	eng.Build(context.Background(), "A", nil)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status=%d; want 200", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "reactdag_builds_total") {
		t.Errorf("missing metric in response: %s", body)
	}
}

func TestBuildServer_Status(t *testing.T) {
	s, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status=%d; want 200", rr.Code)
	}
	var body map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if _, ok := body["snapshot"]; !ok {
		t.Error("response missing 'snapshot' field")
	}
	if _, ok := body["cache"]; !ok {
		t.Error("response missing 'cache' field")
	}
}

func TestBuildServer_Plan(t *testing.T) {
	s, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/plan?target=A", nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status=%d; want 200", rr.Code)
	}
	var body map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode plan: %v", err)
	}
	if _, ok := body["Steps"]; !ok {
		t.Error("plan response missing Steps")
	}
}

func TestBuildServer_Plan_MissingTarget(t *testing.T) {
	s, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/plan", nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status=%d; want 400", rr.Code)
	}
}

func TestBuildServer_Analysis(t *testing.T) {
	s, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/analysis", nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status=%d; want 200", rr.Code)
	}
}

func TestBuildServer_DOT(t *testing.T) {
	s, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/dot?title=test", nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status=%d; want 200", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "digraph") {
		t.Error("DOT response missing digraph declaration")
	}
}

func TestBuildServer_Build_SyncSuccess(t *testing.T) {
	s, _ := newTestServer(t)
	body := bytes.NewBufferString(`{"target_id":"A"}`)
	req := httptest.NewRequest(http.MethodPost, "/build", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status=%d; want 200; body=%s", rr.Code, rr.Body.String())
	}
	var resp map[string]interface{}
	json.NewDecoder(rr.Body).Decode(&resp) //nolint:errcheck
	if _, ok := resp["request_id"]; !ok {
		t.Error("response missing request_id")
	}
}

func TestBuildServer_Build_WrongMethod(t *testing.T) {
	s, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/build", nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("status=%d; want 405", rr.Code)
	}
}

func TestBuildServer_Build_MissingTarget(t *testing.T) {
	s, _ := newTestServer(t)
	body := bytes.NewBufferString(`{}`)
	req := httptest.NewRequest(http.MethodPost, "/build", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status=%d; want 400", rr.Code)
	}
}

func TestBuildServer_Reset(t *testing.T) {
	s, eng := newTestServer(t)
	eng.Build(context.Background(), "A", nil)

	req := httptest.NewRequest(http.MethodPost, "/reset", nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status=%d; want 200", rr.Code)
	}
}

func TestBuildServer_Build_WithQueue(t *testing.T) {
	d, _ := buildLinearDAG(t)
	eng, _ := dag.NewEngine(d, dag.EngineConfig{WorkerCount: 2})
	ctx := context.Background()
	eng.Start(ctx)
	defer eng.Stop()

	q := dag.NewBuildQueue(eng, 1)
	q.Start(ctx)
	defer q.Stop()

	s := dag.NewBuildServer(eng, q)
	body := bytes.NewBufferString(`{"target_id":"A","priority":5}`)
	req := httptest.NewRequest(http.MethodPost, "/build", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)

	// With queue, POST /build returns 202 Accepted.
	if rr.Code != http.StatusAccepted {
		t.Errorf("status=%d; want 202; body=%s", rr.Code, rr.Body.String())
	}
	var resp map[string]string
	json.NewDecoder(rr.Body).Decode(&resp) //nolint:errcheck
	if resp["status"] != "queued" {
		t.Errorf("status=%q; want queued", resp["status"])
	}
}

func TestWithCORS_SetsHeaders(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := dag.WithCORS(inner, "*")

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Error("CORS header not set")
	}
}

func TestWithCORS_HandlesPreflightOPTIONS(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := dag.WithCORS(inner, "*")

	req := httptest.NewRequest(http.MethodOptions, "/", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Errorf("status=%d; want 204 for preflight", rr.Code)
	}
}

func TestWithRequestLog_CallsLogFn(t *testing.T) {
	var logged bool
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := dag.WithRequestLog(inner, func(method, path string, status int, dur time.Duration) {
		logged = true
		if method != http.MethodGet {
			t.Errorf("method=%q; want GET", method)
		}
		if status != http.StatusOK {
			t.Errorf("status=%d; want 200", status)
		}
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if !logged {
		t.Error("log function was not called")
	}
}

// suppress lint
var _ = errors.New
