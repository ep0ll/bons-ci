package s3_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	minio "github.com/minio/minio-go/v7"
	mcreds "github.com/minio/minio-go/v7/pkg/credentials"

	dagstore "github.com/bons/bons-ci/plugins/rbe/dag"
	s3store "github.com/bons/bons-ci/plugins/rbe/dag/backend/s3"
	"github.com/bons/bons-ci/plugins/rbe/dag/testutil"
)

// ——— test env ————————————————————————————————————————————————————————————————

// S3 integration tests require:
//   MINIO_ENDPOINT   e.g. localhost:9000
//   MINIO_ACCESS_KEY (default: minioadmin)
//   MINIO_SECRET_KEY (default: minioadmin)
//   MINIO_USE_SSL    "true" to enable TLS (default: false)
//   MINIO_BUCKET     bucket to use (created automatically, default: dagstore-test)
//
// Run against a local MinIO:
//   docker run -p 9000:9000 -e MINIO_ROOT_USER=minioadmin -e MINIO_ROOT_PASSWORD=minioadmin minio/minio server /data
//   MINIO_ENDPOINT=localhost:9000 go test ./backend/s3/...

func newTestStore(t *testing.T) (*s3store.Store, string) {
	t.Helper()
	skipUnlessMinIO(t)

	endpoint := os.Getenv("MINIO_ENDPOINT")
	access := envOr("MINIO_ACCESS_KEY", "minioadmin")
	secret := envOr("MINIO_SECRET_KEY", "minioadmin")
	useSSL := os.Getenv("MINIO_USE_SSL") == "true"
	bucket := envOr("MINIO_BUCKET", "dagstore-test")

	// Ensure the bucket exists.
	client, err := minio.New(endpoint, &minio.Options{
		Creds:  mcreds.NewStaticV4(access, secret, ""),
		Secure: useSSL,
	})
	if err != nil {
		t.Fatalf("minio client: %v", err)
	}
	ctx := context.Background()
	exists, _ := client.BucketExists(ctx, bucket)
	if !exists {
		if err := client.MakeBucket(ctx, bucket, minio.MakeBucketOptions{}); err != nil {
			t.Fatalf("make bucket: %v", err)
		}
	}

	// Use a unique prefix per test to isolate runs.
	prefix := fmt.Sprintf("test-%s/", t.Name())

	cfg := s3store.Config{
		Endpoint:  endpoint,
		AccessKey: access,
		SecretKey: secret,
		UseSSL:    useSSL,
		Bucket:    bucket,
		KeyPrefix: prefix,
		Workers:   8,
	}

	store, err := s3store.New(cfg, dagstore.DefaultCodec)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	t.Cleanup(func() {
		// Best-effort cleanup of test objects.
		store.Close()
	})

	return store, bucket
}

var hasher = testutil.SHA256Hasher{}

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
		t.Fatalf("build dag: %v", err)
	}
	return dag, verts
}

// ——— Ping ————————————————————————————————————————————————————————————————————

func TestS3_Ping(t *testing.T) {
	store, _ := newTestStore(t)
	if err := store.Ping(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}
}

// ——— DAG round-trip —————————————————————————————————————————————————————————

func TestS3_PutGetDAG(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	dag, _ := buildDAG(t, "s3-dag-1")

	if err := store.PutDAG(ctx, dag); err != nil {
		t.Fatalf("put dag: %v", err)
	}

	got, err := store.GetDAG(ctx, dag.ID)
	if err != nil {
		t.Fatalf("get dag: %v", err)
	}
	if got.ID != dag.ID || got.Hash != dag.Hash {
		t.Errorf("dag mismatch: got %+v", got)
	}
}

func TestS3_GetDAGByHash(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	dag, _ := buildDAG(t, "s3-dag-hash")
	_ = store.PutDAG(ctx, dag)

	got, err := store.GetDAGByHash(ctx, dag.Hash)
	if err != nil {
		t.Fatalf("get by hash: %v", err)
	}
	if got.ID != dag.ID {
		t.Errorf("expected %q, got %q", dag.ID, got.ID)
	}
}

func TestS3_GetDAG_NotFound(t *testing.T) {
	store, _ := newTestStore(t)
	_, err := store.GetDAG(context.Background(), "no-such-dag")
	if !errors.Is(err, dagstore.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

// ——— Vertex round-trip ——————————————————————————————————————————————————————

func TestS3_PutGetVertex(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	dag, verts := buildDAG(t, "s3-vdag")

	_ = store.PutDAG(ctx, dag)
	v := verts[0]
	if err := store.PutVertex(ctx, v); err != nil {
		t.Fatalf("put vertex: %v", err)
	}

	got, err := store.GetVertex(ctx, v.DAGID, v.Hash)
	if err != nil {
		t.Fatalf("get vertex: %v", err)
	}
	if got.Hash != v.Hash {
		t.Errorf("hash mismatch")
	}
}

func TestS3_GetVertexByID(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()

	b := dagstore.NewBuilder("s3-id-dag", hasher)
	bv := b.MustAddVertex(dagstore.VertexSpec{OperationHash: "op", ID: "my-s3-id"})
	dag, verts, _ := b.Build()
	_ = bv
	_ = store.PutDAG(ctx, dag)
	for _, v := range verts {
		_ = store.PutVertex(ctx, v)
	}

	got, err := store.GetVertexByID(ctx, "my-s3-id")
	if err != nil {
		t.Fatalf("get by id: %v", err)
	}
	if got.ID != "my-s3-id" {
		t.Errorf("wrong id: %q", got.ID)
	}
}

func TestS3_ListVertices(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	dag, verts := buildDAG(t, "s3-list-dag")
	_ = store.PutDAG(ctx, dag)
	for _, v := range verts {
		_ = store.PutVertex(ctx, v)
	}

	result, err := store.ListVertices(ctx, dag.ID, dagstore.ListOptions{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(result.Items) != len(verts) {
		t.Errorf("expected %d vertices, got %d", len(verts), len(result.Items))
	}
}

// ——— Stream round-trip ——————————————————————————————————————————————————————

func TestS3_PutGetStream(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	_, verts := buildDAG(t, "s3-stream-dag")
	v := verts[0]
	payload := []byte("build output line 1\nbuild output line 2\n")

	if err := store.PutStream(ctx, v.DAGID, v.Hash, dagstore.StreamStdout,
		bytes.NewReader(payload), int64(len(payload))); err != nil {
		t.Fatalf("put stream: %v", err)
	}

	rc, err := store.GetStream(ctx, v.DAGID, v.Hash, dagstore.StreamStdout)
	if err != nil {
		t.Fatalf("get stream: %v", err)
	}
	defer rc.Close()

	got, _ := io.ReadAll(rc)
	if !bytes.Equal(got, payload) {
		t.Errorf("payload mismatch")
	}
}

func TestS3_StreamLargeBlobMultipart(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping large-blob test in short mode")
	}
	store, _ := newTestStore(t)
	ctx := context.Background()
	_, verts := buildDAG(t, "s3-multipart-dag")
	v := verts[0]

	// 12 MiB — forces MinIO to use multipart upload (threshold 5 MiB).
	const size = 12 << 20
	payload := bytes.Repeat([]byte("x"), size)

	if err := store.PutStream(ctx, v.DAGID, v.Hash, dagstore.StreamStdout,
		bytes.NewReader(payload), int64(size)); err != nil {
		t.Fatalf("put large stream: %v", err)
	}

	rc, err := store.GetStream(ctx, v.DAGID, v.Hash, dagstore.StreamStdout)
	if err != nil {
		t.Fatalf("get large stream: %v", err)
	}
	defer rc.Close()

	n, _ := io.Copy(io.Discard, rc)
	if n != size {
		t.Errorf("expected %d bytes, got %d", size, n)
	}
}

// ——— Compound ops ————————————————————————————————————————————————————————————

func TestS3_PutVertexWithStreams(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	dag, verts := buildDAG(t, "s3-vws-dag")
	_ = store.PutDAG(ctx, dag)
	v := verts[0]

	streams := map[dagstore.StreamType]dagstore.StreamPayload{
		dagstore.StreamStdout: {Reader: strings.NewReader("stdout payload"), Size: 14},
		dagstore.StreamStderr: {Reader: strings.NewReader("stderr payload"), Size: 14},
	}

	if err := store.PutVertexWithStreams(ctx, v, streams); err != nil {
		t.Fatalf("put with streams: %v", err)
	}

	got, err := store.GetVertex(ctx, v.DAGID, v.Hash)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !got.HasStdout || !got.HasStderr {
		t.Errorf("stream flags not set")
	}
}

func TestS3_GetVertexWithStreams(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	dag, verts := buildDAG(t, "s3-gvws-dag")
	_ = store.PutDAG(ctx, dag)
	v := verts[0]

	streams := map[dagstore.StreamType]dagstore.StreamPayload{
		dagstore.StreamStdin:  {Reader: strings.NewReader("in"), Size: 2},
		dagstore.StreamStdout: {Reader: strings.NewReader("out"), Size: 3},
		dagstore.StreamStderr: {Reader: strings.NewReader("err"), Size: 3},
	}
	_ = store.PutVertexWithStreams(ctx, v, streams)

	vs, err := store.GetVertexWithStreams(ctx, dag.ID, v.Hash)
	if err != nil {
		t.Fatalf("get with streams: %v", err)
	}
	defer vs.Close()

	stdin, _ := io.ReadAll(vs.Stdin)
	if string(stdin) != "in" {
		t.Errorf("stdin: %q", stdin)
	}
}

// ——— VerifyVertex ————————————————————————————————————————————————————————————

func TestS3_VerifyVertex_Valid(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	dag, verts := buildDAG(t, "s3-verify-dag")
	_ = store.PutDAG(ctx, dag)
	v := verts[0]
	_ = store.PutVertex(ctx, v)

	if err := store.VerifyVertex(ctx, dag.ID, v.Hash, hasher); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

// ——— Delete ops ——————————————————————————————————————————————————————————————

func TestS3_DeleteVertex(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	dag, verts := buildDAG(t, "s3-del-v-dag")
	_ = store.PutDAG(ctx, dag)
	v := verts[0]
	_ = store.PutVertex(ctx, v)

	if err := store.DeleteVertex(ctx, dag.ID, v.Hash); err != nil {
		t.Fatalf("delete: %v", err)
	}

	_, err := store.GetVertex(ctx, dag.ID, v.Hash)
	if !errors.Is(err, dagstore.ErrNotFound) {
		t.Errorf("expected vertex gone: %v", err)
	}
}

func TestS3_DeleteDAG_Cascade(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	dag, verts := buildDAG(t, "s3-del-dag-cascade")
	_ = store.PutDAG(ctx, dag)
	for _, v := range verts {
		_ = store.PutVertex(ctx, v)
		_ = store.PutStream(ctx, dag.ID, v.Hash, dagstore.StreamStdout,
			strings.NewReader("out"), -1)
	}

	if err := store.DeleteDAG(ctx, dag.ID, true); err != nil {
		t.Fatalf("delete: %v", err)
	}

	_, err := store.GetDAG(ctx, dag.ID)
	if !errors.Is(err, dagstore.ErrNotFound) {
		t.Errorf("dag should be gone: %v", err)
	}
}

// ——— Benchmarks ——————————————————————————————————————————————————————————————

func BenchmarkS3_PutGetVertex(b *testing.B) {
	skipUnlessMinIO(b)
	store, _ := newTestStoreTB(b)
	ctx := context.Background()
	dag, verts := buildDAG(b, "s3-bench-dag")
	_ = store.PutDAG(ctx, dag)
	v := verts[0]
	_ = store.PutVertex(ctx, v)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = store.PutVertex(ctx, v)
		_, _ = store.GetVertex(ctx, v.DAGID, v.Hash)
	}
}

// ——— helpers ————————————————————————————————————————————————————————————————

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

type skipLogger interface {
	Skip(args ...any)
}

func newTestStoreTB(tb testing.TB) (*s3store.Store, string) {
	tb.Helper()
	t, ok := tb.(*testing.T)
	if !ok {
		// For benchmarks, create a fresh T-like env.
		if os.Getenv("MINIO_ENDPOINT") == "" {
			tb.Skip("set MINIO_ENDPOINT to run S3 benchmarks")
		}
	}
	_ = t
	// Reuse newTestStore logic — minimal code duplication.
	endpoint := os.Getenv("MINIO_ENDPOINT")
	access := envOr("MINIO_ACCESS_KEY", "minioadmin")
	secret := envOr("MINIO_SECRET_KEY", "minioadmin")
	useSSL := os.Getenv("MINIO_USE_SSL") == "true"
	bucket := envOr("MINIO_BUCKET", "dagstore-test")

	prefix := fmt.Sprintf("bench-%s-%d/", tb.Name(), time.Now().UnixNano())

	cfg := s3store.Config{
		Endpoint:  endpoint,
		AccessKey: access,
		SecretKey: secret,
		UseSSL:    useSSL,
		Bucket:    bucket,
		KeyPrefix: prefix,
		Workers:   8,
	}

	store, err := s3store.New(cfg, dagstore.DefaultCodec)
	if err != nil {
		tb.Fatalf("new store: %v", err)
	}
	tb.Cleanup(func() { store.Close() })
	return store, bucket
}

func skipUnlessMinIO(tb testing.TB) {
	tb.Helper()
	if os.Getenv("MINIO_ENDPOINT") == "" {
		tb.Skip("set MINIO_ENDPOINT to run S3 integration tests")
	}
}
