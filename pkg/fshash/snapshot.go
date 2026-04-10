package fshash

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// ── Snapshot ──────────────────────────────────────────────────────────────────

// SnapshotEntry records a single entry within a [Snapshot].
type SnapshotEntry struct {
	RelPath string    `json:"path"`
	Kind    EntryKind `json:"kind"`
	Digest  string    `json:"digest"` // hex-encoded
}

// Snapshot is a serialisable record of a directory (or file) digest captured
// at a specific point in time.  It can be written to disk, checked into
// version control, or sent over the network, and later used to verify that a
// tree has not changed.
type Snapshot struct {
	// RootDigest is the hex-encoded digest of the root path.
	RootDigest string `json:"root_digest"`
	// Algorithm is the hash algorithm used (e.g. "sha256").
	Algorithm string `json:"algorithm"`
	// Meta records which metadata flags were active.
	Meta MetaFlag `json:"meta,omitempty"`
	// CreatedAt is informational; it does not affect any digest.
	CreatedAt time.Time `json:"created_at"`
	// Entries holds one record per visited filesystem entry when the snapshot
	// was taken with CollectEntries=true.  May be nil for root-only snapshots.
	Entries []SnapshotEntry `json:"entries,omitempty"`
}

// TakeSnapshot computes the checksum of absPath (with CollectEntries forced to
// true) and returns it wrapped in a [Snapshot].
func TakeSnapshot(ctx context.Context, absPath string, opts ...Option) (*Snapshot, error) {
	cs, err := New(append(opts, WithCollectEntries(true))...)
	if err != nil {
		return nil, err
	}
	res, err := cs.Sum(ctx, absPath)
	if err != nil {
		return nil, err
	}

	entries := make([]SnapshotEntry, len(res.Entries))
	for i, e := range res.Entries {
		entries[i] = SnapshotEntry{RelPath: e.RelPath, Kind: e.Kind, Digest: e.Hex()}
	}

	return &Snapshot{
		RootDigest: res.Hex(),
		Algorithm:  cs.opts.Hasher.Algorithm(),
		Meta:       cs.opts.Meta,
		CreatedAt:  time.Now().UTC(),
		Entries:    entries,
	}, nil
}

// WriteTo serialises the snapshot as pretty-printed JSON to w.
// It implements [io.WriterTo].
func (s *Snapshot) WriteTo(w io.Writer) (int64, error) {
	// Encode to an intermediate buffer so we know the exact byte count before
	// any partial-write scenario can occur.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(s); err != nil {
		return 0, fmt.Errorf("fshash: encode snapshot: %w", err)
	}
	data := buf.Bytes()
	n, err := w.Write(data)
	if err == nil && n < len(data) {
		// io.Writer contract: if err == nil then n == len(p); defensive check.
		err = fmt.Errorf("fshash: WriteTo: short write (%d/%d bytes)", n, len(data))
	}
	return int64(n), err
}

// compile-time assertion: *Snapshot implements io.WriterTo.
var _ io.WriterTo = (*Snapshot)(nil)

// ReadSnapshot deserialises a snapshot previously written by [Snapshot.WriteTo].
func ReadSnapshot(r io.Reader) (*Snapshot, error) {
	var s Snapshot
	if err := json.NewDecoder(r).Decode(&s); err != nil {
		return nil, fmt.Errorf("fshash: decode snapshot: %w", err)
	}
	return &s, nil
}

// VerifyAgainst re-checksums absPath using a fresh [Checksummer] that honours
// the algorithm and metadata flags recorded in the snapshot, compares the
// root digest, and returns nil on a match.  Additional opts are applied after
// the snapshot-derived settings and may override them.
func (s *Snapshot) VerifyAgainst(ctx context.Context, absPath string, opts ...Option) error {
	base := []Option{
		WithAlgorithm(Algorithm(s.Algorithm)),
		WithMetadata(s.Meta),
	}
	cs, err := New(append(base, opts...)...)
	if err != nil {
		return err
	}
	return cs.Verify(ctx, absPath, hexDecode(s.RootDigest))
}

// Diff returns the entry-level differences between s and other.
// Only RelPath and Digest fields are compared; Kind differences are ignored.
func (s *Snapshot) Diff(other *Snapshot) DiffResult {
	aMap := make(map[string]string, len(s.Entries))
	for _, e := range s.Entries {
		if e.RelPath == "." {
			continue // root represented by RootDigest field
		}
		aMap[e.RelPath] = e.Digest
	}
	bMap := make(map[string]string, len(other.Entries))
	for _, e := range other.Entries {
		if e.RelPath == "." {
			continue
		}
		bMap[e.RelPath] = e.Digest
	}

	var dr DiffResult
	for p, da := range aMap {
		if db, ok := bMap[p]; !ok {
			dr.Removed = append(dr.Removed, p)
		} else if da != db {
			dr.Modified = append(dr.Modified, p)
		}
	}
	for p := range bMap {
		if _, ok := aMap[p]; !ok {
			dr.Added = append(dr.Added, p)
		}
	}
	sort.Strings(dr.Added)
	sort.Strings(dr.Removed)
	sort.Strings(dr.Modified)
	return dr
}

// hexDecode converts a hex string to bytes.  Returns nil on malformed input so
// that verification always fails rather than silently passing.
func hexDecode(s string) []byte {
	if len(s)%2 != 0 {
		return nil
	}
	b := make([]byte, len(s)/2)
	for i := range b {
		hi := fromHexNibble(s[i*2])
		lo := fromHexNibble(s[i*2+1])
		if hi > 15 || lo > 15 {
			return nil
		}
		b[i] = hi<<4 | lo
	}
	return b
}

func fromHexNibble(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10
	default:
		return 0xFF // sentinel for "invalid"
	}
}

// ── Inspector ─────────────────────────────────────────────────────────────────

// InspectEntry augments [EntryResult] with cache-hit information.
type InspectEntry struct {
	EntryResult
	// CacheHit is true when this entry's digest was served from the FileCache
	// rather than computed from disk.
	CacheHit bool
}

// Inspector wraps a [Checksummer] and records which file entries were served
// from the [FileCache].  Useful for debugging and measuring cache efficiency.
type Inspector struct {
	cs    *Checksummer
	cache *instrumentedCache
}

// NewInspector wraps cs with cache-hit instrumentation.  The cache argument
// must be the same [FileCache] that cs uses (or nil if cs has no cache, in
// which case all CacheHit values will be false).
func NewInspector(cs *Checksummer, cache FileCache) *Inspector {
	ic := &instrumentedCache{delegate: cache}
	opts2 := cs.opts
	opts2.FileCache = ic
	return &Inspector{
		cs:    &Checksummer{opts: opts2},
		cache: ic,
	}
}

// Sum computes the checksum and returns the result alongside per-entry
// cache-hit information.
func (ins *Inspector) Sum(ctx context.Context, absPath string) (Result, []InspectEntry, error) {
	ins.cache.reset()

	res, err := ins.cs.withCollect().Sum(ctx, absPath)
	if err != nil {
		return Result{}, nil, err
	}

	// hitSnapshot returns a set of absolute paths that were cache hits.
	// For each entry, reconstruct the absolute path so we can look it up.
	hits := ins.cache.hitSnapshot()
	out := make([]InspectEntry, len(res.Entries))
	for i, e := range res.Entries {
		var entryAbs string
		if e.RelPath == "." {
			entryAbs = absPath
		} else {
			entryAbs = filepath.Join(absPath, filepath.FromSlash(e.RelPath))
		}
		_, wasHit := hits[entryAbs]
		out[i] = InspectEntry{EntryResult: e, CacheHit: wasHit}
	}
	return res, out, nil
}

// HitRate returns the fraction of file entries served from cache in the most
// recent Sum call.  Returns 0 when no files were visited.
func (ins *Inspector) HitRate() float64 {
	hits, total := ins.cache.stats()
	if total == 0 {
		return 0
	}
	return float64(hits) / float64(total)
}

// ── instrumentedCache ─────────────────────────────────────────────────────────

// instrumentedCache wraps a [FileCache] and records hit/miss counts and paths.
type instrumentedCache struct {
	delegate FileCache

	mu       sync.Mutex
	hitPaths map[string]struct{}
	nHits    int
	nTotal   int
}

func (ic *instrumentedCache) reset() {
	ic.mu.Lock()
	ic.hitPaths = make(map[string]struct{})
	ic.nHits = 0
	ic.nTotal = 0
	ic.mu.Unlock()
}

func (ic *instrumentedCache) hitSnapshot() map[string]struct{} {
	ic.mu.Lock()
	defer ic.mu.Unlock()
	out := make(map[string]struct{}, len(ic.hitPaths))
	for k := range ic.hitPaths {
		out[k] = struct{}{}
	}
	return out
}

func (ic *instrumentedCache) stats() (hits, total int) {
	ic.mu.Lock()
	defer ic.mu.Unlock()
	return ic.nHits, ic.nTotal
}

func (ic *instrumentedCache) Get(absPath string) ([]byte, bool) {
	// nil delegate means no cache — always miss.
	if ic.delegate == nil {
		ic.mu.Lock()
		ic.nTotal++
		ic.mu.Unlock()
		return nil, false
	}
	d, ok := ic.delegate.Get(absPath)
	ic.mu.Lock()
	ic.nTotal++
	if ok {
		ic.nHits++
		ic.hitPaths[absPath] = struct{}{}
	}
	ic.mu.Unlock()
	return d, ok
}

func (ic *instrumentedCache) Set(absPath string, dgst []byte) {
	if ic.delegate != nil {
		ic.delegate.Set(absPath, dgst)
	}
}

func (ic *instrumentedCache) Invalidate(absPath string) {
	ic.mu.Lock()
	delete(ic.hitPaths, absPath)
	ic.mu.Unlock()
	if ic.delegate != nil {
		ic.delegate.Invalidate(absPath)
	}
}
