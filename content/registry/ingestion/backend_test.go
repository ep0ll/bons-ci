//go:build !windows

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

func TestS3Backend(t *testing.T) {
	ctx := context.Background()

	// Source and destination stores
	dir1, err := os.MkdirTemp("", "backend-src-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir1)

	dir2, err := os.MkdirTemp("", "backend-dst-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir2)

	srcStore, _ := local.NewStore(dir1)
	dstStore, _ := local.NewStore(dir2)

	backend := NewS3Backend(dstStore)

	// Add test blob to source
	data := []byte("nydus superblob")
	dgst := digest.FromBytes(data)
	size := int64(len(data))
	desc := ocispec.Descriptor{
		Digest: dgst,
		Size:   size,
	}

	w, err := srcStore.Writer(ctx, content.WithDescriptor(desc), content.WithRef("src-ref"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(w, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	if err := w.Commit(ctx, size, dgst); err != nil {
		t.Fatal(err)
	}

	// Test Check (Should not exist initially)
	hex, err := backend.Check(dgst)
	if err == nil {
		t.Fatalf("Check should have failed for non-existent blob, got hex: %v", hex)
	}

	// Test Push
	if err := backend.Push(ctx, srcStore, desc); err != nil {
		t.Fatalf("Push failed: %v", err)
	}

	// Test Check (Should exist now)
	hex, err = backend.Check(dgst)
	if err != nil {
		t.Fatalf("Check failed for existing blob: %v", err)
	}
	if hex != dgst.Hex() {
		t.Fatalf("expected hex %q, got %q", dgst.Hex(), hex)
	}

	// Test idempotency of Push
	if err := backend.Push(ctx, srcStore, desc); err != nil {
		t.Fatalf("Second Push (idempotent) failed: %v", err)
	}

	// Verify data in target store
	info, err := dstStore.Info(ctx, dgst)
	if err != nil {
		t.Fatalf("Info missing from dstStore: %v", err)
	}
	if info.Size != size {
		t.Errorf("dstStore Size mismatch: got %v, want %v", info.Size, size)
	}

	ra, err := dstStore.ReaderAt(ctx, desc)
	if err != nil {
		t.Fatalf("dstStore ReaderAt error: %v", err)
	}
	defer ra.Close()

	gotData, _ := io.ReadAll(content.NewReader(ra))
	if !bytes.Equal(gotData, data) {
		t.Fatalf("dstStore Data mismatch: %q != %q", string(gotData), string(data))
	}
}
