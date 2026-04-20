//go:build linux

// Package hashdb provides a persistent, cross-invocation hash result store
// that enables deduplication across multiple overlayfs mergedviews AND across
// separate process lifetimes.
//
// Problem
// ───────
// The in-memory LRU cache in pkg/dedup handles within-process deduplication
// perfectly. But if the engine is stopped and restarted, or if two separate
// engine processes watch different mergedviews of the same lowerdir, the
// in-memory state is lost and files are re-hashed needlessly.
//
// Solution: Backing-file identity as a persistent cache key
// ─────────────────────────────────────────────────────────
// When overlayfs exposes a file from lowerdir L through mergedview M,
// unix.NameToHandleAt(fd, "", AT_EMPTY_PATH) returns the LOWER filesystem's
// (mount_id, file_handle) — identical regardless of which mergedview the fd
// was opened through, and stable across process restarts as long as the lower
// filesystem is not reformatted.
//
// hashdb stores (mount_id, handle_bytes, mtime_ns, size) → hash on disk.
// On lookup: if mtime_ns and size match the current file stat, the stored hash
// is valid — the file content has not changed. Otherwise, the entry is stale
// and recomputation is triggered.
//
// On-disk format
// ──────────────
// Files live in a configurable directory (default: /var/cache/ovlhash/).
// Sharded into 256 files keyed by fnv32(handle_bytes) & 0xFF.
// Each shard is an append-only binary log compacted on Open:
//
//	[magic:4][version:2]
//	[N × Record]
//
// Record (fixed 175 bytes):
//
//	[mount_id:8][handle_type:4][handle_len:1][handle:64]
//	[mtime_ns:8][size:8][hash:32][_pad:50]  // pad to 175 for alignment
//
// Compaction: on Open, each shard is read into memory, deduplicated by
// (mount_id, handle), and rewritten. This keeps shard files small.
//
// Thread safety: all exported methods are safe for concurrent use.
package hashdb

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/bons/bons-ci/plugins/checksummer/pkg/filekey"
)

// ─────────────────────────── constants / format ───────────────────────────────

const (
	magic      = uint32(0x4F564C48) // "OVLH"
	version    = uint16(1)
	recordSize = 175 // bytes per on-disk record (fixed for O(1) seek)

	// headerSize is the size of the shard file header.
	headerSize = 4 + 2 // magic + version

	// DefaultDir is the default cache directory.
	DefaultDir = "/var/cache/ovlhash"

	// numShards must be a power of two.
	numShards = 256
)

// ─────────────────────────── memEntry ────────────────────────────────────────

// memKey is the in-memory lookup key: (mount_id, handle).
// Using an array keeps it stack-allocated as a map key.
type memKey struct {
	mountID    int64
	handleType int32
	handle     [64]byte
	handleLen  uint8
}

func keyToMemKey(k filekey.Key) memKey {
	return memKey{
		mountID:    int64(k.MountID),
		handleType: k.HandleType,
		handle:     k.Handle,
		handleLen:  uint8(k.HandleLen),
	}
}

// memEntry is a cached hash result with its validation fields.
type memEntry struct {
	hash    [32]byte
	mtimeNs int64
	size    int64
}

// ─────────────────────────── shard ───────────────────────────────────────────

type shard struct {
	mu       sync.RWMutex
	entries  map[memKey]memEntry
	dirty    []record // pending writes not yet flushed to disk
	path     string
	disabled bool // set when file I/O fails permanently
}

// record is the fixed-size on-disk representation.
type record struct {
	mountID    int64
	handleType int32
	handleLen  uint8
	handle     [64]byte
	mtimeNs    int64
	size       int64
	hash       [32]byte
	_          [50]byte // padding to recordSize=175
}

func (s *shard) get(k filekey.Key, mtimeNs, size int64) ([]byte, bool) {
	mk := keyToMemKey(k)
	s.mu.RLock()
	e, ok := s.entries[mk]
	s.mu.RUnlock()
	if !ok || e.mtimeNs != mtimeNs || e.size != size {
		return nil, false
	}
	cp := make([]byte, 32)
	copy(cp, e.hash[:])
	return cp, true
}

func (s *shard) put(k filekey.Key, mtimeNs, size int64, hash []byte) {
	if len(hash) != 32 {
		return
	}
	mk := keyToMemKey(k)
	var e memEntry
	copy(e.hash[:], hash)
	e.mtimeNs = mtimeNs
	e.size = size

	r := record{
		mountID:    int64(k.MountID),
		handleType: k.HandleType,
		handleLen:  uint8(k.HandleLen),
		handle:     k.Handle,
		mtimeNs:    mtimeNs,
		size:       size,
	}
	copy(r.hash[:], hash)

	s.mu.Lock()
	s.entries[mk] = e
	if !s.disabled {
		s.dirty = append(s.dirty, r)
	}
	s.mu.Unlock()
}

// flush appends all dirty records to the shard file.
func (s *shard) flush() error {
	s.mu.Lock()
	if len(s.dirty) == 0 || s.disabled {
		s.mu.Unlock()
		return nil
	}
	batch := s.dirty
	s.dirty = s.dirty[:0]
	s.mu.Unlock()

	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		s.mu.Lock()
		s.disabled = true
		s.mu.Unlock()
		return fmt.Errorf("hashdb: open shard %q: %w", s.path, err)
	}
	defer f.Close()

	// Ensure header exists on a new file.
	fi, _ := f.Stat()
	if fi.Size() == 0 {
		if err := writeHeader(f); err != nil {
			return err
		}
	}

	for i := range batch {
		if err := writeRecord(f, &batch[i]); err != nil {
			return fmt.Errorf("hashdb: write record: %w", err)
		}
	}
	return f.Sync()
}

// load reads the shard file into memory, compacts duplicates, rewrites if dirty.
func (s *shard) load() error {
	f, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil // fresh shard
	}
	if err != nil {
		return fmt.Errorf("hashdb: open shard %q: %w", s.path, err)
	}
	defer f.Close()

	if err := readHeader(f); err != nil {
		// Corrupt header – start fresh.
		f.Close()
		_ = os.Remove(s.path)
		return nil
	}

	var loaded []record
	for {
		var r record
		if err := readRecord(f, &r); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				break
			}
			break // ignore partial records
		}
		loaded = append(loaded, r)
	}

	// Deduplicate: last write wins.
	seen := make(map[memKey]int, len(loaded))
	for i, r := range loaded {
		mk := memKey{
			mountID:    r.mountID,
			handleType: r.handleType,
			handle:     r.handle,
			handleLen:  r.handleLen,
		}
		seen[mk] = i
	}

	s.mu.Lock()
	for mk, idx := range seen {
		r := loaded[idx]
		s.entries[mk] = memEntry{hash: r.hash, mtimeNs: r.mtimeNs, size: r.size}
	}
	needsCompact := len(loaded) > len(seen)+64
	s.mu.Unlock()

	// Compact if more than 64 stale records exist.
	if needsCompact {
		_ = s.compact(seen, loaded)
	}
	return nil
}

// compact rewrites the shard file with deduplicated records.
func (s *shard) compact(seen map[memKey]int, loaded []record) error {
	tmp := s.path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if err := writeHeader(f); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	for _, idx := range seen {
		if err := writeRecord(f, &loaded[idx]); err != nil {
			f.Close()
			os.Remove(tmp)
			return err
		}
	}
	f.Close()
	return os.Rename(tmp, s.path)
}

// ─────────────────────────── binary I/O ──────────────────────────────────────

func writeHeader(w io.Writer) error {
	var h [headerSize]byte
	binary.LittleEndian.PutUint32(h[0:4], magic)
	binary.LittleEndian.PutUint16(h[4:6], version)
	_, err := w.Write(h[:])
	return err
}

func readHeader(r io.Reader) error {
	var h [headerSize]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return err
	}
	if binary.LittleEndian.Uint32(h[0:4]) != magic {
		return fmt.Errorf("hashdb: invalid magic")
	}
	if binary.LittleEndian.Uint16(h[4:6]) != version {
		return fmt.Errorf("hashdb: unsupported version")
	}
	return nil
}

func writeRecord(w io.Writer, r *record) error {
	var buf [recordSize]byte
	binary.LittleEndian.PutUint64(buf[0:8], uint64(r.mountID))
	binary.LittleEndian.PutUint32(buf[8:12], uint32(r.handleType))
	buf[12] = r.handleLen
	copy(buf[13:77], r.handle[:])
	binary.LittleEndian.PutUint64(buf[77:85], uint64(r.mtimeNs))
	binary.LittleEndian.PutUint64(buf[85:93], uint64(r.size))
	copy(buf[93:125], r.hash[:])
	// buf[125:175] = padding zeros
	_, err := w.Write(buf[:])
	return err
}

func readRecord(r io.Reader, out *record) error {
	var buf [recordSize]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return err
	}
	out.mountID = int64(binary.LittleEndian.Uint64(buf[0:8]))
	out.handleType = int32(binary.LittleEndian.Uint32(buf[8:12]))
	out.handleLen = buf[12]
	copy(out.handle[:], buf[13:77])
	out.mtimeNs = int64(binary.LittleEndian.Uint64(buf[77:85]))
	out.size = int64(binary.LittleEndian.Uint64(buf[85:93]))
	copy(out.hash[:], buf[93:125])
	return nil
}

// ─────────────────────────── DB ──────────────────────────────────────────────

// DB is a persistent hash result store, sharded across 256 files.
//
// DB is safe for concurrent use. A zero-value DB is invalid; use Open.
type DB struct {
	dir    string
	shards [numShards]*shard

	// Metrics.
	hits   atomic.Int64
	misses atomic.Int64
	stores atomic.Int64
}

// Option configures a DB.
type Option func(*DB)

// Open creates or opens the hash database in dir.
// All 256 shard files under dir are loaded and compacted in parallel.
func Open(dir string, opts ...Option) (*DB, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("hashdb: mkdir %q: %w", dir, err)
	}
	db := &DB{dir: dir}
	for _, o := range opts {
		o(db)
	}

	// Initialise all shards.
	for i := range db.shards {
		db.shards[i] = &shard{
			entries: make(map[memKey]memEntry, 64),
			path:    filepath.Join(dir, fmt.Sprintf("shard_%02x.bin", i)),
		}
	}

	// Load shards in parallel.
	var wg sync.WaitGroup
	errs := make([]error, numShards)
	for i := range db.shards {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = db.shards[i].load()
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			// Non-fatal: log and continue with empty shard.
			_ = err
			db.shards[i].disabled = false
		}
	}
	return db, nil
}

// Get retrieves the stored hash for key if mtime_ns and size still match.
// Returns (nil, false) on miss or stale entry.
func (db *DB) Get(k filekey.Key, mtimeNs, size int64) ([]byte, bool) {
	if k.Source != filekey.SourceHandle {
		// Stat-based keys are not stable across mounts; skip persistent cache.
		return nil, false
	}
	s := db.shardFor(k)
	hash, ok := s.get(k, mtimeNs, size)
	if ok {
		db.hits.Add(1)
	} else {
		db.misses.Add(1)
	}
	return hash, ok
}

// Put stores a hash result for key with the given validation metadata.
// Writes are batched and flushed asynchronously via Flush.
func (db *DB) Put(k filekey.Key, mtimeNs, size int64, hash []byte) {
	if k.Source != filekey.SourceHandle {
		return
	}
	db.shardFor(k).put(k, mtimeNs, size, hash)
	db.stores.Add(1)
}

// Flush persists all pending writes to disk for all shards.
// Safe to call from multiple goroutines; each shard is flushed independently.
func (db *DB) Flush() error {
	var firstErr error
	for _, s := range db.shards {
		if err := s.flush(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Close flushes all pending writes and releases resources.
func (db *DB) Close() error { return db.Flush() }

// Stats returns hit/miss/store counters.
func (db *DB) Stats() (hits, misses, stores int64) {
	return db.hits.Load(), db.misses.Load(), db.stores.Load()
}

// Dir returns the database directory.
func (db *DB) Dir() string { return db.dir }

// shardFor selects the shard for a key using fnv32(handle_bytes) & 0xFF.
func (db *DB) shardFor(k filekey.Key) *shard {
	h := fnv.New32a()
	_, _ = h.Write(k.Handle[:k.HandleLen])
	return db.shards[h.Sum32()&uint32(numShards-1)]
}
