package gitapply

import "context"

// SignatureVerifier verifies the cryptographic signature attached to a raw git
// object.  Implementations can wrap GPG, OpenSSH, X.509, or any other signing
// mechanism.
//
// Both methods receive the raw bytes of the object exactly as returned by
// "git cat-file (commit|tag) <sha>".  The signature is embedded in the object
// in the standard gpgsig / gpgsig-sha256 trailer format.
//
// Return nil to indicate a valid signature; return a wrapped [ErrSignatureVerification]
// (or any error) to indicate failure.
type SignatureVerifier interface {
	// VerifyCommit verifies the signature on a raw commit object.
	VerifyCommit(ctx context.Context, rawObject []byte) error

	// VerifyTag verifies the signature on a raw annotated tag object.
	VerifyTag(ctx context.Context, rawObject []byte) error
}

// SignatureVerifyConfig controls when verification is required and how
// tag vs. commit objects are prioritised.
type SignatureVerifyConfig struct {
	// Verifier performs the actual cryptographic check.  Required.
	Verifier SignatureVerifier

	// RequireSignedTag, when true, requires that the resolved ref points to
	// a signed annotated tag.  If no signed tag is present (or its signature
	// is invalid), Fetch fails with [ErrNoSignedTag] or [ErrSignatureVerification].
	// Has no effect when the ref resolves directly to a commit.
	RequireSignedTag bool

	// IgnoreSignedTag, when true, always verifies the commit object's signature
	// even when a valid signed tag is found.  Normally a valid tag signature
	// is sufficient and the commit signature is not checked.
	IgnoreSignedTag bool
}

// NoSignatureVerifier is a [SignatureVerifier] that always returns nil.
// Use in tests or when signature verification is not required.
type NoSignatureVerifier struct{}

var _ SignatureVerifier = NoSignatureVerifier{}

func (NoSignatureVerifier) VerifyCommit(_ context.Context, _ []byte) error { return nil }
func (NoSignatureVerifier) VerifyTag(_ context.Context, _ []byte) error    { return nil }
