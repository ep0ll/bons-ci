package memory_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	dagstore "github.com/bons/bons-ci/plugins/rbe/dag"
	"github.com/bons/bons-ci/plugins/rbe/dag/backend/memory"
	"github.com/bons/bons-ci/plugins/rbe/dag/testutil"
)

// ——— Fixtures ———————————————————————————————————————————————————————————————

var hasher = testutil.SHA256Hasher{}

func newStore() *memory.Store {
	return memory.New()
}

func buildDAG(t testing.TB, dagID string) (*dagstore.DAGMeta, []*dagstore.VertexMeta) {
	t.Helper()
	b := dagstore.NewBuilder(dagID, hasher)
	root := b.MustAddVertex(dagstore.VertexSpec{OperationHash: "op-root"})
	child := b.MustAddVertex(dagstore.VertexSpec{
		OperationHash: "op-child",
		Inputs:        []dagstore.InputSpec{{Vertex: root}},
	})
	_ = child
	dag, verts, err := b.Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return dag, verts
}

func mustStore(t testing.TB, store dagstore.Store, dag *dagstore.DAGMeta, verts []*dagstore.VertexMeta) {
	t.Helper()
	ctx := context.Background()
	if err := store.PutDAG(ctx, dag); err != nil {
		t.Fatalf("put dag: %v", err)
	}
	for _, v := range verts {
		if err := store.PutVertex(ctx, v); err != nil {
			t.Fatalf("put vertex: %v", err)
		}
	}
}

// ——— DAGStore ————————————————————————————————————————————————————————————————

func TestStore_PutGetDAG(t *testing.T) {
	s := newStore()
	ctx := context.Background()
	dag, _ := buildDAG(t, "dag-1")

	if err := s.PutDAG(ctx, dag); err != nil {
		t.Fatalf("put: %v", err)
	}

	got, err := s.GetDAG(ctx, dag.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ID != dag.ID || got.Hash != dag.Hash {
		t.Errorf("got %+v, want %+v", got, dag)
	}
}

func TestStore_GetDAGByHash(t *testing.T) {
	s := newStore()
	ctx := context.Background()
	dag, _ := buildDAG(t, "dag-hash-test")

	if err := s.PutDAG(ctx, dag); err != nil {
		t.Fatalf("put: %v", err)
	}

	got, err := s.GetDAGByHash(ctx, dag.Hash)
	if err != nil {
		t.Fatalf("get by hash: %v", err)
	}
	if got.ID != dag.ID {
		t.Errorf("wrong dag returned: %q", got.ID)
	}
}

func TestStore_GetDAG_NotFound(t *testing.T) {
	s := newStore()
	_, err := s.GetDAG(context.Background(), "nonexistent")
	if !errors.Is(err, dagstore.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestStore_PutDAG_NilErrors(t *testing.T) {
	s := newStore()
	err := s.PutDAG(context.Background(), nil)
	if !errors.Is(err, dagstore.ErrInvalidArgument) {
		t.Errorf("expected ErrInvalidArgument, got %v", err)
	}
}

func TestStore_ListDAGs(t *testing.T) {
	s := newStore()
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		dag, _ := buildDAG(t, fmt.Sprintf("dag-%d", i))
		if err := s.PutDAG(ctx, dag); err != nil {
			t.Fatal(err)
		}
	}

	result, err := s.ListDAGs(ctx, dagstore.ListOptions{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(result.Items) != 5 {
		t.Errorf("expected 5 dags, got %d", len(result.Items))
	}
}

func TestStore_DeleteDAG_NoCascade(t *testing.T) {
	s := newStore()
	ctx := context.Background()
	dag, verts := buildDAG(t, "dag-del")
	mustStore(t, s, dag, verts)

	if err := s.DeleteDAG(ctx, dag.ID, false); err != nil {
		t.Fatalf("delete: %v", err)
	}

	_, err := s.GetDAG(ctx, dag.ID)
	if !errors.Is(err, dagstore.ErrNotFound) {
		t.Errorf("expected dag gone, got %v", err)
	}

	// Vertices should still be present without cascade.
	for _, v := range verts {
		if _, err := s.GetVertex(ctx, dag.ID, v.Hash); err != nil {
			t.Errorf("vertex %s should still exist: %v", v.Hash, err)
		}
	}
}

func TestStore_DeleteDAG_WithCascade(t *testing.T) {
	s := newStore()
	ctx := context.Background()
	dag, verts := buildDAG(t, "dag-cascade")
	mustStore(t, s, dag, verts)

	// Also store streams.
	for _, v := range verts {
		if err := s.PutStream(ctx, dag.ID, v.Hash, dagstore.StreamStdout,
			strings.NewReader("hello"), -1); err != nil {
			t.Fatal(err)
		}
	}

	if err := s.DeleteDAG(ctx, dag.ID, true); err != nil {
		t.Fatalf("delete: %v", err)
	}

	for _, v := range verts {
		_, err := s.GetVertex(ctx, dag.ID, v.Hash)
		if !errors.Is(err, dagstore.ErrNotFound) {
			t.Errorf("expected vertex gone: %v", err)
		}
	}
}

// ——— VertexStore ————————————————————————————————————————————————————————————

func TestStore_PutGetVertex(t *testing.T) {
	s := newStore()
	ctx := context.Background()
	_, verts := buildDAG(t, "vdag")

	v := verts[0]
	if err := s.PutVertex(ctx, v); err != nil {
		t.Fatalf("put: %v", err)
	}

	got, err := s.GetVertex(ctx, v.DAGID, v.Hash)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Hash != v.Hash {
		t.Errorf("hash mismatch: %s vs %s", got.Hash, v.Hash)
	}
}

func TestStore_GetVertexByID(t *testing.T) {
	s := newStore()
	ctx := context.Background()

	b := dagstore.NewBuilder("dag", hasher)
	bv := b.MustAddVertex(dagstore.VertexSpec{OperationHash: "op", ID: "my-id"})
	_, _ = bv, b

	dag, verts, _ := b.Build()
	mustStore(t, s, dag, verts)

	got, err := s.GetVertexByID(ctx, "my-id")
	if err != nil {
		t.Fatalf("get by id: %v", err)
	}
	if got.ID != "my-id" {
		t.Errorf("wrong id: %s", got.ID)
	}
}

func TestStore_GetVertexByID_NotFound(t *testing.T) {
	s := newStore()
	_, err := s.GetVertexByID(context.Background(), "no-such-id")
	if !errors.Is(err, dagstore.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestStore_GetVertexByTreeHash(t *testing.T) {
	s := newStore()
	ctx := context.Background()
	_, verts := buildDAG(t, "tdag")
	v := verts[0]
	if err := s.PutVertex(ctx, v); err != nil {
		t.Fatal(err)
	}
	// For our vertices, TreeHash == Hash.
	got, err := s.GetVertexByTreeHash(ctx, v.DAGID, v.TreeHash)
	if err != nil {
		t.Fatalf("get by tree hash: %v", err)
	}
	if got.Hash != v.Hash {
		t.Errorf("wrong vertex returned")
	}
}

func TestStore_ListVertices(t *testing.T) {
	s := newStore()
	ctx := context.Background()
	dag, verts := buildDAG(t, "list-dag")
	mustStore(t, s, dag, verts)

	result, err := s.ListVertices(ctx, dag.ID, dagstore.ListOptions{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(result.Items) != len(verts) {
		t.Errorf("expected %d vertices, got %d", len(verts), len(result.Items))
	}
}

func TestStore_DeleteVertex(t *testing.T) {
	s := newStore()
	ctx := context.Background()
	dag, verts := buildDAG(t, "del-v-dag")
	mustStore(t, s, dag, verts)

	v := verts[0]
	if err := s.DeleteVertex(ctx, dag.ID, v.Hash); err != nil {
		t.Fatalf("delete: %v", err)
	}
	_, err := s.GetVertex(ctx, dag.ID, v.Hash)
	if !errors.Is(err, dagstore.ErrNotFound) {
		t.Errorf("expected vertex gone, got %v", err)
	}
}

func TestStore_IsolationBetweenDAGs(t *testing.T) {
	s := newStore()
	ctx := context.Background()

	dag1, verts1 := buildDAG(t, "dag-iso-1")
	dag2, verts2 := buildDAG(t, "dag-iso-2")
	mustStore(t, s, dag1, verts1)
	mustStore(t, s, dag2, verts2)

	r1, _ := s.ListVertices(ctx, "dag-iso-1", dagstore.ListOptions{})
	r2, _ := s.ListVertices(ctx, "dag-iso-2", dagstore.ListOptions{})

	if len(r1.Items) != len(verts1) || len(r2.Items) != len(verts2) {
		t.Error("DAG isolation violated")
	}
}

// ——— StreamStore ————————————————————————————————————————————————————————————

func TestStore_PutGetStream(t *testing.T) {
	s := newStore()
	ctx := context.Background()
	payload := []byte("hello stdout")
	_, verts := buildDAG(t, "stream-dag")
	v := verts[0]

	if err := s.PutStream(ctx, v.DAGID, v.Hash, dagstore.StreamStdout,
		bytes.NewReader(payload), int64(len(payload))); err != nil {
		t.Fatalf("put stream: %v", err)
	}

	rc, err := s.GetStream(ctx, v.DAGID, v.Hash, dagstore.StreamStdout)
	if err != nil {
		t.Fatalf("get stream: %v", err)
	}
	defer rc.Close()

	got, _ := io.ReadAll(rc)
	if !bytes.Equal(got, payload) {
		t.Errorf("payload mismatch: %q vs %q", got, payload)
	}
}

func TestStore_GetStream_NotFound(t *testing.T) {
	s := newStore()
	_, err := s.GetStream(context.Background(), "dag", "vertex", dagstore.StreamStdin)
	if !errors.Is(err, dagstore.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestStore_DeleteStream(t *testing.T) {
	s := newStore()
	ctx := context.Background()
	_, verts := buildDAG(t, "del-stream-dag")
	v := verts[0]

	_ = s.PutStream(ctx, v.DAGID, v.Hash, dagstore.StreamStderr,
		strings.NewReader("err output"), -1)
	_ = s.DeleteStream(ctx, v.DAGID, v.Hash, dagstore.StreamStderr)

	_, err := s.GetStream(ctx, v.DAGID, v.Hash, dagstore.StreamStderr)
	if !errors.Is(err, dagstore.ErrNotFound) {
		t.Errorf("expected stream gone, got %v", err)
	}
}

func TestStore_AllThreeStreams(t *testing.T) {
	s := newStore()
	ctx := context.Background()
	_, verts := buildDAG(t, "streams3-dag")
	v := verts[0]

	payloads := map[dagstore.StreamType][]byte{
		dagstore.StreamStdin:  []byte("stdin data"),
		dagstore.StreamStdout: []byte("stdout data"),
		dagstore.StreamStderr: []byte("stderr data"),
	}

	for st, data := range payloads {
		if err := s.PutStream(ctx, v.DAGID, v.Hash, st, bytes.NewReader(data), int64(len(data))); err != nil {
			t.Fatalf("put %s: %v", st, err)
		}
	}

	for st, want := range payloads {
		rc, err := s.GetStream(ctx, v.DAGID, v.Hash, st)
		if err != nil {
			t.Fatalf("get %s: %v", st, err)
		}
		got, _ := io.ReadAll(rc)
		rc.Close()
		if !bytes.Equal(got, want) {
			t.Errorf("%s payload mismatch", st)
		}
	}
}

// ——— Compound operations ————————————————————————————————————————————————————

func TestStore_PutVertexWithStreams(t *testing.T) {
	s := newStore()
	ctx := context.Background()
	_, verts := buildDAG(t, "vws-dag")
	v := verts[0]

	streams := map[dagstore.StreamType]dagstore.StreamPayload{
		dagstore.StreamStdout: {Reader: strings.NewReader("build output"), Size: -1},
		dagstore.StreamStderr: {Reader: strings.NewReader("warnings"), Size: -1},
	}

	if err := s.PutVertexWithStreams(ctx, v, streams); err != nil {
		t.Fatalf("put with streams: %v", err)
	}

	got, err := s.GetVertex(ctx, v.DAGID, v.Hash)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !got.HasStdout || !got.HasStderr {
		t.Errorf("stream flags not set: stdout=%v stderr=%v", got.HasStdout, got.HasStderr)
	}
	if got.HasStdin {
		t.Error("stdin should not be set")
	}
}

func TestStore_GetVertexWithStreams_AllPresent(t *testing.T) {
	s := newStore()
	ctx := context.Background()
	_, verts := buildDAG(t, "gvws-dag")
	v := verts[0]

	streams := map[dagstore.StreamType]dagstore.StreamPayload{
		dagstore.StreamStdin:  {Reader: strings.NewReader("in"), Size: 2},
		dagstore.StreamStdout: {Reader: strings.NewReader("out"), Size: 3},
		dagstore.StreamStderr: {Reader: strings.NewReader("err"), Size: 3},
	}
	if err := s.PutVertexWithStreams(ctx, v, streams); err != nil {
		t.Fatal(err)
	}

	vs, err := s.GetVertexWithStreams(ctx, v.DAGID, v.Hash)
	if err != nil {
		t.Fatalf("get with streams: %v", err)
	}
	defer vs.Close()

	if vs.Stdin == nil || vs.Stdout == nil || vs.Stderr == nil {
		t.Error("expected all three streams to be present")
	}

	stdin, _ := io.ReadAll(vs.Stdin)
	if string(stdin) != "in" {
		t.Errorf("stdin: %q", stdin)
	}
}

// ——— VerifyVertex ————————————————————————————————————————————————————————————

func TestStore_VerifyVertex_ValidHash(t *testing.T) {
	s := newStore()
	ctx := context.Background()
	_, verts := buildDAG(t, "verify-dag")
	v := verts[0]
	_ = s.PutVertex(ctx, v)

	if err := s.VerifyVertex(ctx, v.DAGID, v.Hash, hasher); err != nil {
		t.Fatalf("verify failed for valid vertex: %v", err)
	}
}

func TestStore_VerifyVertex_TamperedHash(t *testing.T) {
	s := newStore()
	ctx := context.Background()
	_, verts := buildDAG(t, "tamper-dag")
	v := verts[0]
	_ = s.PutVertex(ctx, v)

	// Tamper: modify the stored operation hash without updating the vertex hash.
	tampered := *v
	tampered.OperationHash = "tampered-operation"
	_ = s.PutVertex(ctx, &tampered) // writes under the same vertex hash key

	err := s.VerifyVertex(ctx, v.DAGID, v.Hash, hasher)
	if !errors.Is(err, dagstore.ErrIntegrityViolation) {
		t.Errorf("expected ErrIntegrityViolation, got %v", err)
	}
}

// ——— Lifecycle ——————————————————————————————————————————————————————————————

func TestStore_PingOK(t *testing.T) {
	s := newStore()
	if err := s.Ping(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}
}

func TestStore_CloseAndReject(t *testing.T) {
	s := newStore()
	_ = s.Close()

	err := s.PutDAG(context.Background(), &dagstore.DAGMeta{ID: "x"})
	if !errors.Is(err, dagstore.ErrClosed) {
		t.Errorf("expected ErrClosed after Close(), got %v", err)
	}
}

// ——— Isolation / deep-copy ——————————————————————————————————————————————————

func TestStore_MutatingReturnedMetaDoesNotAffectStore(t *testing.T) {
	s := newStore()
	ctx := context.Background()
	dag, _ := buildDAG(t, "copy-dag")
	_ = s.PutDAG(ctx, dag)

	got, _ := s.GetDAG(ctx, dag.ID)
	got.Hash = "mutated"

	// Re-fetch — should be unchanged.
	got2, _ := s.GetDAG(ctx, dag.ID)
	if got2.Hash == "mutated" {
		t.Error("store returned mutable reference — deep-copy missing")
	}
}

// ——— Concurrency ————————————————————————————————————————————————————————————

func TestStore_ConcurrentPutGet(t *testing.T) {
	s := newStore()
	ctx := context.Background()

	const numDAGs = 50
	var wg sync.WaitGroup
	errors_ := make(chan error, numDAGs*2)

	for i := 0; i < numDAGs; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			dag, verts := buildDAG(t, fmt.Sprintf("concurrent-dag-%d", i))
			if err := s.PutDAG(ctx, dag); err != nil {
				errors_ <- err
				return
			}
			for _, v := range verts {
				if err := s.PutVertex(ctx, v); err != nil {
					errors_ <- err
				}
			}
		}()
	}

	wg.Wait()
	close(errors_)

	for err := range errors_ {
		t.Errorf("concurrent error: %v", err)
	}

	result, _ := s.ListDAGs(ctx, dagstore.ListOptions{})
	if len(result.Items) != numDAGs {
		t.Errorf("expected %d dags, got %d", numDAGs, len(result.Items))
	}
}

func TestStore_ConcurrentStreamWrites(t *testing.T) {
	s := newStore()
	ctx := context.Background()
	_, verts := buildDAG(t, "concurrent-streams")
	v := verts[0]

	var wrote atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			data := fmt.Appendf(nil, "payload-%d", i)
			if err := s.PutStream(ctx, v.DAGID, v.Hash, dagstore.StreamStdout,
				bytes.NewReader(data), int64(len(data))); err == nil {
				wrote.Add(1)
			}
		}()
	}
	wg.Wait()
	// At least one write must have succeeded; the last one wins.
	if wrote.Load() == 0 {
		t.Error("no stream writes succeeded")
	}
}

// ——— Benchmarks ——————————————————————————————————————————————————————————————

func BenchmarkStore_PutVertex(b *testing.B) {
	s := newStore()
	ctx := context.Background()
	dag, _ := buildDAG(b, "bench-dag")
	_ = s.PutDAG(ctx, dag)

	builder := dagstore.NewBuilder("bench-dag", hasher)
	verts := make([]*dagstore.VertexMeta, b.N)
	for i := 0; i < b.N; i++ {
		bv := builder.MustAddVertex(dagstore.VertexSpec{OperationHash: fmt.Sprintf("op-%d", i)})
		verts[i] = func() *dagstore.VertexMeta { m := bv.Meta(); return &m }()
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = s.PutVertex(ctx, verts[i])
	}
}

func BenchmarkStore_GetVertex(b *testing.B) {
	s := newStore()
	ctx := context.Background()
	dag, verts := buildDAG(b, "bench-get-dag")
	mustStore(b, s, dag, verts)
	v := verts[0]

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = s.GetVertex(ctx, v.DAGID, v.Hash)
	}
}

func BenchmarkStore_PutGetStream_1MB(b *testing.B) {
	s := newStore()
	ctx := context.Background()
	_, verts := buildDAG(b, "stream-bench")
	v := verts[0]
	payload := make([]byte, 1<<20) // 1 MiB

	b.ReportAllocs()
	b.SetBytes(int64(len(payload)))
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_ = s.PutStream(ctx, v.DAGID, v.Hash, dagstore.StreamStdout,
			bytes.NewReader(payload), int64(len(payload)))
		rc, _ := s.GetStream(ctx, v.DAGID, v.Hash, dagstore.StreamStdout)
		if rc != nil {
			_, _ = io.Copy(io.Discard, rc)
			rc.Close()
		}
	}
}
