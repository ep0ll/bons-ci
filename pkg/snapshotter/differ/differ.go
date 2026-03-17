package diffview

// differ.go – the Differ interface and its dirsync-backed implementation.
//
// A Differ compares a lower and upper directory tree and streams DiffEntry
// values for every path it encounters.  The concrete implementation here
// (DirsyncDiffer) delegates to the dirsync package; any other implementation
// can be plugged in by satisfying the Differ interface.
//
// Interface segregation: Differ knows nothing about Deleters or Observers.

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/bons/bons-ci/internal/dirsync"
)

// ─── Differ interface ─────────────────────────────────────────────────────────

// DiffStream is the output of a Differ.
//
// ⚠ IMPORTANT: Entries must be fully drained before Err is read.
// Failing to drain Entries blocks the background goroutine.
type DiffStream struct {
	// Entries emits one DiffEntry per path examined during the comparison.
	Entries <-chan DiffEntry

	// Err carries the first fatal walk error, or is closed empty on success.
	// Read only after Entries is fully drained.
	Err <-chan error
}

// Differ compares lower and upper directory trees and produces a DiffStream
// describing every path's disposition relative to the merged directory.
//
// Implementations must be safe to call from multiple goroutines concurrently
// (each call to Diff should be independent).
//
// The merged parameter is passed through so the Differ can compute MergedAbs
// for each entry; the Differ itself does not read from or write to merged.
type Differ interface {
	// Diff starts the comparison and returns immediately.
	// A non-nil error return means the comparison could not be started (e.g.
	// invalid options); it is synchronous and does not involve goroutines.
	Diff(ctx context.Context, lower, upper, merged string) (DiffStream, error)
}

// ─── DirsyncDiffer ────────────────────────────────────────────────────────────

// DirsyncDifferOptions configures DirsyncDiffer.
type DirsyncDifferOptions struct {
	// FollowSymlinks: follow symlinks when stating entries.
	FollowSymlinks bool

	// HashWorkers: goroutines dedicated to SHA-256 content hashing.
	// 0 → runtime.GOMAXPROCS(0).
	HashWorkers int

	// DirsyncOpts carries additional dirsync options (patterns, filters, etc.).
	// FollowSymlinks and HashWorkers from this struct are overridden by the
	// top-level fields above.
	DirsyncOpts dirsync.Options
}

// DirsyncDiffer is a Differ backed by the dirsync package.
//
// It compares lower and upper using dirsync.Diff (O(N) merge-sort walk with
// incremental SHA-256 hashing) and classifies each result:
//
//   ExclusivePath                → DiffEntry{Action: ActionDelete, DeleteReason: ExclusiveLower}
//   CommonPath where equal       → DiffEntry{Action: ActionDelete, DeleteReason: CommonEqual}
//   CommonPath where different   → DiffEntry{Action: ActionRetain, RetainReason: CommonDifferent}
//   CommonPath with hash error   → DiffEntry{Action: ActionRetain, RetainReason: HashError, Err: …}
type DirsyncDiffer struct {
	opts DirsyncDifferOptions
}

// NewDirsyncDiffer creates a DirsyncDiffer with the given options.
func NewDirsyncDiffer(opts DirsyncDifferOptions) *DirsyncDiffer {
	return &DirsyncDiffer{opts: opts}
}

// Diff implements Differ.
func (d *DirsyncDiffer) Diff(ctx context.Context, lower, upper, merged string) (DiffStream, error) {
	dOpts := d.opts.DirsyncOpts
	dOpts.FollowSymlinks = d.opts.FollowSymlinks
	dOpts.HashWorkers = d.opts.HashWorkers

	result, err := dirsync.Diff(ctx, lower, upper, dOpts)
	if err != nil {
		return DiffStream{}, fmt.Errorf("DirsyncDiffer.Diff: %w", err)
	}

	entryCh := make(chan DiffEntry, cap(result.Exclusive)+cap(result.Common))
	errCh := make(chan error, 1)

	go func() {
		defer close(entryCh)
		defer close(errCh)

		fanIn(ctx, result, merged, entryCh, errCh)
	}()

	return DiffStream{Entries: entryCh, Err: errCh}, nil
}

// fanIn drains both dirsync channels concurrently, converts each item into a
// DiffEntry, and sends them to entryCh.  Walk errors are forwarded to errCh.
//
// Both dirsync channels MUST be fully drained before result.Err is read.
func fanIn(
	ctx context.Context,
	result dirsync.Result,
	merged string,
	entryCh chan<- DiffEntry,
	errCh chan<- error,
) {
	// Use two goroutines to drain the dirsync channels simultaneously — they
	// are independent and neither can be allowed to block the other.
	done := make(chan struct{}, 2)

	// Goroutine A: drain ExclusivePath channel.
	go func() {
		defer func() { done <- struct{}{} }()
		for ep := range result.Exclusive {
			entry := exclusiveToEntry(ep, merged)
			select {
			case entryCh <- entry:
			case <-ctx.Done():
				// Drain without sending — keeps dirsync goroutine unblocked.
			}
		}
	}()

	// Goroutine B: drain CommonPath channel.
	go func() {
		defer func() { done <- struct{}{} }()
		for cp := range result.Common {
			entry := commonToEntry(cp, merged)
			select {
			case entryCh <- entry:
			case <-ctx.Done():
			}
		}
	}()

	// Wait for both drainers to finish, then read the walk error.
	<-done
	<-done

	if walkErr := <-result.Err; walkErr != nil && ctx.Err() == nil {
		errCh <- fmt.Errorf("DirsyncDiffer: walk: %w", walkErr)
	}
}

// ─── Conversion helpers ───────────────────────────────────────────────────────

// exclusiveToEntry converts an ExclusivePath (present only in lower) to a
// DiffEntry targeting the merged directory.
func exclusiveToEntry(ep dirsync.ExclusivePath, merged string) DiffEntry {
	return DiffEntry{
		RelPath:      ep.RelPath,
		MergedAbs:    filepath.Join(merged, ep.RelPath),
		Action:       ActionDelete,
		IsDir:        ep.IsDir,
		Pruned:       ep.Pruned,
		DeleteReason: DeleteReasonExclusiveLower,
	}
}

// commonToEntry converts a CommonPath (present in both lower and upper) to a
// DiffEntry targeting the merged directory.
//
// Classification:
//   - Hash or readlink error      → ActionRetain / RetainReasonHashError
//   - MetaEqual or HashEqual      → ActionDelete / DeleteReasonCommonEqual
//   - HashChecked && !HashEqual   → ActionRetain / RetainReasonCommonDifferent
//   - Neither (defensive fallback)→ ActionRetain / RetainReasonCommonDifferent
func commonToEntry(cp dirsync.CommonPath, merged string) DiffEntry {
	base := DiffEntry{
		RelPath:   cp.RelPath,
		MergedAbs: filepath.Join(merged, cp.RelPath),
		IsDir:     cp.LowerInfo != nil && cp.LowerInfo.IsDir(),
		Pruned:    false, // common dirs are traversed, not emitted as pruned roots
	}

	// Per-entry hash / readlink error: preserve for safety.
	if cp.Err != nil {
		base.Action = ActionRetain
		base.RetainReason = RetainReasonHashError
		base.Err = cp.Err
		return base
	}

	if isEqualCommon(cp) {
		base.Action = ActionDelete
		base.DeleteReason = DeleteReasonCommonEqual
		return base
	}

	// Content differs: upper introduced a change — preserve in merged.
	base.Action = ActionRetain
	base.RetainReason = RetainReasonCommonDifferent
	return base
}

// isEqualCommon reports whether lower and upper copies of a CommonPath have
// identical content.
//
// Decision table:
//
//	MetaEqual == true                          → equal (fast path, 0 I/O)
//	HashChecked == true  && HashEqual == true  → equal (SHA-256 / readlink confirmed)
//	HashChecked == true  && HashEqual == false → different
//	HashChecked == false && MetaEqual == false → treat as different (defensive)
func isEqualCommon(cp dirsync.CommonPath) bool {
	if cp.MetaEqual {
		return true
	}
	if cp.HashChecked {
		return cp.HashEqual
	}
	return false
}
