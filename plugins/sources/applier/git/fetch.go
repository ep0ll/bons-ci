package gitapply

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// defaultBranchPattern extracts the branch name from git ls-remote --symref output.
var defaultBranchPattern = regexp.MustCompile(`refs/heads/(\S+)`)

// DefaultFetcher is the production [GitFetcher] implementation.
//
// It manages a temporary bare git repository as a local mirror of the remote,
// then checks out the requested ref into the caller-supplied destination directory.
// Retry logic handles transient ref-clobber and ref-conflict errors that can
// occur when a remote mutable ref is updated between our resolve and fetch steps.
type DefaultFetcher struct {
	runner   ProcessRunner
	auth     AuthProvider
	workDir  string // base for temp dirs; "" means os.TempDir()
	mu       sync.Mutex
	// remoteLocks serialises concurrent fetches to the same remote URL to
	// prevent multiple goroutines from running "git fetch" against the same
	// bare repo simultaneously.
	remoteLocks map[string]*sync.Mutex
}

// FetcherOption is a functional option for [DefaultFetcher].
type FetcherOption func(*DefaultFetcher)

// WithProcessRunner overrides the platform default ProcessRunner.
// Useful in tests or when a custom umask/sandboxing policy is required.
func WithProcessRunner(r ProcessRunner) FetcherOption {
	return func(f *DefaultFetcher) { f.runner = r }
}

// WithAuthProvider sets the AuthProvider used to resolve credentials.
// If not called, [NoAuthProvider] is used (public repositories only).
func WithAuthProvider(p AuthProvider) FetcherOption {
	return func(f *DefaultFetcher) { f.auth = p }
}

// WithWorkDir sets the base directory under which temporary bare repositories
// are created.  Defaults to os.TempDir().
func WithWorkDir(dir string) FetcherOption {
	return func(f *DefaultFetcher) { f.workDir = dir }
}

// NewDefaultFetcher constructs a ready-to-use [DefaultFetcher].
func NewDefaultFetcher(opts ...FetcherOption) (*DefaultFetcher, error) {
	if err := checkGitAvailable(); err != nil {
		return nil, err
	}
	f := &DefaultFetcher{
		runner:      DefaultProcessRunner,
		auth:        NoAuthProvider{},
		remoteLocks: make(map[string]*sync.Mutex),
	}
	for _, o := range opts {
		o(f)
	}
	return f, nil
}

// checkGitAvailable returns [ErrGitNotFound] when the git binary is absent.
func checkGitAvailable() error {
	if _, err := exec.LookPath("git"); err != nil {
		return ErrGitNotFound
	}
	return nil
}

// Fetch implements [GitFetcher].
//
// High-level flow:
//  1. Validate the spec.
//  2. Resolve authentication credentials.
//  3. Acquire a per-remote lock.
//  4. Create a temp bare repository, init if new, fetch the ref.
//  5. On certain ref-conflict errors, retry with a fresh bare repo.
//  6. Verify checksum if requested.
//  7. Checkout into dstDir (normal or KeepGitDir variant).
//  8. Extract subdir if requested.
//  9. Initialise submodules unless suppressed.
// 10. Expire reflog and remove FETCH_HEAD (sensitive data hygiene).
// 11. Verify signatures if requested.
func (f *DefaultFetcher) Fetch(ctx context.Context, spec FetchSpec, dstDir string) (FetchResult, error) {
	if err := spec.Validate(); err != nil {
		return FetchResult{}, fmt.Errorf("gitapply: invalid spec: %w", err)
	}

	// ── 1. Resolve auth ──────────────────────────────────────────────────────

	httpArgs, sshPath, sshCleanup, knownHostsPath, khCleanup, err := f.resolveAuth(ctx, spec)
	defer func() { _ = sshCleanup(); _ = khCleanup() }()
	if err != nil {
		return FetchResult{}, fmt.Errorf("gitapply: resolve auth: %w", err)
	}

	// ── 2. Build base CLI ─────────────────────────────────────────────────────

	baseCLI := newGitCLI(
		f.runner,
		withExtraConfigArgs(httpArgs...),
		withSSHSocket(sshPath),
		withKnownHosts(knownHostsPath),
	)

	// ── 3. Lock per remote ────────────────────────────────────────────────────

	remoteLock := f.lockForRemote(spec.Remote)
	remoteLock.Lock()
	defer remoteLock.Unlock()

	// ── 4. Fetch into a bare repo, with one retry on conflict errors ──────────

	result, bareDir, bareCLI, err := f.fetchWithRetry(ctx, spec, baseCLI)
	defer os.RemoveAll(bareDir)
	if err != nil {
		return FetchResult{}, err
	}

	// ── 5. Checkout ───────────────────────────────────────────────────────────

	if err := f.checkout(ctx, spec, result, bareCLI, bareDir, dstDir); err != nil {
		return FetchResult{}, err
	}

	// ── 6. Signature verification ─────────────────────────────────────────────

	if spec.SignatureVerify != nil {
		if err := f.verifySignature(ctx, spec, result, bareCLI); err != nil {
			return FetchResult{}, err
		}
	}

	return result, nil
}

// lockForRemote returns the mutex dedicated to remote, creating it if necessary.
func (f *DefaultFetcher) lockForRemote(remote string) *sync.Mutex {
	f.mu.Lock()
	defer f.mu.Unlock()
	mu, ok := f.remoteLocks[remote]
	if !ok {
		mu = &sync.Mutex{}
		f.remoteLocks[remote] = mu
	}
	return mu
}

// resolveAuth calls the AuthProvider and returns the components needed to
// configure the gitCLI.
func (f *DefaultFetcher) resolveAuth(
	ctx context.Context,
	spec FetchSpec,
) (httpArgs []string, sshPath string, sshCleanup func() error,
	knownHostsPath string, khCleanup func() error, err error,
) {
	sshCleanup = nopRelease
	khCleanup = nopRelease

	httpArgs, err = f.auth.HTTPAuthArgs(ctx, spec.Remote)
	if err != nil {
		return
	}
	if spec.SSHSocketID != "" {
		sshPath, sshCleanup, err = f.auth.SSHSocket(ctx, spec.SSHSocketID)
		if err != nil {
			return
		}
	}
	if spec.KnownSSHHosts != "" {
		knownHostsPath, khCleanup, err = f.auth.KnownHostsFile(ctx, spec.KnownSSHHosts)
	}
	return
}

// fetchWithRetry runs the bare-repo fetch, retrying with a fresh repo on
// known transient ref-conflict errors (tag clobber, branch rename).
// Returns the FetchResult, the path of the temp bare repo (for cleanup and
// later checkout), and a CLI scoped to that repo.
func (f *DefaultFetcher) fetchWithRetry(
	ctx context.Context,
	spec FetchSpec,
	baseCLI *gitCLI,
) (FetchResult, string, *gitCLI, error) {
	result, bareDir, bareCLI, err := f.fetchIntoBareRepo(ctx, spec, baseCLI)
	if err == nil {
		return result, bareDir, bareCLI, nil
	}

	// Classify the error.  Some conditions require a fresh bare repo:
	//   - A tag was mutated on the remote (would clobber existing tag).
	//   - A branch was renamed so its old path is now a parent dir of a new branch.
	var wce *wouldClobberTagError
	var ulre *unableToUpdateRefError
	if !errors.As(err, &wce) && !errors.As(err, &ulre) {
		return FetchResult{}, bareDir, nil, newFetchError(spec.Remote, err)
	}

	// Clean up the failed bare repo before trying again.
	_ = os.RemoveAll(bareDir)

	result, bareDir, bareCLI, err = f.fetchIntoBareRepo(ctx, spec, baseCLI)
	if err != nil {
		return FetchResult{}, bareDir, nil, newFetchError(spec.Remote, err)
	}
	return result, bareDir, bareCLI, nil
}

// fetchIntoBareRepo creates a temporary bare git repository, initialises it if
// needed, and fetches the requested ref from the remote.
func (f *DefaultFetcher) fetchIntoBareRepo(
	ctx context.Context,
	spec FetchSpec,
	baseCLI *gitCLI,
) (_ FetchResult, bareDir string, bareCLI *gitCLI, retErr error) {
	// Create temp directory for the bare repo.
	bareDir, err := os.MkdirTemp(f.workDir, "gitapply-bare-*")
	if err != nil {
		return FetchResult{}, "", nil, fmt.Errorf("gitapply: create bare repo dir: %w", err)
	}
	defer func() {
		if retErr != nil {
			_ = os.RemoveAll(bareDir)
			bareDir = ""
		}
	}()

	bareCLI = baseCLI.with(withGitDir(bareDir))

	// ── Init bare repo ────────────────────────────────────────────────────────

	sha256 := false // updated after ls-remote if we detect SHA-256 format
	initArgs := []string{
		"-c", "init.defaultBranch=master", // suppress confusing hint output
		"init", "--bare",
	}
	if _, err := bareCLI.run(ctx, initArgs...); err != nil {
		return FetchResult{}, bareDir, nil, fmt.Errorf("gitapply: git init bare: %w", err)
	}
	if _, err := bareCLI.run(ctx, "remote", "add", "origin", spec.Remote); err != nil {
		return FetchResult{}, bareDir, nil, fmt.Errorf("gitapply: git remote add: %w", err)
	}

	// ── Resolve ref ───────────────────────────────────────────────────────────

	ref := spec.Ref

	// Fast path: if the spec names a bare commit SHA we skip ls-remote and
	// fetch the commit directly if it is not already present.
	if IsCommitSHA(ref) {
		result, err := f.fetchCommitSHA(ctx, spec, ref, bareCLI, bareDir, sha256)
		if err != nil {
			return FetchResult{}, bareDir, nil, err
		}
		return result, bareDir, bareCLI, nil
	}

	// Determine default branch if ref is empty.
	if ref == "" {
		ref, err = f.defaultBranch(ctx, bareCLI, spec.Remote)
		if err != nil {
			return FetchResult{}, bareDir, nil, err
		}
	}

	// ── Fetch by ref ──────────────────────────────────────────────────────────

	result, err := f.fetchRef(ctx, spec, ref, bareCLI, bareDir, sha256)
	if err != nil {
		return FetchResult{}, bareDir, nil, err
	}
	return result, bareDir, bareCLI, nil
}

// defaultBranch queries the remote for its HEAD ref using ls-remote --symref.
func (f *DefaultFetcher) defaultBranch(ctx context.Context, bareCLI *gitCLI, remote string) (string, error) {
	buf, err := bareCLI.run(ctx, "ls-remote", "--symref", remote, "HEAD")
	if err != nil {
		return "", fmt.Errorf("gitapply: ls-remote for default branch of %s: %w",
			redactURL(remote), err)
	}
	matches := defaultBranchPattern.FindStringSubmatch(string(buf))
	if len(matches) < 2 {
		return "", fmt.Errorf("%w: ls-remote returned no HEAD for %s",
			ErrRefNotFound, redactURL(remote))
	}
	return matches[1], nil
}

// fetchCommitSHA fetches a bare commit SHA.  If the commit is already present
// in the bare repo, the fetch is skipped.
func (f *DefaultFetcher) fetchCommitSHA(
	ctx context.Context,
	spec FetchSpec,
	sha string,
	bareCLI *gitCLI,
	bareDir string,
	sha256 bool,
) (FetchResult, error) {
	// Skip fetch if the commit is already present.
	if _, err := bareCLI.run(ctx, "cat-file", "-e", sha+"^{commit}"); err != nil {
		// Not present: fetch.
		_ = os.RemoveAll(filepath.Join(bareDir, "shallow.lock")) // clean stale lock

		fetchArgs := []string{"fetch", "--tags"}
		if _, err := os.Lstat(filepath.Join(bareDir, "shallow")); err == nil {
			fetchArgs = append(fetchArgs, "--unshallow")
		}
		fetchArgs = append(fetchArgs, "origin", sha)

		if _, err := bareCLI.run(ctx, fetchArgs...); err != nil {
			return FetchResult{}, fmt.Errorf("gitapply: fetch commit %s from %s: %w",
				sha, redactURL(spec.Remote), err)
		}
	}

	if err := f.verifyChecksum(ctx, bareCLI, spec.Checksum, sha, ""); err != nil {
		return FetchResult{}, err
	}

	return FetchResult{CommitSHA: sha, Ref: sha}, nil
}

// fetchRef fetches a named ref (branch, tag, full refs/… path) from the remote.
func (f *DefaultFetcher) fetchRef(
	ctx context.Context,
	spec FetchSpec,
	ref string,
	bareCLI *gitCLI,
	bareDir string,
	sha256 bool,
) (FetchResult, error) {
	// Compute the local ref name under refs/tags/ so fetched refs are
	// advertised on subsequent operations.  Force-update in case the remote
	// branch has moved forward.
	localTagRef := ref
	if !strings.HasPrefix(ref, "refs/tags/") {
		localTagRef = "tags/" + ref
	}

	_ = os.RemoveAll(filepath.Join(bareDir, "shallow.lock")) // remove stale lock

	fetchArgs := []string{"fetch", "--depth=1", "--no-tags", "origin",
		"--force", ref + ":" + localTagRef}

	rawErr := func() error {
		_, err := bareCLI.run(ctx, fetchArgs...)
		return err
	}()
	if rawErr != nil {
		// Let the caller inspect the error and decide whether to retry.
		return FetchResult{}, classifyFetchError(
			fmt.Errorf("gitapply: fetch ref %q from %s: %w",
				ref, redactURL(spec.Remote), rawErr),
		)
	}

	// Resolve SHA for the fetched ref.
	commitSHA, tagSHA, err := f.resolveRefSHA(ctx, bareCLI, ref)
	if err != nil {
		return FetchResult{}, err
	}

	if err := f.verifyChecksum(ctx, bareCLI, spec.Checksum, commitSHA, tagSHA); err != nil {
		return FetchResult{}, err
	}

	return FetchResult{
		CommitSHA: commitSHA,
		Ref:       fullyQualifiedRef(ref),
		TagSHA:    tagSHA,
	}, nil
}

// resolveRefSHA returns (commitSHA, tagSHA, err).
// tagSHA is non-empty only when ref points to an annotated tag object.
func (f *DefaultFetcher) resolveRefSHA(
	ctx context.Context,
	bareCLI *gitCLI,
	ref string,
) (commitSHA, tagSHA string, err error) {
	buf, err := bareCLI.run(ctx, "rev-parse", ref)
	if err != nil {
		return "", "", fmt.Errorf("gitapply: rev-parse %q: %w", ref, err)
	}
	sha := strings.TrimSpace(string(buf))

	// Check if this is a tag object (annotated tag) or a commit object directly.
	typeBuf, err := bareCLI.run(ctx, "cat-file", "-t", sha)
	if err != nil {
		return "", "", fmt.Errorf("gitapply: cat-file -t %s: %w", sha, err)
	}
	objType := strings.TrimSpace(string(typeBuf))

	if objType == "tag" {
		// Dereference the tag to get the underlying commit SHA.
		commitBuf, err := bareCLI.run(ctx, "rev-parse", sha+"^{commit}")
		if err != nil {
			return "", "", fmt.Errorf("gitapply: dereference tag %s: %w", sha, err)
		}
		return strings.TrimSpace(string(commitBuf)), sha, nil
	}
	// Lightweight tag or branch: sha is already a commit.
	return sha, "", nil
}

// verifyChecksum ensures the resolved commit SHA has the expected prefix.
// It checks both the commit SHA and the tag SHA (for annotated tags).
// Does nothing when expectedPrefix is empty.
func (f *DefaultFetcher) verifyChecksum(
	_ context.Context,
	_ *gitCLI,
	expectedPrefix, commitSHA, tagSHA string,
) error {
	if expectedPrefix == "" {
		return nil
	}
	if strings.HasPrefix(commitSHA, expectedPrefix) {
		return nil
	}
	if tagSHA != "" && strings.HasPrefix(tagSHA, expectedPrefix) {
		return nil
	}
	return &ChecksumMismatchError{
		ExpectedPrefix: expectedPrefix,
		ActualSHA:      commitSHA,
		AltSHA:         tagSHA,
	}
}

// ─── Checkout ─────────────────────────────────────────────────────────────────

// checkout populates dstDir from the bare repository according to spec.
// Two paths exist:
//   - KeepGitDir=true:  a full clone via file:// so that .git is preserved.
//   - KeepGitDir=false: a plain "git checkout <sha> -- ." with optional subdir extraction.
func (f *DefaultFetcher) checkout(
	ctx context.Context,
	spec FetchSpec,
	result FetchResult,
	bareCLI *gitCLI,
	bareDir string,
	dstDir string,
) error {
	if spec.KeepGitDir {
		return f.checkoutWithGitDir(ctx, spec, result, bareCLI, bareDir, dstDir)
	}
	return f.checkoutPlain(ctx, spec, result, bareCLI, dstDir)
}

// checkoutWithGitDir clones the bare repo into dstDir using the file:// protocol
// and checks out the commit.  The .git directory is retained.
//
// Security note: the file:// scheme disables Git's "local clone" optimisations
// (hardlinks, alternates) which on some Git versions can be abused to copy
// files from outside the repository into the build context.
func (f *DefaultFetcher) checkoutWithGitDir(
	ctx context.Context,
	spec FetchSpec,
	result FetchResult,
	bareCLI *gitCLI,
	bareDir string,
	dstDir string,
) error {
	if err := os.MkdirAll(dstDir, 0o711); err != nil {
		return fmt.Errorf("gitapply: mkdir checkout dir: %w", err)
	}

	gitDirPath := filepath.Join(dstDir, ".git")
	// Construct a working-tree CLI that shares the same runner and auth config
	// as the bare-repo CLI but points git-dir and work-tree at the new checkout.
	cloneCLI := newGitCLI(
		bareCLI.runner,
		withGitDir(gitDirPath),
		withWorkTree(dstDir),
		withExtraConfigArgs(bareCLI.extraConfigArgs...),
		withSSHSocket(bareCLI.sshSocketPath),
		withKnownHosts(bareCLI.knownHostsPath),
	)

	// Init the working-tree repo.
	if _, err := cloneCLI.run(ctx,
		"-c", "init.defaultBranch=master",
		"init",
	); err != nil {
		return fmt.Errorf("gitapply: init checkout repo: %w", err)
	}

	// Add origin pointing to the local bare repo via file:// (no local-clone tricks).
	if _, err := cloneCLI.run(ctx, "remote", "add", "origin", "file://"+bareDir); err != nil {
		return fmt.Errorf("gitapply: add origin for checkout: %w", err)
	}

	// Build the refspec.  Annotated tags need special treatment.
	pullRef := buildPullRef(result)

	if _, err := cloneCLI.run(ctx,
		"fetch", "-u", "--depth=1", "origin", pullRef,
	); err != nil {
		return fmt.Errorf("gitapply: fetch for checkout: %w", err)
	}
	if _, err := cloneCLI.run(ctx, "checkout", "FETCH_HEAD"); err != nil {
		return fmt.Errorf("gitapply: checkout FETCH_HEAD: %w", err)
	}

	// Rewrite origin to the real (redacted) remote URL so the .git dir does
	// not permanently reference the ephemeral temp path.
	if _, err := cloneCLI.run(ctx,
		"remote", "set-url", "origin", redactURL(spec.Remote),
	); err != nil {
		return fmt.Errorf("gitapply: update origin URL: %w", err)
	}

	if err := f.postCheckoutCleanup(ctx, cloneCLI, gitDirPath); err != nil {
		return err
	}

	if !spec.SkipSubmodules {
		if err := f.initSubmodules(ctx, cloneCLI, dstDir); err != nil {
			return err
		}
	}
	return nil
}

// checkoutPlain performs a "git checkout <sha> -- ." into dstDir.
// When spec.Subdir is set, only the files under that subdirectory are
// moved to dstDir and everything else is discarded.
func (f *DefaultFetcher) checkoutPlain(
	ctx context.Context,
	spec FetchSpec,
	result FetchResult,
	bareCLI *gitCLI,
	dstDir string,
) error {
	// When a subdir is requested we need a staging area so we can then rename
	// just that subtree into dstDir.  Otherwise we work directly in dstDir.
	workDir := dstDir
	var stagingDir string
	if spec.Subdir != "" {
		var err error
		stagingDir, err = os.MkdirTemp(f.workDir, "gitapply-stage-*")
		if err != nil {
			return fmt.Errorf("gitapply: create staging dir: %w", err)
		}
		defer os.RemoveAll(stagingDir)
		workDir = stagingDir
	}

	checkoutCLI := bareCLI.with(withWorkTree(workDir))

	// Use the commit SHA (not the ref name) so the checkout is deterministic
	// even if the ref has moved on the remote.
	if _, err := checkoutCLI.run(ctx, "checkout", result.CommitSHA, "--", "."); err != nil {
		return fmt.Errorf("gitapply: checkout %s: %w", result.CommitSHA, err)
	}

	if spec.Subdir != "" {
		if err := extractSubdir(workDir, spec.Subdir, dstDir); err != nil {
			return err
		}
	}

	if !spec.SkipSubmodules {
		// git-submodule requires a .git marker in the process CWD to locate
		// the git directory.  For a plain checkout (KeepGitDir=false) there is
		// no .git in dstDir — the git dir lives in the temp bare repo.
		// We write a gitlink file (.git containing "gitdir: <bareDir>") to
		// satisfy the check, then remove it unconditionally after the update.
		//
		// This mirrors how git itself creates gitlink files for worktrees and
		// submodule checkouts.
		gitLinkPath := filepath.Join(dstDir, ".git")
		gitLinkContent := "gitdir: " + bareCLI.gitDir + "\n"
		if err := os.WriteFile(gitLinkPath, []byte(gitLinkContent), 0o644); err != nil {
			return fmt.Errorf("gitapply: write .git gitlink: %w", err)
		}
		submodCLI := bareCLI.with(withWorkTree(dstDir))
		submodErr := f.initSubmodules(ctx, submodCLI, dstDir)
		// Always remove the gitlink — even on error — so the checkout dir is
		// clean.  A stale gitlink could confuse subsequent git operations.
		if rmErr := os.Remove(gitLinkPath); rmErr != nil && !os.IsNotExist(rmErr) {
			if submodErr == nil {
				return fmt.Errorf("gitapply: remove .git gitlink: %w", rmErr)
			}
			// Prefer the submodule error; the gitlink will be cleaned up when
			// the caller removes dstDir on failure.
		}
		if submodErr != nil {
			return submodErr
		}
	}
	return nil
}

// extractSubdir moves the contents of subdir (relative to srcRoot) into dstDir.
// The source directory is removed after the move.
func extractSubdir(srcRoot, subdir, dstDir string) error {
	subdirPath := filepath.Join(srcRoot, filepath.FromSlash(subdir))
	d, err := os.Open(subdirPath)
	if err != nil {
		return fmt.Errorf("gitapply: open subdir %q: %w", subdir, err)
	}
	defer d.Close()

	entries, err := d.Readdirnames(0)
	if err != nil {
		return fmt.Errorf("gitapply: readdir subdir %q: %w", subdir, err)
	}
	for _, name := range entries {
		src := filepath.Join(subdirPath, name)
		dst := filepath.Join(dstDir, name)
		if err := os.Rename(src, dst); err != nil {
			return fmt.Errorf("gitapply: move %q to dstDir: %w", name, err)
		}
	}
	if err := d.Close(); err != nil {
		return fmt.Errorf("gitapply: close subdir: %w", err)
	}
	// Remove the now-empty staging root.
	if err := os.RemoveAll(srcRoot); err != nil {
		return fmt.Errorf("gitapply: remove staging dir: %w", err)
	}
	return nil
}

// initSubmodules initialises git submodules in workTree.
//
// It returns immediately (no error) when there is no .gitmodules file in
// workTree, because there are no submodules to initialise.  This avoids
// spawning git-submodule unnecessarily and is correct — a missing .gitmodules
// means the repository has no registered submodules.
//
// Platform note: git-submodule is a shell script (on macOS / Apple Git) or a
// Perl script (on most Linux distributions).  Either way, it detects the
// working tree by looking for a .git marker in the process working directory
// (cmd.Dir), not from the --git-dir / --work-tree flags passed to the parent
// git invocation.  We therefore derive a CLI with withDir(workTree) so that
// the script finds the correct .git on all platforms.
func (f *DefaultFetcher) initSubmodules(ctx context.Context, cli *gitCLI, workTree string) error {
	// Early exit: if .gitmodules does not exist there are no submodules.
	// This is a correctness check, not just an optimisation — calling
	// git-submodule on a repo with no .gitmodules is always a no-op, but on
	// some platforms the shell script still exits non-zero when it cannot
	// confirm a valid working tree.
	gitModulesPath := filepath.Join(workTree, ".gitmodules")
	if _, err := os.Stat(gitModulesPath); os.IsNotExist(err) {
		return nil
	}

	// Set the process CWD to workTree so git-submodule finds .git there.
	submodCLI := cli.with(withDir(workTree))
	if _, err := submodCLI.run(ctx,
		"submodule", "update", "--init", "--recursive", "--depth=1",
	); err != nil {
		return fmt.Errorf("gitapply: submodule update in %s: %w", workTree, err)
	}
	return nil
}

// postCheckoutCleanup removes FETCH_HEAD and expires the reflog to prevent
// later processes from reading the upstream URL or commit history.
//
// Security rationale:
//   - FETCH_HEAD contains the remote URL and the fetched SHA in plain text.
//   - The reflog can leak commit SHAs from branches that were never meant to
//     be visible in the resulting image.
func (f *DefaultFetcher) postCheckoutCleanup(
	ctx context.Context,
	cli *gitCLI,
	gitDirPath string,
) error {
	// Expire the reflog immediately so no history leaks.
	if _, err := cli.run(ctx, "reflog", "expire", "--all", "--expire=now"); err != nil {
		// Non-fatal; log and continue.
		_ = err
	}
	// Remove FETCH_HEAD so the upstream URL is not embedded in the .git dir.
	fetchHead := filepath.Join(gitDirPath, "FETCH_HEAD")
	if err := os.Remove(fetchHead); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("gitapply: remove FETCH_HEAD: %w", err)
	}
	return nil
}

// ─── Signature verification ───────────────────────────────────────────────────

// verifySignature fetches the raw git objects and delegates to the verifier.
func (f *DefaultFetcher) verifySignature(
	ctx context.Context,
	spec FetchSpec,
	result FetchResult,
	bareCLI *gitCLI,
) error {
	cfg := spec.SignatureVerify

	// Try tag signature first (unless IgnoreSignedTag is set).
	if !cfg.IgnoreSignedTag && result.TagSHA != "" {
		rawTag, err := bareCLI.run(ctx, "cat-file", "tag", result.TagSHA)
		if err != nil {
			return fmt.Errorf("gitapply: cat-file tag %s: %w", result.TagSHA, err)
		}
		tagErr := cfg.Verifier.VerifyTag(ctx, rawTag)
		if tagErr == nil {
			// Tag signature is valid; no need to check the commit.
			return nil
		}
		if cfg.RequireSignedTag {
			return fmt.Errorf("%w: tag %s: %v", ErrSignatureVerification, result.TagSHA, tagErr)
		}
		// Tag signature is present but invalid and RequireSignedTag is false;
		// fall through and attempt commit verification.
	}

	if cfg.RequireSignedTag && result.TagSHA == "" {
		return ErrNoSignedTag
	}

	// Verify the commit object.
	rawCommit, err := bareCLI.run(ctx, "cat-file", "commit", result.CommitSHA)
	if err != nil {
		return fmt.Errorf("gitapply: cat-file commit %s: %w", result.CommitSHA, err)
	}
	if err := cfg.Verifier.VerifyCommit(ctx, rawCommit); err != nil {
		return fmt.Errorf("%w: commit %s: %v", ErrSignatureVerification, result.CommitSHA, err)
	}
	return nil
}

// ─── Utilities ────────────────────────────────────────────────────────────────

// buildPullRef constructs the refspec to pass to "git fetch origin <refspec>"
// when doing a KeepGitDir clone from the local bare repo.
func buildPullRef(result FetchResult) string {
	if result.TagSHA != "" {
		// Annotated tag: fetch as a tag so the object is preserved.
		tagName := result.Ref
		if !strings.HasPrefix(tagName, "refs/tags/") {
			tagName = "refs/tags/" + tagName
		}
		return result.TagSHA + ":" + tagName
	}
	// Regular commit or lightweight tag.
	return result.CommitSHA
}

// fullyQualifiedRef ensures a ref is fully qualified (refs/heads/... or refs/tags/...).
// Bare commit SHAs are returned unchanged.
func fullyQualifiedRef(ref string) string {
	if strings.HasPrefix(ref, "refs/") || IsCommitSHA(ref) {
		return ref
	}
	// Heuristic: refs without a slash are branch names.
	return "refs/heads/" + ref
}
