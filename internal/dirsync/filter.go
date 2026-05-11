package dirsync

import (
	"fmt"
	"strings"

	"github.com/moby/patternmatcher"
)

// ─────────────────────────────────────────────────────────────────────────────
// Filter interface
// ─────────────────────────────────────────────────────────────────────────────

// Filter controls which relative paths participate in the diff.
//
// All implementations must be safe for concurrent use because the walker may
// call Include from a goroutine while multiple Classifiers share a Filter.
type Filter interface {
	// Include returns true when relPath should be processed.
	//
	// isDir is true when the entry is a directory. Implementations must return
	// true for directories whose descendants may match — even when the directory
	// itself does not — so the walker can recurse into them and find children.
	Include(relPath string, isDir bool) bool

	// RequiredPaths returns paths (relative to lower root) that must exist
	// before classification begins. The Classifier reports a [RequiredPathError]
	// for each absent path.
	RequiredPaths() []string
}

// ─────────────────────────────────────────────────────────────────────────────
// NoopFilter — zero-overhead passthrough (the default)
// ─────────────────────────────────────────────────────────────────────────────

// NoopFilter is a [Filter] that accepts every path and requires nothing.
// Both methods inline to nothing, so there is genuinely zero overhead on
// the hot path compared to having no filter at all.
type NoopFilter struct{}

// Include implements [Filter] — always returns true.
func (NoopFilter) Include(_ string, _ bool) bool { return true }

// RequiredPaths implements [Filter] — always returns nil.
func (NoopFilter) RequiredPaths() []string { return nil }

// ─────────────────────────────────────────────────────────────────────────────
// PatternFilter — powered by github.com/moby/patternmatcher
// ─────────────────────────────────────────────────────────────────────────────

// PatternFilter implements [Filter] using Docker-compatible gitignore-style
// patterns via [github.com/moby/patternmatcher] — the same engine used by
// BuildKit and Docker's .dockerignore processing.
//
// # Pattern syntax (full gitignore / Moby subset)
//
//   - "vendor"      — matches "vendor" and any descendant
//   - "*.go"        — matches any .go file at any depth (base name match)
//   - "!vendor/x"   — negation: re-includes a previously excluded path
//   - "**/vendor"   — matches "vendor" at any depth
//   - "pkg/**"      — matches any path under pkg/
//
// Patterns are evaluated in order; the last matching pattern wins.
// Negations (!) enable fine-grained exceptions without restructuring the list.
//
// # Evaluation order
//
//  1. Path matches any exclude pattern (after negations)     → rejected.
//  2. No include patterns configured                          → accepted.
//  3. Path matches any include pattern (after negations)     → accepted.
//  4. isDir && any include pattern could match a descendant  → accepted
//     (allows walker to descend and find matching children).
//  5. Otherwise                                              → rejected.
type PatternFilter struct {
	includePatterns []string
	excludePatterns []string
	requiredPaths   []string

	includeMatcher *patternmatcher.PatternMatcher // nil when no include patterns
	excludeMatcher *patternmatcher.PatternMatcher // nil when no exclude patterns
}

// NewPatternFilter constructs a [PatternFilter] from pattern lists.
// Returns an error when any pattern has invalid glob syntax.
// All slices are copied defensively; caller's slices are not retained.
func NewPatternFilter(include, exclude, required []string) (*PatternFilter, error) {
	f := &PatternFilter{
		includePatterns: copyStrings(include),
		excludePatterns: copyStrings(exclude),
		requiredPaths:   copyStrings(required),
	}
	var err error
	if len(include) > 0 {
		if f.includeMatcher, err = patternmatcher.New(include); err != nil {
			return nil, fmt.Errorf("filter: invalid include patterns: %w", err)
		}
	}
	if len(exclude) > 0 {
		if f.excludeMatcher, err = patternmatcher.New(exclude); err != nil {
			return nil, fmt.Errorf("filter: invalid exclude patterns: %w", err)
		}
	}
	return f, nil
}

// Include implements [Filter].
func (f *PatternFilter) Include(relPath string, isDir bool) bool {
	// Step 1: Exclusion wins over everything.
	if f.excludeMatcher != nil {
		if excluded, err := f.excludeMatcher.MatchesOrParentMatches(relPath); err == nil && excluded {
			return false
		}
	}

	// Step 2: No include patterns → accept everything not excluded.
	if f.includeMatcher == nil {
		return true
	}

	// Step 3: Direct include-pattern match.
	if matched, err := f.includeMatcher.MatchesOrParentMatches(relPath); err == nil && matched {
		return true
	}

	// Step 4: Allow directory descent when a child might match.
	if isDir && f.couldHaveMatchingDescendant(relPath) {
		return true
	}
	return false
}

// RequiredPaths implements [Filter].
func (f *PatternFilter) RequiredPaths() []string { return f.requiredPaths }

// couldHaveMatchingDescendant reports whether any include pattern might match
// a path beneath dirPath, driving the walker's recurse-vs-skip decision for
// directories that don't directly match.
func (f *PatternFilter) couldHaveMatchingDescendant(dirPath string) bool {
	for _, raw := range f.includePatterns {
		// Strip leading negation for the descent check.
		pat := strings.TrimPrefix(raw, "!")
		if strings.HasPrefix(pat, dirPath+"/") {
			return true
		}
		// Wildcard patterns ("*.go", "**") could match anything beneath.
		if strings.ContainsAny(pat, "*?[") {
			return true
		}
	}
	return false
}

// copyStrings clones src into a new slice. Returns nil for empty input.
func copyStrings(src []string) []string {
	if len(src) == 0 {
		return nil
	}
	dst := make([]string, len(src))
	copy(dst, src)
	return dst
}
