package registry

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

func TestRegistryStore_Info(t *testing.T) {
	ctx := context.Background()
	localStore := NewMockStore()
	st, err := NewStore(localStore, WithReference("docker.io/library/alpine:latest"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	
	// Create seed local content.
	blob := []byte("hello world")
	dgst := digest.FromBytes(blob)
	w, err := localStore.Writer(ctx, content.WithRef("test-ref"))
	if err != nil {
		t.Fatal(err)
	}
	w.Write(blob)
	w.Commit(ctx, int64(len(blob)), dgst)

	// Test Info retrieves local cached content successfully.
	info, err := st.Info(ctx, dgst)
	if err != nil {
		t.Fatalf("Info failed on seeded blob: %v", err)
	}
	if info.Digest != dgst {
		t.Errorf("expected %v, got %v", dgst, info.Digest)
	}
}

func TestRegistryStore_Delete(t *testing.T) {
	ctx := context.Background()
	localStore := NewMockStore()
	st, err := NewStore(localStore, WithReference("docker.io/library/alpine:latest"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	blob := []byte("to be deleted")
	dgst := digest.FromBytes(blob)
	w, err := localStore.Writer(ctx, content.WithRef("del-ref"))
	if err != nil {
		t.Fatal(err)
	}
	w.Write(blob)
	w.Commit(ctx, int64(len(blob)), dgst)

	if err := st.Delete(ctx, dgst); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	if _, err := st.Info(ctx, dgst); err == nil {
		t.Errorf("expected not found error after delete")
	}
}

func TestRegistryStore_Writer(t *testing.T) {
	// Directly tests ingester interface capabilities
	ctx := context.Background()
	localStore := NewMockStore()
	st, err := NewStore(localStore, WithReference("docker.io/library/alpine:latest"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	// This validates writer creation failure when ref/digest is missing because
	// pusher cannot determine remote endpoint. Without a mocking registry transport,
	// this should inherently error since the remote is fake.
	desc := ocispec.Descriptor{
		Digest: digest.FromString("fake"),
		Size:   4,
	}
	
	_, err = st.Writer(ctx, content.WithDescriptor(desc), content.WithRef("testwriter"))
	if err == nil {
		t.Errorf("expected error building remote pusher without registry mock")
	}
}

func TestRegistryReader(t *testing.T) {
	ctx := context.Background()
	localStore := NewMockStore()
	blob := []byte("streamed content")
	dgst := digest.FromBytes(blob)
	
	rc := io.NopCloser(bytes.NewReader(blob))
	w, err := localStore.Writer(ctx, content.WithRef("fetch-ref"))
	if err != nil {
		t.Fatal(err)
	}

	r, err := newRegistryReader(rc, w, int64(len(blob)))
	if err != nil {
		t.Fatal(err)
	}

	out, err := io.ReadAll(r.Reader())
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "streamed content" {
		t.Errorf("expected 'streamed content', got %s", string(out))
	}

	// Committing the tee-reader side
	if err := w.Commit(ctx, int64(len(blob)), dgst); err != nil {
		t.Fatalf("expected commit to succeed on cache writer: %v", err)
	}

	// Verify it wrote successfully to cache store!
	info, err := localStore.Info(ctx, dgst)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size != int64(len(blob)) {
		t.Errorf("expected size %d, got %d", len(blob), info.Size)
	}
}
