package diffview

// diffview.go – DiffView: a thin facade over dirsync.Pipeline.
//
// # What diffview adds on top of dirsync
//
// dirsync provides the complete infrastructure:
//   - Classifier:    O(L+U) two-pointer walk producing exclusive/common streams
//   - HashPipeline:  parallel, pool-optimised content comparison
//   - Pipeline:      wires classifier → hash → handlers → batcher → MergedView
//   - MergedView:    abstraction over the merged directory's mutation operations
//   - Batcher:       batched syscall dispatch (GoroutineBatcher / IOURingBatcher)
//
// diffview adds only what dirsync does not provide:
//   - Domain vocabulary: DiffEntry, Action, DeleteReason, RetainReason
//   - Observer pattern:  classification events with human-readable context
//   - Apply API:         single call that wires and runs the full pipeline
//   - Result:            structured outcome with per-category counts
//
// # Pipeline topology (inside Apply)
//
//   dirsync.Classifier.Classify(ctx)
//       │
//       ├── exclusiveCh ──► exclusiveHandler ──► observer.OnEvent ──► batcher.Submit
//       │
//       └── rawCommonCh ──► HashPipeline.Run ──► hashedCh
//                                                    │
//                                                    └── commonHandler ──► observer.OnEvent
//                                                                          ──► batcher.Submit (if hash-equal)
//
//   dirsync.Pipeline.Run drives the above; Apply calls batcher.Close after.
//
// # Deletion semantics (upper - lower)
//
//   ExclusivePath (lower only)                → delete from merged (OpRemoveAll or OpRemove)
//   CommonPath where lower == upper (hash)    → delete from merged (OpRemove)
//   CommonPath where lower != upper (hash)    → retain in merged (no op)
//   CommonPath where comparison unchecked     → retain in merged (safety: no op)

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync/atomic"

	dirsync "github.com/bons/bons-ci/internal/dirsync"
)

// ─── Result ───────────────────────────────────────────────────────────────────

// Result summarises the outcome of a single Apply call.
//
// Counts reflect classification decisions, not confirmed filesystem outcomes.
// A BatchOp submission failure increments SubmitFailed and does NOT increment
// DeletedExclusive or DeletedEqual. Batcher flush errors (i.e. the actual
// unlink/rmdir syscall failures) are accumulated in Err after Apply returns.
type Result struct {
	// DeletedExclusive: exclusive-lower paths whose BatchOp was submitted.
	DeletedExclusive int

	// DeletedEqual: common-equal paths whose BatchOp was submitted.
	DeletedEqual int

	// RetainedDiff: common-different paths kept in merged (upper changed them).
	RetainedDiff int

	// RetainedHashErr: paths kept because content comparison failed.
	RetainedHashErr int

	// SubmitFailed: paths for which batcher.Submit returned an error.
	// Actual filesystem errors go into Err via the batcher's flush mechanism.
	SubmitFailed int

	// Err is non-nil when the walk, hash pipeline, or batcher flush produced
	// errors. Wraps dirsync.Pipeline's combined error output.
	Err error
}

// Total returns the sum of all classified paths.
func (r Result) Total() int {
	return r.DeletedExclusive + r.DeletedEqual +
		r.RetainedDiff + r.RetainedHashErr + r.SubmitFailed
}

// OK reports whether the run completed without any errors.
func (r Result) OK() bool { return r.Err == nil && r.SubmitFailed == 0 }

// ─── DiffView ─────────────────────────────────────────────────────────────────

// DiffView applies the upper-vs-lower diff to a merged directory.
//
// Construct with New; configure with functional options. A single DiffView
// instance may be reused across multiple Apply calls safely.
type DiffView struct {
	cfg config
}

// New creates a DiffView with the supplied options.
// Safe defaults are applied for every unset option:
//
//   - Observer:       NoopObserver
//   - MergedView:     FSMergedView (real filesystem)
//   - Batcher:        NewBestBatcher (io_uring on Linux 5.11+, GoroutineBatcher elsewhere)
//   - Hash workers:   runtime.NumCPU()
//   - Concurrency:    runtime.NumCPU() workers each for exclusive and common paths
func New(opts ...Option) *DiffView {
	cfg := defaultConfig()
	for _, o := range opts {
		o(&cfg)
	}
	return &DiffView{cfg: cfg}
}

// ─── Apply ────────────────────────────────────────────────────────────────────

// Apply computes the diff between lowerRoot and upperRoot and applies it to
// mergedRoot by deleting the appropriate paths from the merged directory.
//
// Deletion targets are always in mergedRoot, never in lowerRoot or upperRoot.
//
// Apply blocks until all classification, hashing, and batcher flush operations
// are complete, then returns a Result.
//
// Error handling:
//   - Empty root strings → immediate non-nil error return (no goroutines started).
//   - Walk or hash errors → Result.Err (pipeline ran to completion).
//   - Batcher flush errors → Result.Err (all entries were still classified).
//   - Batcher submit errors → Result.SubmitFailed (counted per-entry).
func (dv *DiffView) Apply(ctx context.Context, lowerRoot, upperRoot, mergedRoot string) (Result, error) {
	if err := validateRoots(lowerRoot, upperRoot, mergedRoot); err != nil {
		return Result{}, err
	}

	// ── Build infrastructure components ──────────────────────────────────
	view, err := dv.cfg.viewFactory(mergedRoot)
	if err != nil {
		return Result{}, fmt.Errorf("diffview.Apply: merged view: %w", err)
	}

	batcher, err := dv.cfg.batcherFactory(view)
	if err != nil {
		return Result{}, fmt.Errorf("diffview.Apply: batcher: %w", err)
	}

	classifier := dirsync.NewClassifier(lowerRoot, upperRoot, dv.cfg.classifierOpts...)
	hashPipeline := dirsync.NewHashPipeline(dv.cfg.hashOpts...)

	// ── Build atomic counters ─────────────────────────────────────────────
	// Counters are incremented only AFTER the relevant action completes.
	var (
		cDeletedExcl  atomic.Int64
		cDeletedEqual atomic.Int64
		cRetainedDiff atomic.Int64
		cRetainedHash atomic.Int64
		cSubmitFailed atomic.Int64
	)

	obs := dv.cfg.observer // capture for closures

	// ── ExclusiveHandler ─────────────────────────────────────────────────
	// Called by Pipeline.runExclusivePool for every lower-only path.
	// Converts to DiffEntry, notifies observer, submits deletion BatchOp.
	excHandler := dirsync.ExclusiveHandlerFunc(func(ctx context.Context, ep dirsync.ExclusivePath) error {
		entry := exclusiveToEntry(ep, mergedRoot)

		op := exclusiveToBatchOp(ep)
		submitErr := batcher.Submit(ctx, op)

		// Notify observer regardless of submit outcome so it sees every path.
		obs.OnEvent(Event{Entry: entry, SubmitErr: submitErr})

		if submitErr != nil {
			cSubmitFailed.Add(1)
			return fmt.Errorf("exclusive submit %q: %w", ep.Path, submitErr)
		}
		cDeletedExcl.Add(1)
		return nil
	})

	// ── CommonHandler ────────────────────────────────────────────────────
	// Called by Pipeline.runCommonPool for every hash-enriched common path.
	// Classification: equal → delete; different or unchecked → retain.
	comHandler := dirsync.CommonHandlerFunc(func(ctx context.Context, cp dirsync.CommonPath) error {
		entry := commonToEntry(cp, mergedRoot)

		if entry.Action == ActionDelete {
			op := commonToBatchOp(cp)
			submitErr := batcher.Submit(ctx, op)

			obs.OnEvent(Event{Entry: entry, SubmitErr: submitErr})

			if submitErr != nil {
				cSubmitFailed.Add(1)
				return fmt.Errorf("common submit %q: %w", cp.Path, submitErr)
			}
			cDeletedEqual.Add(1)
			return nil
		}

		// ActionRetain: no batcher submission, just observe and count.
		obs.OnEvent(Event{Entry: entry})
		switch entry.RetainReason {
		case RetainReasonCommonDifferent:
			cRetainedDiff.Add(1)
		case RetainReasonHashError:
			cRetainedHash.Add(1)
		}
		return nil
	})

	// ── Assemble and run the pipeline ────────────────────────────────────
	// dirsync.Pipeline handles all channel lifecycle, worker pool management,
	// context propagation, and error collection. We supply only the handlers.
	pipeOpts := append(
		dv.cfg.pipelineOpts,
		dirsync.WithHashPipeline(hashPipeline),
		// The batcher is submitted to by the handlers above; we do NOT also
		// wire WithExclusiveBatcher / WithCommonBatcher — that would cause
		// double submissions. The handlers own the batcher interaction.
	)

	pl := dirsync.NewPipeline(classifier, excHandler, comHandler, pipeOpts...)
	pResult := pl.Run(ctx)

	// Flush any remaining ops (auto-flush threshold may not have triggered
	// for the last batch) then release batcher resources.
	var flushErr error
	if err := batcher.Close(ctx); err != nil && !isContextErr(err) {
		flushErr = fmt.Errorf("batcher close: %w", err)
	}

	return Result{
		DeletedExclusive: int(cDeletedExcl.Load()),
		DeletedEqual:     int(cDeletedEqual.Load()),
		RetainedDiff:     int(cRetainedDiff.Load()),
		RetainedHashErr:  int(cRetainedHash.Load()),
		SubmitFailed:     int(cSubmitFailed.Load()),
		Err:              joinErrs(pResult.Err, flushErr),
	}, nil
}

// ─── Conversion helpers ───────────────────────────────────────────────────────

// exclusiveToEntry converts a dirsync.ExclusivePath to a diffview.DiffEntry.
// The ExclusivePath.Path field is the forward-slash relative path; MergedAbs
// is resolved against mergedRoot using the OS path separator.
func exclusiveToEntry(ep dirsync.ExclusivePath, mergedRoot string) DiffEntry {
	return DiffEntry{
		RelPath:      ep.Path,
		MergedAbs:    filepath.Join(mergedRoot, filepath.FromSlash(ep.Path)),
		Action:       ActionDelete,
		IsDir:        ep.Kind == dirsync.PathKindDir,
		Collapsed:    ep.Collapsed,
		DeleteReason: DeleteReasonExclusiveLower,
	}
}

// commonToEntry converts a hash-enriched dirsync.CommonPath to a DiffEntry.
//
// Decision rules (BuildKit DiffOp / upper-minus-lower semantics):
//
//	HashEqual checked and true   → ActionDelete / DeleteReasonCommonEqual
//	HashEqual checked and false  → ActionRetain / RetainReasonCommonDifferent
//	HashEqual nil (not checked)  → ActionRetain / RetainReasonCommonDifferent (safety)
//
// There is no RetainReasonHashError path here: hash errors are forwarded on
// HashPipeline's error channel and surface in PipelineResult.Err. When a hash
// fails, the CommonPath simply has HashEqual==nil and is treated as "different"
// (retain), which is the safe default.
func commonToEntry(cp dirsync.CommonPath, mergedRoot string) DiffEntry {
	base := DiffEntry{
		RelPath:   cp.Path,
		MergedAbs: filepath.Join(mergedRoot, filepath.FromSlash(cp.Path)),
		IsDir:     cp.Kind == dirsync.PathKindDir,
	}

	eq, checked := cp.IsContentEqual()
	if checked && eq {
		base.Action = ActionDelete
		base.DeleteReason = DeleteReasonCommonEqual
		return base
	}

	base.Action = ActionRetain
	base.RetainReason = RetainReasonCommonDifferent
	return base
}

// exclusiveToBatchOp converts an ExclusivePath to a BatchOp.
// Collapsed dirs use OpRemoveAll (one kernel op for the entire subtree);
// leaf entries use OpRemove.
func exclusiveToBatchOp(ep dirsync.ExclusivePath) dirsync.BatchOp {
	kind := dirsync.OpRemove
	if ep.Collapsed {
		kind = dirsync.OpRemoveAll
	}
	return dirsync.BatchOp{Kind: kind, RelPath: ep.Path, Tag: ep}
}

// commonToBatchOp converts a hash-equal CommonPath to a BatchOp.
// Common entries are always leaf removals (OpRemove); they are never collapsed.
func commonToBatchOp(cp dirsync.CommonPath) dirsync.BatchOp {
	return dirsync.BatchOp{Kind: dirsync.OpRemove, RelPath: cp.Path, Tag: cp}
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

// validateRoots returns a descriptive error if any root path is empty.
func validateRoots(lower, upper, merged string) error {
	switch {
	case lower == "":
		return errors.New("diffview: lowerRoot must not be empty")
	case upper == "":
		return errors.New("diffview: upperRoot must not be empty")
	case merged == "":
		return errors.New("diffview: mergedRoot must not be empty")
	}
	return nil
}

// joinErrs combines two errors losslessly via errors.Join.
// Returns nil when both are nil.
func joinErrs(a, b error) error {
	if a == nil && b == nil {
		return nil
	}
	return errors.Join(a, b)
}

// isContextErr reports whether err is a context cancellation or deadline.
func isContextErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
