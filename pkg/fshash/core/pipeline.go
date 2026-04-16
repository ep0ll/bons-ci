package core

import (
	"context"
	"hash"
	"io"
	"sort"
)

// ── Pipeline ──────────────────────────────────────────────────────────────────
//
// Pipeline is a composable operator chain over Stream[T].
// It provides functional combinators: Map, Filter, Reduce, Tee, Fan-out.
// All operators return a new Stream and are non-blocking.

// MapStream applies fn to every value emitted by src and forwards the result
// to a new Stream. The output stream closes when src closes.
func MapStream[A, B any](ctx context.Context, src *Stream[A], fn func(A) B) *Stream[B] {
	out := NewStream[B](ctx, cap(src.Chan()))
	go func() {
		defer out.Close()
		for v := range src.Chan() {
			if !out.Emit(fn(v)) {
				return
			}
		}
	}()
	return out
}

// FilterStream forwards only values for which keep returns true.
func FilterStream[T any](ctx context.Context, src *Stream[T], keep func(T) bool) *Stream[T] {
	out := NewStream[T](ctx, cap(src.Chan()))
	go func() {
		defer out.Close()
		for v := range src.Chan() {
			if keep(v) {
				if !out.Emit(v) {
					return
				}
			}
		}
	}()
	return out
}

// TeeStream fans src out to two independent Streams that both receive
// every event. Events are delivered synchronously so neither side blocks
// the other — if one is full, its copy is dropped.
func TeeStream[T any](ctx context.Context, src *Stream[T]) (*Stream[T], *Stream[T]) {
	a := NewStream[T](ctx, cap(src.Chan()))
	b := NewStream[T](ctx, cap(src.Chan()))
	go func() {
		defer a.Close()
		defer b.Close()
		for v := range src.Chan() {
			a.TryEmit(v) //nolint:errcheck
			b.TryEmit(v) //nolint:errcheck
		}
	}()
	return a, b
}

// DrainStream reads all values from src into a slice and returns it.
// Blocks until src is closed.
func DrainStream[T any](src *Stream[T]) []T {
	var out []T
	for v := range src.Chan() {
		out = append(out, v)
	}
	return out
}

// ── HashPipeline ──────────────────────────────────────────────────────────────
//
// HashPipeline hashes a stream of io.Reader values concurrently using a
// worker pool. Results are emitted in ARRIVAL ORDER (not input order);
// callers that need input order should use SumMany instead.

// HashResult pairs an input index with its computed digest (or error).
type HashResult struct {
	Index  int
	Digest []byte
	Err    error
}

// HashReaderStream hashes each reader in readers concurrently (up to workers
// goroutines) and streams HashResult values to the returned Stream as they
// complete. Order is non-deterministic; use Index for correlation.
func HashReaderStream(
	ctx context.Context,
	readers []io.Reader,
	newHash func() hash.Hash,
	pool BufPool,
	workers int,
) *Stream[HashResult] {
	if workers < 1 {
		workers = 1
	}
	out := NewStream[HashResult](ctx, len(readers))
	go func() {
		defer out.Close()
		wp := NewWorkerPool(workers)
		defer wp.Stop()
		for i, r := range readers {
			i, r := i, r
			wp.Submit(func() {
				h := newHash()
				buf := pool.GetStream()
				defer pool.Put(buf)
				_, err := copyBuffer(h, r, *buf)
				var dgst []byte
				if err == nil {
					var sink DigestSink
					dgst = CloneDigest(sink.Sum(h))
				}
				out.Emit(HashResult{Index: i, Digest: dgst, Err: err}) //nolint:errcheck
			})
		}
	}()
	return out
}

func copyBuffer(dst io.Writer, src io.Reader, buf []byte) (int64, error) {
	var total int64
	for {
		n, err := src.Read(buf)
		if n > 0 {
			nw, werr := dst.Write(buf[:n])
			total += int64(nw)
			if werr != nil {
				return total, werr
			}
		}
		if err == io.EOF {
			return total, nil
		}
		if err != nil {
			return total, err
		}
	}
}

// ── SortedBatch ───────────────────────────────────────────────────────────────
//
// SortedBatch collects all items from src into a slice and sorts them by
// the provided less function. Useful for ordering async results by index.

// SortStream drains src and returns a sorted slice.
func SortStream[T any](src *Stream[T], less func(T, T) bool) []T {
	items := DrainStream(src)
	sort.Slice(items, func(i, j int) bool { return less(items[i], items[j]) })
	return items
}
