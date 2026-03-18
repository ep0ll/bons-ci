package differ

import (
	"path/filepath"
	"strings"
)

// Filter determines which relative paths participate in the diff.
// All implementations must be safe for concurrent use from multiple goroutines.
type Filter interface {
	// Include returns true if the given relative path should be processed.
	//
	// isDir signals that the entry is a directory. Implementations should return
	// true for directories whose descendants may still match, even if the
	// directory itself does not match an include pattern. This preserves correct
	// recursive descent.
	Include(relPath string, isDir bool) bool

	// RequiredPaths returns paths (relative to the lower root) that must exist
	// in the lower directory tree. The caller is responsible for reporting
	// missing required paths as errors.
	RequiredPaths() []string
}

// NoopFilter is a [Filter] that accepts every path and requires nothing.
// It is the zero-overhead default used when no filtering options are set.
type NoopFilter struct{}

func (NoopFilter) Include(_ string, _ bool) bool { return true }
func (NoopFilter) RequiredPaths() []string        { return nil }

// PatternFilter implements [Filter] using include/exclude glob patterns and
// required-path assertions.
//
// Evaluation order (first match wins):
//  1. If relPath matches an exclude pattern  → rejected.
//  2. If no include patterns are configured  → accepted.
//  3. If relPath matches an include pattern  → accepted.
//  4. If isDir and any include pattern could reside under relPath → accepted
//     (allows the walker to recurse and find matching descendants).
//  5. Otherwise → rejected.
//
// Pattern syntax when [WithAllowWildcards] is false (default): exact prefix
// match. Pattern "vendor" matches "vendor" and "vendor/foo/bar".
//
// Pattern syntax when [WithAllowWildcards] is true: filepath.Match with the
// same prefix semantics applied first, then glob applied to both the full
// relative path and its base name.
type PatternFilter struct {
	includePatterns []string
	excludePatterns []string
	requiredPaths   []string
	allowWildcards  bool
}

// NewPatternFilter constructs a PatternFilter. All slices are defensively
// copied to prevent aliasing.
func NewPatternFilter(include, exclude, required []string, allowWildcards bool) *PatternFilter {
	clone := func(s []string) []string {
		if len(s) == 0 {
			return nil
		}
		c := make([]string, len(s))
		copy(c, s)
		return c
	}
	return &PatternFilter{
		includePatterns: clone(include),
		excludePatterns: clone(exclude),
		requiredPaths:   clone(required),
		allowWildcards:  allowWildcards,
	}
}

// Include implements [Filter].
func (f *PatternFilter) Include(relPath string, isDir bool) bool {
	// Exclusions take highest precedence.
	for _, pat := range f.excludePatterns {
		if f.matchesPath(pat, relPath) {
			return false
		}
	}

	// No inclusions configured → include everything not excluded.
	if len(f.includePatterns) == 0 {
		return true
	}

	for _, pat := range f.includePatterns {
		if f.matchesPath(pat, relPath) {
			return true
		}
		// For directories allow descent so descendants can be matched.
		if isDir && f.couldMatchUnder(pat, relPath) {
			return true
		}
	}
	return false
}

// RequiredPaths implements [Filter].
func (f *PatternFilter) RequiredPaths() []string { return f.requiredPaths }

// matchesPath reports whether pattern matches relPath as an exact hit or a
// directory-prefix hit ("vendor" matches "vendor/pkg/foo").
func (f *PatternFilter) matchesPath(pattern, relPath string) bool {
	if relPath == pattern || strings.HasPrefix(relPath, pattern+"/") {
		return true
	}
	if !f.allowWildcards {
		return false
	}
	if ok, _ := filepath.Match(pattern, relPath); ok {
		return true
	}
	// Also match against the base component alone (e.g., "*.go" hits "pkg/main.go").
	if ok, _ := filepath.Match(pattern, filepath.Base(relPath)); ok {
		return true
	}
	return false
}

// couldMatchUnder reports whether pattern could match any entry beneath
// dirPath, used to decide whether to recurse into a directory.
func (f *PatternFilter) couldMatchUnder(pattern, dirPath string) bool {
	// Non-wildcard: check if pattern is a descendant of dirPath.
	if strings.HasPrefix(pattern, dirPath+"/") {
		return true
	}
	if !f.allowWildcards {
		return false
	}
	// Any wildcard pattern that contains a wildcard character could
	// potentially match beneath any directory.
	return strings.ContainsAny(pattern, "*?[")
}
