package fshash

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"

	"github.com/bons/bons-ci/pkg/fshash/core"
)

// ── HashReader ────────────────────────────────────────────────────────────────

// HashReader hashes the byte stream produced by r using the algorithm in opts.
// No filesystem access; no metadata header — raw bytes only.
func HashReader(ctx context.Context, r io.Reader, opts ...Option) ([]byte, error) {
	cs, err := New(append([]Option{WithWorkers(1)}, opts...)...)
	if err != nil {
		return nil, err
	}
	h := cs.opts.Hasher.New()
	buf := cs.opts.Pool.GetStream()
	defer cs.opts.Pool.Put(buf)

	for {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		n, rerr := r.Read(*buf)
		if n > 0 {
			core.MustWrite(h, (*buf)[:n])
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return nil, fmt.Errorf("fshash: HashReader: %w", rerr)
		}
	}
	return h.Sum(nil), nil
}

// ── Walk ──────────────────────────────────────────────────────────────────────

// WalkFunc is called once per visited entry after its digest is ready.
// Returning a non-nil error aborts the walk.
type WalkFunc func(entry EntryResult) error

// Walk computes the checksum of absPath, calling fn for every entry in sorted
// relPath order (directories arrive after all their children — bottom-up).
func (cs *Checksummer) Walk(ctx context.Context, absPath string, fn WalkFunc) (Result, error) {
	res, err := cs.withCollect().Sum(ctx, absPath)
	if err != nil {
		return Result{}, err
	}
	for _, e := range res.Entries {
		if err := fn(e); err != nil {
			return Result{}, fmt.Errorf("fshash: Walk: %w", err)
		}
	}
	return Result{Digest: res.Digest}, nil
}

// ── SumStream — reactive streaming walk ───────────────────────────────────────

// ── Canonicalize ──────────────────────────────────────────────────────────────

// Canonicalize writes a human-readable, sorted, line-oriented representation
// of the tree to w:
//
//	<hex-digest>  <kind>    <rel-path>
//
// The final line is always the root with kind "root" and relPath ".".
// Format is deliberately similar to sha256sum(1) for easy auditing.
func (cs *Checksummer) Canonicalize(ctx context.Context, absPath string, w io.Writer) ([]byte, error) {
	res, err := cs.withCollect().Sum(ctx, absPath)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	for _, e := range res.Entries {
		if e.RelPath == "." {
			continue
		}
		fmt.Fprintf(&buf, "%s  %s  %s\n", e.Hex(), e.Kind.String(), e.RelPath)
	}
	fmt.Fprintf(&buf, "%s  %s  .\n", res.Hex(), "root")
	if _, err := w.Write(buf.Bytes()); err != nil {
		return nil, fmt.Errorf("fshash: Canonicalize write: %w", err)
	}
	return res.Digest, nil
}

// ── CanonicalEntry / ReadCanonical ────────────────────────────────────────────

// CanonicalEntry is one parsed line from Canonicalize output.
type CanonicalEntry struct {
	Digest  []byte
	Kind    string // "file", "dir", "symlink", "other", or "root"
	RelPath string
}

// ReadCanonical parses the output of Canonicalize. Handles paths with spaces.
func ReadCanonical(r io.Reader) ([]CanonicalEntry, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("fshash: ReadCanonical: %w", err)
	}
	const sep = "  "
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	out := make([]CanonicalEntry, 0, len(lines))
	for ln, line := range lines {
		if line == "" {
			continue
		}
		i1 := strings.Index(line, sep)
		if i1 < 0 {
			return nil, fmt.Errorf("fshash: ReadCanonical line %d: missing digest separator", ln+1)
		}
		rest := line[i1+len(sep):]
		i2 := strings.Index(rest, sep)
		if i2 < 0 {
			return nil, fmt.Errorf("fshash: ReadCanonical line %d: missing kind separator", ln+1)
		}
		dgst, err := hex.DecodeString(line[:i1])
		if err != nil {
			return nil, fmt.Errorf("fshash: ReadCanonical line %d: bad digest: %w", ln+1, err)
		}
		out = append(out, CanonicalEntry{
			Digest:  dgst,
			Kind:    strings.TrimSpace(rest[:i2]),
			RelPath: rest[i2+len(sep):],
		})
	}
	return out, nil
}

// ── Diff ──────────────────────────────────────────────────────────────────────

func (cs *Checksummer) Diff(ctx context.Context, absPathA, absPathB string) (DiffResult, error) {
	rA, err := cs.withCollect().Sum(ctx, absPathA)
	if err != nil {
		return DiffResult{}, fmt.Errorf("fshash: Diff A: %w", err)
	}
	rB, err := cs.withCollect().Sum(ctx, absPathB)
	if err != nil {
		return DiffResult{}, fmt.Errorf("fshash: Diff B: %w", err)
	}
	return buildDiff(rA.Entries, rB.Entries), nil
}

// ParallelDiff runs both Sum calls concurrently, halving wall-clock time on
// I/O-bound workloads.
func (cs *Checksummer) ParallelDiff(ctx context.Context, absPathA, absPathB string) (DiffResult, error) {
	type sr struct {
		res Result
		err error
	}
	chA, chB := make(chan sr, 1), make(chan sr, 1)
	go func() { r, e := cs.withCollect().Sum(ctx, absPathA); chA <- sr{r, e} }()
	go func() { r, e := cs.withCollect().Sum(ctx, absPathB); chB <- sr{r, e} }()
	rA, rB := <-chA, <-chB
	if rA.err != nil {
		return DiffResult{}, fmt.Errorf("fshash: ParallelDiff A: %w", rA.err)
	}
	if rB.err != nil {
		return DiffResult{}, fmt.Errorf("fshash: ParallelDiff B: %w", rB.err)
	}
	return buildDiff(rA.res.Entries, rB.res.Entries), nil
}

func buildDiff(aEntries, bEntries []EntryResult) DiffResult {
	aMap := entryMap(aEntries)
	bMap := entryMap(bEntries)
	var dr DiffResult
	for p, da := range aMap {
		if db, ok := bMap[p]; !ok {
			dr.Removed = append(dr.Removed, p)
		} else if !bytes.Equal(da, db) {
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

func entryMap(entries []EntryResult) map[string][]byte {
	m := make(map[string][]byte, len(entries))
	for i := range entries {
		if entries[i].RelPath != "." {
			m[entries[i].RelPath] = entries[i].Digest
		}
	}
	return m
}

// ── SumMany ───────────────────────────────────────────────────────────────────

// SumMany computes digests for all paths concurrently, returning results in
// the same order as the input slice. Errors are per-path; a nil error means
// success for that index.
func (cs *Checksummer) SumMany(ctx context.Context, paths []string) ([]Result, []error) {
	results := make([]Result, len(paths))
	errs := make([]error, len(paths))

	sem := make(chan struct{}, cs.opts.Workers)
	var wg sync.WaitGroup

	for i, p := range paths {
		i, p := i, p
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			for j := i; j < len(paths); j++ {
				errs[j] = ctx.Err()
			}
			wg.Wait()
			return results, errs
		}
		wg.Add(1)
		go func() {
			defer func() { <-sem; wg.Done() }()
			if ctx.Err() != nil {
				errs[i] = ctx.Err()
				return
			}
			results[i], errs[i] = cs.Sum(ctx, p)
		}()
	}
	wg.Wait()
	return results, errs
}

// ── Convenience functions ─────────────────────────────────────────────────────

// FileDigest returns the digest of a single file.
func FileDigest(ctx context.Context, absPath string, opts ...Option) ([]byte, error) {
	cs, err := New(append([]Option{WithWorkers(1)}, opts...)...)
	if err != nil {
		return nil, err
	}
	res, err := cs.Sum(ctx, absPath)
	if err != nil {
		return nil, err
	}
	return res.Digest, nil
}

// DirDigest returns the root digest of a directory tree.
func DirDigest(ctx context.Context, absPath string, opts ...Option) ([]byte, error) {
	cs, err := New(opts...)
	if err != nil {
		return nil, err
	}
	res, err := cs.Sum(ctx, absPath)
	if err != nil {
		return nil, err
	}
	return res.Digest, nil
}

// NewCachingChecksummer creates a Checksummer backed by the given cache.
func NewCachingChecksummer(cache FileCache, opts ...Option) (*Checksummer, error) {
	return New(append([]Option{WithFileCache(cache)}, opts...)...)
}
