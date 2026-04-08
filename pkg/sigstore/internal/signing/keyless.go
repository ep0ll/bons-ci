package signing

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"fmt"
	"log/slog"
	"time"

	"github.com/bons/bons-ci/pkg/sigstore/internal/domain"
	"github.com/bons/bons-ci/pkg/sigstore/internal/observability"
)

// KeylessSignerConfig holds all external dependencies for the keyless flow.
// All URLs are injected — no hardcoded Sigstore production URLs in code.
type KeylessSignerConfig struct {
	// FulcioURL is the Certificate Authority endpoint.
	// Production: https://fulcio.sigstore.dev
	FulcioURL string

	// RekorURL is the transparency log endpoint.
	// Production: https://rekor.sigstore.dev
	RekorURL string

	// OIDCIssuer is the issuer URL for workload identity tokens.
	// e.g. https://accounts.google.com, https://token.actions.githubusercontent.com
	OIDCIssuer string

	// OIDCClientID is the audience for the OIDC token request.
	OIDCClientID string

	Logger  *slog.Logger
	Metrics *observability.Metrics
}

// KeylessSigner implements Signer using the Sigstore keyless flow:
//
//  1. Generate ephemeral ECDSA key pair in memory
//  2. Request OIDC token from the configured issuer (workload identity)
//  3. Submit CSR + OIDC token to Fulcio → receive short-lived cert chain
//  4. Sign the image digest with the ephemeral key
//  5. Attach signature + cert chain to Rekor transparency log
//  6. Push signature and cert to the OCI registry
//
// The ephemeral key never touches disk; cert lifetime is ~10 minutes.
// This is the recommended flow for CI/CD environments (GitHub Actions, GKE WI).
//
// Trade-off: requires network access to Fulcio and Rekor. For air-gapped
// environments, swap for StaticKeySigner or KMSSigner.
type KeylessSigner struct {
	cfg KeylessSignerConfig
}

// NewKeylessSigner constructs a KeylessSigner. Validation is performed at
// construction time so misconfigured services fail fast at startup.
func NewKeylessSigner(cfg KeylessSignerConfig) (*KeylessSigner, error) {
	if cfg.FulcioURL == "" {
		return nil, fmt.Errorf("keyless signer: FulcioURL is required")
	}
	if cfg.RekorURL == "" {
		return nil, fmt.Errorf("keyless signer: RekorURL is required")
	}
	if cfg.OIDCIssuer == "" {
		return nil, fmt.Errorf("keyless signer: OIDCIssuer is required")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &KeylessSigner{cfg: cfg}, nil
}

// Sign executes the keyless signing flow.
//
// NOTE: The Sigstore/Cosign library calls are shown as logical steps with
// clear comments. In production, replace the stub calls with:
//
//	import (
//	    "github.com/sigstore/cosign/v2/cmd/cosign/cli/sign"
//	    "github.com/sigstore/cosign/v2/pkg/cosign"
//	    "github.com/sigstore/cosign/v2/pkg/oci/remote"
//	    "github.com/sigstore/fulcio/pkg/api"
//	)
//
// The interface contract remains unchanged — callers are fully insulated.
func (s *KeylessSigner) Sign(ctx context.Context, req SignRequest) (domain.SigningResult, error) {
	start := time.Now()
	log := s.cfg.Logger.With("image_ref", req.ImageRef, "signer", "keyless")

	log.Info("starting keyless signing flow")

	// ── Step 1: Generate ephemeral key pair ──────────────────────────────────
	privateKey, err := s.generateEphemeralKey(ctx)
	if err != nil {
		return domain.SigningResult{}, fmt.Errorf("keyless sign ephemeral key: %w", err)
	}

	// ── Step 2: Obtain OIDC token (workload identity) ────────────────────────
	oidcToken, err := s.fetchOIDCToken(ctx)
	if err != nil {
		return domain.SigningResult{}, fmt.Errorf("keyless sign oidc token: %w", err)
	}

	// ── Step 3: Request certificate from Fulcio ──────────────────────────────
	certChain, err := s.requestFulcioCert(ctx, privateKey.Public(), oidcToken)
	if err != nil {
		return domain.SigningResult{}, &ErrFulcioUnreachable{
			Cause: fmt.Errorf("fulcio cert request: %w", err),
		}
	}

	// ── Step 4: Sign the image digest ────────────────────────────────────────
	signatureRef, err := s.signDigest(ctx, req, privateKey, certChain)
	if err != nil {
		return domain.SigningResult{}, &ErrSigningFailed{
			ImageRef: req.ImageRef,
			Cause:    fmt.Errorf("signing digest: %w", err),
		}
	}

	// ── Step 5: Submit to Rekor transparency log ─────────────────────────────
	var logIndex int64
	if req.AttachToRekor {
		logIndex, err = s.attachToRekor(ctx, req.ImageRef, signatureRef, certChain)
		if err != nil {
			// Rekor failure is retryable but does not invalidate the signature.
			// Log and propagate; the resilience policy decides whether to retry.
			log.Warn("rekor attachment failed", "error", err)
			return domain.SigningResult{}, &ErrRekorUnreachable{
				Cause: fmt.Errorf("rekor attachment: %w", err),
			}
		}
	}

	result := domain.SigningResult{
		ImageRef:      req.ImageRef,
		SignatureRef:  signatureRef,
		CertChain:     certChain,
		RekorLogIndex: logIndex,
		SignedAt:      time.Now().UTC(),
	}

	if s.cfg.Metrics != nil {
		s.cfg.Metrics.SigningDuration.WithLabelValues("keyless", "success").
			Observe(time.Since(start).Seconds())
	}

	log.Info("keyless signing complete",
		"signature_ref", signatureRef,
		"rekor_index", logIndex,
		"duration_ms", time.Since(start).Milliseconds(),
	)

	return result, nil
}

// generateEphemeralKey creates a P-256 key pair. P-256 is the curve mandated
// by the Sigstore specification for keyless signing.
func (s *KeylessSigner) generateEphemeralKey(_ context.Context) (*ecdsa.PrivateKey, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate ephemeral P-256 key: %w", err)
	}
	return key, nil
}

// fetchOIDCToken retrieves a workload identity token from the OIDC issuer.
// In production this calls the metadata server (GKE, AWS IMDS) or reads a
// projected service account token (Kubernetes).
//
// PRODUCTION: replace with actual OIDC provider SDK call:
//
//	github.com/sigstore/sigstore/pkg/oauthflow
func (s *KeylessSigner) fetchOIDCToken(_ context.Context) (string, error) {
	// STUB: returns a placeholder. Replace with real OIDC token fetch.
	s.cfg.Logger.Debug("fetching OIDC token", "issuer", s.cfg.OIDCIssuer)
	return "stub-oidc-token", nil
}

// requestFulcioCert submits a CSR and OIDC proof to Fulcio and returns the
// PEM-encoded short-lived certificate chain.
//
// PRODUCTION: replace with:
//
//	fulcioClient := api.NewClient(s.cfg.FulcioURL, api.WithUserAgent("signing-service/1.0"))
//	certResp, err := fulcioClient.SigningCert(ctx, api.CertificateRequest{...}, token)
func (s *KeylessSigner) requestFulcioCert(_ context.Context, _ crypto.PublicKey, _ string) (string, error) {
	s.cfg.Logger.Debug("requesting Fulcio certificate", "url", s.cfg.FulcioURL)
	// STUB: return placeholder PEM
	return "-----BEGIN CERTIFICATE-----\nSTUB\n-----END CERTIFICATE-----\n", nil
}

// signDigest signs the image digest using the ephemeral key and cert chain,
// then pushes the signature to the OCI registry co-located with the image.
//
// PRODUCTION: replace with cosign OCI signing:
//
//	ref, err := name.ParseReference(req.ImageRef)
//	sv, err := signature.LoadECDSASignerVerifier(privateKey, crypto.SHA256)
//	ociSig, err := static.NewSignature(payload, b64sig, static.WithCertChain(cert, chain))
//	err = remote.WriteSignature(ref, ociSig, remote.WithAuthFromKeychain(authn.DefaultKeychain))
func (s *KeylessSigner) signDigest(_ context.Context, req SignRequest, _ *ecdsa.PrivateKey, _ string) (string, error) {
	s.cfg.Logger.Debug("signing image digest", "image_ref", req.ImageRef)
	// STUB: return placeholder signature ref
	return req.ImageRef + ".sig", nil
}

// attachToRekor submits the signature bundle to the Rekor transparency log
// and returns the assigned log index.
//
// PRODUCTION: replace with:
//
//	rekorClient := gclient.NewHTTPClientWithConfig(nil, gclient.DefaultTransportConfig().WithHost(rekorHost))
//	entry, err := rekor.TLogUpload(ctx, rekorClient, sig, signedPayload, pemCert)
func (s *KeylessSigner) attachToRekor(_ context.Context, imageRef, sigRef, certChain string) (int64, error) {
	s.cfg.Logger.Debug("attaching to Rekor",
		"url", s.cfg.RekorURL,
		"image_ref", imageRef,
		"sig_ref", sigRef,
	)
	_ = certChain
	// STUB: return placeholder log index
	return 42, nil
}
