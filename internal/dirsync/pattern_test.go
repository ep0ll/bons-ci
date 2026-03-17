package dirsync

// pattern_test.go – white-box tests for patternSet.
//
// patternSet is an unexported type; these tests live in package dirsync
// (not dirsync_test) so they can access the type directly.

import (
	"testing"
)

// ─── Construction ─────────────────────────────────────────────────────────────

func TestPatternSet_Empty(t *testing.T) {
	ps, err := newPatternSet(nil, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ps.matches("anything") {
		t.Error("empty patternSet should match nothing")
	}
}

func TestPatternSet_InvalidGlob(t *testing.T) {
	// "[" is an unclosed bracket — invalid glob.
	_, err := newPatternSet([]string{"["}, true)
	if err == nil {
		t.Error("expected error for invalid glob pattern '['")
	}
}

func TestPatternSet_ValidGlob_NoError(t *testing.T) {
	_, err := newPatternSet([]string{"*.go", "cmd/*", "vendor"}, true)
	if err != nil {
		t.Fatalf("unexpected error for valid patterns: %v", err)
	}
}

func TestPatternSet_BlankAndDotPatternsSkipped(t *testing.T) {
	ps, err := newPatternSet([]string{"", ".", "real.txt"}, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ps.matches("real.txt") {
		t.Error("real.txt should match")
	}
	// Patterns "" and "." must have been discarded.
	if len(ps.patterns) != 1 {
		t.Errorf("expected 1 surviving pattern, got %d: %v", len(ps.patterns), ps.patterns)
	}
}

// ─── Literal mode ─────────────────────────────────────────────────────────────

// Rule 1: exact full-path match.
func TestLiteral_ExactFullPath(t *testing.T) {
	ps := mustPS(t, []string{"cmd/main.go"}, false)
	if !ps.matches("cmd/main.go") {
		t.Error("exact path should match")
	}
	if ps.matches("cmd/main_test.go") {
		t.Error("different basename should not match")
	}
}

// Rule 2: base-name match.
func TestLiteral_BaseName(t *testing.T) {
	ps := mustPS(t, []string{"Makefile"}, false)
	if !ps.matches("Makefile") {
		t.Error("root-level Makefile should match")
	}
	if !ps.matches("src/Makefile") {
		t.Error("nested Makefile should match via base-name rule")
	}
	if !ps.matches("a/b/c/Makefile") {
		t.Error("deeply nested Makefile should match via base-name rule")
	}
	if ps.matches("src/Makefile.bak") {
		t.Error("Makefile.bak should not match pattern Makefile")
	}
}

// Rule 3: directory-prefix match.
func TestLiteral_DirectoryPrefix(t *testing.T) {
	ps := mustPS(t, []string{"vendor"}, false)
	if !ps.matches("vendor") {
		t.Error("vendor itself should match")
	}
	if !ps.matches("vendor/pkg/x.go") {
		t.Error("vendor/pkg/x.go should match via prefix rule")
	}
	if !ps.matches("vendor/a/b/c/d.go") {
		t.Error("deeply nested vendor path should match")
	}
	// Must NOT match "vendor_extra" — separator required.
	if ps.matches("vendor_extra/x.go") {
		t.Error("vendor_extra should not match pattern vendor")
	}
	if ps.matches("notvendor") {
		t.Error("notvendor should not match pattern vendor")
	}
}

func TestLiteral_MultiplePatterns(t *testing.T) {
	ps := mustPS(t, []string{"vendor", "*.go", "README.md"}, false)
	// "vendor" by prefix
	if !ps.matches("vendor/x.go") {
		t.Error("vendor/x.go should match via vendor prefix")
	}
	// "README.md" by base-name (literal mode: "*.go" is a literal name, not a glob)
	if !ps.matches("README.md") {
		t.Error("README.md should match")
	}
	// "*.go" is the literal string "*.go" in literal mode — not a glob.
	if !ps.matches("*.go") {
		t.Error("literal *.go should match the exact string *.go")
	}
	// A real .go file does NOT match "*.go" in literal mode.
	if ps.matches("main.go") {
		t.Error("main.go should NOT match literal pattern *.go")
	}
}

// ─── Glob mode ────────────────────────────────────────────────────────────────

func TestGlob_StarSuffix(t *testing.T) {
	ps := mustPS(t, []string{"*.go"}, true)
	if !ps.matches("main.go") {
		t.Error("main.go should match *.go")
	}
	if !ps.matches("cmd/main.go") {
		t.Error("cmd/main.go should match *.go via base-name glob")
	}
	if ps.matches("main.go.bak") {
		t.Error("main.go.bak should not match *.go")
	}
}

func TestGlob_FullPathGlob(t *testing.T) {
	ps := mustPS(t, []string{"cmd/*.go"}, true)
	if !ps.matches("cmd/main.go") {
		t.Error("cmd/main.go should match cmd/*.go")
	}
	if ps.matches("pkg/main.go") {
		t.Error("pkg/main.go should not match cmd/*.go")
	}
}

func TestGlob_QuestionMark(t *testing.T) {
	ps := mustPS(t, []string{"file?.txt"}, true)
	if !ps.matches("file1.txt") {
		t.Error("file1.txt should match file?.txt")
	}
	if !ps.matches("sub/fileA.txt") {
		t.Error("sub/fileA.txt should match via base-name glob")
	}
	if ps.matches("file10.txt") {
		t.Error("file10.txt should not match file?.txt (? matches single char)")
	}
}

func TestGlob_LiteralPattern_InGlobMode(t *testing.T) {
	// A pattern with no wildcards still works in glob mode.
	ps := mustPS(t, []string{"go.mod"}, true)
	if !ps.matches("go.mod") {
		t.Error("go.mod should match literal go.mod in glob mode")
	}
	if !ps.matches("sub/go.mod") {
		t.Error("sub/go.mod should match via base-name glob in glob mode")
	}
}

func TestGlob_VendorDir(t *testing.T) {
	// In glob mode, "vendor" matches exactly — no prefix expansion.
	// Use "vendor*" to match the directory and its children via full-path.
	ps := mustPS(t, []string{"vendor"}, true)
	if !ps.matches("vendor") {
		t.Error("vendor should match")
	}
	// In glob mode there's no implicit prefix rule: "vendor/x.go" does NOT
	// match "vendor" (no wildcard, no separator).
	if ps.matches("vendor/x.go") {
		t.Error("vendor/x.go should NOT match literal glob 'vendor' (no prefix rule in glob mode)")
	}
}

// ─── Helper ───────────────────────────────────────────────────────────────────

// mustPS is a test helper that builds a patternSet or fails the test.
func mustPS(t *testing.T, patterns []string, wildcard bool) patternSet {
	t.Helper()
	ps, err := newPatternSet(patterns, wildcard)
	if err != nil {
		t.Fatalf("newPatternSet(%v, %v): %v", patterns, wildcard, err)
	}
	return ps
}
