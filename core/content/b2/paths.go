package b2

import (
	"fmt"
	"strings"

	"github.com/opencontainers/go-digest"
)

// ObjectPaths provides methods to build and parse tenant-scoped S3 object keys.
type ObjectPaths struct {
	tenant      string
	blobsPrefix string
}

// NewObjectPaths creates an ObjectPaths for the given config.
func NewObjectPaths(cfg Config) (ObjectPaths, error) {
	if err := validateTenant(cfg.Tenant); err != nil {
		return ObjectPaths{}, err
	}
	return ObjectPaths{
		tenant:      cfg.Tenant,
		blobsPrefix: cfg.BlobsPrefix,
	}, nil
}

// BlobPath returns the full S3 key for a blob:
// {tenant}/{blobsPrefix}{algorithm}/{encoded}.
func (p ObjectPaths) BlobPath(dgst digest.Digest) string {
	return strings.Join([]string{
		p.tenant,
		p.blobsPrefix + dgst.Algorithm().String(),
		dgst.Encoded(),
	}, "/")
}

// Folder joins the tenant with the given path segments.
func (p ObjectPaths) Folder(parts ...string) string {
	all := append([]string{p.tenant}, parts...)
	return strings.Join(all, "/")
}

// TrimBlobPrefix removes the "{tenant}/{blobsPrefix}" prefix from a key,
// returning the remaining "{algorithm}/{encoded}" portion.
func (p ObjectPaths) TrimBlobPrefix(key string) string {
	prefix := p.tenant + "/" + p.blobsPrefix
	after, _ := strings.CutPrefix(key, prefix)
	return after
}

// DigestToPath converts a validated digest into a bare blob path
// (without tenant prefix): "{blobsPrefix}{algorithm}/{encoded}".
func DigestToPath(dgst digest.Digest) (string, error) {
	if err := dgst.Validate(); err != nil {
		return "", err
	}
	return DefaultBlobsPrefix + dgst.Algorithm().String() + "/" + dgst.Encoded(), nil
}

// digestFromPath extracts a digest from an S3 object key that has been
// trimmed by ObjectPaths.TrimBlobPrefix to "{algorithm}/{encoded}".
func digestFromPath(key string, paths ObjectPaths) (digest.Digest, error) {
	trimmed := paths.TrimBlobPrefix(key)
	if trimmed == "" {
		return "", fmt.Errorf("b2: cannot extract digest from key %q", key)
	}

	parts := strings.SplitN(trimmed, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", fmt.Errorf("b2: malformed blob key %q (trimmed: %q)", key, trimmed)
	}

	return digest.Parse(parts[0] + ":" + parts[1])
}

// validateTenant ensures a tenant string is non-empty and contains no slashes.
func validateTenant(tenant string) error {
	if tenant == "" {
		return fmt.Errorf("b2: tenant must not be empty")
	}
	if strings.Contains(tenant, "/") {
		return fmt.Errorf("b2: tenant must not contain '/', got %q", tenant)
	}
	return nil
}
