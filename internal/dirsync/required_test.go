package dirsync

// required_test.go – white-box unit tests for requiredTracker.

import (
	"errors"
	"strings"
	"testing"
)

// ─── Construction ─────────────────────────────────────────────────────────────

func TestRequiredTracker_NilOnEmpty(t *testing.T) {
	if tr := newRequiredTracker(nil); tr != nil {
		t.Errorf("expected nil tracker for empty paths, got %v", tr)
	}
	if tr := newRequiredTracker([]string{}); tr != nil {
		t.Errorf("expected nil tracker for empty slice, got %v", tr)
	}
	// Dot/blank entries are cleaned away → nil tracker.
	if tr := newRequiredTracker([]string{"", ".", "/"}); tr != nil {
		t.Errorf("expected nil tracker for blank/dot paths, got %v", tr)
	}
}

func TestRequiredTracker_NonNilOnRealPath(t *testing.T) {
	tr := newRequiredTracker([]string{"go.mod"})
	if tr == nil {
		t.Fatal("expected non-nil tracker for real path")
	}
}

// ─── markSeen ─────────────────────────────────────────────────────────────────

func TestRequiredTracker_MarkSeen_SatisfiesPath(t *testing.T) {
	tr := newRequiredTracker([]string{"go.mod"})
	if tr.missingPaths() == nil {
		t.Fatal("sanity: go.mod should be missing before markSeen")
	}
	tr.markSeen("go.mod")
	if m := tr.missingPaths(); m != nil {
		t.Errorf("after markSeen: expected no missing paths, got %v", m)
	}
}

func TestRequiredTracker_MarkSeen_Idempotent(t *testing.T) {
	tr := newRequiredTracker([]string{"go.mod"})
	tr.markSeen("go.mod")
	tr.markSeen("go.mod") // second call must not cause errors
	if m := tr.missingPaths(); m != nil {
		t.Errorf("double markSeen should still satisfy; got missing: %v", m)
	}
}

func TestRequiredTracker_MarkSeen_UnknownPath_Ignored(t *testing.T) {
	tr := newRequiredTracker([]string{"go.mod"})
	tr.markSeen("unrelated.txt") // must not panic or corrupt state
	if m := tr.missingPaths(); len(m) != 1 || m[0] != "go.mod" {
		t.Errorf("expected [go.mod] missing, got %v", m)
	}
}

// ─── Nil receiver safety ─────────────────────────────────────────────────────

func TestRequiredTracker_NilReceiver_SafeToCall(t *testing.T) {
	var tr *requiredTracker
	// None of these must panic.
	tr.markSeen("anything")
	if m := tr.missingPaths(); m != nil {
		t.Errorf("nil tracker.missingPaths() should return nil, got %v", m)
	}
	if err := tr.missingError(); err != nil {
		t.Errorf("nil tracker.missingError() should return nil, got %v", err)
	}
}

// ─── missingPaths ─────────────────────────────────────────────────────────────

func TestRequiredTracker_MissingPaths_Sorted(t *testing.T) {
	tr := newRequiredTracker([]string{"z.txt", "a.txt", "m.txt"})
	// Mark none seen.
	m := tr.missingPaths()
	if len(m) != 3 {
		t.Fatalf("expected 3 missing, got %d: %v", len(m), m)
	}
	// Must be sorted.
	for i := 1; i < len(m); i++ {
		if m[i] < m[i-1] {
			t.Errorf("missingPaths not sorted: %v", m)
		}
	}
}

func TestRequiredTracker_MissingPaths_PartialSeen(t *testing.T) {
	tr := newRequiredTracker([]string{"a.txt", "b.txt", "c.txt"})
	tr.markSeen("b.txt")
	m := tr.missingPaths()
	if len(m) != 2 {
		t.Fatalf("expected 2 missing, got %d: %v", len(m), m)
	}
	for _, p := range m {
		if p == "b.txt" {
			t.Error("b.txt was marked seen, should not be in missing")
		}
	}
}

func TestRequiredTracker_MissingPaths_NilWhenAllSeen(t *testing.T) {
	tr := newRequiredTracker([]string{"a.txt", "b.txt"})
	tr.markSeen("a.txt")
	tr.markSeen("b.txt")
	if m := tr.missingPaths(); m != nil {
		t.Errorf("all seen: expected nil, got %v", m)
	}
}

// ─── missingError ─────────────────────────────────────────────────────────────

func TestRequiredTracker_MissingError_NilWhenSatisfied(t *testing.T) {
	tr := newRequiredTracker([]string{"go.mod"})
	tr.markSeen("go.mod")
	if err := tr.missingError(); err != nil {
		t.Errorf("expected nil error when all seen, got: %v", err)
	}
}

func TestRequiredTracker_MissingError_TypedError(t *testing.T) {
	tr := newRequiredTracker([]string{"go.mod", "go.sum"})
	// Neither seen.
	err := tr.missingError()
	if err == nil {
		t.Fatal("expected non-nil error")
	}
	var mErr *MissingRequiredPathsError
	if !errors.As(err, &mErr) {
		t.Fatalf("expected *MissingRequiredPathsError, got %T: %v", err, err)
	}
	if len(mErr.Paths) != 2 {
		t.Errorf("expected 2 missing paths, got %d: %v", len(mErr.Paths), mErr.Paths)
	}
}

func TestMissingRequiredPathsError_ErrorString(t *testing.T) {
	err := &MissingRequiredPathsError{Paths: []string{"a.txt", "b.txt"}}
	s := err.Error()
	if s == "" {
		t.Error("Error() should return non-empty string")
	}
	// Must mention both paths.
	for _, p := range []string{"a.txt", "b.txt"} {
		if !contains(s, p) {
			t.Errorf("Error() %q does not mention path %q", s, p)
		}
	}
}

// ─── cleanRelPath ─────────────────────────────────────────────────────────────

func TestCleanRelPath(t *testing.T) {
	cases := []struct{ input, want string }{
		// Blank / root inputs → empty string
		{"", ""},
		{".", ""},
		{"/", ""},
		{"//", ""},
		// Leading separator stripped
		{"/go.mod", "go.mod"},
		{"//go.mod", "go.mod"},
		// Dot-prefix collapsed by filepath.Clean (key bug that was previously unfixed)
		{"./go.mod", "go.mod"},
		{"././go.mod", "go.mod"},
		// Normal relative paths unchanged
		{"go.mod", "go.mod"},
		{"sub/go.mod", "sub/go.mod"},
		{"/sub/go.mod", "sub/go.mod"},
		{"./sub/go.mod", "sub/go.mod"},
		// Redundant separators cleaned
		{"sub//go.mod", "sub/go.mod"},
	}
	for _, tc := range cases {
		got := cleanRelPath(tc.input)
		if got != tc.want {
			t.Errorf("cleanRelPath(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// ─── Helper ───────────────────────────────────────────────────────────────────

func contains(s, sub string) bool {
	return strings.Contains(s, sub)
}
