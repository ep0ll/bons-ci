// Package vault provides the OCI Vault adapter for the vault.Client interface.
package vault

import (
	"context"
	"encoding/base64"
	"fmt"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

// compile-time interface check
var _ Client = (*OCIVaultClient)(nil)

const defaultCacheTTL = 5 * time.Minute

// cacheEntry holds a cached secret value with an expiry time.
type cacheEntry struct {
	value     []byte
	expiresAt time.Time
}

// secretMapping maps logical secret IDs to OCI Vault secret OCIDs.
// In production this mapping is loaded from config or discovered via tags.
type secretMapping map[string]string

// OCIVaultClient is the production OCI Secrets Service adapter.
// It caches secret values for up to cacheTTL to reduce API call volume
// during burst runner provisioning.
type OCIVaultClient struct {
	mu         sync.RWMutex
	cache      map[string]*cacheEntry
	cacheTTL   time.Duration
	mapping    secretMapping // logical name → OCI secret OCID
	logger     zerolog.Logger
	// ociClient is the underlying OCI Secrets client.
	// Type is interface{} here so the package compiles without the OCI SDK
	// available in restricted network environments; swap for the real type in prod.
	ociClient  interface{ GetSecretBundle(ctx context.Context, secretID string) (string, error) }
}

// OCIVaultConfig holds configuration for the OCI Vault adapter.
type OCIVaultConfig struct {
	// SecretMapping maps logical secret names to OCI Vault secret OCIDs.
	// Example: {"github-app-key-tenant-abc": "ocid1.vaultsecret.oc1..."}
	SecretMapping map[string]string
	// CacheTTL overrides the default 5-minute cache TTL.
	CacheTTL time.Duration
}

// NewOCIVaultClient creates a production OCI Vault client.
// In a real deployment the ociSecretsClient is constructed from instance principal auth.
func NewOCIVaultClient(cfg OCIVaultConfig, ociSecretsClient interface {
	GetSecretBundle(ctx context.Context, secretID string) (string, error)
}, logger zerolog.Logger) *OCIVaultClient {
	ttl := cfg.CacheTTL
	if ttl == 0 {
		ttl = defaultCacheTTL
	}
	return &OCIVaultClient{
		cache:     make(map[string]*cacheEntry),
		cacheTTL:  ttl,
		mapping:   cfg.SecretMapping,
		logger:    logger.With().Str("component", "vault").Logger(),
		ociClient: ociSecretsClient,
	}
}

// GetSecret retrieves a secret by logical name, using the in-memory cache.
func (v *OCIVaultClient) GetSecret(ctx context.Context, secretID string) ([]byte, error) {
	// Fast path: read from cache without exclusive lock.
	v.mu.RLock()
	entry, ok := v.cache[secretID]
	v.mu.RUnlock()
	if ok && time.Now().Before(entry.expiresAt) {
		return entry.value, nil
	}

	// Slow path: fetch from OCI Vault and populate cache.
	v.mu.Lock()
	defer v.mu.Unlock()

	// Double-check after acquiring write lock (another goroutine may have populated).
	entry, ok = v.cache[secretID]
	if ok && time.Now().Before(entry.expiresAt) {
		return entry.value, nil
	}

	ocid, exists := v.mapping[secretID]
	if !exists {
		return nil, fmt.Errorf("secret %q: %w", secretID, ErrSecretNotFound)
	}

	v.logger.Debug().Str("secret_id", secretID).Msg("fetching secret from OCI Vault")

	// The OCI Secrets Service returns base64-encoded secret content.
	b64Value, err := v.ociClient.GetSecretBundle(ctx, ocid)
	if err != nil {
		return nil, fmt.Errorf("fetch secret %q (ocid %s): %w", secretID, ocid, err)
	}

	decoded, err := base64.StdEncoding.DecodeString(b64Value)
	if err != nil {
		return nil, fmt.Errorf("decode secret %q: %w", secretID, err)
	}

	v.cache[secretID] = &cacheEntry{
		value:     decoded,
		expiresAt: time.Now().Add(v.cacheTTL),
	}
	return decoded, nil
}

// InvalidateCache removes all cached entries for the given secret ID.
// Call after a known secret rotation.
func (v *OCIVaultClient) InvalidateCache(secretID string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	delete(v.cache, secretID)
}
