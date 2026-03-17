package dirsync

// filter.go – path-filter abstractions and concrete implementations.

import "fmt"
//
// Dependency graph (no cycles):
//
//   patternSet  ←──  IncludeFilter
//   patternSet  ←──  ExcludeFilter
//   PathFilter  ←──  CompositeFilter (holds include + exclude)
//   Options     ──►  BuildFilter() → PathFilter
//
// The walker depends only on PathFilter, not on any concrete type.
// This makes the filter subsystem open for extension without modifying the walker.

// ─── Decision type ────────────────────────────────────────────────────────────

// FilterDecision is the outcome of consulting a PathFilter for a single path.
//
// The three values have different effects on directories vs files:
//
//	         │  Emit?  │  Recurse into dir?
//	─────────┼─────────┼───────────────────
//	Allow    │  yes    │  yes
//	Skip     │  no     │  yes  (children may still match IncludePatterns)
//	Prune    │  no     │  no   (entire subtree is excluded)
//
// For files (non-directories), Skip and Prune are equivalent.
type FilterDecision int

const (
	// FilterAllow: include this entry in output; traverse if directory.
	FilterAllow FilterDecision = iota

	// FilterSkip: omit this entry from output; still traverse if directory.
	// Use this when a directory doesn't match an include pattern but its
	// children might (e.g. IncludePatterns=["*.go"] applied to "cmd/").
	FilterSkip

	// FilterPrune: omit this entry from output; do NOT traverse if directory.
	// Use this for ExcludePatterns on directories — stops all further descent.
	FilterPrune
)

// ─── Interface ────────────────────────────────────────────────────────────────

// PathFilter decides how to handle a single filesystem entry during the walk.
//
// Implementations must be safe for concurrent reads from multiple goroutines
// (the walker is single-threaded, but tests may call Decide concurrently).
// Implementations must not perform any filesystem I/O.
type PathFilter interface {
	// Decide returns the handling decision for the entry at relPath.
	// relPath is a cleaned, slash-separated path relative to the root being walked.
	// isDir is true when the entry is a directory.
	Decide(relPath string, isDir bool) FilterDecision
}

// ─── NopFilter ────────────────────────────────────────────────────────────────

// NopFilter allows every path without inspection.
// It is returned by BuildFilter when no filtering options are active.
type NopFilter struct{}

func (NopFilter) Decide(_ string, _ bool) FilterDecision { return FilterAllow }

// ─── IncludeFilter ────────────────────────────────────────────────────────────

// IncludeFilter emits only paths that match at least one include pattern.
//
// Directory behaviour:
//   - Directory matches a pattern → FilterAllow (emit + recurse).
//   - Directory does NOT match   → FilterSkip   (don't emit, but recurse so
//     children can still be tested against patterns).
//
// File behaviour:
//   - File matches a pattern     → FilterAllow.
//   - File does NOT match        → FilterSkip.
//
// Empty pattern set: FilterAllow for everything (no-op include).
type IncludeFilter struct {
	patterns patternSet
}

func (f *IncludeFilter) Decide(relPath string, _ bool) FilterDecision {
	if len(f.patterns.patterns) == 0 {
		return FilterAllow
	}
	if f.patterns.matches(relPath) {
		return FilterAllow
	}
	// For directories: Skip (not Prune) so children remain reachable.
	// For files: Skip.
	// Both cases fall through to the same return value.
	return FilterSkip
}

// ─── ExcludeFilter ────────────────────────────────────────────────────────────

// ExcludeFilter suppresses paths that match at least one exclude pattern.
//
// Directory behaviour:
//   - Directory matches a pattern → FilterPrune (suppress + stop recursion).
//     This is the key difference from IncludeFilter: once a directory is excluded
//     there is no reason to visit its children, saving getdents64 syscalls.
//
// File behaviour:
//   - File matches a pattern     → FilterSkip.
//
// Empty pattern set: FilterAllow for everything (no-op exclude).
type ExcludeFilter struct {
	patterns patternSet
}

func (f *ExcludeFilter) Decide(relPath string, isDir bool) FilterDecision {
	if len(f.patterns.patterns) == 0 {
		return FilterAllow
	}
	if f.patterns.matches(relPath) {
		if isDir {
			return FilterPrune // stop recursion entirely
		}
		return FilterSkip
	}
	return FilterAllow
}

// ─── CompositeFilter ──────────────────────────────────────────────────────────

// CompositeFilter chains an optional ExcludeFilter and an optional IncludeFilter.
//
// Evaluation order:
//  1. ExcludeFilter is consulted first (higher priority).
//     If it returns Skip or Prune, that decision is final.
//  2. IncludeFilter is consulted only when ExcludeFilter returns Allow.
//
// Rationale: "exclude wins over include" matches the mental model of tools like
// rsync, .gitignore, and Docker .dockerignore.
type CompositeFilter struct {
	include PathFilter // nil → include all
	exclude PathFilter // nil → exclude nothing
}

func (c *CompositeFilter) Decide(relPath string, isDir bool) FilterDecision {
	// Exclude check: a non-Allow result immediately stops evaluation.
	if c.exclude != nil {
		if d := c.exclude.Decide(relPath, isDir); d != FilterAllow {
			return d
		}
	}
	// Include check: only reached when exclude says Allow.
	if c.include != nil {
		return c.include.Decide(relPath, isDir)
	}
	return FilterAllow
}

// ─── Constructor ──────────────────────────────────────────────────────────────

// NewCompositeFilter builds a CompositeFilter that evaluates exclude before
// include.  Either argument may be nil (treated as allow-all for that axis).
//
// Typical use: compose a caller-supplied PathFilter with the pattern-based
// filter produced by BuildFilter:
//
//	builtin, _ := dirsync.BuildFilter(opts)
//	custom     := myFilter{}
//	combined   := dirsync.NewCompositeFilter(custom, builtin)
//
// The exclude argument is consulted first.  If it returns Skip or Prune, that
// decision is final.  Only when exclude returns Allow is include consulted.
func NewCompositeFilter(exclude, include PathFilter) PathFilter {
	if exclude == nil && include == nil {
		return NopFilter{}
	}
	return &CompositeFilter{exclude: exclude, include: include}
}


// BuildFilter constructs the appropriate PathFilter from Options.
//
// Returns NopFilter when no filtering options are active (zero allocation
// overhead in the common case).
// Returns an error when any glob pattern is syntactically invalid.
func BuildFilter(opts Options) (PathFilter, error) {
	hasInclude := len(opts.IncludePatterns) > 0
	hasExclude := len(opts.ExcludePatterns) > 0

	if !hasInclude && !hasExclude {
		return NopFilter{}, nil
	}

	var (
		incFilter PathFilter
		excFilter PathFilter
	)

	if hasInclude {
		ps, err := newPatternSet(opts.IncludePatterns, opts.AllowWildcards)
		if err != nil {
			return nil, fmt.Errorf("IncludePatterns: %w", err)
		}
		incFilter = &IncludeFilter{patterns: ps}
	}

	if hasExclude {
		ps, err := newPatternSet(opts.ExcludePatterns, opts.AllowWildcards)
		if err != nil {
			return nil, fmt.Errorf("ExcludePatterns: %w", err)
		}
		excFilter = &ExcludeFilter{patterns: ps}
	}

	// If only one side is active, wrap it directly rather than adding a composite.
	if incFilter == nil {
		return excFilter, nil
	}
	if excFilter == nil {
		return incFilter, nil
	}
	return &CompositeFilter{include: incFilter, exclude: excFilter}, nil
}
