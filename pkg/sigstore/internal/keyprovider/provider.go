// Package keyprovider defines the KeyProvider interface and concrete
// implementations for static keys, KMS, and environment-based key loading.
//
// Dependency Inversion: the signing layer depends on this abstraction, not
// on any concrete key backend. Swap backends at bootstrap without modifying
// any other package.
package keyprovider

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"

	"github.com/bons/bons-ci/pkg/sigstore/internal/domain"
)

// KeyProvider resolves a crypto.Signer for a given KeySpec.
// The returned Signer may be:
//   - An in-process ECDSA/RSA key (StaticKeyProvider)
//   - A handle whose Sign method proxies to a remote KMS (KMSKeyProvider)
//
// Implementations must be safe for concurrent use and must not cache key
// material beyond what the underlying backend allows (KMS handles state).
type KeyProvider interface {
	// GetSigner returns a crypto.Signer for the given spec.
	// Returns ErrKeyNotFound if the spec cannot be resolved.
	GetSigner(ctx context.Context, spec domain.KeySpec) (crypto.Signer, error)

	// PublicKey returns just the public key for verification use cases.
	// Avoids unnecessary access to signing credentials for read-only operations.
	PublicKey(ctx context.Context, spec domain.KeySpec) (crypto.PublicKey, error)
}

// ErrKeyNotFound is returned when no key matches the given KeySpec.
type ErrKeyNotFound struct{ Name string }

func (e *ErrKeyNotFound) Error() string { return "key not found: " + e.Name }

// --- StaticKeyProvider ──────────────────────────────────────────────────────

// KeyEntry is a named key registered with StaticKeyProvider.
type KeyEntry struct {
	Name   string
	Signer crypto.Signer // loaded at startup; never replaced
}

// StaticKeyProvider holds a fixed map of named crypto.Signers.
// Suitable for: dev, CI with pre-baked secrets, environments without KMS.
//
// Keys are loaded once at construction (fail-fast pattern) so the service
// refuses to start with invalid key material rather than failing at sign time.
type StaticKeyProvider struct {
	keys map[string]crypto.Signer // immutable after construction
}

// NewStaticKeyProvider validates and stores the provided entries.
func NewStaticKeyProvider(entries []KeyEntry) (*StaticKeyProvider, error) {
	if len(entries) == 0 {
		return nil, fmt.Errorf("static key provider: at least one entry required")
	}
	m := make(map[string]crypto.Signer, len(entries))
	for _, e := range entries {
		if e.Name == "" {
			return nil, fmt.Errorf("static key provider: entry has empty name")
		}
		if e.Signer == nil {
			return nil, fmt.Errorf("static key provider: entry %q has nil signer", e.Name)
		}
		m[e.Name] = e.Signer
	}
	return &StaticKeyProvider{keys: m}, nil
}

func (p *StaticKeyProvider) GetSigner(_ context.Context, spec domain.KeySpec) (crypto.Signer, error) {
	s, ok := p.keys[spec.Name]
	if !ok {
		return nil, &ErrKeyNotFound{Name: spec.Name}
	}
	return s, nil
}

func (p *StaticKeyProvider) PublicKey(ctx context.Context, spec domain.KeySpec) (crypto.PublicKey, error) {
	s, err := p.GetSigner(ctx, spec)
	if err != nil {
		return nil, err
	}
	return s.Public(), nil
}

// LoadECDSAKeyFromFile reads a PEM-encoded ECDSA private key from path.
// Use this at bootstrap to load keys from mounted secrets.
func LoadECDSAKeyFromFile(path string) (*ecdsa.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("load ECDSA key from %q: %w", path, err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("load ECDSA key from %q: no PEM block found", path)
	}
	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse ECDSA key from %q: %w", path, err)
	}
	return key, nil
}

// --- KMSKeyProvider stub ────────────────────────────────────────────────────

// KMSKeyProvider resolves keys from a cloud KMS.
//
// KMS paths follow the Cosign convention:
//   - gcpkms://projects/P/locations/L/keyRings/R/cryptoKeys/K/cryptoKeyVersions/V
//   - awskms://arn:aws:kms:region:account:key/key-id
//   - azurekms://vault-name.vault.azure.net/key-name
//
// PRODUCTION: replace the stub with the appropriate KMS client SDK.
// The crypto.Signer returned never exports the private key; all signing
// happens inside the KMS boundary (compliance + HSM backing).
type KMSKeyProvider struct {
	// kmsClient would be the actual KMS client (e.g. cloudkms.Service)
}

// NewKMSKeyProvider constructs a KMSKeyProvider.
// PRODUCTION: accept kms client or credentials config and initialise the client.
func NewKMSKeyProvider() *KMSKeyProvider {
	return &KMSKeyProvider{}
}

func (p *KMSKeyProvider) GetSigner(_ context.Context, spec domain.KeySpec) (crypto.Signer, error) {
	if spec.KMSPath == "" {
		return nil, fmt.Errorf("kms key provider: KMSPath is required")
	}
	// STUB: in production, use cosign's KMS signer:
	//   sv, err := kms.Get(ctx, spec.KMSPath, crypto.SHA256)
	return nil, fmt.Errorf("kms key provider: STUB — implement KMS client for path %q", spec.KMSPath)
}

func (p *KMSKeyProvider) PublicKey(ctx context.Context, spec domain.KeySpec) (crypto.PublicKey, error) {
	s, err := p.GetSigner(ctx, spec)
	if err != nil {
		return nil, err
	}
	return s.Public(), nil
}
