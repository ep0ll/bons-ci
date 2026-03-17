package dirsync

// filter_test.go – white-box unit tests for the filter subsystem.
//
// Tests are in package dirsync (not dirsync_test) to access unexported types
// (IncludeFilter, ExcludeFilter, CompositeFilter) directly.

import (
	"testing"
)

// ─── FilterDecision stringer (for readable test output) ──────────────────────

func decisionName(d FilterDecision) string {
	switch d {
	case FilterAllow:
		return "Allow"
	case FilterSkip:
		return "Skip"
	case FilterPrune:
		return "Prune"
	default:
		return "unknown"
	}
}

// assertDecision is a concise assertion helper.
func assertDecision(t *testing.T, f PathFilter, relPath string, isDir bool, want FilterDecision) {
	t.Helper()
	got := f.Decide(relPath, isDir)
	if got != want {
		t.Errorf("Decide(%q, isDir=%v) = %s, want %s",
			relPath, isDir, decisionName(got), decisionName(want))
	}
}

// ─── NopFilter ────────────────────────────────────────────────────────────────

func TestNopFilter_AlwaysAllow(t *testing.T) {
	f := NopFilter{}
	for _, path := range []string{"", "file.txt", "vendor/x.go", "a/b/c"} {
		for _, isDir := range []bool{true, false} {
			assertDecision(t, f, path, isDir, FilterAllow)
		}
	}
}

// ─── IncludeFilter ────────────────────────────────────────────────────────────

func TestIncludeFilter_EmptyPatterns_AllowAll(t *testing.T) {
	f := &IncludeFilter{patterns: mustPS(t, nil, false)}
	assertDecision(t, f, "anything", false, FilterAllow)
	assertDecision(t, f, "vendor/x.go", true, FilterAllow)
}

func TestIncludeFilter_MatchingFile_Allow(t *testing.T) {
	f := &IncludeFilter{patterns: mustPS(t, []string{"go.mod"}, false)}
	assertDecision(t, f, "go.mod", false, FilterAllow)
	assertDecision(t, f, "sub/go.mod", false, FilterAllow) // base-name rule
}

func TestIncludeFilter_NonMatchingFile_Skip(t *testing.T) {
	f := &IncludeFilter{patterns: mustPS(t, []string{"go.mod"}, false)}
	assertDecision(t, f, "README.md", false, FilterSkip)
}

// Directories that don't match should be Skip (not Prune) so children
// remain reachable for evaluation.
func TestIncludeFilter_NonMatchingDir_Skip_NotPrune(t *testing.T) {
	f := &IncludeFilter{patterns: mustPS(t, []string{"go.mod"}, false)}
	// "vendor" doesn't match "go.mod", but it must be Skip so we still
	// descend into vendor/ to check if any files there match.
	assertDecision(t, f, "vendor", true, FilterSkip)
	assertDecision(t, f, "cmd", true, FilterSkip)
}

func TestIncludeFilter_GlobMode(t *testing.T) {
	f := &IncludeFilter{patterns: mustPS(t, []string{"*.go"}, true)}
	assertDecision(t, f, "main.go", false, FilterAllow)
	assertDecision(t, f, "cmd/main.go", false, FilterAllow) // base-name glob
	assertDecision(t, f, "main.txt", false, FilterSkip)
}

// ─── ExcludeFilter ────────────────────────────────────────────────────────────

func TestExcludeFilter_EmptyPatterns_AllowAll(t *testing.T) {
	f := &ExcludeFilter{patterns: mustPS(t, nil, false)}
	assertDecision(t, f, "vendor/x.go", true, FilterAllow)
	assertDecision(t, f, "file.txt", false, FilterAllow)
}

func TestExcludeFilter_MatchingFile_Skip(t *testing.T) {
	f := &ExcludeFilter{patterns: mustPS(t, []string{"secret.txt"}, false)}
	assertDecision(t, f, "secret.txt", false, FilterSkip)
	assertDecision(t, f, "sub/secret.txt", false, FilterSkip) // base-name rule
}

// A matching directory must be Pruned (not just Skipped) to stop all descent.
func TestExcludeFilter_MatchingDir_Prune(t *testing.T) {
	f := &ExcludeFilter{patterns: mustPS(t, []string{"vendor"}, false)}
	assertDecision(t, f, "vendor", true, FilterPrune)
	// Children would also match via the prefix rule, but the parent prune
	// means the walker never even calls Decide for them.
	assertDecision(t, f, "vendor/pkg", true, FilterPrune)
	assertDecision(t, f, "vendor/x.go", false, FilterSkip)
}

func TestExcludeFilter_NonMatchingEntry_Allow(t *testing.T) {
	f := &ExcludeFilter{patterns: mustPS(t, []string{"vendor"}, false)}
	assertDecision(t, f, "cmd/main.go", false, FilterAllow)
	assertDecision(t, f, "src", true, FilterAllow)
}

func TestExcludeFilter_GlobMode_Dir_Prune(t *testing.T) {
	f := &ExcludeFilter{patterns: mustPS(t, []string{".*"}, true)}
	// ".git" directory should be pruned.
	assertDecision(t, f, ".git", true, FilterPrune)
	// ".gitignore" file should be skipped.
	assertDecision(t, f, ".gitignore", false, FilterSkip)
	// Normal files unaffected.
	assertDecision(t, f, "main.go", false, FilterAllow)
}

// ─── CompositeFilter ──────────────────────────────────────────────────────────

func TestComposite_ExcludeBeatsInclude(t *testing.T) {
	// Include *.go but exclude vendor/.
	// vendor/main.go should be Pruned (exclude wins).
	inc := &IncludeFilter{patterns: mustPS(t, []string{"*.go"}, true)}
	exc := &ExcludeFilter{patterns: mustPS(t, []string{"vendor"}, false)}
	f := &CompositeFilter{include: inc, exclude: exc}

	// vendor/ is excluded → Prune regardless of include.
	assertDecision(t, f, "vendor", true, FilterPrune)
	// cmd/main.go is not excluded and matches include → Allow.
	assertDecision(t, f, "cmd/main.go", false, FilterAllow)
	// main.txt not excluded, not included → Skip.
	assertDecision(t, f, "main.txt", false, FilterSkip)
}

func TestComposite_OnlyInclude(t *testing.T) {
	inc := &IncludeFilter{patterns: mustPS(t, []string{"go.mod"}, false)}
	f := &CompositeFilter{include: inc, exclude: nil}
	assertDecision(t, f, "go.mod", false, FilterAllow)
	assertDecision(t, f, "main.go", false, FilterSkip)
}

func TestComposite_OnlyExclude(t *testing.T) {
	exc := &ExcludeFilter{patterns: mustPS(t, []string{"tmp"}, false)}
	f := &CompositeFilter{include: nil, exclude: exc}
	assertDecision(t, f, "tmp", true, FilterPrune)
	assertDecision(t, f, "src/main.go", false, FilterAllow)
}

func TestComposite_NilBothSides_Allow(t *testing.T) {
	f := &CompositeFilter{include: nil, exclude: nil}
	assertDecision(t, f, "anything", false, FilterAllow)
}

// ─── BuildFilter factory ──────────────────────────────────────────────────────

func TestBuildFilter_NoOptions_NopFilter(t *testing.T) {
	f, err := BuildFilter(Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := f.(NopFilter); !ok {
		t.Errorf("expected NopFilter, got %T", f)
	}
}

func TestBuildFilter_IncludeOnly(t *testing.T) {
	f, err := BuildFilter(Options{
		IncludePatterns: []string{"*.go"},
		AllowWildcards:  true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := f.(*IncludeFilter); !ok {
		t.Errorf("expected *IncludeFilter, got %T", f)
	}
}

func TestBuildFilter_ExcludeOnly(t *testing.T) {
	f, err := BuildFilter(Options{
		ExcludePatterns: []string{"vendor"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := f.(*ExcludeFilter); !ok {
		t.Errorf("expected *ExcludeFilter, got %T", f)
	}
}

func TestBuildFilter_IncludeAndExclude_Composite(t *testing.T) {
	f, err := BuildFilter(Options{
		IncludePatterns: []string{"*.go"},
		ExcludePatterns: []string{"vendor"},
		AllowWildcards:  true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := f.(*CompositeFilter); !ok {
		t.Errorf("expected *CompositeFilter, got %T", f)
	}
}

func TestBuildFilter_InvalidGlob_Error(t *testing.T) {
	_, err := BuildFilter(Options{
		IncludePatterns: []string{"[bad"},
		AllowWildcards:  true,
	})
	if err == nil {
		t.Error("expected error for invalid include glob pattern")
	}

	_, err = BuildFilter(Options{
		ExcludePatterns: []string{"[bad"},
		AllowWildcards:  true,
	})
	if err == nil {
		t.Error("expected error for invalid exclude glob pattern")
	}
}

// TestBuildFilter_LiteralMode_NoGlobValidation confirms that patterns containing
// glob metacharacters are accepted without error when AllowWildcards=false.
func TestBuildFilter_LiteralMode_NoGlobValidation(t *testing.T) {
	_, err := BuildFilter(Options{
		IncludePatterns: []string{"[bracket"},
		AllowWildcards:  false, // literal: no glob validation
	})
	if err != nil {
		t.Errorf("literal mode should accept any string as a pattern, got: %v", err)
	}
}

// ─── NewCompositeFilter constructor ──────────────────────────────────────────

func TestNewCompositeFilter_BothNil_NopFilter(t *testing.T) {
	f := NewCompositeFilter(nil, nil)
	if _, ok := f.(NopFilter); !ok {
		t.Errorf("expected NopFilter when both args nil, got %T", f)
	}
}

func TestNewCompositeFilter_ExcludeOnly(t *testing.T) {
	exc := &ExcludeFilter{patterns: mustPS(t, []string{"vendor"}, false)}
	f := NewCompositeFilter(exc, nil)
	assertDecision(t, f, "vendor", true, FilterPrune)
	assertDecision(t, f, "src/main.go", false, FilterAllow)
}

func TestNewCompositeFilter_IncludeOnly(t *testing.T) {
	inc := &IncludeFilter{patterns: mustPS(t, []string{"*.go"}, true)}
	f := NewCompositeFilter(nil, inc)
	assertDecision(t, f, "main.go", false, FilterAllow)
	assertDecision(t, f, "README.md", false, FilterSkip)
}

func TestNewCompositeFilter_ExcludeVetoesInclude(t *testing.T) {
	// Custom "exclude" layer that blocks "secret.*"; include layer allows *.go.
	// secret.go matches include but must be blocked by exclude.
	exc := &ExcludeFilter{patterns: mustPS(t, []string{"secret.go"}, false)}
	inc := &IncludeFilter{patterns: mustPS(t, []string{"*.go"}, true)}
	f := NewCompositeFilter(exc, inc)

	// secret.go: exclude fires first → Skip (file, not dir).
	assertDecision(t, f, "secret.go", false, FilterSkip)
	// main.go: exclude allows → include allows.
	assertDecision(t, f, "main.go", false, FilterAllow)
	// README.md: exclude allows → include skips.
	assertDecision(t, f, "README.md", false, FilterSkip)
}

func TestNewCompositeFilter_ReturnedIsComposite(t *testing.T) {
	exc := &ExcludeFilter{patterns: mustPS(t, []string{"tmp"}, false)}
	inc := &IncludeFilter{patterns: mustPS(t, []string{"*.go"}, true)}
	f := NewCompositeFilter(exc, inc)
	if _, ok := f.(*CompositeFilter); !ok {
		t.Errorf("expected *CompositeFilter, got %T", f)
	}
}
