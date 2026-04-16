package fshash

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"time"

	"github.com/bons/bons-ci/pkg/fshash/core"
)

// SnapshotEntry is one entry within a Snapshot.
type SnapshotEntry struct {
	RelPath string    `json:"path"`
	Kind    EntryKind `json:"kind"`
	Digest  string    `json:"digest"`
}

// Snapshot is a serialisable record of a tree digest at a point in time.
type Snapshot struct {
	RootDigest string          `json:"root_digest"`
	Algorithm  string          `json:"algorithm"`
	MetaRaw    uint8           `json:"meta,omitempty"`
	CreatedAt  time.Time       `json:"created_at"`
	Entries    []SnapshotEntry `json:"entries,omitempty"`
}

// Meta returns the MetaFlag stored in the snapshot.
func (s *Snapshot) Meta() core.MetaFlag { return core.MetaFlag(s.MetaRaw) }

// TakeSnapshot checksums absPath (CollectEntries forced on) and wraps the result.
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
		MetaRaw:    uint8(cs.opts.Meta),
		CreatedAt:  time.Now().UTC(),
		Entries:    entries,
	}, nil
}

// WriteTo serialises the Snapshot as pretty-printed JSON. Implements io.WriterTo.
func (s *Snapshot) WriteTo(w io.Writer) (int64, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(s); err != nil {
		return 0, fmt.Errorf("fshash: encode snapshot: %w", err)
	}
	n, err := w.Write(buf.Bytes())
	return int64(n), err
}

var _ io.WriterTo = (*Snapshot)(nil)

// ReadSnapshot deserialises a Snapshot written by WriteTo.
func ReadSnapshot(r io.Reader) (*Snapshot, error) {
	var s Snapshot
	if err := json.NewDecoder(r).Decode(&s); err != nil {
		return nil, fmt.Errorf("fshash: decode snapshot: %w", err)
	}
	return &s, nil
}

// VerifyAgainst re-checksums absPath using the snapshot's algorithm/meta.
func (s *Snapshot) VerifyAgainst(ctx context.Context, absPath string, opts ...Option) error {
	base := []Option{
		WithAlgorithm(core.Algorithm(s.Algorithm)),
		WithMetadata(s.Meta()),
	}
	cs, err := New(append(base, opts...)...)
	if err != nil {
		return err
	}
	return cs.Verify(ctx, absPath, hexDecode(s.RootDigest))
}

// Diff returns entry-level differences between s and other.
func (s *Snapshot) Diff(other *Snapshot) DiffResult {
	aMap := make(map[string]string, len(s.Entries))
	for _, e := range s.Entries {
		if e.RelPath != "." {
			aMap[e.RelPath] = e.Digest
		}
	}
	bMap := make(map[string]string, len(other.Entries))
	for _, e := range other.Entries {
		if e.RelPath != "." {
			bMap[e.RelPath] = e.Digest
		}
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

func hexDecode(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil
	}
	return b
}

// ── Inspector ──────────────────────────────────────────────────────────────────

// InspectEntry augments EntryResult with cache-hit information.
type InspectEntry struct {
	EntryResult
	CacheHit bool
}

// Inspector wraps a Checksummer and records which file entries hit the cache.
type Inspector struct {
	cs    *Checksummer
	cache *instrumentedCache
}

// NewInspector wraps cs with cache-hit instrumentation.
func NewInspector(cs *Checksummer, cache FileCache) *Inspector {
	ic := &instrumentedCache{delegate: cache}
	opts2 := cs.opts
	opts2.FileCache = ic
	return &Inspector{cs: &Checksummer{opts: opts2}, cache: ic}
}

// Sum computes the checksum and returns per-entry cache-hit information.
func (ins *Inspector) Sum(ctx context.Context, absPath string) (Result, []InspectEntry, error) {
	ins.cache.reset()
	res, err := ins.cs.withCollect().Sum(ctx, absPath)
	if err != nil {
		return Result{}, nil, err
	}
	hits := ins.cache.hitSnapshot()
	out := make([]InspectEntry, len(res.Entries))
	for i, e := range res.Entries {
		absE := absPath
		if e.RelPath != "." {
			absE = filepath.Join(absPath, filepath.FromSlash(e.RelPath))
		}
		_, wasHit := hits[absE]
		out[i] = InspectEntry{EntryResult: e, CacheHit: wasHit}
	}
	return res, out, nil
}

// HitRate returns the fraction of file entries served from cache in the last Sum.
func (ins *Inspector) HitRate() float64 {
	hits, total := ins.cache.stats()
	if total == 0 {
		return 0
	}
	return float64(hits) / float64(total)
}
