// Package vault defines the secret-management port for the BYOC platform.
// All secrets (GitHub App private keys, DB passwords, webhook secrets) are
// fetched exclusively through this interface — never from env vars or files.
package vault

import "context"

// Client is the secret management port. The OCI Vault adapter implements this.
type Client interface {
	// GetSecret fetches the current value of the named secret.
	// secretID is the logical name used by the platform (e.g. "github-app-key-tenant-abc").
	// The implementation maps this to an OCI Vault secret OCID.
	// Implementations should apply a short in-memory cache (5 min TTL) to avoid
	// thundering-herd on OCI Vault during burst runner creation.
	GetSecret(ctx context.Context, secretID string) ([]byte, error)
}

// Sentinel errors for the vault package.
var (
	ErrSecretNotFound = &vaultError{code: "SECRET_NOT_FOUND", msg: "secret not found in vault"}
	ErrAccessDenied   = &vaultError{code: "ACCESS_DENIED", msg: "vault access denied"}
)

type vaultError struct {
	code string
	msg  string
}

func (e *vaultError) Error() string { return e.msg }
