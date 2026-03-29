package gitapply

import (
	"errors"
	"fmt"
)

// Package-level sentinel errors.  Use errors.Is to test for these in calling
// code; the concrete types below carry additional diagnostic context.
var (
	// ErrGitNotFound is returned when the git binary is absent from PATH.
	ErrGitNotFound = errors.New("gitapply: git binary not found on PATH")

	// ErrInvalidRemote is returned when the remote URL is empty, uses an
	// unsupported transport, or embeds credentials that should be supplied
	// via AuthProvider instead.
	ErrInvalidRemote = errors.New("gitapply: invalid or unsupported remote URL")

	// ErrInvalidRef is returned when the ref string is structurally invalid.
	ErrInvalidRef = errors.New("gitapply: invalid ref")

	// ErrInvalidChecksum is returned when the checksum field is present but
	// is not a valid hex string of 7–64 characters.
	ErrInvalidChecksum = errors.New("gitapply: invalid checksum; expected 7–64 hex characters")

	// ErrChecksumMismatch is returned when the resolved commit SHA does not
	// have the expected checksum as a prefix.
	ErrChecksumMismatch = errors.New("gitapply: commit checksum mismatch")

	// ErrSubdirTraversal is returned when the subdir field contains ".."
	// components or is an absolute path.
	ErrSubdirTraversal = errors.New("gitapply: subdir must not escape the repository root")

	// ErrRefNotFound is returned when the remote does not advertise the
	// requested ref.
	ErrRefNotFound = errors.New("gitapply: ref not found in remote")

	// ErrSignatureVerification is returned when commit or tag signature
	// verification fails.
	ErrSignatureVerification = errors.New("gitapply: signature verification failed")

	// ErrNoSignedTag is returned when RequireSignedTag is set but the
	// resolved ref does not point to a signed annotated tag.
	ErrNoSignedTag = errors.New("gitapply: signed tag required but none found")
)

// ChecksumMismatchError carries the expected prefix and the full actual SHA so
// callers can surface meaningful diagnostics.
type ChecksumMismatchError struct {
	ExpectedPrefix string
	ActualSHA      string
	// AltSHA is the commit SHA pointed to by an annotated tag (non-empty only
	// when the ref resolves to a tag rather than a commit directly).
	AltSHA string
}

func (e *ChecksumMismatchError) Error() string {
	if e.AltSHA != "" {
		return fmt.Sprintf(
			"gitapply: commit checksum mismatch: expected prefix %q, got %q or %q",
			e.ExpectedPrefix, e.ActualSHA, e.AltSHA,
		)
	}
	return fmt.Sprintf(
		"gitapply: commit checksum mismatch: expected prefix %q, got %q",
		e.ExpectedPrefix, e.ActualSHA,
	)
}

// Is satisfies errors.Is so callers can write errors.Is(err, ErrChecksumMismatch).
func (e *ChecksumMismatchError) Is(target error) bool {
	return target == ErrChecksumMismatch
}

// FetchError wraps an underlying error and always carries a redacted remote URL
// so that auth tokens embedded in URLs never leak into log output.
type FetchError struct {
	// RedactedRemote is the remote URL with any credentials stripped.
	RedactedRemote string
	Cause          error
}

func (e *FetchError) Error() string {
	return fmt.Sprintf("gitapply: fetch from %q: %v", e.RedactedRemote, e.Cause)
}

func (e *FetchError) Unwrap() error { return e.Cause }

// newFetchError is a convenience constructor that redacts the URL automatically.
func newFetchError(remote string, cause error) *FetchError {
	return &FetchError{RedactedRemote: redactURL(remote), Cause: cause}
}

// gitExecError is returned when a git sub-process exits non-zero.
// The args slice has auth -c arguments replaced with "<redacted>".
type gitExecError struct {
	args   []string
	stderr string
	cause  error
}

func (e *gitExecError) Error() string {
	return fmt.Sprintf("git %v: %v\nstderr: %s", e.args, e.cause, e.stderr)
}

func (e *gitExecError) Unwrap() error { return e.cause }

// wouldClobberTagError and unableToUpdateRefError are sentinel wrapper types
// used internally to detect conditions that warrant a retry with a fresh bare
// repository.  They are never returned to callers directly.
type wouldClobberTagError struct{ cause error }

func (e *wouldClobberTagError) Error() string { return e.cause.Error() }
func (e *wouldClobberTagError) Unwrap() error { return e.cause }

type unableToUpdateRefError struct{ cause error }

func (e *unableToUpdateRefError) Error() string { return e.cause.Error() }
func (e *unableToUpdateRefError) Unwrap() error { return e.cause }

// classifyFetchError maps known git stderr substrings to typed errors so that
// [DefaultFetcher] can decide whether a retry is warranted.
func classifyFetchError(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if contains(msg, "rejected") && contains(msg, "(would clobber existing tag)") {
		return &wouldClobberTagError{cause: err}
	}
	if contains(msg, "some local refs could not be updated") ||
		contains(msg, "unable to update local ref") ||
		contains(msg, "refname conflict") {
		return &unableToUpdateRefError{cause: err}
	}
	return err
}

func contains(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) &&
		func() bool {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}()
}
