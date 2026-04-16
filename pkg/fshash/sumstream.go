package fshash

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/bons/bons-ci/pkg/fshash/core"
)

// ── SumStream ─────────────────────────────────────────────────────────────────

// SumStream hashes absPath and pushes each EntryResult to the returned
// [core.Stream] as soon as its digest is ready — file entries arrive as they
// complete in parallel, and a directory entry arrives immediately after all of
// its children (bottom-up). The root "." entry is always the last item emitted.
//
// Unlike Walk (which collects everything first), SumStream allows consumers to
// process entries concurrently with the ongoing filesystem traversal:
//
//	stream := cs.SumStream(ctx, "/data")
//	for e := range stream.Chan() {
//	    fmt.Printf("%s  %s\n", e.Hex(), e.RelPath)
//	}
//
// The stream is closed once the root entry is emitted or ctx is cancelled.
// A buffer of 256 entries is pre-allocated; use [Checksummer.BoundedSumStream]
// when memory pressure matters.
func (cs *Checksummer) SumStream(ctx context.Context, absPath string) *core.Stream[EntryResult] {
	return cs.BoundedSumStream(ctx, absPath, 256)
}

// BoundedSumStream is like SumStream but accepts an explicit channel buffer
// size. A small bufSize (e.g. 8) exercises backpressure against the walker
// when the consumer is slower than the hasher.
func (cs *Checksummer) BoundedSumStream(ctx context.Context, absPath string, bufSize int) *core.Stream[EntryResult] {
	if bufSize < 1 {
		bufSize = 64
	}
	s := core.NewStream[EntryResult](ctx, bufSize)
	go func() {
		defer s.Close()
		fi, err := cs.opts.Walker.Lstat(absPath)
		if err != nil {
			return
		}
		wp := core.NewWorkerPool(cs.opts.Workers)
		defer wp.Stop()
		var visited map[string]struct{}
		if cs.opts.FollowSymlinks {
			visited = make(map[string]struct{}, 8)
		}
		// The collect callback pushes entries to the stream as they are computed.
		// This is what makes SumStream truly streaming rather than collect-then-drain.
		cs.dispatch(ctx, wp, absPath, ".", fi, visited, func(e EntryResult) { //nolint:errcheck
			s.Emit(e) // closed stream silently drops; ctx guards the loop
		})
	}()
	return s
}

// ── MerkleStream ──────────────────────────────────────────────────────────────

// MerkleStream walks absPath emitting every entry except the root "." node,
// suitable for building content-addressed Merkle structures. Entries arrive
// in the same bottom-up order as SumStream.
//
// The root digest can be reconstructed from the emitted entries using
// [MerkleRoot].
func (cs *Checksummer) MerkleStream(ctx context.Context, absPath string) *core.Stream[EntryResult] {
	return core.FilterStream(ctx, cs.SumStream(ctx, absPath),
		func(e EntryResult) bool { return e.RelPath != "." })
}

// MerkleRoot reconstructs the root digest from a completed MerkleStream by
// hashing all (relPath, digest) pairs in sorted relPath order — identical to
// what [Checksummer.Sum] would return for the same tree.
//
// drain must be the fully-consumed output of MerkleStream (after its channel
// closes).
func MerkleRoot(entries []EntryResult, newHash func() interface {
	Write([]byte) (int, error)
	Sum([]byte) []byte
}) []byte {
	sorted := make([]EntryResult, len(entries))
	copy(sorted, entries)
	slices.SortFunc(sorted, func(a, b EntryResult) int {
		if a.RelPath < b.RelPath {
			return -1
		}
		if a.RelPath > b.RelPath {
			return 1
		}
		return 0
	})
	h := newHash()
	nul := []byte{0x00}
	for _, e := range sorted {
		core.WriteString(h, e.RelPath)
		core.MustWrite(h, nul)
		core.MustWrite(h, e.Digest)
	}
	return core.CloneDigest(h.Sum(nil))
}

// ── ParallelSumStream ─────────────────────────────────────────────────────────

// ParallelSumStream hashes all paths in absPaths concurrently, merging their
// entry streams into a single output [core.Stream]. Entries from different roots
// may interleave; each entry's RelPath is prefixed with "<index>:" so consumers
// can correlate entries back to their originating root.
//
//	for e := range cs.ParallelSumStream(ctx, roots).Chan() {
//	    idx, rel, _ := strings.Cut(e.RelPath, ":")
//	    fmt.Printf("root[%s]: %s %s\n", idx, rel, e.Hex())
//	}
func (cs *Checksummer) ParallelSumStream(ctx context.Context, absPaths []string) *core.Stream[EntryResult] {
	out := core.NewStream[EntryResult](ctx, len(absPaths)*64)
	go func() {
		defer out.Close()
		var wg sync.WaitGroup
		for i, p := range absPaths {
			i, p := i, p
			wg.Add(1)
			go func() {
				defer wg.Done()
				prefix := fmt.Sprintf("%d:", i)
				for e := range cs.SumStream(ctx, p).Chan() {
					e.RelPath = prefix + e.RelPath
					if !out.Emit(e) {
						return
					}
				}
			}()
		}
		wg.Wait()
	}()
	return out
}

// ── ChangeFeed ────────────────────────────────────────────────────────────────

// ChangedEntry augments [EntryResult] with the previous digest for the same
// path. PrevDigest is nil for entries that did not exist in the prior poll.
type ChangedEntry struct {
	EntryResult
	// PrevDigest is nil on the first poll (entry is new) or when the entry
	// is first seen. It is non-nil on subsequent polls when a change was
	// detected.
	PrevDigest []byte
}

// ChangeFeed polls absPath at interval and emits [ChangedEntry] values to the
// returned [core.Stream] for every entry whose digest changes between polls.
//
// The first poll emits all entries with PrevDigest==nil ("new"). Subsequent
// polls emit only entries that were added, removed, or modified.
//
// The stream is closed when ctx is cancelled. The caller controls poll
// frequency via interval; a zero interval defaults to 5 seconds.
//
//	feed := cs.ChangeFeed(ctx, "/data", 30*time.Second)
//	for change := range feed.Chan() {
//	    fmt.Printf("changed: %s (was %x now %x)\n",
//	        change.RelPath, change.PrevDigest, change.Digest)
//	}
func (cs *Checksummer) ChangeFeed(ctx context.Context, absPath string, interval time.Duration) *core.Stream[ChangedEntry] {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	out := core.NewStream[ChangedEntry](ctx, 64)
	go func() {
		defer out.Close()
		prev := make(map[string][]byte)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		// First pass: emit everything immediately without waiting for tick.
		if !cs.changeFeedPass(ctx, absPath, prev, out, true) {
			return
		}

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if !cs.changeFeedPass(ctx, absPath, prev, out, false) {
					return
				}
			}
		}
	}()
	return out
}

// changeFeedPass runs one Sum pass. firstPass=true emits all entries with nil
// PrevDigest; firstPass=false emits only entries that changed.
// Returns false if the stream was closed or ctx was cancelled.
func (cs *Checksummer) changeFeedPass(
	ctx context.Context,
	absPath string,
	prev map[string][]byte,
	out *core.Stream[ChangedEntry],
	firstPass bool,
) bool {
	res, err := cs.withCollect().Sum(ctx, absPath)
	if err != nil {
		return false
	}
	curr := make(map[string][]byte, len(res.Entries))
	for _, e := range res.Entries {
		curr[e.RelPath] = e.Digest
		prevDgst := prev[e.RelPath]
		changed := firstPass || !bytesEq(prevDgst, e.Digest)
		if changed {
			if !out.Emit(ChangedEntry{EntryResult: e, PrevDigest: prevDgst}) {
				return false
			}
		}
	}
	// Emit removed entries (existed in prev but not curr).
	if !firstPass {
		for relPath, prevDgst := range prev {
			if _, exists := curr[relPath]; !exists {
				removed := ChangedEntry{
					EntryResult: EntryResult{RelPath: relPath, Digest: nil},
					PrevDigest:  prevDgst,
				}
				if !out.Emit(removed) {
					return false
				}
			}
		}
	}
	// Replace prev with curr for next pass.
	for k := range prev {
		delete(prev, k)
	}
	for k, v := range curr {
		prev[k] = v
	}
	return ctx.Err() == nil
}

func bytesEq(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
