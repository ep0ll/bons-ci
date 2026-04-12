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
)

// ── HashReader ────────────────────────────────────────────────────────────────

// HashReader computes a digest over the bytes produced by r using the algorithm
// selected in opts.  No filesystem access is performed; no metadata header is
// written — only the raw byte stream is hashed.
//
// This is useful for hashing in-memory data, network streams, or file contents
// you have already opened without going through [Checksummer.Sum].
//
// Example:
//
//	dgst, err := fshash.HashReader(ctx, strings.NewReader("hello, world"))
func HashReader(ctx context.Context, r io.Reader, opts ...Option) ([]byte, error) {
	cs, err := New(append([]Option{WithWorkers(1)}, opts...)...)
	if err != nil {
		return nil, err
	}

	h := cs.opts.Hasher.New()
	buf, _ := getBuf()
	defer putBuf(buf)

	for {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		n, rerr := r.Read(*buf)
		if n > 0 {
			mustWrite(h, (*buf)[:n])
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

// ── Walk ─────────────────────────────────────────────────────────────────────

// WalkFunc is called once per visited entry after its digest is computed.
// Returning a non-nil error aborts the walk and causes [Checksummer.Walk] to
// return that error (wrapped).
//
// Entries arrive in sorted relPath order (bottom-up for directories): a
// directory's WalkFunc call happens after all of its children have been
// visited.
type WalkFunc func(entry EntryResult) error

// Walk computes the checksum of absPath, calling fn for every visited entry in
// sorted relPath order.  It returns the root digest alongside any error from fn
// or the filesystem.
//
// Walk is equivalent to Sum with CollectEntries=true followed by iterating
// res.Entries, but makes the streaming intent explicit.
func (cs *Checksummer) Walk(ctx context.Context, absPath string, fn WalkFunc) (Result, error) {
	res, err := cs.withCollect().Sum(ctx, absPath)
	if err != nil {
		return Result{}, err
	}

	for _, e := range res.Entries {
		if err := fn(e); err != nil {
			return Result{}, fmt.Errorf("fshash: Walk callback: %w", err)
		}
	}
	return Result{Digest: res.Digest}, nil
}

// ── Canonicalize ──────────────────────────────────────────────────────────────

// Canonicalize writes a human-readable, line-oriented representation of the
// tree to w.  Each line has the form:
//
//	<hex-digest>  <kind>    <rel-path>
//
// Entries are written in sorted relPath order.  The final line is always the
// root digest with kind "root" and relPath ".".
//
// This format is suitable for auditing, diffing, and storage in version
// control.  It is deliberately similar to sha256sum(1) output.
//
// Canonicalize returns the root digest (identical to [Checksummer.Sum]).
func (cs *Checksummer) Canonicalize(ctx context.Context, absPath string, w io.Writer) ([]byte, error) {
	res, err := cs.withCollect().Sum(ctx, absPath)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	for _, e := range res.Entries {
		if e.RelPath == "." {
			// The root entry is represented by the "root" summary line below;
			// emitting it again here would create a confusing duplicate.
			continue
		}
		fmt.Fprintf(&buf, "%s  %-7s  %s\n", e.Hex(), e.Kind.String(), e.RelPath)
	}
	// The final "root" line always carries the overall root digest.
	fmt.Fprintf(&buf, "%s  %-7s  .\n", res.Hex(), "root")

	if _, err := w.Write(buf.Bytes()); err != nil {
		return nil, fmt.Errorf("fshash: Canonicalize write: %w", err)
	}
	return res.Digest, nil
}

// ── CanonicalEntry / ReadCanonical ───────────────────────────────────────────

// CanonicalEntry is one parsed record from the output of [Checksummer.Canonicalize].
type CanonicalEntry struct {
	// Digest is the raw digest bytes for this entry.
	Digest []byte
	// Kind is the string form of [EntryKind] (e.g. "file", "dir", "symlink",
	// "other") or "root" for the final summary line.
	Kind string
	// RelPath is the slash-separated path relative to the root, or "." for the
	// root summary line.
	RelPath string
}

// ReadCanonical parses the output produced by [Checksummer.Canonicalize] and
// returns one [CanonicalEntry] per line.  The last entry always has Kind "root".
//
// The line format is exactly "<hex-digest>  <kind>  <relpath>" with two spaces
// between each field.  Paths containing single spaces are handled correctly.
func ReadCanonical(r io.Reader) ([]CanonicalEntry, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("fshash: ReadCanonical: %w", err)
	}

	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	out := make([]CanonicalEntry, 0, len(lines))

	const sep = "  " // exactly two spaces, matching Canonicalize output
	for lineNum, line := range lines {
		if line == "" {
			continue
		}
		// Split on the first two-space separator to get digest.
		idx1 := strings.Index(line, sep)
		if idx1 < 0 {
			return nil, fmt.Errorf("fshash: ReadCanonical line %d: missing separator after digest", lineNum+1)
		}
		digestHex := line[:idx1]
		rest := line[idx1+len(sep):]

		// Split rest on the next two-space separator to get kind and relpath.
		idx2 := strings.Index(rest, sep)
		if idx2 < 0 {
			return nil, fmt.Errorf("fshash: ReadCanonical line %d: missing separator after kind", lineNum+1)
		}
		kind := strings.TrimSpace(rest[:idx2]) // trim padding spaces from kind column
		relPath := rest[idx2+len(sep):]

		dgst, err := hex.DecodeString(digestHex)
		if err != nil {
			return nil, fmt.Errorf("fshash: ReadCanonical line %d: bad digest %q: %w", lineNum+1, digestHex, err)
		}
		out = append(out, CanonicalEntry{
			Digest:  dgst,
			Kind:    kind,
			RelPath: relPath,
		})
	}
	return out, nil
}

// ── ParallelDiff ──────────────────────────────────────────────────────────────

// ParallelDiff computes both directory trees concurrently and returns their
// diff.  It is equivalent to [Checksummer.Diff] but halves wall-clock time on
// I/O-bound workloads by running both Sum calls simultaneously.
func (cs *Checksummer) ParallelDiff(ctx context.Context, absPathA, absPathB string) (DiffResult, error) {
	type sumResult struct {
		res Result
		err error
	}

	chA := make(chan sumResult, 1)
	chB := make(chan sumResult, 1)

	go func() {
		res, err := cs.withCollect().Sum(ctx, absPathA)
		chA <- sumResult{res, err}
	}()
	go func() {
		res, err := cs.withCollect().Sum(ctx, absPathB)
		chB <- sumResult{res, err}
	}()

	rA, rB := <-chA, <-chB

	if rA.err != nil {
		return DiffResult{}, fmt.Errorf("fshash: ParallelDiff A: %w", rA.err)
	}
	if rB.err != nil {
		return DiffResult{}, fmt.Errorf("fshash: ParallelDiff B: %w", rB.err)
	}

	aMap := entryMap(rA.res.Entries)
	bMap := entryMap(rB.res.Entries)

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
	return dr, nil
}

// ── SumMany ──────────────────────────────────────────────────────────────────

// SumMany computes the digest of each path in paths concurrently and returns
// results in the same order as paths.
//
// At most [Options.Workers] Sum calls run simultaneously.  Because each Sum
// call spawns its own internal worker pool, the total goroutine count is at
// most Workers² — acceptable for typical values but worth noting for very
// large worker counts.
//
// Errors are returned per-path; a nil error means success for that path.
// SumMany never returns a top-level non-nil error.
func (cs *Checksummer) SumMany(ctx context.Context, paths []string) (results []Result, errs []error) {
	results = make([]Result, len(paths))
	errs = make([]error, len(paths))

	// Semaphore limits simultaneous Sum calls to cs.opts.Workers.
	sem := make(chan struct{}, cs.opts.Workers)
	var wg sync.WaitGroup

	for i, p := range paths {
		i, p := i, p
		// Acquire a semaphore slot.  Use a select so that ctx cancellation
		// unblocks the loop even when all workers are busy.
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			// Fill remaining results with ctx error without spawning goroutines.
			for j := i; j < len(paths); j++ {
				errs[j] = ctx.Err()
			}
			wg.Wait()
			return results, errs
		}
		wg.Add(1)
		go func() {
			defer func() {
				<-sem // release slot
				wg.Done()
			}()
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
