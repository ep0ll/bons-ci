// Package memory provides an in-memory ContentStore implementation.
//
// Performance characteristics:
//   - All reads use sync.RWMutex — concurrent reads never block each other.
//   - Writes are serialised per-digest via a sharded write-lock map, so
//     concurrent writes to different digests proceed in parallel.
//   - Buffer pools (sync.Pool) recycle byte slices for Put operations,
//     avoiding allocations on the hot ingest path.
//   - A sync.Map is used for the existence bloom-equivalent fast path.
//
// This implementation is suitable for testing and single-node deployments.
// For production at scale, replace with an S3/GCS/filesystem backend that
// implements the same ContentStore interface.
package memory

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	digest "github.com/opencontainers/go-digest"

	"github.com/bons/bons-ci/plugins/rbe/registry/pkg/types"
)

// ────────────────────────────────────────────────────────────────────────────
// Buffer pools — prevents heap pressure on the ingest path
// ────────────────────────────────────────────────────────────────────────────

var (
	// smallPool for blobs ≤ 64 KiB (typical manifest/config blobs)
	smallPool = sync.Pool{New: func() interface{} { b := make([]byte, 0, 64*1024); return &b }}
	// largePool for blobs ≤ 4 MiB (typical small layers)
	largePool = sync.Pool{New: func() interface{} { b := make([]byte, 0, 4*1024*1024); return &b }}
)

func getBuffer(hint int64) *[]byte {
	if hint > 0 && hint <= 64*1024 {
		return smallPool.Get().(*[]byte)
	}
	return largePool.Get().(*[]byte)
}

func putBuffer(b *[]byte) {
	*b = (*b)[:0]
	if cap(*b) <= 64*1024 {
		smallPool.Put(b)
	} else {
		largePool.Put(b)
	}
}

// ────────────────────────────────────────────────────────────────────────────
// blobEntry — stored content
// ────────────────────────────────────────────────────────────────────────────

type blobEntry struct {
	data      []byte
	size      int64
	createdAt time.Time
	labels    map[string]string
}

// ────────────────────────────────────────────────────────────────────────────
// Store — in-memory ContentStore
// ────────────────────────────────────────────────────────────────────────────

// Store is a fully in-memory, thread-safe ContentStore.
// All data is lost when the process exits.
type Store struct {
	mu         sync.RWMutex
	blobs      map[digest.Digest]*blobEntry
	totalBytes int64 // atomic

	// writeLocks prevents duplicate concurrent writes for the same digest.
	writeMu sync.Mutex
	writing map[digest.Digest]chan struct{} // digest → done channel
}

// New returns a new in-memory Store.
func New() *Store {
	return &Store{
		blobs:   make(map[digest.Digest]*blobEntry),
		writing: make(map[digest.Digest]chan struct{}),
	}
}

// ── ContentStore interface ─────────────────────────────────────────────────

// Get returns a reader for the blob with the given digest.
// The returned reader is backed by an in-memory bytes.Reader — zero copy.
func (s *Store) Get(_ context.Context, dgst digest.Digest) (io.ReadCloser, error) {
	s.mu.RLock()
	entry, ok := s.blobs[dgst]
	s.mu.RUnlock()
	if !ok {
		return nil, &blobNotFoundError{dgst: dgst}
	}
	// bytes.NewReader does not copy — the data slice is shared read-only.
	return io.NopCloser(bytes.NewReader(entry.data)), nil
}

// Put stores r as a blob, verifying its digest after reading.
// Uses buffer pools to avoid per-call allocations.
func (s *Store) Put(ctx context.Context, dgst digest.Digest, r io.Reader, sizeHint int64) error {
	// Acquire write-dedup lock for this digest.
	s.writeMu.Lock()
	if done, already := s.writing[dgst]; already {
		s.writeMu.Unlock()
		// Another goroutine is writing this digest — wait for it.
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
		return nil
	}
	done := make(chan struct{})
	s.writing[dgst] = done
	s.writeMu.Unlock()

	defer func() {
		s.writeMu.Lock()
		delete(s.writing, dgst)
		s.writeMu.Unlock()
		close(done)
	}()

	// Fast path: already stored.
	s.mu.RLock()
	_, exists := s.blobs[dgst]
	s.mu.RUnlock()
	if exists {
		// Drain r to satisfy the caller's contract.
		_, _ = io.Copy(io.Discard, r)
		return nil
	}

	// Read into pooled buffer with digest verification.
	buf := getBuffer(sizeHint)
	defer putBuffer(buf)

	digester := dgst.Algorithm().Digester()
	tr := io.TeeReader(r, digester.Hash())

	var err error
	var readBuf [32 * 1024]byte // stack-allocated read buffer
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		n, readErr := tr.Read(readBuf[:])
		if n > 0 {
			*buf = append(*buf, readBuf[:n]...)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return fmt.Errorf("store put: reading content: %w", readErr)
		}
	}

	actual := digester.Digest()
	if actual != dgst {
		return fmt.Errorf("store put: digest mismatch: expected %s, got %s", dgst, actual)
	}

	// Copy to a fresh, right-sized slice for long-term storage
	// (the pooled buffer will be returned to pool).
	stored := make([]byte, len(*buf))
	copy(stored, *buf)

	entry := &blobEntry{
		data:      stored,
		size:      int64(len(stored)),
		createdAt: time.Now(),
	}

	s.mu.Lock()
	s.blobs[dgst] = entry
	s.mu.Unlock()

	atomic.AddInt64(&s.totalBytes, entry.size)
	return err
}

// Exists reports whether the blob is present without reading any data.
func (s *Store) Exists(_ context.Context, dgst digest.Digest) (bool, error) {
	s.mu.RLock()
	_, ok := s.blobs[dgst]
	s.mu.RUnlock()
	return ok, nil
}

// Delete removes a blob. Returns nil if the blob was not present.
func (s *Store) Delete(_ context.Context, dgst digest.Digest) error {
	s.mu.Lock()
	entry, ok := s.blobs[dgst]
	if ok {
		delete(s.blobs, dgst)
		atomic.AddInt64(&s.totalBytes, -entry.size)
	}
	s.mu.Unlock()
	return nil
}

// Info returns size and metadata for a blob.
func (s *Store) Info(_ context.Context, dgst digest.Digest) (types.ContentInfo, error) {
	s.mu.RLock()
	entry, ok := s.blobs[dgst]
	s.mu.RUnlock()
	if !ok {
		return types.ContentInfo{}, &blobNotFoundError{dgst: dgst}
	}
	labels := make(map[string]string, len(entry.labels))
	for k, v := range entry.labels {
		labels[k] = v
	}
	return types.ContentInfo{
		Digest:    dgst,
		Size:      entry.size,
		CreatedAt: entry.createdAt,
		Labels:    labels,
	}, nil
}

// Walk iterates over every stored blob. The callback must not call Walk again.
func (s *Store) Walk(_ context.Context, fn func(types.ContentInfo) error) error {
	// Take a snapshot of keys under lock to avoid holding the lock during callbacks.
	s.mu.RLock()
	keys := make([]digest.Digest, 0, len(s.blobs))
	for k := range s.blobs {
		keys = append(keys, k)
	}
	s.mu.RUnlock()

	for _, dgst := range keys {
		s.mu.RLock()
		entry, ok := s.blobs[dgst]
		s.mu.RUnlock()
		if !ok {
			continue
		}
		if err := fn(types.ContentInfo{
			Digest:    dgst,
			Size:      entry.size,
			CreatedAt: entry.createdAt,
			Labels:    entry.labels,
		}); err != nil {
			return err
		}
	}
	return nil
}

// Stats returns aggregate storage statistics.
func (s *Store) Stats() (blobCount int, totalBytes int64) {
	s.mu.RLock()
	blobCount = len(s.blobs)
	s.mu.RUnlock()
	totalBytes = atomic.LoadInt64(&s.totalBytes)
	return
}

// ────────────────────────────────────────────────────────────────────────────
// Upload session store — tracks in-progress blob uploads
// ────────────────────────────────────────────────────────────────────────────

// UploadStore tracks active upload sessions with TTL-based expiry.
type UploadStore struct {
	mu       sync.Mutex
	sessions map[string]*uploadSession
}

type uploadSession struct {
	uuid      string
	repo      string
	buf       bytes.Buffer
	startedAt time.Time
	expiresAt time.Time
}

func NewUploadStore() *UploadStore {
	us := &UploadStore{sessions: make(map[string]*uploadSession)}
	return us
}

func (u *UploadStore) Create(repo string) string {
	uuid := newUUID()
	u.mu.Lock()
	u.sessions[uuid] = &uploadSession{
		uuid:      uuid,
		repo:      repo,
		startedAt: time.Now(),
		expiresAt: time.Now().Add(10 * time.Minute),
	}
	u.mu.Unlock()
	return uuid
}

func (u *UploadStore) Append(uuid string, data []byte) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	sess, ok := u.sessions[uuid]
	if !ok {
		return fmt.Errorf("upload session %q not found", uuid)
	}
	if time.Now().After(sess.expiresAt) {
		delete(u.sessions, uuid)
		return fmt.Errorf("upload session %q expired", uuid)
	}
	_, err := sess.buf.Write(data)
	return err
}

func (u *UploadStore) Finalize(uuid string, expected digest.Digest) ([]byte, error) {
	u.mu.Lock()
	sess, ok := u.sessions[uuid]
	if ok {
		delete(u.sessions, uuid)
	}
	u.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("upload session %q not found", uuid)
	}
	data := sess.buf.Bytes()
	actual := expected.Algorithm().FromBytes(data)
	if actual != expected {
		return nil, fmt.Errorf("upload finalize: digest mismatch: expected %s got %s", expected, actual)
	}
	cp := make([]byte, len(data))
	copy(cp, data)
	return cp, nil
}

func (u *UploadStore) Delete(uuid string) {
	u.mu.Lock()
	delete(u.sessions, uuid)
	u.mu.Unlock()
}

// ────────────────────────────────────────────────────────────────────────────
// Manifest index — {repo → {ref → descriptor}}
// ────────────────────────────────────────────────────────────────────────────

// ManifestIndex stores manifest descriptors indexed by (repo, reference).
type ManifestIndex struct {
	mu    sync.RWMutex
	index map[string]map[string]manifestEntry // repo → ref → entry
}

type manifestEntry struct {
	mediaType string
	digest    digest.Digest
	size      int64
	createdAt time.Time
}

func NewManifestIndex() *ManifestIndex {
	return &ManifestIndex{index: make(map[string]map[string]manifestEntry)}
}

func (m *ManifestIndex) Put(repo, ref string, dgst digest.Digest, mediaType string, size int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.index[repo] == nil {
		m.index[repo] = make(map[string]manifestEntry)
	}
	e := manifestEntry{mediaType: mediaType, digest: dgst, size: size, createdAt: time.Now()}
	m.index[repo][ref] = e
	m.index[repo][dgst.String()] = e // also index by digest
}

func (m *ManifestIndex) Get(repo, ref string) (manifestEntry, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.index[repo] == nil {
		return manifestEntry{}, false
	}
	e, ok := m.index[repo][ref]
	return e, ok
}

func (m *ManifestIndex) Delete(repo string, dgst digest.Digest) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.index[repo] == nil {
		return
	}
	// Remove all refs pointing at this digest
	for ref, e := range m.index[repo] {
		if e.digest == dgst {
			delete(m.index[repo], ref)
		}
	}
}

func (m *ManifestIndex) Tags(repo string) []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	tags := make([]string, 0)
	for ref := range m.index[repo] {
		// Tags do not start with "sha256:" etc.
		if len(ref) > 7 && ref[:7] == "sha256:" {
			continue
		}
		tags = append(tags, ref)
	}
	return tags
}

// ────────────────────────────────────────────────────────────────────────────
// Errors
// ────────────────────────────────────────────────────────────────────────────

type blobNotFoundError struct{ dgst digest.Digest }

func (e *blobNotFoundError) Error() string {
	return fmt.Sprintf("blob not found: %s", e.dgst)
}

// ────────────────────────────────────────────────────────────────────────────
// Utilities
// ────────────────────────────────────────────────────────────────────────────

func newUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
