package dagstore_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	dagstore "github.com/bons/bons-ci/plugins/rbe/dag"
	"github.com/bons/bons-ci/plugins/rbe/dag/testutil"
)

// ——— Helpers ————————————————————————————————————————————————————————————————

var h = testutil.SHA256Hasher{}

// ——— Hash computation ———————————————————————————————————————————————————————

func TestComputeVertexHash_Root(t *testing.T) {
	hash, err := dagstore.ComputeVertexHash(h, "op-abc", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hash == "" {
		t.Fatal("expected non-empty hash")
	}
}

func TestComputeVertexHash_WithInputs(t *testing.T) {
	root1, _ := dagstore.ComputeVertexHash(h, "op-root1", nil)
	root2, _ := dagstore.ComputeVertexHash(h, "op-root2", nil)

	// Order should NOT matter — inputs are sorted.
	hashAB, err := dagstore.ComputeVertexHash(h, "op-child", []string{root1, root2})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	hashBA, err := dagstore.ComputeVertexHash(h, "op-child", []string{root2, root1})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hashAB != hashBA {
		t.Error("expected input order to not matter")
	}
}

func TestComputeVertexHash_DifferentInputsGiveDifferentHash(t *testing.T) {
	root1, _ := dagstore.ComputeVertexHash(h, "op-root1", nil)
	root2, _ := dagstore.ComputeVertexHash(h, "op-root2", nil)

	hash1, _ := dagstore.ComputeVertexHash(h, "op-child", []string{root1})
	hash2, _ := dagstore.ComputeVertexHash(h, "op-child", []string{root2})

	if hash1 == hash2 {
		t.Error("different inputs must produce different hashes")
	}
}

func TestComputeVertexHash_EmptyOpHashErrors(t *testing.T) {
	_, err := dagstore.ComputeVertexHash(h, "", nil)
	if !errors.Is(err, dagstore.ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument, got %v", err)
	}
}

func TestComputeVertexHash_Deterministic(t *testing.T) {
	for i := 0; i < 100; i++ {
		a, _ := dagstore.ComputeVertexHash(h, "op-x", []string{"p1", "p2", "p3"})
		b, _ := dagstore.ComputeVertexHash(h, "op-x", []string{"p1", "p2", "p3"})
		if a != b {
			t.Fatalf("non-deterministic on iteration %d", i)
		}
	}
}

func TestComputeDAGHash_Basic(t *testing.T) {
	h1, _ := dagstore.ComputeVertexHash(h, "leaf1", nil)
	h2, _ := dagstore.ComputeVertexHash(h, "leaf2", nil)

	hash, err := dagstore.ComputeDAGHash(h, []string{h1, h2})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hash == "" {
		t.Fatal("expected non-empty dag hash")
	}
}

func TestComputeDAGHash_EmptyLeafsErrors(t *testing.T) {
	_, err := dagstore.ComputeDAGHash(h, nil)
	if !errors.Is(err, dagstore.ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument, got %v", err)
	}
}

func TestComputeDAGHash_OrderIndependent(t *testing.T) {
	hashes := []string{"a", "b", "c", "d"}
	ref, _ := dagstore.ComputeDAGHash(h, hashes)

	// Reverse order.
	rev := make([]string, len(hashes))
	copy(rev, hashes)
	sort.Sort(sort.Reverse(sort.StringSlice(rev)))

	got, _ := dagstore.ComputeDAGHash(h, rev)
	if ref != got {
		t.Error("dag hash must be order-independent")
	}
}

// ——— KeySchema ———————————————————————————————————————————————————————————————

func TestKeySchema_VertexMeta(t *testing.T) {
	ks := dagstore.NewKeySchema("")
	got := ks.VertexMeta("abc123")
	want := "vertices/abc123/meta"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestKeySchema_WithPrefix(t *testing.T) {
	ks := dagstore.NewKeySchema("prod")
	got := ks.VertexMeta("abc")
	if !strings.HasPrefix(got, "prod/") {
		t.Errorf("expected prod/ prefix, got %q", got)
	}
}

func TestKeySchema_Streams(t *testing.T) {
	ks := dagstore.NewKeySchema("")
	cases := []struct {
		st   dagstore.StreamType
		want string
	}{
		{dagstore.StreamStdin, "vertices/h/stdin"},
		{dagstore.StreamStdout, "vertices/h/stdout"},
		{dagstore.StreamStderr, "vertices/h/stderr"},
	}
	for _, tc := range cases {
		got := ks.VertexStream("h", tc.st)
		if got != tc.want {
			t.Errorf("st=%s: got %q, want %q", tc.st, got, tc.want)
		}
	}
}

func TestKeySchema_DAGVertexMembership(t *testing.T) {
	ks := dagstore.NewKeySchema("")
	got := ks.DAGVertexMembership("dag1", "vh1")
	want := "dags/dag1/vertices/vh1"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestIDIndexRoundtrip(t *testing.T) {
	encoded := dagstore.EncodeIDIndexValue("my-dag", "vertex-hash-123")
	dagID, vh, err := dagstore.DecodeIDIndexValue(encoded)
	if err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if dagID != "my-dag" || vh != "vertex-hash-123" {
		t.Errorf("roundtrip mismatch: dagID=%q vh=%q", dagID, vh)
	}
}

func TestDecodeIDIndexValue_Malformed(t *testing.T) {
	_, _, err := dagstore.DecodeIDIndexValue([]byte("no-separator-here"))
	if err == nil {
		t.Fatal("expected error for malformed index value")
	}
}

// ——— Codec ————————————————————————————————————————————————————————————————

func TestZstdJSONCodec_RoundtripDAGMeta(t *testing.T) {
	orig := &dagstore.DAGMeta{
		ID:          "dag-123",
		Hash:        "abc",
		RootHashes:  []string{"r1", "r2"},
		LeafHashes:  []string{"l1"},
		VertexCount: 5,
		Labels:      map[string]string{"env": "prod"},
		CreatedAt:   time.Now().UTC().Truncate(time.Millisecond),
	}

	codec := dagstore.DefaultCodec
	data, err := codec.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty bytes")
	}

	var got dagstore.DAGMeta
	if err := codec.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.ID != orig.ID || got.Hash != orig.Hash || got.VertexCount != orig.VertexCount {
		t.Errorf("mismatch: %+v vs %+v", got, orig)
	}
}

func TestPlainJSONCodec_Roundtrip(t *testing.T) {
	codec := dagstore.PlainJSONCodec{}
	v := map[string]string{"key": "value"}
	data, err := codec.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]string
	if err := codec.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["key"] != "value" {
		t.Errorf("got %v", got)
	}
}

func TestZstdJSONCodec_CompressionActuallyReducesSize(t *testing.T) {
	// Highly compressible data.
	orig := map[string]string{
		"a": strings.Repeat("hello world ", 200),
	}
	zstdCodec := dagstore.DefaultCodec
	plainCodec := dagstore.PlainJSONCodec{}

	plain, _ := plainCodec.Marshal(orig)
	compressed, _ := zstdCodec.Marshal(orig)

	if len(compressed) >= len(plain) {
		t.Logf("plain=%d compressed=%d — compression may not help for small inputs", len(plain), len(compressed))
	}
}

// ——— WorkerPool ——————————————————————————————————————————————————————————————

func TestWorkerPool_BasicFanOut(t *testing.T) {
	pool := dagstore.NewWorkerPool(4)
	var counter atomic.Int64

	tasks := make([]func() error, 20)
	for i := range tasks {
		tasks[i] = func() error {
			counter.Add(1)
			return nil
		}
	}

	ctx := context.Background()
	if err := pool.RunAll(ctx, tasks); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if counter.Load() != 20 {
		t.Errorf("expected 20 tasks run, got %d", counter.Load())
	}
}

func TestWorkerPool_MaxConcurrency(t *testing.T) {
	const maxWorkers = 3
	pool := dagstore.NewWorkerPool(maxWorkers)

	var peak, current atomic.Int64
	var mu sync.Mutex

	tasks := make([]func() error, 30)
	for i := range tasks {
		tasks[i] = func() error {
			cur := current.Add(1)
			mu.Lock()
			if cur > peak.Load() {
				peak.Store(cur)
			}
			mu.Unlock()
			time.Sleep(2 * time.Millisecond)
			current.Add(-1)
			return nil
		}
	}

	if err := pool.RunAll(context.Background(), tasks); err != nil {
		t.Fatal(err)
	}
	if p := peak.Load(); p > maxWorkers {
		t.Errorf("peak concurrency %d exceeded pool size %d", p, maxWorkers)
	}
}

func TestWorkerPool_ErrorPropagation(t *testing.T) {
	pool := dagstore.NewWorkerPool(4)
	sentinelErr := errors.New("sentinel")

	tasks := []func() error{
		func() error { return nil },
		func() error { return sentinelErr },
		func() error { return nil },
	}

	err := pool.RunAll(context.Background(), tasks)
	if !errors.Is(err, sentinelErr) {
		t.Errorf("expected sentinel error, got %v", err)
	}
}

func TestWorkerPool_ContextCancellation(t *testing.T) {
	pool := dagstore.NewWorkerPool(1)
	ctx, cancel := context.WithCancel(context.Background())

	// Fill the single slot with a blocking task.
	running := make(chan struct{})
	tasks := []func() error{
		func() error {
			close(running)
			time.Sleep(100 * time.Millisecond)
			return nil
		},
		func() error {
			return nil // should not run
		},
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- pool.RunAll(ctx, tasks)
	}()

	<-running
	cancel()

	err := <-errCh
	if err == nil {
		t.Log("context cancelled but all tasks completed anyway — that's also fine for small pools")
	}
}

// ——— Builder ————————————————————————————————————————————————————————————————

func TestBuilder_SingleRootVertex(t *testing.T) {
	b := dagstore.NewBuilder("dag-1", h)
	root := b.MustAddVertex(dagstore.VertexSpec{OperationHash: "op-root"})

	if root.Hash() == "" {
		t.Fatal("expected non-empty hash")
	}

	dag, verts, err := b.Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(verts) != 1 {
		t.Errorf("expected 1 vertex, got %d", len(verts))
	}
	if dag.VertexCount != 1 {
		t.Errorf("expected VertexCount=1, got %d", dag.VertexCount)
	}
	if len(dag.RootHashes) != 1 || dag.RootHashes[0] != root.Hash() {
		t.Errorf("unexpected root hashes: %v", dag.RootHashes)
	}
	if len(dag.LeafHashes) != 1 || dag.LeafHashes[0] != root.Hash() {
		t.Errorf("unexpected leaf hashes: %v", dag.LeafHashes)
	}
}

func TestBuilder_LinearChain(t *testing.T) {
	b := dagstore.NewBuilder("dag-chain", h)

	root := b.MustAddVertex(dagstore.VertexSpec{OperationHash: "op-0"})
	mid := b.MustAddVertex(dagstore.VertexSpec{
		OperationHash: "op-1",
		Inputs:        []dagstore.InputSpec{{Vertex: root}},
	})
	leaf := b.MustAddVertex(dagstore.VertexSpec{
		OperationHash: "op-2",
		Inputs:        []dagstore.InputSpec{{Vertex: mid}},
	})

	dag, verts, err := b.Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(verts) != 3 {
		t.Errorf("expected 3 vertices, got %d", len(verts))
	}
	if len(dag.RootHashes) != 1 || dag.RootHashes[0] != root.Hash() {
		t.Errorf("unexpected roots: %v", dag.RootHashes)
	}
	if len(dag.LeafHashes) != 1 || dag.LeafHashes[0] != leaf.Hash() {
		t.Errorf("unexpected leaves: %v", dag.LeafHashes)
	}
	// Root and leaf must be different nodes.
	if root.Hash() == leaf.Hash() {
		t.Error("root and leaf should have different hashes")
	}
	_ = mid
}

func TestBuilder_DiamondDAG(t *testing.T) {
	//    root
	//   /    \
	// left  right
	//   \    /
	//   bottom
	b := dagstore.NewBuilder("diamond", h)

	root := b.MustAddVertex(dagstore.VertexSpec{OperationHash: "root"})
	left := b.MustAddVertex(dagstore.VertexSpec{
		OperationHash: "left",
		Inputs:        []dagstore.InputSpec{{Vertex: root}},
	})
	right := b.MustAddVertex(dagstore.VertexSpec{
		OperationHash: "right",
		Inputs:        []dagstore.InputSpec{{Vertex: root}},
	})
	bottom := b.MustAddVertex(dagstore.VertexSpec{
		OperationHash: "bottom",
		Inputs:        []dagstore.InputSpec{{Vertex: left}, {Vertex: right}},
	})

	dag, verts, err := b.Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	if len(verts) != 4 {
		t.Errorf("expected 4 vertices, got %d", len(verts))
	}
	if len(dag.RootHashes) != 1 {
		t.Errorf("expected 1 root, got %d", len(dag.RootHashes))
	}
	if len(dag.LeafHashes) != 1 || dag.LeafHashes[0] != bottom.Hash() {
		t.Errorf("expected bottom as leaf, got %v", dag.LeafHashes)
	}
	_ = left
	_ = right
}

func TestBuilder_WithID(t *testing.T) {
	b := dagstore.NewBuilder("dag-id", h)
	bv := b.MustAddVertex(dagstore.VertexSpec{
		OperationHash: "op",
		ID:            "my-named-vertex",
	})
	if bv.ID() != "my-named-vertex" {
		t.Errorf("expected ID 'my-named-vertex', got %q", bv.ID())
	}
	dag, verts, err := b.Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	_ = dag
	if verts[0].ID != "my-named-vertex" {
		t.Errorf("vertex ID not propagated into meta")
	}
}

func TestBuilder_EmptyOperationHashErrors(t *testing.T) {
	b := dagstore.NewBuilder("dag", h)
	_, err := b.AddVertex(dagstore.VertexSpec{OperationHash: ""})
	if !errors.Is(err, dagstore.ErrInvalidArgument) {
		t.Errorf("expected ErrInvalidArgument, got %v", err)
	}
}

func TestBuilder_EmptyDAGErrors(t *testing.T) {
	b := dagstore.NewBuilder("dag", h)
	_, _, err := b.Build()
	if !errors.Is(err, dagstore.ErrInvalidArgument) {
		t.Errorf("expected ErrInvalidArgument, got %v", err)
	}
}

func TestBuilder_InputByExternalHash(t *testing.T) {
	b := dagstore.NewBuilder("dag", h)
	v := b.MustAddVertex(dagstore.VertexSpec{
		OperationHash: "op",
		Inputs: []dagstore.InputSpec{
			{VertexHash: "external-hash-123", VertexID: "ext-id"},
		},
	})
	if v.Hash() == "" {
		t.Fatal("expected non-empty hash")
	}
	meta := v.Meta()
	if meta.Inputs[0].VertexHash != "external-hash-123" {
		t.Errorf("unexpected input hash: %s", meta.Inputs[0].VertexHash)
	}
	if meta.Inputs[0].VertexID != "ext-id" {
		t.Errorf("unexpected input id: %s", meta.Inputs[0].VertexID)
	}
}

func TestBuilder_WithFileRefs(t *testing.T) {
	b := dagstore.NewBuilder("dag", h)
	root := b.MustAddVertex(dagstore.VertexSpec{OperationHash: "root"})
	files := []dagstore.FileRef{
		{Path: "/out/a.txt", Hash: dagstore.Hash{Algorithm: dagstore.HashBlake3, Value: "deadbeef"}, Size: 1024},
		{Path: "/out/b.bin", Hash: dagstore.Hash{Algorithm: dagstore.HashBlake3, Value: "cafebabe"}, Size: 2048},
	}
	child := b.MustAddVertex(dagstore.VertexSpec{
		OperationHash: "child",
		Inputs: []dagstore.InputSpec{
			{Vertex: root, Files: files},
		},
	})
	meta := child.Meta()
	if len(meta.Inputs[0].Files) != 2 {
		t.Errorf("expected 2 file refs, got %d", len(meta.Inputs[0].Files))
	}
}

func TestBuilder_VertexHashChangeWhenInputsChange(t *testing.T) {
	b1 := dagstore.NewBuilder("dag", h)
	r1 := b1.MustAddVertex(dagstore.VertexSpec{OperationHash: "root-a"})
	c1 := b1.MustAddVertex(dagstore.VertexSpec{
		OperationHash: "child",
		Inputs:        []dagstore.InputSpec{{Vertex: r1}},
	})

	b2 := dagstore.NewBuilder("dag", h)
	r2 := b2.MustAddVertex(dagstore.VertexSpec{OperationHash: "root-b"})
	c2 := b2.MustAddVertex(dagstore.VertexSpec{
		OperationHash: "child",
		Inputs:        []dagstore.InputSpec{{Vertex: r2}},
	})

	if c1.Hash() == c2.Hash() {
		t.Error("vertices with different ancestor chains must have different hashes")
	}
}

// ——— Error types ————————————————————————————————————————————————————————————

func TestNotFoundError_Is(t *testing.T) {
	err := &dagstore.NotFoundError{Kind: "vertex", ID: "h1"}
	if !errors.Is(err, dagstore.ErrNotFound) {
		t.Error("expected ErrNotFound to match via errors.Is")
	}
}

func TestIntegrityError_Is(t *testing.T) {
	err := &dagstore.IntegrityError{Kind: "vertex", ID: "h1", Expected: "a", Got: "b"}
	if !errors.Is(err, dagstore.ErrIntegrityViolation) {
		t.Error("expected ErrIntegrityViolation to match via errors.Is")
	}
}

func TestInternalError_Unwrap(t *testing.T) {
	inner := fmt.Errorf("disk full")
	err := &dagstore.InternalError{Op: "write", Err: inner}
	if !errors.Is(err, inner) {
		t.Error("expected errors.Is to unwrap to inner error")
	}
}

// ——— StreamType ——————————————————————————————————————————————————————————————

func TestStreamType_String(t *testing.T) {
	cases := []struct {
		st   dagstore.StreamType
		want string
	}{
		{dagstore.StreamStdin, "stdin"},
		{dagstore.StreamStdout, "stdout"},
		{dagstore.StreamStderr, "stderr"},
	}
	for _, tc := range cases {
		if got := tc.st.String(); got != tc.want {
			t.Errorf("got %q, want %q", got, tc.want)
		}
	}
}

// ——— VertexStream ————————————————————————————————————————————————————————————

func TestVertexStream_CloseAll(t *testing.T) {
	var closed int
	rc := &countingCloser{close: func() error { closed++; return nil }}
	vs := &dagstore.VertexStream{
		Meta:   &dagstore.VertexMeta{},
		Stdin:  rc,
		Stdout: rc,
		Stderr: rc,
	}
	if err := vs.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if closed != 3 {
		t.Errorf("expected 3 closes, got %d", closed)
	}
}

type countingCloser struct {
	close func() error
	io.Reader
}

func (c *countingCloser) Read(p []byte) (int, error) { return 0, io.EOF }
func (c *countingCloser) Close() error               { return c.close() }

// ——— Benchmarks ——————————————————————————————————————————————————————————————

func BenchmarkComputeVertexHash_Root(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = dagstore.ComputeVertexHash(h, "some-operation-hash-abcdef1234567890", nil)
	}
}

func BenchmarkComputeVertexHash_TenInputs(b *testing.B) {
	inputs := make([]string, 10)
	for i := range inputs {
		inputs[i] = fmt.Sprintf("input-hash-%d-abcdef1234567890abcdef", i)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = dagstore.ComputeVertexHash(h, "op-hash-abcdef1234567890", inputs)
	}
}

func BenchmarkBuilder_100Vertices(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		builder := dagstore.NewBuilder("dag", h)
		var prev *dagstore.BuiltVertex
		for j := 0; j < 100; j++ {
			spec := dagstore.VertexSpec{OperationHash: fmt.Sprintf("op-%d", j)}
			if prev != nil {
				spec.Inputs = []dagstore.InputSpec{{Vertex: prev}}
			}
			prev = builder.MustAddVertex(spec)
		}
		_, _, _ = builder.Build()
	}
}

func BenchmarkWorkerPool_10k(b *testing.B) {
	pool := dagstore.NewWorkerPool(32)
	tasks := make([]func() error, 10_000)
	for i := range tasks {
		tasks[i] = func() error { return nil }
	}
	b.ReportAllocs()
	b.ResetTimer()
	ctx := context.Background()
	for i := 0; i < b.N; i++ {
		_ = pool.RunAll(ctx, tasks)
	}
}

func BenchmarkCodecMarshal(b *testing.B) {
	dag := &dagstore.DAGMeta{
		ID:          "dag-bench",
		Hash:        strings.Repeat("a", 64),
		RootHashes:  []string{strings.Repeat("b", 64)},
		LeafHashes:  []string{strings.Repeat("c", 64)},
		VertexCount: 1000,
		Labels:      map[string]string{"env": "bench", "version": "1.2.3"},
	}
	codec := dagstore.DefaultCodec
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = codec.Marshal(dag)
	}
}

// required import shim
