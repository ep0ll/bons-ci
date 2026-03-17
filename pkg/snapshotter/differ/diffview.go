package diffview

// diffview.go – the DiffView orchestrator and public API surface.
//
// DiffView wires a Differ, a Deleter, and an Observer together and applies
// a diff between lower and upper to the merged directory.
//
// Dependency inversion: DiffView depends only on interfaces.
// New Differs, Deleters, or Observers can be plugged in without modifying
// this file.
//
// Concurrency model:
//
//   Differ.Diff     →  DiffStream.Entries channel
//       │
//       ▼
//   engine goroutine — reads entries, routes ActionDelete to worker pool
//       │
//       ├── worker goroutine 1  ── Deleter.Delete  ── Observer.OnEvent
//       ├── worker goroutine 2  ── Deleter.Delete  ── Observer.OnEvent
//       └── …
//
// Retained entries (ActionRetain) are handled inline by the engine goroutine
// without involving the worker pool.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
)

// ─── Option (functional options) ─────────────────────────────────────────────

// Option is a functional option for configuring a DiffView.
type Option func(*DiffView)

// WithDiffer sets the Differ used to compare lower and upper.
// Default: DirsyncDiffer with zero DirsyncDifferOptions.
func WithDiffer(d Differ) Option {
	return func(dv *DiffView) { dv.differ = d }
}

// WithDeleter sets the Deleter used to remove entries from merged.
// Default: FSDeleter (real filesystem deletions).
func WithDeleter(d Deleter) Option {
	return func(dv *DiffView) { dv.deleter = d }
}

// WithObserver sets the Observer that receives every event.
// Default: NoopObserver.
func WithObserver(o Observer) Option {
	return func(dv *DiffView) { dv.observer = o }
}

// WithWorkers sets the number of concurrent Deleter goroutines.
// Default: 1 (serial deletions; safe on local filesystems).
// Increase for NFS or object-storage backends.
func WithWorkers(n int) Option {
	return func(dv *DiffView) {
		if n > 0 {
			dv.workers = n
		}
	}
}

// ─── DiffView ─────────────────────────────────────────────────────────────────

// DiffView computes and applies a diff between lower and upper to a merged
// directory by deleting from merged the paths that are either exclusive to
// lower or identical in both lower and upper.
//
// Construct with New; configure with functional options.
type DiffView struct {
	differ   Differ
	deleter  Deleter
	observer Observer
	workers  int
}

// New creates a DiffView with the supplied options.
// Safe defaults are applied for any option that is not provided.
func New(opts ...Option) *DiffView {
	dv := &DiffView{
		differ:   NewDirsyncDiffer(DirsyncDifferOptions{}),
		deleter:  NewFSDeleter(),
		observer: NoopObserver{},
		workers:  1,
	}
	for _, o := range opts {
		o(dv)
	}
	return dv
}

// ─── Result ───────────────────────────────────────────────────────────────────

// Result summarises the outcome of an Apply call.
//
// All counts represent completed actions:
//   - DeletedExclusive / DeletedEqual: Deleter.Delete returned nil.
//   - A failure does NOT increment these counts; it increments DeleteFailed
//     and adds an entry to Err.
type Result struct {
	// DeletedExclusive: lower-exclusive paths successfully deleted from merged.
	DeletedExclusive int

	// DeletedEqual: common-and-equal paths successfully deleted from merged.
	DeletedEqual int

	// RetainedDiff: common-and-different paths preserved in merged (the effective diff).
	RetainedDiff int

	// RetainedHashErr: paths preserved because content hashing failed.
	RetainedHashErr int

	// DeleteFailed: paths whose deletion was attempted but failed.
	DeleteFailed int

	// Err is non-nil when one or more deletions failed or the walk failed.
	// Type-assert to *DeletionErrors to inspect individual failures.
	Err error
}

// ─── DeletionErrors ───────────────────────────────────────────────────────────

// DeletionErrors collects every individual failure from a single Apply call.
//
//	var dErr *diffview.DeletionErrors
//	if errors.As(result.Err, &dErr) {
//	    for _, e := range dErr.Errors {
//	        log.Printf("failed: %s – %v", e.RelPath, e.Err)
//	    }
//	}
type DeletionErrors struct {
	Errors []DeletionError
}

func (e *DeletionErrors) Error() string {
	return fmt.Sprintf("diffview: %d deletion(s) failed", len(e.Errors))
}

// DeletionError is one failure from the DeletionErrors list.
type DeletionError struct {
	RelPath   string // relative path
	MergedAbs string // absolute path in merged that could not be deleted
	Err       error
}

func (e DeletionError) Error() string {
	return fmt.Sprintf("delete %q: %v", e.MergedAbs, e.Err)
}

func (e DeletionError) Unwrap() error { return e.Err }

// ─── Apply ────────────────────────────────────────────────────────────────────

// Apply computes the diff between lowerRoot and upperRoot and applies it to
// mergedRoot by deleting the appropriate paths.
//
// Deletions target mergedRoot, not lowerRoot or upperRoot.  The relative paths
// produced by the Differ are resolved against mergedRoot.
//
// Apply blocks until all deletions are complete and returns a Result.
// It returns a non-nil error only for configuration problems (empty paths,
// invalid glob patterns, Differ startup failure).  Deletion failures are
// embedded in Result.Err as *DeletionErrors.
func (dv *DiffView) Apply(ctx context.Context, lowerRoot, upperRoot, mergedRoot string) (Result, error) {
	if lowerRoot == "" {
		return Result{}, errors.New("diffview: lowerRoot must not be empty")
	}
	if upperRoot == "" {
		return Result{}, errors.New("diffview: upperRoot must not be empty")
	}
	if mergedRoot == "" {
		return Result{}, errors.New("diffview: mergedRoot must not be empty")
	}

	stream, err := dv.differ.Diff(ctx, lowerRoot, upperRoot, mergedRoot)
	if err != nil {
		return Result{}, fmt.Errorf("diffview.Apply: %w", err)
	}

	return dv.run(ctx, stream), nil
}

// ─── run (internal engine) ────────────────────────────────────────────────────

// run drains the DiffStream and orchestrates the worker pool.
func (dv *DiffView) run(ctx context.Context, stream DiffStream) Result {
	// ── Atomic counters (incremented only after a confirmed outcome) ───────
	var (
		cDeletedExcl   atomic.Int64
		cDeletedEqual  atomic.Int64
		cRetainedDiff  atomic.Int64
		cRetainedHash  atomic.Int64
		cDeleteFailed  atomic.Int64
	)

	// ── Deletion error collection ─────────────────────────────────────────
	var (
		errMu   sync.Mutex
		delErrs []DeletionError
	)
	recordFailure := func(entry DiffEntry, err error) {
		errMu.Lock()
		delErrs = append(delErrs, DeletionError{
			RelPath:   entry.RelPath,
			MergedAbs: entry.MergedAbs,
			Err:       err,
		})
		errMu.Unlock()
		cDeleteFailed.Add(1)
		dv.observer.OnEvent(Event{Entry: entry, DeleteErr: err})
	}

	// ── Worker pool ────────────────────────────────────────────────────────
	// Workers call Deleter.Delete and then increment the appropriate counter.
	// Counter increments happen AFTER the outcome is known — ensuring counts
	// reflect reality, not intent.
	jobCh := make(chan DiffEntry, dv.workers*16)

	var poolWg sync.WaitGroup
	for i := 0; i < dv.workers; i++ {
		poolWg.Add(1)
		go func() {
			defer poolWg.Done()
			for entry := range jobCh {
				if ctx.Err() != nil {
					continue // drain without acting
				}
				if err := dv.deleter.Delete(entry); err != nil {
					recordFailure(entry, err)
				} else {
					// Successful deletion: increment and notify.
					switch entry.DeleteReason {
					case DeleteReasonExclusiveLower:
						cDeletedExcl.Add(1)
					case DeleteReasonCommonEqual:
						cDeletedEqual.Add(1)
					}
					dv.observer.OnEvent(Event{Entry: entry})
				}
			}
		}()
	}

	// ── Engine goroutine ───────────────────────────────────────────────────
	// Drains DiffStream.Entries; routes deletions to workers, handles
	// retentions inline (no deletion needed, just observe).
	for entry := range stream.Entries {
		switch entry.Action {
		case ActionDelete:
			select {
			case jobCh <- entry:
			case <-ctx.Done():
				// Drain without enqueuing — keeps Differ unblocked.
			}

		case ActionRetain:
			// Retained paths need no worker; observe and count inline.
			switch entry.RetainReason {
			case RetainReasonCommonDifferent:
				cRetainedDiff.Add(1)
			case RetainReasonHashError:
				cRetainedHash.Add(1)
			}
			dv.observer.OnEvent(Event{Entry: entry})
		}
	}

	// Signal pool: no more jobs.
	close(jobCh)
	poolWg.Wait()

	// Read the Differ's walk error only after Entries is fully drained.
	var walkErr error
	if err := <-stream.Err; err != nil {
		walkErr = err
	}

	return Result{
		DeletedExclusive: int(cDeletedExcl.Load()),
		DeletedEqual:     int(cDeletedEqual.Load()),
		RetainedDiff:     int(cRetainedDiff.Load()),
		RetainedHashErr:  int(cRetainedHash.Load()),
		DeleteFailed:     int(cDeleteFailed.Load()),
		Err:              combineErrors(walkErr, delErrs),
	}
}

// ─── Error helpers ────────────────────────────────────────────────────────────

func combineErrors(walkErr error, delErrs []DeletionError) error {
	if walkErr == nil && len(delErrs) == 0 {
		return nil
	}
	if walkErr != nil && len(delErrs) == 0 {
		return walkErr
	}
	if walkErr != nil {
		delErrs = append(delErrs, DeletionError{
			RelPath:   "<walk>",
			MergedAbs: "<walk>",
			Err:       walkErr,
		})
	}
	return &DeletionErrors{Errors: delErrs}
}
