package signing

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/bons/bons-ci/pkg/sigstore/internal/domain"
	"github.com/bons/bons-ci/pkg/sigstore/internal/keyprovider"
	"github.com/bons/bons-ci/pkg/sigstore/internal/observability"
)

// StaticKeySigner implements Signer using a key resolved from a KeyProvider.
// Use this for:
//   - Self-managed key pairs stored in a secret manager
//   - KMS-backed keys (GCP KMS, AWS KMS, HashiCorp Vault)
//   - Local dev / air-gapped environments
//
// The actual key material is never held in memory beyond the Sign call — each
// invocation fetches a fresh crypto.Signer from the KeyProvider, which may
// return a handle that delegates signing to KMS without ever exporting the key.
type StaticKeySigner struct {
	keyProvider keyprovider.KeyProvider
	rekorURL    string
	attachRekor bool
	log         *slog.Logger
	metrics     *observability.Metrics
}

// StaticKeySignerConfig bundles construction parameters.
type StaticKeySignerConfig struct {
	// KeyProvider resolves key material at signing time. Injected dependency.
	KeyProvider keyprovider.KeyProvider

	// RekorURL is optional; if set, signatures are anchored in the transparency log.
	RekorURL string

	// AttachToRekorByDefault sets the opt-in/opt-out default for Rekor attachment.
	// Can be overridden per-request via SignRequest.AttachToRekor.
	AttachToRekorByDefault bool

	Logger  *slog.Logger
	Metrics *observability.Metrics
}

// NewStaticKeySigner validates config and returns a ready-to-use StaticKeySigner.
func NewStaticKeySigner(cfg StaticKeySignerConfig) (*StaticKeySigner, error) {
	if cfg.KeyProvider == nil {
		return nil, fmt.Errorf("static key signer: KeyProvider is required")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &StaticKeySigner{
		keyProvider: cfg.KeyProvider,
		rekorURL:    cfg.RekorURL,
		attachRekor: cfg.AttachToRekorByDefault,
		log:         cfg.Logger,
		metrics:     cfg.Metrics,
	}, nil
}

// Sign resolves the key, signs the image digest, and optionally attaches to Rekor.
func (s *StaticKeySigner) Sign(ctx context.Context, req SignRequest) (domain.SigningResult, error) {
	start := time.Now()
	log := s.log.With("image_ref", req.ImageRef, "signer", "static_key", "key_hint", req.KeySpec.Name)

	log.Info("starting static-key signing flow")

	// ── Resolve key via injected KeyProvider ─────────────────────────────────
	cryptoSigner, err := s.keyProvider.GetSigner(ctx, req.KeySpec)
	if err != nil {
		return domain.SigningResult{}, fmt.Errorf("static key signer: resolve key %q: %w",
			req.KeySpec.Name, err)
	}

	// ── Sign the image digest ────────────────────────────────────────────────
	// PRODUCTION: use cosign with the crypto.Signer:
	//   sv, err := signature.LoadSigner(cryptoSigner, crypto.SHA256)
	//   ociRef, err := name.ParseReference(req.ImageRef)
	//   err = sign.SignCmd(ctx, sv, ociRef, ...)
	_ = cryptoSigner
	signatureRef := req.ImageRef + ".sig"

	// ── Optionally attach to Rekor ───────────────────────────────────────────
	var logIndex int64
	attachRekor := req.AttachToRekor || s.attachRekor
	if attachRekor && s.rekorURL != "" {
		logIndex, err = s.submitToRekor(ctx, req.ImageRef, signatureRef)
		if err != nil {
			return domain.SigningResult{}, &ErrRekorUnreachable{
				Cause: fmt.Errorf("static key sign rekor: %w", err),
			}
		}
	}

	result := domain.SigningResult{
		ImageRef:      req.ImageRef,
		SignatureRef:  signatureRef,
		RekorLogIndex: logIndex,
		SignedAt:      time.Now().UTC(),
	}

	if s.metrics != nil {
		s.metrics.SigningDuration.WithLabelValues("static_key", "success").
			Observe(time.Since(start).Seconds())
	}

	log.Info("static-key signing complete",
		"signature_ref", signatureRef,
		"duration_ms", time.Since(start).Milliseconds(),
	)

	return result, nil
}

func (s *StaticKeySigner) submitToRekor(_ context.Context, imageRef, sigRef string) (int64, error) {
	s.log.Debug("submitting to Rekor", "url", s.rekorURL, "image", imageRef, "sig", sigRef)
	// STUB — production: use rekor client SDK
	return 43, nil
}
