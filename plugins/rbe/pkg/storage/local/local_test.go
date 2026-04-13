package local_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/bons/bons-ci/plugins/rbe/pkg/storage"
	"github.com/bons/bons-ci/plugins/rbe/pkg/storage/local"
)

func newTestStore(t *testing.T) *local.Store {
	t.Helper()
	s, err := local.New(context.Background(), local.Config{Root: t.TempDir()})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return s
}

func TestPutGetDelete(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	digest := "sha256:" + strings.Repeat("a", 64)
	data := []byte("hello, rbed!")

	// Put
	if err := s.Put(ctx, digest, bytes.NewReader(data), int64(len(data)), storage.PutOptions{}); err != nil {
		t.Fatalf("put: %v", err)
	}

	// Stat
	info, err := s.Stat(ctx, digest)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Size != int64(len(data)) {
		t.Errorf("stat size: want %d, got %d", len(data), info.Size)
	}

	// Get
	rc, size, err := s.Get(ctx, digest, storage.GetOptions{})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer rc.Close()
	got := make([]byte, size)
	if _, err := rc.Read(got); err != nil && err.Error() != "EOF" {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Errorf("data mismatch: want %q, got %q", data, got)
	}

	// Delete
	if err := s.Delete(ctx, digest); err != nil {
		t.Fatalf("delete: %v", err)
	}

	// Should be gone
	if _, _, err := s.Get(ctx, digest, storage.GetOptions{}); err == nil {
		t.Fatal("expected error after delete")
	}
}

func TestExists(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	digest := "sha256:" + strings.Repeat("b", 64)
	ok, _, _ := s.Exists(ctx, digest)
	if ok {
		t.Fatal("expected not exists")
	}

	s.Put(ctx, digest, bytes.NewReader([]byte("x")), 1, storage.PutOptions{}) //nolint:errcheck
	ok, size, err := s.Exists(ctx, digest)
	if err != nil {
		t.Fatalf("exists: %v", err)
	}
	if !ok {
		t.Fatal("expected exists")
	}
	if size != 1 {
		t.Errorf("size: want 1, got %d", size)
	}
}

func TestRangeGet(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	digest := "sha256:" + strings.Repeat("c", 64)
	data := []byte("0123456789")
	s.Put(ctx, digest, bytes.NewReader(data), int64(len(data)), storage.PutOptions{}) //nolint:errcheck

	rc, size, err := s.Get(ctx, digest, storage.GetOptions{Offset: 3, Length: 4})
	if err != nil {
		t.Fatalf("range get: %v", err)
	}
	defer rc.Close()
	if size != 4 {
		t.Errorf("range size: want 4, got %d", size)
	}
	got := make([]byte, 4)
	rc.Read(got) //nolint:errcheck
	if !bytes.Equal(got, data[3:7]) {
		t.Errorf("range data: want %q, got %q", data[3:7], got)
	}
}

func TestChunkedUpload(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	uploadID := "upload-test-1"
	if err := s.InitiateUpload(ctx, uploadID, nil); err != nil {
		t.Fatalf("initiate: %v", err)
	}

	part1 := []byte("hello ")
	part2 := []byte("world!")
	s.UploadPart(ctx, uploadID, 1, bytes.NewReader(part1), int64(len(part1))) //nolint:errcheck
	s.UploadPart(ctx, uploadID, 2, bytes.NewReader(part2), int64(len(part2))) //nolint:errcheck

	digest := "sha256:" + strings.Repeat("d", 64)
	err := s.CompleteUpload(ctx, uploadID, digest, []storage.Part{
		{Number: 1, Size: int64(len(part1))},
		{Number: 2, Size: int64(len(part2))},
	})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}

	rc, size, err := s.Get(ctx, digest, storage.GetOptions{})
	if err != nil {
		t.Fatalf("get after complete: %v", err)
	}
	defer rc.Close()
	got := make([]byte, size)
	rc.Read(got) //nolint:errcheck
	expected := append(part1, part2...)
	if !bytes.Equal(got, expected) {
		t.Errorf("assembled: want %q, got %q", expected, got)
	}
}

func TestAbortUpload(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	uploadID := "abort-test-1"
	s.InitiateUpload(ctx, uploadID, nil)                               //nolint:errcheck
	s.UploadPart(ctx, uploadID, 1, bytes.NewReader([]byte("data")), 4) //nolint:errcheck

	if err := s.AbortUpload(ctx, uploadID); err != nil {
		t.Fatalf("abort: %v", err)
	}
	if _, err := s.GetUploadStatus(ctx, uploadID); err == nil {
		t.Fatal("expected error after abort")
	}
}
