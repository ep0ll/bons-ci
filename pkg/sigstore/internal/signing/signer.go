// Package signing defines the Signer interface and all concrete implementations.
//
// Interface Segregation: Signer is narrow — one method, one responsibility.
// Callers never need to know whether they're talking to Cosign keyless,
// a static key, or a KMS-backed signer. Swap by injecting a different
// implementation at bootstrap time.
//
// Sigstore abstraction layer:
//   - KeylessSigner   → Cosign keyless flow (OIDC → Fulcio cert → Rekor log)
//   - StaticKeySigner → Cosign with a pre-loaded crypto.Signer
//   - KMSSigner       → Cosign with a cloud KMS key (GCP/AWS/Azure)
//
// To add a new backend: implement Signer and register it in bootstrap/wire.go.
// Zero changes to core service logic required.
package signing

import (
	"context"

	"github.com/bons/bons-ci/pkg/sigstore/internal/domain"
)

// Signer is the single-method interface every signing backend must satisfy.
//
// Contract:
//   - Sign must be idempotent with respect to the image digest: signing the
//     same digest twice must not produce conflicting log entries.
//   - Sign must respect ctx cancellation and deadline.
//   - All errors must be wrapped with fmt.Errorf("...: %w", err) so callers
//     can use errors.Is / errors.As for resilience decisions.
type Signer interface {
	Sign(ctx context.Context, req SignRequest) (domain.SigningResult, error)
}

// SignRequest carries all inputs needed to sign one image.
// Keeping this as a value type (not pointer) makes the API immutable at call sites.
type SignRequest struct {
	// ImageRef is the fully-qualified image reference including digest.
	// Digest pinning is mandatory for supply-chain integrity.
	// Example: "registry.example.com/app@sha256:abc123..."
	ImageRef string

	// KeySpec selects the key or key-flow to use.
	// Zero value → keyless signing via Fulcio.
	KeySpec domain.KeySpec

	// Annotations are added as OCI image annotations on the signature.
	Annotations map[string]string

	// AttachToRekor controls whether to submit to the Rekor transparency log.
	// Defaults to true in the keyless flow; opt-out for air-gapped environments.
	AttachToRekor bool
}

// VerifyRequest carries inputs for a signature verification call.
type VerifyRequest struct {
	ImageRef    string
	KeySpec     domain.KeySpec
	CertChain   string // optional: expected cert chain PEM
	Annotations map[string]string
}

// Verifier is intentionally segregated from Signer: not all deployments need
// inline verification, and mixing concerns leads to god objects.
type Verifier interface {
	Verify(ctx context.Context, req VerifyRequest) error
}

// SignerVerifier is a convenience composite for deployments that need both.
type SignerVerifier interface {
	Signer
	Verifier
}

// --- sentinel errors --------------------------------------------------------

// ErrSigningFailed wraps the root cause with signing-specific context.
type ErrSigningFailed struct {
	ImageRef string
	Cause    error
}

func (e *ErrSigningFailed) Error() string {
	return "signing failed for " + e.ImageRef + ": " + e.Cause.Error()
}
func (e *ErrSigningFailed) Unwrap() error { return e.Cause }

// ErrRekorUnreachable indicates the transparency log is unreachable.
// The resilience policy treats this as retryable.
type ErrRekorUnreachable struct{ Cause error }

func (e *ErrRekorUnreachable) Error() string {
	return "rekor unreachable: " + e.Cause.Error()
}
func (e *ErrRekorUnreachable) Unwrap() error { return e.Cause }

// ErrFulcioUnreachable indicates the certificate authority is unreachable.
type ErrFulcioUnreachable struct{ Cause error }

func (e *ErrFulcioUnreachable) Error() string {
	return "fulcio unreachable: " + e.Cause.Error()
}
func (e *ErrFulcioUnreachable) Unwrap() error { return e.Cause }
