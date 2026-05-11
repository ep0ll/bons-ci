package dirsync

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"sync"
)

// ─────────────────────────────────────────────────────────────────────────────
// HashPipeline
// ─────────────────────────────────────────────────────────────────────────────

// HashPipeline is a concurrent transformation stage that enriches [CommonPath]
// values with content equality information by comparing corresponding files in
// the lower and upper directories.
//
// # Two levels of parallelism
//
//   - File-level: up to Workers goroutines compare different file pairs simultaneously.
//   - Segment-level (large files only): [TwoPhaseHasher] spawns SegmentWorkers
//     goroutines that each read a different byte range of the same file pair
//     concurrently via pread64, with early cancellation on the first mismatch.
//
// # Stream contract
//
// Run returns the enriched channel. The caller must drain both the returned
// channel and the errCh passed in. Both channels are closed before Run's
// goroutine exits.
type HashPipeline struct {
	// Hasher performs the actual file comparison. Nil defaults to a
	// TwoPhaseHasher with the shared buffer and hash pools.
	Hasher ContentHasher

	// Workers is the maximum number of concurrent file-comparison goroutines.
	// Zero or negative defaults to runtime.NumCPU().
	Workers int

	// BufPool is forwarded to the default TwoPhaseHasher when Hasher is nil.
	BufPool *BufPool

}

// ─────────────────────────────────────────────────────────────────────────────
// Constructor
// ─────────────────────────────────────────────────────────────────────────────

// NewHashPipeline constructs a [HashPipeline] with sensible defaults.
// All settings can be overridden via [HashPipelineOption] functions.
func NewHashPipeline(opts ...HashPipelineOption) *HashPipeline {
	p := &HashPipeline{Workers: runtime.NumCPU()}
	for _, o := range opts {
		o(p)
	}
	// Build the concrete hasher only if the caller didn't supply one.
	if p.Hasher == nil {
		p.Hasher = &TwoPhaseHasher{BufPool: p.BufPool}
	}
	return p
}

// HashPipelineOption is a functional option for [HashPipeline].
type HashPipelineOption func(*HashPipeline)

// WithHasher replaces the default [TwoPhaseHasher] with a custom implementation.
// Useful for injecting test doubles or alternative comparison strategies.
func WithHasher(h ContentHasher) HashPipelineOption {
	return func(p *HashPipeline) { p.Hasher = h }
}

// WithHashWorkers sets the maximum number of concurrent file-comparison goroutines.
// Values ≤ 0 are ignored; the default (runtime.NumCPU()) remains.
func WithHashWorkers(n int) HashPipelineOption {
	return func(p *HashPipeline) {
		if n > 0 {
			p.Workers = n
		}
	}
}

// WithBufPool sets the buffer pool forwarded to the default TwoPhaseHasher.
// Has no effect when WithHasher is also used.
func WithBufPool(bp *BufPool) HashPipelineOption {
	return func(p *HashPipeline) { p.BufPool = bp }
}


// ─────────────────────────────────────────────────────────────────────────────
// Run
// ─────────────────────────────────────────────────────────────────────────────

// Run starts the hash-enrichment pipeline and returns the enriched channel.
//
// It consumes CommonPath values from in, performs content comparison for each
// regular file and symlink, stamps HashEqual on the result, and forwards all
// entries (including directories and special files) to the returned channel.
//
// Errors from comparison are forwarded to errCh and closed when all input
// has been processed.
//
// The returned channel is closed after all goroutines finish.
//
// Callers must drain both the returned channel and errCh to prevent goroutine
// leaks. The goroutines exit cleanly when ctx is cancelled.
func (p *HashPipeline) Run(
	ctx context.Context,
	lowerRoot, upperRoot string,
	in <-chan CommonPath,
	errCh chan<- error,
) <-chan CommonPath {
	workers := p.Workers
	if workers <= 0 {
		workers = runtime.NumCPU()
	}

	// Buffer the output to avoid head-of-line blocking: the output consumer
	// may be slower than the input producer during burst periods.
	out := make(chan CommonPath, cap(in)+workers)

	go func() {
		defer close(out)
		defer close(errCh)

		// Semaphore limits concurrent goroutines to workers.
		sem := make(chan struct{}, workers)
		var wg sync.WaitGroup

		for cp := range in {
			if ctx.Err() != nil {
				// Context cancelled: drain input without spawning new goroutines.
				// This drains the in channel so the classifier goroutine can exit.
				continue
			}

			cp := cp // capture loop variable before handing off to goroutine

			// Acquire a semaphore slot. Using select with ctx.Done() prevents
			// blocking forever when all slots are occupied and ctx is cancelled.
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				continue
			}

			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { <-sem }() // release slot when done

				p.enrichOne(ctx, lowerRoot, upperRoot, &cp, errCh)

				select {
				case out <- cp:
				case <-ctx.Done():
				}
			}()
		}
		wg.Wait()
	}()

	return out
}

// ─────────────────────────────────────────────────────────────────────────────
// enrichOne — per-entry comparison
// ─────────────────────────────────────────────────────────────────────────────

// enrichOne performs content comparison for one CommonPath and stamps HashEqual.
//
// Only regular files and symlinks are compared; directories and special files
// pass through with HashEqual == nil (comparison not applicable).
func (p *HashPipeline) enrichOne(
	ctx context.Context,
	lowerRoot, upperRoot string,
	cp *CommonPath,
	errCh chan<- error,
) {
	if ctx.Err() != nil {
		return
	}

	// Only compare types where byte-equality is meaningful.
	switch cp.Kind {
	case PathKindFile, PathKindSymlink:
		// proceed
	default:
		return // HashEqual stays nil for directories and special files
	}

	hasher := p.Hasher
	if hasher == nil {
		hasher = defaultTwoPhaseHasher
	}

	lAbs := filepath.Join(lowerRoot, filepath.FromSlash(cp.Path))
	uAbs := filepath.Join(upperRoot, filepath.FromSlash(cp.Path))

	eq, err := hasher.Equal(lAbs, uAbs, cp.LowerInfo, cp.UpperInfo)
	if err != nil {
		sendErr(ctx, errCh, fmt.Errorf("hash %q: %w", cp.Path, err))
		return
	}
	cp.HashEqual = hashEqualPtr(eq)
}
