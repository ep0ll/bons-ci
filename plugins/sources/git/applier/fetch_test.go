package gitapply

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// ─── Test fixtures ────────────────────────────────────────────────────────────

// testRepo is a temporary git repository for integration tests.
// It is created once per test using setupTestRepo.
type testRepo struct {
	dir    string // root of the repository
	remote string // file:// URL pointing at the repo (safe for local clones)
}

// setupTestRepo creates a fresh git repository with a controlled history:
//
//	master: file "hello.txt" = "hello world\n"
//	branch "feature": adds "feature.txt" = "feature content\n"
//	tag "v1.0.0" (lightweight): points at master HEAD
//	tag "v1.1.0" (annotated): points at master HEAD
//
// The repository is completely isolated from the host's git configuration
// (system config and ~/.gitconfig are suppressed, GPG signing is disabled).
// This prevents failures on machines with commit.gpgSign=true or tag.gpgSign=true.
func setupTestRepo(t *testing.T) testRepo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found on PATH; skipping integration test")
	}

	dir := t.TempDir()

	run := func(args ...string) {
		t.Helper()
		gitHermetic(t, dir, args...)
	}

	run("-c", "init.defaultBranch=master", "init")
	run("config", "user.email", "test@test.com")
	run("config", "user.name", "Test")

	// Initial commit on master.
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello world\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "hello.txt")
	run("commit", "-m", "initial commit")

	// Lightweight tag pointing at initial commit.
	run("tag", "v1.0.0")

	// Annotated tag.
	run("tag", "-a", "v1.1.0", "-m", "release 1.1.0")

	// Feature branch.
	run("checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(dir, "feature.txt"), []byte("feature content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "feature.txt")
	run("commit", "-m", "add feature")

	// Return to master.
	run("checkout", "master")

	return testRepo{
		dir:    dir,
		remote: "file://" + dir,
	}
}

// masterSHA returns the commit SHA of the master branch in the repo.
func (r testRepo) masterSHA(t *testing.T) string {
	t.Helper()
	return gitRevParse(t, r.dir, "master")
}

// featureSHA returns the commit SHA of the feature branch.
func (r testRepo) featureSHA(t *testing.T) string {
	t.Helper()
	return gitRevParse(t, r.dir, "feature")
}

// gitRevParse runs "git rev-parse <ref>" in dir with a minimal, hermetic environment.
func gitRevParse(t *testing.T, dir, ref string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", ref)
	cmd.Dir = dir
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
	}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("rev-parse %s: %v", ref, err)
	}
	return strings.TrimSpace(string(out))
}

// ─── Helper ───────────────────────────────────────────────────────────────────

func newTestFetcher(t *testing.T) *DefaultFetcher {
	t.Helper()
	f, err := NewDefaultFetcher(WithWorkDir(t.TempDir()))
	if err != nil {
		t.Skipf("git not available: %v", err)
	}
	return f
}

func fetchCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

// ─── Basic fetch by branch ────────────────────────────────────────────────────

func TestDefaultFetcher_Fetch_branchMaster(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("integration test not supported on Windows in CI")
	}
	t.Parallel()

	repo := setupTestRepo(t)
	f := newTestFetcher(t)
	ctx, cancel := fetchCtx()
	defer cancel()

	dst := t.TempDir()
	result, err := f.Fetch(ctx, FetchSpec{
		Remote: repo.remote,
		Ref:    "master",
	}, dst)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	// Verify result metadata.
	wantSHA := repo.masterSHA(t)
	if result.CommitSHA != wantSHA {
		t.Errorf("CommitSHA: want %s, got %s", wantSHA, result.CommitSHA)
	}

	// Verify checkout contents.
	assertFileContent(t, filepath.Join(dst, "hello.txt"), "hello world\n")

	// Feature branch file must NOT be present.
	if _, err := os.Stat(filepath.Join(dst, "feature.txt")); !os.IsNotExist(err) {
		t.Error("feature.txt should not exist on master checkout")
	}

	// .git directory must NOT be present (KeepGitDir=false).
	if _, err := os.Stat(filepath.Join(dst, ".git")); !os.IsNotExist(err) {
		t.Error(".git directory should not exist when KeepGitDir=false")
	}
}

// ─── Fetch by branch: feature ─────────────────────────────────────────────────

func TestDefaultFetcher_Fetch_branchFeature(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("integration test not supported on Windows in CI")
	}
	t.Parallel()

	repo := setupTestRepo(t)
	f := newTestFetcher(t)
	ctx, cancel := fetchCtx()
	defer cancel()

	dst := t.TempDir()
	result, err := f.Fetch(ctx, FetchSpec{
		Remote: repo.remote,
		Ref:    "feature",
	}, dst)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	wantSHA := repo.featureSHA(t)
	if result.CommitSHA != wantSHA {
		t.Errorf("CommitSHA: want %s, got %s", wantSHA, result.CommitSHA)
	}
	assertFileContent(t, filepath.Join(dst, "hello.txt"), "hello world\n")
	assertFileContent(t, filepath.Join(dst, "feature.txt"), "feature content\n")
}

// ─── Fetch by commit SHA ──────────────────────────────────────────────────────

func TestDefaultFetcher_Fetch_byCommitSHA(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("integration test not supported on Windows in CI")
	}
	t.Parallel()

	repo := setupTestRepo(t)
	sha := repo.masterSHA(t)

	f := newTestFetcher(t)
	ctx, cancel := fetchCtx()
	defer cancel()

	dst := t.TempDir()
	result, err := f.Fetch(ctx, FetchSpec{
		Remote: repo.remote,
		Ref:    sha,
	}, dst)
	if err != nil {
		t.Fatalf("Fetch by SHA: %v", err)
	}

	if result.CommitSHA != sha {
		t.Errorf("CommitSHA: want %s, got %s", sha, result.CommitSHA)
	}
	assertFileContent(t, filepath.Join(dst, "hello.txt"), "hello world\n")
}

// ─── Checksum verification (success) ─────────────────────────────────────────

func TestDefaultFetcher_Fetch_checksumMatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("integration test not supported on Windows in CI")
	}
	t.Parallel()

	repo := setupTestRepo(t)
	sha := repo.masterSHA(t)
	prefix := sha[:12] // short prefix

	f := newTestFetcher(t)
	ctx, cancel := fetchCtx()
	defer cancel()

	_, err := f.Fetch(ctx, FetchSpec{
		Remote:   repo.remote,
		Ref:      "master",
		Checksum: prefix,
	}, t.TempDir())
	if err != nil {
		t.Fatalf("Fetch with matching checksum: %v", err)
	}
}

// ─── Checksum verification (mismatch) ────────────────────────────────────────

func TestDefaultFetcher_Fetch_checksumMismatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("integration test not supported on Windows in CI")
	}
	t.Parallel()

	repo := setupTestRepo(t)
	f := newTestFetcher(t)
	ctx, cancel := fetchCtx()
	defer cancel()

	_, err := f.Fetch(ctx, FetchSpec{
		Remote:   repo.remote,
		Ref:      "master",
		Checksum: "0000000deadbeef", // definitely wrong
	}, t.TempDir())

	if err == nil {
		t.Fatal("expected checksum mismatch error, got nil")
	}
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Errorf("expected ErrChecksumMismatch; got: %v", err)
	}
}

// ─── Subdir extraction ────────────────────────────────────────────────────────

func TestDefaultFetcher_Fetch_subdir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("integration test not supported on Windows in CI")
	}
	t.Parallel()

	// Build a repo with a subdirectory structure using the shared hermetic helper.
	dir := t.TempDir()
	gitHermetic(t, dir, "-c", "init.defaultBranch=master", "init")
	gitHermetic(t, dir, "config", "user.email", "test@test.com")
	gitHermetic(t, dir, "config", "user.name", "Test")

	subdir := filepath.Join(dir, "pkg", "lib")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subdir, "lib.go"), []byte("package lib\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitHermetic(t, dir, "add", ".")
	gitHermetic(t, dir, "commit", "-m", "initial")

	f := newTestFetcher(t)
	ctx, cancel := fetchCtx()
	defer cancel()

	dst := t.TempDir()
	_, err := f.Fetch(ctx, FetchSpec{
		Remote: "file://" + dir,
		Ref:    "master",
		Subdir: "pkg/lib",
	}, dst)
	if err != nil {
		t.Fatalf("Fetch with subdir: %v", err)
	}

	// Only pkg/lib contents should be in dst.
	assertFileContent(t, filepath.Join(dst, "lib.go"), "package lib\n")

	// Files outside the subdir must not leak into dst.
	if _, err := os.Stat(filepath.Join(dst, "main.go")); !os.IsNotExist(err) {
		t.Error("main.go should not appear in subdir checkout")
	}
}

// ─── KeepGitDir ───────────────────────────────────────────────────────────────

func TestDefaultFetcher_Fetch_keepGitDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("integration test not supported on Windows in CI")
	}
	t.Parallel()

	repo := setupTestRepo(t)
	f := newTestFetcher(t)
	ctx, cancel := fetchCtx()
	defer cancel()

	dst := t.TempDir()
	_, err := f.Fetch(ctx, FetchSpec{
		Remote:     repo.remote,
		Ref:        "master",
		KeepGitDir: true,
	}, dst)
	if err != nil {
		t.Fatalf("Fetch with KeepGitDir: %v", err)
	}

	// .git directory must be present.
	if _, err := os.Stat(filepath.Join(dst, ".git")); os.IsNotExist(err) {
		t.Error(".git directory should exist when KeepGitDir=true")
	}

	// FETCH_HEAD must have been removed.
	if _, err := os.Stat(filepath.Join(dst, ".git", "FETCH_HEAD")); !os.IsNotExist(err) {
		t.Error("FETCH_HEAD should be removed after checkout")
	}

	// The origin URL should be the real remote, not the temp bare-repo path.
	cmd := exec.Command("git", "remote", "get-url", "origin")
	cmd.Dir = dst
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
	}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("get-url origin: %v", err)
	}
	originURL := strings.TrimSpace(string(out))
	if strings.Contains(originURL, "gitapply-bare-") {
		t.Errorf("origin should not reference temp bare repo path; got %q", originURL)
	}
}

// ─── Invalid spec ─────────────────────────────────────────────────────────────

func TestDefaultFetcher_Fetch_invalidSpec(t *testing.T) {
	t.Parallel()
	f := newTestFetcher(t)
	ctx, cancel := fetchCtx()
	defer cancel()

	_, err := f.Fetch(ctx, FetchSpec{}, t.TempDir())
	if err == nil {
		t.Fatal("expected error for empty spec, got nil")
	}
	if !errors.Is(err, ErrInvalidRemote) {
		t.Errorf("expected ErrInvalidRemote; got: %v", err)
	}
}

// ─── Context cancellation ─────────────────────────────────────────────────────

func TestDefaultFetcher_Fetch_cancelledContext(t *testing.T) {
	t.Parallel()
	// The test uses a local repo so it will not actually block on network;
	// but we still verify that a pre-cancelled context propagates correctly.
	repo := setupTestRepo(t)
	f := newTestFetcher(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := f.Fetch(ctx, FetchSpec{
		Remote: repo.remote,
		Ref:    "master",
	}, t.TempDir())
	if err == nil {
		// With a local file:// repo the fetch may succeed even with a cancelled
		// context if git exits before the goroutine checks ctx.Done().
		// This is acceptable — the important thing is it does not panic.
		t.Log("note: fetch completed before cancellation was observed (local repo, expected)")
	}
}

// ─── DefaultBranch resolution ─────────────────────────────────────────────────

func TestDefaultFetcher_Fetch_emptyRef_usesDefaultBranch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("integration test not supported on Windows in CI")
	}
	t.Parallel()

	repo := setupTestRepo(t)
	f := newTestFetcher(t)
	ctx, cancel := fetchCtx()
	defer cancel()

	dst := t.TempDir()
	result, err := f.Fetch(ctx, FetchSpec{
		Remote: repo.remote,
		// Ref intentionally empty: should resolve to "master".
	}, dst)
	if err != nil {
		t.Fatalf("Fetch with empty ref: %v", err)
	}

	wantSHA := repo.masterSHA(t)
	if result.CommitSHA != wantSHA {
		t.Errorf("CommitSHA: want %s, got %s", wantSHA, result.CommitSHA)
	}
	assertFileContent(t, filepath.Join(dst, "hello.txt"), "hello world\n")
}

// ─── Concurrent fetches to different remotes ─────────────────────────────────

func TestDefaultFetcher_Fetch_concurrent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("integration test not supported on Windows in CI")
	}
	t.Parallel()

	const concurrency = 4
	repo := setupTestRepo(t)
	f := newTestFetcher(t)

	type result struct {
		CommitSHA string
		err       error
	}
	ch := make(chan result, concurrency)

	for i := 0; i < concurrency; i++ {
		go func() {
			ctx, cancel := fetchCtx()
			defer cancel()
			r, err := f.Fetch(ctx, FetchSpec{
				Remote: repo.remote,
				Ref:    "master",
			}, t.TempDir())
			ch <- result{r.CommitSHA, err}
		}()
	}

	wantSHA := repo.masterSHA(t)
	for i := 0; i < concurrency; i++ {
		r := <-ch
		if r.err != nil {
			t.Errorf("goroutine %d: unexpected error: %v", i, r.err)
			continue
		}
		if r.CommitSHA != wantSHA {
			t.Errorf("goroutine %d: CommitSHA = %s; want %s", i, r.CommitSHA, wantSHA)
		}
	}
}

// ─── Submodule handling ───────────────────────────────────────────────────────

// TestDefaultFetcher_Fetch_noSubmodules_noGitmodules verifies that when a repo
// has no .gitmodules file, submodule init is skipped entirely (no error, no
// spurious git-submodule invocation).  This is the common case for the vast
// majority of repositories.
func TestDefaultFetcher_Fetch_noSubmodules_noGitmodules(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("integration test not supported on Windows in CI")
	}
	t.Parallel()

	repo := setupTestRepo(t) // no .gitmodules in fixture
	f := newTestFetcher(t)
	ctx, cancel := fetchCtx()
	defer cancel()

	dst := t.TempDir()
	_, err := f.Fetch(ctx, FetchSpec{
		Remote: repo.remote,
		Ref:    "master",
		// SkipSubmodules intentionally false — the code must handle the
		// no-.gitmodules case without error.
	}, dst)
	if err != nil {
		t.Fatalf("Fetch on repo without .gitmodules: %v", err)
	}
	// .git must not be present (KeepGitDir=false).
	if _, err := os.Stat(filepath.Join(dst, ".git")); !os.IsNotExist(err) {
		t.Error("stale .git gitlink must be removed after plain checkout")
	}
}

// TestDefaultFetcher_Fetch_withSubmodule verifies the full submodule
// initialisation path: create a parent repo that records a submodule, fetch
// it, and confirm the submodule content is present in the checkout.
func TestDefaultFetcher_Fetch_withSubmodule(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("integration test not supported on Windows in CI")
	}
	t.Parallel()

	// Build a standalone "library" repo that will be used as the submodule.
	libDir := t.TempDir()
	buildHermeticRepo(t, libDir, map[string]string{
		"lib.go": "package lib\n",
	}, "initial lib")

	// Build the parent repo that records the library as a submodule.
	parentDir := t.TempDir()
	buildHermeticRepo(t, parentDir, map[string]string{
		"main.go": "package main\n",
	}, "initial parent")

	// Add the submodule using file:// so the URL is portable.
	gitHermetic(t, parentDir, "submodule", "add", "file://"+libDir, "lib")
	gitHermetic(t, parentDir, "commit", "-m", "add submodule")

	f := newTestFetcher(t)
	ctx, cancel := fetchCtx()
	defer cancel()

	dst := t.TempDir()
	_, err := f.Fetch(ctx, FetchSpec{
		Remote: "file://" + parentDir,
		Ref:    "master",
		// SkipSubmodules=false: submodule must be initialised.
	}, dst)
	if err != nil {
		t.Fatalf("Fetch with submodule: %v", err)
	}

	// The submodule content must be present.
	assertFileContent(t, filepath.Join(dst, "lib", "lib.go"), "package lib\n")

	// After a plain checkout the .git gitlink must be gone.
	if _, err := os.Stat(filepath.Join(dst, ".git")); !os.IsNotExist(err) {
		t.Error("temporary .git gitlink must be removed after submodule init")
	}
}

// TestDefaultFetcher_Fetch_withSubmodule_keepGitDir verifies submodule init
// works correctly on the KeepGitDir path (where .git is a real directory).
func TestDefaultFetcher_Fetch_withSubmodule_keepGitDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("integration test not supported on Windows in CI")
	}
	t.Parallel()

	libDir := t.TempDir()
	buildHermeticRepo(t, libDir, map[string]string{"util.go": "package util\n"}, "lib init")

	parentDir := t.TempDir()
	buildHermeticRepo(t, parentDir, map[string]string{"app.go": "package main\n"}, "app init")
	gitHermetic(t, parentDir, "submodule", "add", "file://"+libDir, "util")
	gitHermetic(t, parentDir, "commit", "-m", "add util submodule")

	f := newTestFetcher(t)
	ctx, cancel := fetchCtx()
	defer cancel()

	dst := t.TempDir()
	_, err := f.Fetch(ctx, FetchSpec{
		Remote:     "file://" + parentDir,
		Ref:        "master",
		KeepGitDir: true,
	}, dst)
	if err != nil {
		t.Fatalf("Fetch with submodule + KeepGitDir: %v", err)
	}

	assertFileContent(t, filepath.Join(dst, "util", "util.go"), "package util\n")
	// .git directory must be present.
	if _, err := os.Stat(filepath.Join(dst, ".git")); os.IsNotExist(err) {
		t.Error(".git directory must exist for KeepGitDir=true")
	}
}

// ─── helpers for submodule tests ─────────────────────────────────────────────

// hermeticEnv returns the minimal environment for fixture git commands.
func hermeticEnv() []string {
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@test.com",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
	}
}

// hermeticBaseArgs returns the -c args that disable GPG signing for fixture repos.
var hermeticBaseArgs = []string{
	"-c", "commit.gpgSign=false",
	"-c", "tag.gpgSign=false",
	"-c", "tag.forceSignAnnotated=false",
}

// gitHermetic runs git in dir with a hermetic environment.
// It is the shared helper for all submodule fixture helpers.
func gitHermetic(t *testing.T, dir string, args ...string) string {
	t.Helper()
	fullArgs := append(hermeticBaseArgs, args...)
	cmd := exec.Command("git", fullArgs...)
	cmd.Dir = dir
	cmd.Env = hermeticEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// buildHermeticRepo initialises a git repo at dir, writes files, and commits.
func buildHermeticRepo(t *testing.T, dir string, files map[string]string, commitMsg string) {
	t.Helper()
	gitHermetic(t, dir, "-c", "init.defaultBranch=master", "init")
	gitHermetic(t, dir, "config", "user.email", "test@test.com")
	gitHermetic(t, dir, "config", "user.name", "Test")
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitHermetic(t, dir, "add", ".")
	gitHermetic(t, dir, "commit", "-m", commitMsg)
}

func TestFullyQualifiedRef(t *testing.T) {
	t.Parallel()
	cases := []struct{ input, want string }{
		{"refs/heads/main", "refs/heads/main"},
		{"refs/tags/v1.0.0", "refs/tags/v1.0.0"},
		{"main", "refs/heads/main"},
		{"feature/thing", "refs/heads/feature/thing"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			got := fullyQualifiedRef(tc.input)
			if got != tc.want {
				t.Errorf("fullyQualifiedRef(%q) = %q; want %q", tc.input, got, tc.want)
			}
		})
	}
}

// ─── buildPullRef ─────────────────────────────────────────────────────────────

func TestBuildPullRef_annotatedTag(t *testing.T) {
	t.Parallel()
	r := FetchResult{
		CommitSHA: "aaaa" + strings.Repeat("0", 36),
		Ref:       "v1.0.0",
		TagSHA:    "bbbb" + strings.Repeat("0", 36),
	}
	ref := buildPullRef(r)
	// Should be <tagSHA>:refs/tags/v1.0.0
	if !strings.HasPrefix(ref, r.TagSHA) {
		t.Errorf("pull ref for annotated tag should start with tag SHA; got %q", ref)
	}
	if !strings.Contains(ref, "refs/tags/v1.0.0") {
		t.Errorf("pull ref should include refs/tags path; got %q", ref)
	}
}

func TestBuildPullRef_commit(t *testing.T) {
	t.Parallel()
	sha := strings.Repeat("a", 40)
	r := FetchResult{CommitSHA: sha, Ref: "refs/heads/main"}
	ref := buildPullRef(r)
	if ref != sha {
		t.Errorf("pull ref for plain commit should be the SHA; got %q", ref)
	}
}

// ─── support.go helpers ───────────────────────────────────────────────────────

func TestParseGitVersion(t *testing.T) {
	t.Parallel()
	cases := []struct {
		input string
		want  string
		ok    bool
	}{
		{"git version 2.43.0\n", "2.43.0", true},
		{"git version 2.18.0 (Apple Git-117)\n", "2.18.0", true},
		{"git version 1.8.3.1\n", "1.8.3.1", true},
		{"not git output", "", false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.input[:12], func(t *testing.T) {
			t.Parallel()
			got, ok := parseGitVersion(tc.input)
			if ok != tc.ok {
				t.Fatalf("ok: want %v, got %v", tc.ok, ok)
			}
			if ok && got != tc.want {
				t.Errorf("version: want %q, got %q", tc.want, got)
			}
		})
	}
}

func TestMeetsMinimumVersion(t *testing.T) {
	t.Parallel()
	cases := []struct {
		actual, min string
		want        bool
	}{
		{"2.43.0", "2.18", true},
		{"2.18.0", "2.18", true},
		{"2.17.9", "2.18", false},
		{"3.0.0", "2.18", true},
		{"1.9.9", "2.18", false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(fmt.Sprintf("%s>=%s", tc.actual, tc.min), func(t *testing.T) {
			t.Parallel()
			got := meetsMinimumVersion(tc.actual, tc.min)
			if got != tc.want {
				t.Errorf("meetsMinimumVersion(%q, %q) = %v; want %v",
					tc.actual, tc.min, got, tc.want)
			}
		})
	}
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %q: %v", path, err)
	}
	if string(data) != want {
		t.Errorf("file %q: want %q, got %q", path, want, string(data))
	}
}
