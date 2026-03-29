// Package httpapplier implements a secure, high-performance HTTP content
// fetcher that satisfies the containerd diff.Applier contract.
//
// Design goals:
//   - Mirror buildkit's security posture (credential redaction, PGP verify,
//     digest pinning, safe filenames, restricted headers).
//   - Add defence-in-depth: TLS-only by default, max response size, read
//     deadlines, no directory traversal, fsync before commit.
//   - Expose two clean interfaces so callers can plug in custom fetch, verify,
//     and unpack logic without touching this package.
//   - Bridge to containerd's diff.Applier via a thin adaptor so existing
//     containerd tooling keeps working unchanged.
package httpapplier

import (
	"context"
	"io"
	"net/http"
	"time"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/containerd/containerd/v2/core/mount"
)

// ─── Primary interface ────────────────────────────────────────────────────────

// HTTPFetcher downloads a remote resource described by FetchRequest and writes
// the raw bytes to the supplied writer. It is the single unit of work that
// callers must implement (or reuse the default) to customise network behaviour.
//
// Implementations MUST:
//   - Honour ctx cancellation / deadline.
//   - Reject non-HTTPS schemes unless explicitly allowed via options.
//   - Verify the response digest (if provided in the request) before returning.
//   - Never follow redirects to non-HTTPS destinations.
//
// The returned FetchResult carries the verified digest and final filename so
// upper layers can make caching decisions without re-reading the writer.
type HTTPFetcher interface {
	Fetch(ctx context.Context, req FetchRequest, dst io.Writer) (FetchResult, error)
}

// UnpackOptions passes metadata for single-file unpacking (application/octet-stream).
// Ignored for archive media types, which define their own internal metadata.
type UnpackOptions struct {
	Filename string
	MTime    *time.Time
	UID      *int
	GID      *int
	Perm     *int // expected to be os.FileMode
}

// Unpacker knows how to apply a raw byte stream on top of a set of mounts.
// The default implementation handles tar / tar+gzip / tar+zstd content; callers
// can supply their own for exotic formats (e.g. squashfs, erofs).
type Unpacker interface {
	Unpack(ctx context.Context, src io.Reader, mediaType string, mounts []mount.Mount, opts UnpackOptions) error
}

// SignatureVerifier validates an opaque digital signature against the fetched
// payload. The buildkit project ships PGP; callers can plug in cosign, sigstore
// transparency-log checks, etc.
type SignatureVerifier interface {
	// Verify is called with the already-downloaded file path, the raw signature
	// bytes, and any extra verifier-specific options.
	Verify(ctx context.Context, filePath string, sig []byte, opts VerifyOptions) error
}

// ─── Containerd bridge interface ──────────────────────────────────────────────

// ApplyOpt is a functional option for Apply, matching containerd's convention.
type ApplyOpt func(*ApplyConfig)

// ApplyConfig is the resolved set of options passed to Apply internals.
type ApplyConfig struct {
	// ParentDigests are the layers already present in the mount stack.
	// Used to construct the correct diff-id chain.
	ParentDigests []digest.Digest

	// Labels are propagated to the returned descriptor as annotations.
	Labels map[string]string

	// ProcessorFunc, if non-nil, wraps the raw reader before unpacking.
	// Useful for on-the-fly decryption or decompression pre-processing.
	ProcessorFunc func(r io.Reader) (io.Reader, error)
}

// HTTPApplier is the primary interface exported by this package.
// It mirrors containerd's diff.Applier but is specialised for HTTP sources.
//
// The descriptor's URLs field carries the remote addresses; the Digest field
// is the content digest used for pinned verification.
type HTTPApplier interface {
	// Apply fetches the content described by desc, verifies it, unpacks it onto
	// mounts, and returns a new descriptor whose digest reflects the unpacked
	// diff-id (not the compressed digest).
	Apply(ctx context.Context, desc ocispec.Descriptor, mounts []mount.Mount, opts ...ApplyOpt) (ocispec.Descriptor, error)
}

// ContainerdApplierAdaptor bridges HTTPApplier to the containerd diff.Applier
// signature so this package can be dropped into any containerd-based runtime
// (BuildKit, nerdctl, Moby) without modification.
//
// Usage:
//
//	httpApp := httpapplier.New(httpapplier.Options{...})
//	ctdApp  := httpapplier.NewContainerdAdaptor(httpApp)
//	// ctdApp now satisfies diff.Applier
type ContainerdApplierAdaptor interface {
	// Apply satisfies containerd/diff.Applier.
	Apply(ctx context.Context, desc ocispec.Descriptor, mounts []mount.Mount, opts ...ApplyOpt) (ocispec.Descriptor, error)
}

// ─── Value types ──────────────────────────────────────────────────────────────

// FetchRequest is a fully-validated, immutable description of one HTTP fetch.
// All fields that influence cache keys or security decisions are explicit here
// rather than hidden inside options maps (compare buildkit's HTTPIdentifier).
type FetchRequest struct {
	// URL is the validated, credential-redacted fetch target.
	// Only https:// is accepted unless InsecureAllowHTTP is set in Options.
	URL string

	// PinnedDigest, if non-zero, is asserted against the downloaded bytes.
	// The fetch is aborted with ErrDigestMismatch if they diverge.
	PinnedDigest digest.Digest

	// Filename overrides the server-derived filename for the saved file.
	Filename string

	// AuthHeaderSecret is the name of the secret whose value is used verbatim
	// as the Authorization header (mirrors buildkit's AttrHTTPAuthHeaderSecret).
	AuthHeaderSecret string

	// ExtraHeaders are additional request headers whitelisted by the caller
	// (only Accept and User-Agent pass the default allowlist).
	ExtraHeaders []HeaderField

	// Signature, if non-nil, triggers PGP signature verification after fetch.
	Signature *SignatureOptions

	// MaxBytes caps the response body. Zero means DefaultMaxBodyBytes.
	MaxBytes int64

	// ReadTimeout overrides the per-read deadline. Zero → DefaultReadTimeout.
	ReadTimeout time.Duration

	// ChecksumRequest, if non-nil, requests a secondary hash of (body||Suffix).
	ChecksumRequest *ChecksumRequest
}

// FetchResult is returned by HTTPFetcher.Fetch after a successful, verified fetch.
type FetchResult struct {
	// Digest is the sha256 of the raw downloaded bytes (before unpacking).
	Digest digest.Digest

	// Filename is the final filename written to disk (server or manual).
	Filename string

	// LastModified is the parsed Last-Modified response header, if present.
	LastModified *time.Time

	// ChecksumResponse is populated when FetchRequest.ChecksumRequest != nil.
	ChecksumResponse *ChecksumResponse
}

// HeaderField is a single HTTP header name/value pair.  Names are always stored
// in canonical form (http.CanonicalHeaderKey).
type HeaderField struct {
	Name  string
	Value string
}

// SignatureOptions carries the PGP material needed for detached-signature
// verification (mirrors buildkit's HTTPSignatureVerifyOptions).
type SignatureOptions struct {
	// ArmoredPubKey is one or more concatenated ASCII-armored public key blocks.
	ArmoredPubKey []byte
	// ArmoredSignature is the detached ASCII-armored signature to verify.
	ArmoredSignature []byte
}

// VerifyOptions is passed to SignatureVerifier.Verify so implementations can
// carry verifier-specific state without polluting FetchRequest.
type VerifyOptions struct {
	// PubKey and Signature mirror SignatureOptions; extracted here so
	// SignatureVerifier is independent of FetchRequest.
	PubKey    []byte
	Signature []byte
}

// ChecksumRequest asks the fetcher to compute hash(body || Suffix) using Algo.
type ChecksumRequest struct {
	Algo   ChecksumAlgo
	Suffix []byte
}

// ChecksumResponse is the result of a ChecksumRequest.
type ChecksumResponse struct {
	// DigestString is the hex-encoded algo:hash string (e.g. "sha256:abc…").
	DigestString string
	// Suffix echoes back the suffix that was appended before hashing.
	Suffix []byte
}

// ChecksumAlgo enumerates the supported secondary hash algorithms.
type ChecksumAlgo int

const (
	ChecksumAlgoSHA256 ChecksumAlgo = iota
	ChecksumAlgoSHA384
	ChecksumAlgoSHA512
)

// ─── Sentinel errors ──────────────────────────────────────────────────────────

// Well-typed sentinel errors allow callers to inspect failures without
// string matching, matching Go's errors.Is / errors.As idiom.

// ErrDigestMismatch is returned when the downloaded digest differs from the
// pinned digest in FetchRequest.
type ErrDigestMismatch struct {
	URL      string
	Got      digest.Digest
	Expected digest.Digest
}

func (e *ErrDigestMismatch) Error() string {
	return "digest mismatch for " + e.URL + ": got " + e.Got.String() + " expected " + e.Expected.String()
}

// ErrInsecureScheme is returned when a non-HTTPS URL is rejected.
type ErrInsecureScheme struct{ URL string }

func (e *ErrInsecureScheme) Error() string {
	return "insecure scheme rejected for " + e.URL + " (only https allowed)"
}

// ErrBodyTooLarge is returned when the response body exceeds MaxBytes.
type ErrBodyTooLarge struct {
	URL   string
	Limit int64
}

func (e *ErrBodyTooLarge) Error() string {
	return "response body exceeds limit for " + e.URL
}

// ErrHTTPStatus wraps non-2xx responses.
type ErrHTTPStatus struct {
	URL    string
	Status int
}

func (e *ErrHTTPStatus) Error() string {
	return "unexpected HTTP status " + http.StatusText(e.Status) + " for " + e.URL
}
