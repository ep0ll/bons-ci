package ingestion

import (
	"bytes"
	"context"
	"io"
	"os"
	"testing"

	"github.com/bons/bons-ci/content/local"
	"github.com/containerd/containerd/v2/core/content"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

func TestFanoutStore(t *testing.T) {
	ctx := context.Background()

	// Create two temp stores
	dir1, err := os.MkdirTemp("", "store1-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir1)

	dir2, err := os.MkdirTemp("", "store2-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir2)

	store1, err := local.NewStore(dir1)
	if err != nil {
		t.Fatal(err)
	}
	store2, err := local.NewStore(dir2)
	if err != nil {
		t.Fatal(err)
	}

	fanout := NewFanoutStore(store1, store2)

	// Test data
	data := []byte("hello fanout world")
	dgst := digest.FromBytes(data)
	size := int64(len(data))
	desc := ocispec.Descriptor{
		Digest: dgst,
		Size:   size,
	}

	// Write to fanout store
	w, err := fanout.Writer(ctx, content.WithDescriptor(desc), content.WithRef("test-ref"))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := io.Copy(w, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}

	if err := w.Commit(ctx, size, dgst); err != nil {
		t.Fatal(err)
	}

	// Verify data exists in both stores
	for i, store := range []content.Store{store1, store2} {
		info, err := store.Info(ctx, dgst)
		if err != nil {
			t.Errorf("store%d: Info failed: %v", i+1, err)
			continue
		}
		if info.Size != size {
			t.Errorf("store%d: Size mismatch: %v != %v", i+1, info.Size, size)
		}

		ra, err := store.ReaderAt(ctx, desc)
		if err != nil {
			t.Errorf("store%d: ReaderAt failed: %v", i+1, err)
			continue
		}

		gotData, err := io.ReadAll(content.NewReader(ra))
		ra.Close()
		if err != nil {
			t.Errorf("store%d: ReadAll failed: %v", i+1, err)
			continue
		}

		if !bytes.Equal(gotData, data) {
			t.Errorf("store%d: Data mismatch: %q != %q", i+1, string(gotData), string(data))
		}
	}
}
