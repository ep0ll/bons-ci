package dirsync

// pattern.go – low-level path pattern matching.
//
// A patternSet is a compiled, immutable set of patterns.
// It is the only place in the package that knows how patterns are interpreted;
// all higher-level filter types (IncludeFilter, ExcludeFilter) delegate here.
//
// Two matching modes:
//
//  AllowWildcards=false (literal mode):
//    A pattern matches relPath when any of the following hold:
//      1. relPath == pattern                               (exact)
//      2. filepath.Base(relPath) == pattern               (base-name)
//      3. strings.HasPrefix(relPath, pattern+"/")         (directory prefix)
//    Examples: "vendor" matches "vendor/pkg/x.go"; "Makefile" matches "src/Makefile".
//
//  AllowWildcards=true (glob mode):
//    A pattern matches relPath when any of the following hold:
//      1. filepath.Match(pattern, relPath)                 (full-path glob)
//      2. filepath.Match(pattern, filepath.Base(relPath))  (base-name glob)
//    Examples: "*.go" matches "cmd/main.go"; "vendor/**" is not supported (no **).
//    Malformed glob patterns are caught at construction time.
//
// Neither mode performs any filesystem access.

import (
	"fmt"
	"path/filepath"
	"strings"
)

// patternSet is a compiled, immutable set of patterns.
// Zero value is valid and matches nothing.
type patternSet struct {
	patterns []string
	wildcard bool // true → use filepath.Match; false → literal matching
}

// newPatternSet compiles patterns and validates glob syntax when wildcard=true.
// Returns an error if any pattern is a malformed glob.
func newPatternSet(patterns []string, wildcard bool) (patternSet, error) {
	cleaned := make([]string, 0, len(patterns))
	for _, p := range patterns {
		p = filepath.Clean(p)
		if p == "" || p == "." {
			continue
		}
		if wildcard {
			// Validate pattern syntax now so we never get a runtime error later.
			if _, err := filepath.Match(p, ""); err != nil {
				return patternSet{}, fmt.Errorf("invalid glob pattern %q: %w", p, err)
			}
		}
		cleaned = append(cleaned, p)
	}
	return patternSet{patterns: cleaned, wildcard: wildcard}, nil
}

// matches reports whether relPath satisfies at least one pattern in the set.
// relPath must be a cleaned, slash-separated relative path (no leading slash).
func (ps patternSet) matches(relPath string) bool {
	if len(ps.patterns) == 0 {
		return false
	}
	base := filepath.Base(relPath)

	for _, pat := range ps.patterns {
		if ps.wildcard {
			if matchGlob(pat, relPath) || matchGlob(pat, base) {
				return true
			}
		} else {
			if matchLiteral(pat, relPath, base) {
				return true
			}
		}
	}
	return false
}

// ─── Literal matching ─────────────────────────────────────────────────────────

// matchLiteral applies the three literal-mode rules.
func matchLiteral(pat, relPath, base string) bool {
	// Rule 1: exact full-path match.
	if relPath == pat {
		return true
	}
	// Rule 2: base-name match (e.g. pattern "Makefile" matches "src/Makefile").
	if base == pat {
		return true
	}
	// Rule 3: directory-prefix match (e.g. pattern "vendor" matches "vendor/x/y").
	// We append the separator to avoid "vendor_extra/..." matching pattern "vendor".
	if strings.HasPrefix(relPath, pat+string(filepath.Separator)) {
		return true
	}
	return false
}

// ─── Glob matching ────────────────────────────────────────────────────────────

// matchGlob wraps filepath.Match and silently treats malformed patterns as
// non-matching (construction-time validation catches real errors earlier).
func matchGlob(pat, s string) bool {
	ok, _ := filepath.Match(pat, s)
	return ok
}
