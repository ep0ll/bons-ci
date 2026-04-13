package dag_test

import (
	"context"
	"strings"
	"testing"

	"github.com/bons/bons-ci/plugins/rbe/pkg/dag"
	"github.com/bons/bons-ci/plugins/rbe/pkg/models"
)

// ─── In-memory metadata store for unit tests ─────────────────────────────────

type memStore struct {
	data map[string][]byte
}

func newMemStore() *memStore { return &memStore{data: map[string][]byte{}} }

func (m *memStore) Get(_ context.Context, key []byte) ([]byte, error) {
	v, ok := m.data[string(key)]
	if !ok {
		return nil, &keyNotFound{}
	}
	return v, nil
}

func (m *memStore) Put(_ context.Context, key, value []byte, _ ...interface{}) error {
	m.data[string(key)] = value
	return nil
}

func (m *memStore) Delete(_ context.Context, key []byte) error {
	delete(m.data, string(key))
	return nil
}

func (m *memStore) ScanPrefix(_ context.Context, prefix []byte, limit int) ([]interface{}, error) {
	var out []interface{}
	for k, v := range m.data {
		if strings.HasPrefix(k, string(prefix)) {
			out = append(out, struct{ Key, Value []byte }{[]byte(k), v})
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

type keyNotFound struct{}

func (e *keyNotFound) Error() string { return "key not found" }

// ─── CacheKey determinism test ────────────────────────────────────────────────

func TestComputeCacheKey_Deterministic(t *testing.T) {
	v := &models.Vertex{
		ID:        "v1",
		DAGID:     "dag1",
		OpType:    "exec",
		OpPayload: []byte(`{"cmd":["go","build","./..."]}`),
		Env:       map[string]string{"GOOS": "linux", "GOARCH": "amd64"},
		Platform:  &models.Platform{OS: "linux", Arch: "amd64"},
		InputFiles: []models.FileRef{
			{Path: "/src/main.go", Digest: "sha256:aaa"},
			{Path: "/src/go.mod", Digest: "sha256:bbb"},
		},
		Inputs: []models.VertexInput{
			{VertexID: "dep1", OutputIdx: 0, Files: []models.FileRef{
				{Path: "/lib/foo.a", Digest: "sha256:ccc"},
			}},
		},
	}

	k1 := dag.ComputeCacheKey(v)
	k2 := dag.ComputeCacheKey(v)
	if k1 != k2 {
		t.Fatalf("cache key is not deterministic: %s != %s", k1, k2)
	}
	if !strings.HasPrefix(k1, "sha256:") {
		t.Fatalf("expected sha256: prefix, got %s", k1)
	}
}

func TestComputeCacheKey_ChangesOnInputChange(t *testing.T) {
	base := &models.Vertex{
		OpType: "exec",
		InputFiles: []models.FileRef{
			{Path: "/src/main.go", Digest: "sha256:aaa"},
		},
	}
	modified := &models.Vertex{
		OpType: "exec",
		InputFiles: []models.FileRef{
			{Path: "/src/main.go", Digest: "sha256:bbb"}, // digest changed
		},
	}
	k1 := dag.ComputeCacheKey(base)
	k2 := dag.ComputeCacheKey(modified)
	if k1 == k2 {
		t.Fatal("cache key should differ when input file digest changes")
	}
}

func TestComputeCacheKey_OrderIndependent(t *testing.T) {
	v1 := &models.Vertex{
		OpType: "exec",
		InputFiles: []models.FileRef{
			{Path: "/b", Digest: "sha256:bbb"},
			{Path: "/a", Digest: "sha256:aaa"},
		},
	}
	v2 := &models.Vertex{
		OpType: "exec",
		InputFiles: []models.FileRef{
			{Path: "/a", Digest: "sha256:aaa"},
			{Path: "/b", Digest: "sha256:bbb"},
		},
	}
	k1 := dag.ComputeCacheKey(v1)
	k2 := dag.ComputeCacheKey(v2)
	if k1 != k2 {
		t.Fatal("cache key should be order-independent for input files")
	}
}

func TestComputeCacheKeyFromParts(t *testing.T) {
	key := dag.ComputeCacheKeyFromParts(
		"sha256:op",
		[]string{"sha256:file1", "sha256:file2"},
		[]string{"sha256:dep1"},
		models.Platform{OS: "linux", Arch: "amd64"},
		"",
	)
	if key == "" {
		t.Fatal("expected non-empty cache key")
	}
	// Same inputs, different selector → different key.
	key2 := dag.ComputeCacheKeyFromParts(
		"sha256:op",
		[]string{"sha256:file1", "sha256:file2"},
		[]string{"sha256:dep1"},
		models.Platform{OS: "linux", Arch: "amd64"},
		"bust",
	)
	if key == key2 {
		t.Fatal("selector should change the cache key")
	}
}
