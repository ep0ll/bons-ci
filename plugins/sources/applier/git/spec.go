package gitapply

import (
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
)

// OCI descriptor annotation keys used to encode git fetch parameters.
// These follow containerd's snapshot annotation convention.
const (
	AnnotationRemote           = "containerd.io/snapshot/git/remote"
	AnnotationRef              = "containerd.io/snapshot/git/ref"
	AnnotationChecksum         = "containerd.io/snapshot/git/checksum"
	AnnotationSubdir           = "containerd.io/snapshot/git/subdir"
	AnnotationKeepGitDir       = "containerd.io/snapshot/git/keep-git-dir"
	AnnotationSkipSubmodules   = "containerd.io/snapshot/git/skip-submodules"
	AnnotationAuthTokenSecret  = "containerd.io/snapshot/git/auth-token-secret"
	AnnotationAuthHeaderSecret = "containerd.io/snapshot/git/auth-header-secret"
	AnnotationSSHSocketID      = "containerd.io/snapshot/git/ssh-socket-id"
	AnnotationKnownSSHHosts    = "containerd.io/snapshot/git/known-ssh-hosts"
)

// commitSHAPattern matches a full (not abbreviated) hex commit SHA.
// Both SHA-1 (40 chars) and SHA-256 (64 chars) are accepted.
var commitSHAPattern = regexp.MustCompile(`^[a-fA-F0-9]{40}([a-fA-F0-9]{24})?$`)

// checksumPattern matches a valid checksum prefix — at least 7 hex chars.
var checksumPattern = regexp.MustCompile(`^[a-fA-F0-9]{7,64}$`)

// FetchSpec fully describes a git content fetch operation.
// It is transport-agnostic: the same struct is used for HTTPS, SSH, and
// local file:// remotes.
type FetchSpec struct {
	// Remote is the full git transport URL (https://, ssh://, git://, git@...).
	// Required.  Must not embed credentials (user:pass@host) — use AuthTokenSecret
	// or AuthHeaderSecret instead.
	Remote string

	// Ref is the git ref to resolve: a branch name, tag name, or a full
	// refs/... path.  If empty the remote's default branch is used.
	// A bare commit SHA is also accepted; it bypasses ref resolution and
	// triggers an unshallow fetch if needed.
	Ref string

	// Checksum is an optional expected commit-SHA prefix (at least 7 hex chars).
	// When set the resolved commit must have this value as a prefix; a mismatch
	// causes Fetch to fail with [ErrChecksumMismatch].
	// Useful for pinning a mutable ref (branch / tag) to a specific commit.
	Checksum string

	// Subdir is an optional sub-directory within the repository to expose at
	// dstDir.  Only files under this path will be visible after checkout.
	// Must not contain ".." components or an absolute path — validated by [Validate].
	Subdir string

	// KeepGitDir, when true, includes a .git directory in the checkout so the
	// result is a valid git working tree.  The local clone uses the file://
	// protocol to defeat local-clone optimisations that could copy unintended
	// host data into the build context.
	KeepGitDir bool

	// SkipSubmodules, when true, skips "git submodule update --init".
	// Useful for repositories that use submodules pointing at private remotes
	// that are not accessible in the fetch environment.
	SkipSubmodules bool

	// AuthTokenSecret names the secret that holds a Personal Access Token or
	// similar bearer token for HTTPS remotes.  How the name is resolved into
	// a token value is the responsibility of the [AuthProvider].
	AuthTokenSecret string

	// AuthHeaderSecret names the secret holding a complete Authorization header
	// value for HTTPS remotes (e.g. "basic <base64>" or "bearer <token>").
	// Takes precedence over AuthTokenSecret when both are set.
	AuthHeaderSecret string

	// SSHSocketID names the SSH agent socket to mount for SSH remotes.
	// How the ID is resolved into a socket path is up to the [AuthProvider].
	SSHSocketID string

	// KnownSSHHosts is the literal content of a known_hosts file used to pin
	// the server's host key for SSH remotes.
	// If empty and SSHSocketID is set, git will still refuse connections to
	// unknown hosts because StrictHostKeyChecking=yes is always enforced.
	KnownSSHHosts string

	// SignatureVerify, when non-nil, verifies the commit or tag signature
	// after fetching.  A verification failure aborts the Fetch with
	// [ErrSignatureVerification].
	SignatureVerify *SignatureVerifyConfig
}

// Validate checks that the FetchSpec is well-formed and safe to use.
// [DefaultFetcher.Fetch] calls this automatically before doing any work.
func (s FetchSpec) Validate() error {
	if s.Remote == "" {
		return ErrInvalidRemote
	}
	if !isGitTransport(s.Remote) {
		return fmt.Errorf("%w: %q", ErrInvalidRemote, redactURL(s.Remote))
	}
	if s.Checksum != "" && !checksumPattern.MatchString(s.Checksum) {
		return ErrInvalidChecksum
	}
	if err := validateSubdir(s.Subdir); err != nil {
		return err
	}
	return nil
}

// isGitTransport reports whether rawURL uses a recognised git transport scheme.
// Accepted: https://, http://, ssh://, git://, file://, git@<host>: (SCP syntax).
//
// file:// is included because:
//   - It is a standard git transport for local repositories.
//   - It is used internally by checkoutWithGitDir to clone from the temp bare
//     repo, deliberately disabling hard-link optimisations that can leak host
//     files when using plain directory paths.
//   - Tests rely on local file:// remotes to avoid network access.
func isGitTransport(rawURL string) bool {
	for _, pfx := range []string{"https://", "http://", "ssh://", "git://", "file://"} {
		if strings.HasPrefix(rawURL, pfx) {
			return true
		}
	}
	// SCP-style SSH: git@github.com:user/repo.git
	if idx := strings.Index(rawURL, "@"); idx != -1 {
		rest := rawURL[idx+1:]
		if strings.Contains(rest, ":") && !strings.Contains(rest, "://") {
			return true
		}
	}
	return false
}

// validateSubdir rejects subdir paths that could traverse outside the repository root.
func validateSubdir(subdir string) error {
	if subdir == "" {
		return nil
	}
	cleaned := path.Clean(subdir)
	if path.IsAbs(cleaned) {
		return fmt.Errorf("%w: absolute path %q", ErrSubdirTraversal, subdir)
	}
	for _, part := range strings.Split(cleaned, "/") {
		if part == ".." {
			return fmt.Errorf("%w: %q", ErrSubdirTraversal, subdir)
		}
	}
	return nil
}

// redactURL returns the URL with any embedded credentials (user:pass@) removed.
// Safe to use in error messages and logs.
func redactURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		// Not a standard URL (e.g. git@host:path); return as-is because
		// SCP-style URLs do not carry passwords.
		return rawURL
	}
	u.User = nil
	return u.String()
}

// IsCommitSHA reports whether s is a full (non-abbreviated) commit SHA.
func IsCommitSHA(s string) bool {
	return commitSHAPattern.MatchString(s)
}

// FetchResult describes what was actually fetched and checked out.
type FetchResult struct {
	// CommitSHA is the full (40- or 64-char) hex SHA of the commit that was
	// checked out.  This is always a commit object, never a tag object.
	CommitSHA string

	// Ref is the fully-qualified ref that was resolved (e.g. refs/heads/main,
	// refs/tags/v1.0.0).  Empty when the spec named a bare commit SHA.
	Ref string

	// TagSHA is the SHA of the annotated tag object, when the resolved ref
	// pointed to an annotated tag rather than a commit directly.
	// Empty for lightweight tags and branch refs.
	TagSHA string
}
