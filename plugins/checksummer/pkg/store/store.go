//go:build linux

// Package store persists hash results from the engine.
//
// Two backends are provided:
//
//   - MemStore   – concurrent in-memory map (fastest, no durability)
//   - FileStore  – append-only NDJSON log (durable, queryable via jq/grep)
//
// Both implement the Store interface so they can be composed via MultiStore.
//
// Usage:
//
//	st := store.NewFileStore("/var/lib/checksumengine/results.ndjson")
//	eng.Hooks().PostHash.Register(hooks.NewHook("store", hooks.PriorityLast,
//	    func(ctx context.Context, p hooks.HashPayload) error {
//	        return st.Put(ctx, store.Record{
//	            Key:  p.Key, Path: p.Path, Hash: p.Hash, Size: p.FileSize,
//	        })
//	    }))
package store

import (
	"bufio"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// ─────────────────────────── Record ──────────────────────────────────────────

// Record is a single hash result entry.
type Record struct {
	Key       string    `json:"key"`
	Path      string    `json:"path"`
	Hash      []byte    `json:"-"`
	HashHex   string    `json:"hash"`
	Size      int64     `json:"size"`
	Cached    bool      `json:"cached,omitempty"`
	Deduped   bool      `json:"deduped,omitempty"`
	ComputedAt time.Time `json:"computed_at"`
}

// WithHash sets Hash and HashHex from a raw digest.
func (r Record) WithHash(hash []byte) Record {
	r.Hash = hash
	r.HashHex = hex.EncodeToString(hash)
	return r
}

func (r Record) String() string {
	return fmt.Sprintf("%s  %s (%d bytes)", r.HashHex, r.Path, r.Size)
}

// ─────────────────────────── Store interface ─────────────────────────────────

// Store is a write-optimised hash result sink.
// All implementations must be safe for concurrent use.
type Store interface {
	// Put stores a record.  May block if the backend is full.
	Put(ctx context.Context, r Record) error

	// Get retrieves the most recent record for key.
	// Returns (zero, false, nil) if not found.
	Get(ctx context.Context, key string) (Record, bool, error)

	// Len returns the number of stored records.
	Len() int64

	// Close flushes and closes the backend.
	Close() error
}

// ─────────────────────────── MemStore ────────────────────────────────────────

// MemStore is a sharded in-memory hash result map.
// It is the fastest backend but has no durability guarantees.
type MemStore struct {
	shards  [64]memShard
	mask    uint64
	count   atomic.Int64
}

type memShard struct {
	mu      sync.RWMutex
	records map[string]Record
}

// NewMemStore creates a MemStore.
func NewMemStore() *MemStore {
	ms := &MemStore{mask: 63}
	for i := range ms.shards {
		ms.shards[i].records = make(map[string]Record)
	}
	return ms
}

func (ms *MemStore) shard(key string) *memShard {
	h := fnv64(key)
	return &ms.shards[h&ms.mask]
}

// Put stores a record.
func (ms *MemStore) Put(_ context.Context, r Record) error {
	s := ms.shard(r.Key)
	s.mu.Lock()
	_, exists := s.records[r.Key]
	s.records[r.Key] = r
	s.mu.Unlock()
	if !exists {
		ms.count.Add(1)
	}
	return nil
}

// Get retrieves a record by key.
func (ms *MemStore) Get(_ context.Context, key string) (Record, bool, error) {
	s := ms.shard(key)
	s.mu.RLock()
	r, ok := s.records[key]
	s.mu.RUnlock()
	return r, ok, nil
}

// Len returns the number of stored records.
func (ms *MemStore) Len() int64 { return ms.count.Load() }

// Close is a no-op for MemStore.
func (ms *MemStore) Close() error { return nil }

// All returns a snapshot of all records (copy; safe to read after Close).
func (ms *MemStore) All() []Record {
	result := make([]Record, 0, ms.count.Load())
	for i := range ms.shards {
		s := &ms.shards[i]
		s.mu.RLock()
		for _, r := range s.records {
			result = append(result, r)
		}
		s.mu.RUnlock()
	}
	return result
}

// ─────────────────────────── FileStore ───────────────────────────────────────

// FileStore appends hash records as NDJSON (one JSON object per line) to a
// file.  It uses a buffered writer and flushes periodically or when full.
//
// File format (each line is one complete JSON object):
//
//	{"key":"stat:17:4283","path":"/usr/lib/libssl.so","hash":"ab12…","size":1234567,"computed_at":"2024-01-02T15:04:05Z"}
type FileStore struct {
	path    string
	mem     *MemStore // also keep in memory for fast Get
	mu      sync.Mutex
	f       *os.File
	bw      *bufio.Writer
	buf     int
	maxBuf  int
	writes  atomic.Int64
	flushes atomic.Int64
	closed  atomic.Bool
}

// FileStoreOption configures a FileStore.
type FileStoreOption func(*FileStore)

// WithBufSize sets the write buffer size in bytes.  Default: 256 KiB.
func WithBufSize(n int) FileStoreOption {
	return func(fs *FileStore) { fs.maxBuf = n }
}

// NewFileStore opens (or creates) the NDJSON log at path.
func NewFileStore(path string, opts ...FileStoreOption) (*FileStore, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("store: open %q: %w", path, err)
	}
	fs := &FileStore{
		path:   path,
		mem:    NewMemStore(),
		f:      f,
		maxBuf: 256 << 10,
	}
	for _, o := range opts {
		o(fs)
	}
	fs.bw = bufio.NewWriterSize(f, fs.maxBuf)
	return fs, nil
}

// Put writes a record to the log and the in-memory index.
func (fs *FileStore) Put(ctx context.Context, r Record) error {
	if fs.closed.Load() {
		return fmt.Errorf("store: closed")
	}
	if r.ComputedAt.IsZero() {
		r.ComputedAt = time.Now().UTC()
	}
	if r.HashHex == "" && len(r.Hash) > 0 {
		r.HashHex = hex.EncodeToString(r.Hash)
	}

	data, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("store: marshal: %w", err)
	}
	data = append(data, '\n')

	fs.mu.Lock()
	_, err = fs.bw.Write(data)
	fs.buf += len(data)
	if fs.buf >= fs.maxBuf {
		err = fs.bw.Flush()
		fs.buf = 0
		fs.flushes.Add(1)
	}
	fs.mu.Unlock()

	if err != nil {
		return fmt.Errorf("store: write: %w", err)
	}

	fs.writes.Add(1)
	return fs.mem.Put(ctx, r)
}

// Get looks up a record from the in-memory index (O(1), no disk IO).
func (fs *FileStore) Get(ctx context.Context, key string) (Record, bool, error) {
	return fs.mem.Get(ctx, key)
}

// Len returns the number of written records.
func (fs *FileStore) Len() int64 { return fs.writes.Load() }

// Flush forces a write of any buffered data to the OS.
func (fs *FileStore) Flush() error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	err := fs.bw.Flush()
	fs.buf = 0
	fs.flushes.Add(1)
	return err
}

// Close flushes and closes the underlying file.
func (fs *FileStore) Close() error {
	if !fs.closed.CompareAndSwap(false, true) {
		return nil
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if err := fs.bw.Flush(); err != nil {
		return fmt.Errorf("store: flush on close: %w", err)
	}
	return fs.f.Close()
}

// Path returns the log file path.
func (fs *FileStore) Path() string { return fs.path }

// Stats returns write and flush counts.
func (fs *FileStore) Stats() (writes, flushes int64) {
	return fs.writes.Load(), fs.flushes.Load()
}

// ─────────────────────────── MultiStore ──────────────────────────────────────

// MultiStore fans writes out to multiple Store backends simultaneously.
// Get is served from the first backend.
type MultiStore struct {
	stores []Store
}

// NewMultiStore wraps multiple Store implementations.
func NewMultiStore(stores ...Store) *MultiStore {
	return &MultiStore{stores: stores}
}

// Put writes to all backends.  Returns the first error; does not short-circuit.
func (ms *MultiStore) Put(ctx context.Context, r Record) error {
	var firstErr error
	for _, s := range ms.stores {
		if err := s.Put(ctx, r); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Get returns the first hit from any backend.
func (ms *MultiStore) Get(ctx context.Context, key string) (Record, bool, error) {
	for _, s := range ms.stores {
		if r, ok, err := s.Get(ctx, key); err == nil && ok {
			return r, true, nil
		}
	}
	return Record{}, false, nil
}

// Len returns the maximum Len across all backends.
func (ms *MultiStore) Len() int64 {
	var max int64
	for _, s := range ms.stores {
		if n := s.Len(); n > max {
			max = n
		}
	}
	return max
}

// Close closes all backends.
func (ms *MultiStore) Close() error {
	var firstErr error
	for _, s := range ms.stores {
		if err := s.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// ─────────────────────────── NopStore ────────────────────────────────────────

// NopStore discards all records.  Useful in tests or when storage is disabled.
type NopStore struct{}

func (NopStore) Put(_ context.Context, _ Record) error               { return nil }
func (NopStore) Get(_ context.Context, _ string) (Record, bool, error) { return Record{}, false, nil }
func (NopStore) Len() int64                                           { return 0 }
func (NopStore) Close() error                                         { return nil }

// ─────────────────────────── helpers ─────────────────────────────────────────

func fnv64(s string) uint64 {
	const (
		offset = uint64(14695981039346656037)
		prime  = uint64(1099511628211)
	)
	h := offset
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= prime
	}
	return h
}

// Compile-time interface checks.
var (
	_ Store = (*MemStore)(nil)
	_ Store = (*FileStore)(nil)
	_ Store = (*MultiStore)(nil)
	_ Store = NopStore{}
)
