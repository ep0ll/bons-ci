// Package upperpruner demonstrates how to use the dirsync package to prune
// from an upper directory every file that is also present in lower with
// identical content.
//
// # Semantics
//
//	upper directory = the MergedView (deletion target)
//	lower directory = the reference baseline
//
//	After Apply:
//	  upper keeps  : files that exist only in upper, OR that differ from lower
//	  upper removes: files whose content is identical in both lower and upper
//	                 (they are redundant — lower already carries the canonical copy)
//
// # Relationship to the overlay model
//
//	In a standard lower/upper/merged overlay:
//	  merged = lower ∪ (upper overrides lower)
//
//	This operation computes:
//	  upper' = upper − {paths where lower == upper}
//
//	That is: strip from upper everything that did not actually change relative
//	to lower, leaving only the true diff layer.
//
// # Wire-up
//
//	dirsync.NewClassifier(lower, upper)     → produces ExclusivePath + CommonPath streams
//	dirsync.NewHashPipeline(...)            → enriches CommonPath with HashEqual
//	dirsync.NewFSMergedView(upper)          → upper IS the target for deletions
//	dirsync.NewBestBatcher(view)            → io_uring on Linux 5.11+, goroutine pool elsewhere
//	dirsync.NewPipeline(...)
//	    WithExclusiveBatcher(NopBatcher)    → ignore lower-exclusive paths (they aren't in upper)
//	    WithCommonBatcher(batcher)          → delete hash-equal common paths from upper
//
// Note: exclusive paths (paths only in lower, not in upper) are ignored here
// because they do not exist in upper — there is nothing to delete.
package upperpruner

import (
	"context"
	"errors"
	"fmt"

	dirsync "github.com/bons/bons-ci/internal/dirsync"
)

// Result summarises a completed Prune call.
type Result struct {
	// CommonEqual is the number of paths that existed in both lower and upper
	// with identical content and were successfully deleted from upper.
	CommonEqual int64

	// CommonDifferent is the number of paths that existed in both lower and
	// upper but with different content. These are retained in upper.
	CommonDifferent int64

	// LowerExclusive is the number of paths that exist only in lower.
	// These are informational only — nothing is done to upper for them.
	LowerExclusive int64

	// Err holds any walk, hash, or batcher-flush errors from the pipeline.
	Err error
}

// Prune removes from upperDir every file whose content is identical to the
// corresponding file in lowerDir.
//
// After Prune, upperDir contains only the files that are either:
//   - Unique to upper  (no counterpart in lower), or
//   - Different from lower  (upper introduced a change)
//
// upperDir is both the comparison target and the deletion target.
// lowerDir is read-only; it is never modified.
//
// opts are applied to the dirsync.Classifier (e.g. include/exclude patterns,
// FollowSymlinks). Hash pipeline and batcher options are passed via pipeOpts.
func Prune(
	ctx context.Context,
	lowerDir, upperDir, merged string,
	classOpts []dirsync.ClassifierOption,
	pipeOpts []dirsync.PipelineOption,
) (Result, error) {
	if lowerDir == "" {
		return Result{}, errors.New("upperpruner: lowerDir must not be empty")
	}
	if upperDir == "" {
		return Result{}, errors.New("upperpruner: upperDir must not be empty")
	}
	if merged == "" {
		return Result{}, errors.New("upperpruner: merged must not be empty")
	}

	// ── Step 1: MergedView = upper directory ─────────────────────────────
	//
	// FSMergedView.Remove and RemoveAll operate on upperDir. When a BatchOp
	// is executed, the file at op.RelPath is deleted from upper.
	view, err := dirsync.NewFSMergedView(merged)
	if err != nil {
		return Result{}, fmt.Errorf("upperpruner: merged view on %q: %w", upperDir, err)
	}

	// ── Step 2: Batcher = best platform batcher targeting upper ───────────
	//
	// NewBestBatcher returns an IOURingBatcher on Linux 5.11+ (single
	// io_uring_enter per flush regardless of batch size) and a
	// GoroutineBatcher on older kernels and non-Linux platforms.
	//
	// The batcher is wired to `view` — every BatchOp it executes calls
	// view.Remove(relPath) or view.RemoveAll(relPath), which resolves the
	// path inside upperDir.
	batcher, err := dirsync.NewBestBatcher(view)
	if err != nil {
		return Result{}, fmt.Errorf("upperpruner: batcher: %w", err)
	}

	// ── Step 3: Atomic counters for the two relevant outcomes ─────────────
	//
	// We use handler closures (not struct types) here because the logic is
	// simple enough that a named type would add boilerplate without clarity.
	var result Result

	// Exclusive paths (lower-only): these do not exist in upper, so no
	// action is needed. We count them for informational purposes only.
	// NopBatcher swallows any BatchOp submitted for exclusive paths.
	excHandler := dirsync.ExclusiveHandlerFunc(
		func(_ context.Context, ep dirsync.ExclusivePath) error {
			result.LowerExclusive++
			return nil
		},
	)

	// Common paths: enriched by the HashPipeline with HashEqual before this
	// handler is called.
	//
	//   HashEqual == true  → content is identical → delete from upper
	//   HashEqual == false → upper differs from lower → keep in upper
	//   HashEqual == nil   → comparison not performed (dir, special file) → keep
	comHandler := dirsync.CommonHandlerFunc(
		func(_ context.Context, cp dirsync.CommonPath) error {
			eq, checked := cp.IsContentEqual()
			if checked && eq {
				result.CommonEqual++
				// Let the common batcher handle the deletion — it will call
				// view.Remove(cp.Path) when the batch is flushed.
				return nil
			}
			result.CommonDifferent++
			return nil
		},
	)

	// ── Step 4: Classifier — lower and upper, same as always ─────────────
	//
	// The classifier produces:
	//   ExclusivePath: path exists in lower but not in upper → excHandler
	//   CommonPath   : path exists in both                  → HashPipeline → comHandler
	classifier := dirsync.NewClassifier(lowerDir, upperDir, classOpts...)

	// ── Step 5: Assemble and run the pipeline ─────────────────────────────
	//
	// WithExclusiveBatcher(NopBatcher):
	//   Exclusive paths are not in upper — nothing to delete.
	//   The NopBatcher accepts and discards every op submitted for them.
	//
	// WithCommonBatcher(batcher):
	//   For common paths where hash comparison confirms equality, the pipeline
	//   calls batcher.Submit(OpRemove, relPath). The batcher batches those
	//   ops and flushes them to view (= upper) efficiently.
	opts := append(
		[]dirsync.PipelineOption{
			dirsync.WithExclusiveBatcher(dirsync.NopBatcher{}),
			dirsync.WithCommonBatcher(batcher),
		},
		pipeOpts...,
	)

	pl := dirsync.NewPipeline(classifier, excHandler, comHandler, opts...)
	pr := pl.Run(ctx)

	// The pipeline flushes the batcher internally at the end of Run, but
	// Close is still required to release io_uring ring resources (or the
	// goroutine pool's semaphore). Any flush error from Close is merged
	// into the result error.
	closeErr := batcher.Close(ctx)

	result.Err = errors.Join(pr.Err, closeErr)
	return result, nil
}
