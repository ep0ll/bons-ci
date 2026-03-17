package dirsync

// required.go – required-path tracking and error reporting.
//
// requiredTracker records which paths from Options.RequiredPaths have been
// "seen" (reached the emission stage) during the walk.  After the walk
// completes, missingError() returns a typed error listing any unsatisfied
// required paths.
//
// The tracker is owned entirely by the walk goroutine and is never accessed
// from hash worker goroutines; no mutex is needed.
//
// Nil-safe: all methods silently no-op on a nil receiver, so the walker
// can call tracker methods unconditionally without nil guards everywhere.

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// ─── Tracker ──────────────────────────────────────────────────────────────────

// requiredTracker maintains a seen-flag for each required path.
// It is created by newRequiredTracker and mutated by markSeen.
type requiredTracker struct {
	// seen maps clean relPath → observed.
	// Only paths from RequiredPaths are keyed here; all other markSeen calls
	// are O(1) map lookups that simply find no key.
	seen map[string]bool
}

// newRequiredTracker creates a tracker pre-loaded with the given paths.
// Returns nil when paths is empty so callers can elide the tracker entirely.
func newRequiredTracker(paths []string) *requiredTracker {
	if len(paths) == 0 {
		return nil
	}
	m := make(map[string]bool, len(paths))
	for _, p := range paths {
		c := cleanRelPath(p)
		if c != "" {
			m[c] = false
		}
	}
	if len(m) == 0 {
		return nil
	}
	return &requiredTracker{seen: m}
}

// markSeen records that relPath was observed during the walk.
// Idempotent; safe to call multiple times for the same path.
// No-op on a nil receiver.
func (t *requiredTracker) markSeen(relPath string) {
	if t == nil {
		return
	}
	if _, ok := t.seen[relPath]; ok {
		t.seen[relPath] = true
	}
}

// missingPaths returns the sorted list of required paths that were never seen.
// Returns nil when all required paths were observed.
// No-op (returns nil) on a nil receiver.
func (t *requiredTracker) missingPaths() []string {
	if t == nil {
		return nil
	}
	var missing []string
	for p, seen := range t.seen {
		if !seen {
			missing = append(missing, p)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)
	return missing
}

// missingError returns a *MissingRequiredPathsError when any required path was
// not seen during the walk, or nil if all were satisfied.
// No-op (returns nil) on a nil receiver.
func (t *requiredTracker) missingError() error {
	m := t.missingPaths()
	if len(m) == 0 {
		return nil
	}
	return &MissingRequiredPathsError{Paths: m}
}

// ─── Error type ───────────────────────────────────────────────────────────────

// MissingRequiredPathsError is returned when one or more paths listed in
// Options.RequiredPaths were not found (or were filtered out) during the walk.
//
// Callers can inspect which paths are missing:
//
//	var mErr *dirsync.MissingRequiredPathsError
//	if errors.As(err, &mErr) {
//	    for _, p := range mErr.Paths {
//	        fmt.Println("missing:", p)
//	    }
//	}
type MissingRequiredPathsError struct {
	// Paths is the sorted list of required paths that were not observed.
	Paths []string
}

func (e *MissingRequiredPathsError) Error() string {
	return fmt.Sprintf("required paths not found: %s",
		strings.Join(e.Paths, ", "))
}

// ─── Helper ───────────────────────────────────────────────────────────────────

// cleanRelPath normalises a user-supplied path:
//   - Strips leading path separators (/ and \).
//   - Runs filepath.Clean to collapse ".", "..", duplicate separators, etc.
//   - Returns "" for blank, ".", or absolute-root inputs.
//
// Examples:
//
//	""            → ""
//	"."           → ""
//	"/go.mod"     → "go.mod"
//	"./go.mod"    → "go.mod"
//	"sub/go.mod"  → "sub/go.mod"
func cleanRelPath(p string) string {
	if p == "" {
		return ""
	}
	// Strip any leading separators before filepath.Clean so that an input
	// like "/sub/go.mod" is treated as a relative path "sub/go.mod" rather
	// than an absolute path.
	c := strings.TrimLeft(p, "/\\")
	if c == "" {
		return ""
	}
	c = filepath.Clean(c)
	if c == "." {
		return ""
	}
	return c
}
