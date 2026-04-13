// Package local implements storage.Store using the local filesystem.
// Suitable for development and single-node deployments.
package local

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bons/bons-ci/plugins/rbe/pkg/errors"
	"github.com/bons/bons-ci/plugins/rbe/pkg/storage"
)

type Config struct {
	Root string
}

type uploadMeta struct {
	UploadID  string            `json:"upload_id"`
	Parts     []storage.Part    `json:"parts"`
	Metadata  map[string]string `json:"metadata"`
	CreatedAt time.Time         `json:"created_at"`
}

type Store struct {
	root    string
	mu      sync.RWMutex
	uploads map[string]*uploadMeta
}

func New(_ context.Context, cfg Config) (*Store, error) {
	for _, d := range []string{"blobs", "uploads"} {
		if err := os.MkdirAll(filepath.Join(cfg.Root, d), 0o755); err != nil {
			return nil, fmt.Errorf("local store: mkdir %s: %w", d, err)
		}
	}
	return &Store{root: cfg.Root, uploads: make(map[string]*uploadMeta)}, nil
}

func (s *Store) blobPath(digest string) string {
	parts := strings.SplitN(digest, ":", 2)
	if len(parts) == 2 {
		return filepath.Join(s.root, "blobs", parts[0], parts[1][:2], parts[1])
	}
	return filepath.Join(s.root, "blobs", digest)
}

func (s *Store) uploadDir(uploadID string) string {
	return filepath.Join(s.root, "uploads", uploadID)
}

func (s *Store) Put(_ context.Context, digest string, r io.Reader, _ int64, opts storage.PutOptions) error {
	p := s.blobPath(digest)
	if !opts.Overwrite {
		if _, err := os.Stat(p); err == nil {
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(p), "tmp-*")
	if err != nil {
		return err
	}
	tmpName := f.Name()
	if _, err := io.Copy(f, r); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, p)
}

func (s *Store) Get(_ context.Context, digest string, opts storage.GetOptions) (io.ReadCloser, int64, error) {
	p := s.blobPath(digest)
	f, err := os.Open(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, errors.NewBlobUnknown(digest)
		}
		return nil, 0, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, 0, err
	}
	size := info.Size()
	if opts.Offset > 0 {
		if _, err := f.Seek(opts.Offset, io.SeekStart); err != nil {
			_ = f.Close()
			return nil, 0, err
		}
		size -= opts.Offset
	}
	if opts.Length > 0 && opts.Length < size {
		size = opts.Length
		return &limitedReadCloser{f, io.LimitReader(f, opts.Length)}, size, nil
	}
	return f, size, nil
}

func (s *Store) Stat(_ context.Context, digest string) (*storage.BlobInfo, error) {
	p := s.blobPath(digest)
	fi, err := os.Stat(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errors.NewBlobUnknown(digest)
		}
		return nil, err
	}
	return &storage.BlobInfo{Digest: digest, Size: fi.Size(), CreatedAt: fi.ModTime()}, nil
}

func (s *Store) Exists(ctx context.Context, digest string) (bool, int64, error) {
	info, err := s.Stat(ctx, digest)
	if err != nil {
		if errors.Is(err, errors.ErrNotFound) {
			return false, 0, nil
		}
		return false, 0, err
	}
	return true, info.Size, nil
}

func (s *Store) Delete(_ context.Context, digest string) error {
	return os.Remove(s.blobPath(digest))
}

func (s *Store) List(_ context.Context, prefix string, opts storage.ListOptions) (*storage.ListResult, error) {
	root := filepath.Join(s.root, "blobs")
	var blobs []storage.BlobInfo
	err := filepath.Walk(root, func(path string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		parts := strings.Split(rel, string(os.PathSeparator))
		if len(parts) < 3 {
			return nil
		}
		digest := parts[0] + ":" + parts[2]
		if prefix != "" && !strings.HasPrefix(digest, prefix) {
			return nil
		}
		blobs = append(blobs, storage.BlobInfo{Digest: digest, Size: fi.Size(), CreatedAt: fi.ModTime()})
		return nil
	})
	return &storage.ListResult{Blobs: blobs}, err
}

func (s *Store) InitiateUpload(_ context.Context, uploadID string, metadata map[string]string) error {
	dir := s.uploadDir(uploadID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	s.mu.Lock()
	s.uploads[uploadID] = &uploadMeta{UploadID: uploadID, Metadata: metadata, CreatedAt: time.Now()}
	s.mu.Unlock()
	meta := s.uploads[uploadID]
	data, _ := json.Marshal(meta)
	return os.WriteFile(filepath.Join(dir, "meta.json"), data, 0o644)
}

func (s *Store) UploadPart(_ context.Context, uploadID string, partNum int, r io.Reader, _ int64) (string, error) {
	partPath := filepath.Join(s.uploadDir(uploadID), fmt.Sprintf("part-%05d", partNum))
	f, err := os.Create(partPath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := io.Copy(f, r); err != nil {
		return "", err
	}
	return fmt.Sprintf("part-%05d", partNum), nil
}

func (s *Store) CompleteUpload(ctx context.Context, uploadID, digest string, parts []storage.Part) error {
	dst := s.blobPath(digest)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	for _, p := range parts {
		partPath := filepath.Join(s.uploadDir(uploadID), fmt.Sprintf("part-%05d", p.Number))
		f, err := os.Open(partPath)
		if err != nil {
			return err
		}
		_, err = io.Copy(out, f)
		_ = f.Close()
		if err != nil {
			return err
		}
	}
	_ = os.RemoveAll(s.uploadDir(uploadID))
	s.mu.Lock()
	delete(s.uploads, uploadID)
	s.mu.Unlock()
	return nil
}

func (s *Store) AbortUpload(_ context.Context, uploadID string) error {
	_ = os.RemoveAll(s.uploadDir(uploadID))
	s.mu.Lock()
	delete(s.uploads, uploadID)
	s.mu.Unlock()
	return nil
}

func (s *Store) GetUploadStatus(_ context.Context, uploadID string) (*storage.UploadStatus, error) {
	s.mu.RLock()
	meta, ok := s.uploads[uploadID]
	s.mu.RUnlock()
	if !ok {
		return nil, errors.ErrUploadNotFound
	}
	return &storage.UploadStatus{UploadID: meta.UploadID, CreatedAt: meta.CreatedAt}, nil
}

func (s *Store) Copy(ctx context.Context, srcDigest, dstDigest string) error {
	r, _, err := s.Get(ctx, srcDigest, storage.GetOptions{})
	if err != nil {
		return err
	}
	defer r.Close()
	return s.Put(ctx, dstDigest, r, 0, storage.PutOptions{})
}

func (s *Store) URL(_ context.Context, _ string, _ time.Duration) (string, error) {
	return "", nil // local store does not support presigned URLs
}

func (s *Store) Close() error { return nil }

type limitedReadCloser struct {
	f io.Closer
	r io.Reader
}

func (l *limitedReadCloser) Read(p []byte) (int, error) { return l.r.Read(p) }
func (l *limitedReadCloser) Close() error               { return l.f.Close() }
