package dirprune

// pruner.go – Pruner: delete everything that does not match the filter.
//
// # Problem statement
//
// Given a target directory and a set of filter rules (include patterns,
// exclude patterns, required paths, etc.), delete every entry in that
// directory that does not satisfy the rules. Entries that satisfy the rules
// are left completely untouched.
//
// # Algorithm
//
// The core is a recursive single-directory walk using os.ReadDir (which returns
// entries in lexicographic order). For each entry the Pruner evaluates the
// dirsync Filter:
//
//   filter.Include(relPath, isDir) == false
//     → entry does not match the rules → must be deleted.
//     → If isDir: emit Collapsed (OpRemoveAll), do NOT recurse.
//       One syscall removes the entire subtree regardless of depth.
//     → If file/symlink: emit Deleted (OpRemove).
//
//   filter.Include(relPath, isDir) == true — two sub-cases:
//     (A) directMatch: filter.Include(relPath, false) == true
//         The directory itself explicitly matches a pattern (e.g. "src").
//         → Recurse to check children; directory is unconditionally kept.
//     (B) descentOnly: filter.Include(relPath, false) == false
//         The directory passes only because a wildcard might match something
//         underneath it (e.g. "*.go" allows descent into any dir).
//         → Recurse. Track whether any child was kept (keptAny).
//         → If keptAny: keep the directory (it is a live parent).
//         → If !keptAny: no matching descendant was found; emit OpRemoveAll
//           for the now-empty directory so it is cleaned up.
//
// # BUG FIX C3 (critical)
//
// The original walkDir treated both sub-cases identically: recurse and never
// emit any op for the directory itself. When case (B) fired (e.g. "*.go" with
// directory "scripts/") and no child matched, the children were deleted but
// the empty directory was left on disk.
//
// The fix adds a keptAny return value to walkDir and emits OpRemoveAll for the
// directory when recursion yields zero kept entries.
//
// # Syscall efficiency
//
// Non-matching directories (filter.Include==false) are collapsed into a single
// OpRemoveAll, stopping recursion immediately. This reduces deletion cost from
// O(N_files) to O(1) for unmatched subtrees.
//
// # Required-path pre-flight
//
// RequiredPaths are validated before the walk starts. If any required path is
// absent, Prune returns a descriptive error without deleting anything.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"

	dirsync "github.com/bons/bons-ci/internal/dirsync"
)

// ─────────────────────────────────────────────────────────────────────────────
// Result
// ─────────────────────────────────────────────────────────────────────────────

// Result summarises a completed [Pruner.Prune] call.
type Result struct {
	// Kept is the number of entries that matched the filter and were left
	// in the target directory.
	Kept int64

	// Deleted is the number of individual files whose deletion op was
	// successfully submitted.
	Deleted int64

	// Collapsed is the number of directory subtrees whose deletion was
	// submitted as a single OpRemoveAll (one syscall per subtree).
	Collapsed int64

	// SubmitErrors is the number of entries for which Batcher.Submit failed.
	SubmitErrors int64

	// Err is non-nil when required paths were absent, the walk encountered
	// I/O errors, or the batcher flush/close failed (combined via errors.Join).
	Err error
}

// Total returns the sum of all processed entries.
func (r Result) Total() int64 {
	return r.Kept + r.Deleted + r.Collapsed + r.SubmitErrors
}

// OK reports whether the prune completed without any errors.
func (r Result) OK() bool { return r.Err == nil && r.SubmitErrors == 0 }

// ─────────────────────────────────────────────────────────────────────────────
// Pruner
// ─────────────────────────────────────────────────────────────────────────────

// Pruner prunes a target directory by deleting every entry that does not
// satisfy its filter rules. A single Pruner instance may be reused across
// multiple Prune calls safely.
//
// Construct with [New]; configure with functional [Option]s.
type Pruner struct {
	cfg config
}

// New creates a Pruner with the supplied options.
func New(opts ...Option) *Pruner {
	cfg := defaultConfig()
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.workers <= 0 {
		cfg.workers = runtime.NumCPU()
	}
	return &Pruner{cfg: cfg}
}

// Prune deletes from targetDir every entry that does not match the configured
// filter rules. Matching entries are left untouched.
//
// Error handling:
//   - Invalid targetDir (empty, not a directory) → immediate non-nil error.
//   - Absent required paths → non-nil error, no deletions performed.
//   - Walk I/O errors → collected in Result.Err; walk continues.
//   - Batcher.Submit errors → counted in Result.SubmitErrors.
//   - Batcher flush/close errors → collected in Result.Err.
func (p *Pruner) Prune(ctx context.Context, targetDir string) (Result, error) {
	if targetDir == "" {
		return Result{}, errors.New("dirprune: targetDir must not be empty")
	}

	info, err := os.Stat(targetDir)
	if err != nil {
		return Result{}, fmt.Errorf("dirprune: stat target %q: %w", targetDir, err)
	}
	if !info.IsDir() {
		return Result{}, fmt.Errorf("dirprune: target %q is not a directory", targetDir)
	}

	filter := p.buildFilter()

	if err := p.checkRequiredPaths(targetDir, filter); err != nil {
		return Result{}, err
	}

	view, err := dirsync.NewFSMergedView(targetDir)
	if err != nil {
		return Result{}, fmt.Errorf("dirprune: merged view %q: %w", targetDir, err)
	}

	batcher, err := p.cfg.batcherFactory(view)
	if err != nil {
		return Result{}, fmt.Errorf("dirprune: batcher: %w", err)
	}

	// BUG FIX M1: removed dead `relDir string` parameter that was always "".
	result := p.walk(ctx, targetDir, filter, batcher)

	if closeErr := batcher.Close(ctx); closeErr != nil && !isContextErr(closeErr) {
		result.Err = errors.Join(result.Err, fmt.Errorf("batcher close: %w", closeErr))
	}

	return result, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Walk algorithm
// ─────────────────────────────────────────────────────────────────────────────

// walk drives the recursive directory scan and the submission worker pool.
//
// BUG FIX M1: removed the dead `relDir string` parameter that was always ""
// at every call site.
func (p *Pruner) walk(
	ctx context.Context,
	targetRoot string,
	filter dirsync.Filter,
	batcher dirsync.Batcher,
) Result {
	var (
		kept         atomic.Int64
		deleted      atomic.Int64
		collapsed    atomic.Int64
		submitErrors atomic.Int64
		walkErrs     []error
		walkErrMu    sync.Mutex
	)

	addWalkErr := func(err error) {
		walkErrMu.Lock()
		walkErrs = append(walkErrs, err)
		walkErrMu.Unlock()
	}

	type submitJob struct {
		op   dirsync.BatchOp
		disp Disposition
		ev   Event
	}
	jobCh := make(chan submitJob, p.cfg.workers*16)

	// Worker pool: concurrently calls batcher.Submit.
	var poolWg sync.WaitGroup
	for range p.cfg.workers {
		poolWg.Add(1)
		go func() {
			defer poolWg.Done()
			for job := range jobCh {
				if ctx.Err() != nil {
					continue // drain without submitting
				}
				ev := job.ev
				if err := batcher.Submit(ctx, job.op); err != nil {
					ev.SubmitErr = err
					submitErrors.Add(1)
				} else {
					switch job.disp {
					case DispositionDeleted:
						deleted.Add(1)
					case DispositionCollapsed:
						collapsed.Add(1)
					}
				}
				p.cfg.observer.OnEvent(ev)
			}
		}()
	}

	// enqueue sends a deletion job to the worker pool.
	enqueue := func(relPath string, isDir bool, opKind dirsync.OpKind, disp Disposition, tag any) {
		select {
		case jobCh <- submitJob{
			op:   dirsync.BatchOp{Kind: opKind, RelPath: relPath, Tag: tag},
			disp: disp,
			ev:   Event{RelPath: relPath, IsDir: isDir, Disposition: disp},
		}:
		case <-ctx.Done():
		}
	}

	// walkDir visits one directory level and recurses into matching subdirs.
	// Returns keptAny=true when at least one descendant was kept.
	//
	// BUG FIX C3: walkDir now returns bool (keptAny) so callers can decide
	// whether an empty parent directory should be deleted.
	var walkDir func(relDir string) (keptAny bool)
	walkDir = func(relDir string) (keptAny bool) {
		if ctx.Err() != nil {
			return false
		}

		absDir := filepath.Join(targetRoot, filepath.FromSlash(relDir))
		entries, err := readEntries(absDir, p.cfg.followSymlinks)
		if err != nil {
			addWalkErr(fmt.Errorf("read dir %q: %w", relDir, err))
			return false
		}

		for _, e := range entries {
			if ctx.Err() != nil {
				return keptAny
			}

			relPath := joinRel(relDir, e.Name())
			info, err := e.Info()
			if err != nil {
				addWalkErr(fmt.Errorf("stat %q: %w", relPath, err))
				continue
			}
			isDir := info.IsDir()

			if !filter.Include(relPath, isDir) {
				// Entry does not match → delete it.
				if isDir {
					enqueue(relPath, true, dirsync.OpRemoveAll, DispositionCollapsed, info)
				} else {
					enqueue(relPath, false, dirsync.OpRemove, DispositionDeleted, info)
				}
				continue
			}

			if !isDir {
				// Leaf file matches → keep it.
				kept.Add(1)
				keptAny = true
				p.cfg.observer.OnEvent(Event{
					RelPath:     relPath,
					IsDir:       false,
					Disposition: DispositionKept,
				})
				continue
			}

			// Directory passes filter.Include.
			//
			// BUG FIX C3: distinguish between the two reasons a directory can
			// pass the filter:
			//
			// (A) directMatch: filter.Include(relPath, false)==true
			//     The directory itself matches a pattern directly (e.g. "src").
			//     Keep unconditionally regardless of child results.
			//
			// (B) descentOnly: filter.Include(relPath, false)==false
			//     The directory passes only via couldMatchUnder because a
			//     wildcard pattern might match descendants (e.g. "*.go").
			//     If no descendant is kept, the directory must be deleted.
			directMatch := filter.Include(relPath, false)
			childKept := walkDir(relPath)

			if directMatch || childKept {
				keptAny = true
			} else {
				// Case B with no matching descendants: directory is now empty.
				// Emit OpRemoveAll to clean it up. FSMergedView ops are
				// idempotent (ENOENT is silently swallowed), so any previously
				// enqueued child ops do not cause errors.
				enqueue(relPath, true, dirsync.OpRemoveAll, DispositionCollapsed, info)
			}
		}
		return keptAny
	}

	walkDir("")

	close(jobCh)
	poolWg.Wait()

	return Result{
		Kept:         kept.Load(),
		Deleted:      deleted.Load(),
		Collapsed:    collapsed.Load(),
		SubmitErrors: submitErrors.Load(),
		Err:          errors.Join(walkErrs...),
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

func (p *Pruner) buildFilter() dirsync.Filter {
	hasPats := len(p.cfg.includePatterns) > 0 ||
		len(p.cfg.excludePatterns) > 0 ||
		len(p.cfg.requiredPaths) > 0
	if !hasPats && !p.cfg.allowWildcards {
		return dirsync.NoopFilter{}
	}
	return dirsync.NewPatternFilter(
		p.cfg.includePatterns,
		p.cfg.excludePatterns,
		p.cfg.requiredPaths,
		p.cfg.allowWildcards,
	)
}

func (p *Pruner) checkRequiredPaths(targetDir string, filter dirsync.Filter) error {
	required := filter.RequiredPaths()
	if len(required) == 0 {
		return nil
	}
	var missing []string
	for _, rel := range required {
		abs := filepath.Join(targetDir, filepath.FromSlash(rel))
		if _, err := os.Lstat(abs); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				missing = append(missing, rel)
			} else {
				return fmt.Errorf("dirprune: stat required path %q: %w", rel, err)
			}
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("dirprune: required paths absent from %q: [%s]",
			targetDir, strings.Join(missing, ", "))
	}
	return nil
}

func readEntries(absDir string, followSymlinks bool) ([]os.DirEntry, error) {
	entries, err := os.ReadDir(absDir)
	if err != nil {
		return nil, err
	}
	if !followSymlinks {
		return entries, nil
	}
	result := make([]os.DirEntry, len(entries))
	for i, e := range entries {
		if e.Type()&fs.ModeSymlink == 0 {
			result[i] = e
			continue
		}
		target := filepath.Join(absDir, e.Name())
		tInfo, err := os.Stat(target)
		if err != nil {
			result[i] = e // broken symlink: treat as opaque leaf
			continue
		}
		result[i] = &resolvedEntry{DirEntry: e, resolved: tInfo}
	}
	return result, nil
}

type resolvedEntry struct {
	os.DirEntry
	resolved fs.FileInfo
}

func (r *resolvedEntry) Info() (fs.FileInfo, error) { return r.resolved, nil }
func (r *resolvedEntry) IsDir() bool                { return r.resolved.IsDir() }
func (r *resolvedEntry) Type() fs.FileMode          { return r.resolved.Mode().Type() }

func joinRel(relDir, name string) string {
	if relDir == "" {
		return name
	}
	return relDir + "/" + name
}

func isContextErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
