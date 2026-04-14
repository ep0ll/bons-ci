package b2

import (
	"testing"

	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testPaths(t *testing.T) ObjectPaths {
	t.Helper()
	p, err := NewObjectPaths(Config{
		Tenant:      "acme",
		BlobsPrefix: "blobs/",
	})
	require.NoError(t, err)
	return p
}

// ---------------------------------------------------------------------------
// NewObjectPaths
// ---------------------------------------------------------------------------

func TestNewObjectPaths_Valid(t *testing.T) {
	p, err := NewObjectPaths(Config{Tenant: "t1", BlobsPrefix: "blobs/"})
	require.NoError(t, err)
	assert.Equal(t, "t1", p.tenant)
	assert.Equal(t, "blobs/", p.blobsPrefix)
}

func TestNewObjectPaths_EmptyTenant(t *testing.T) {
	_, err := NewObjectPaths(Config{Tenant: ""})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not be empty")
}

func TestNewObjectPaths_TenantWithSlash(t *testing.T) {
	_, err := NewObjectPaths(Config{Tenant: "a/b"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not contain '/'")
}

// ---------------------------------------------------------------------------
// BlobPath
// ---------------------------------------------------------------------------

func TestObjectPaths_BlobPath(t *testing.T) {
	p := testPaths(t)
	dgst := digest.Digest("sha256:abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890")
	got := p.BlobPath(dgst)
	assert.Equal(t, "acme/blobs/sha256/abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890", got)
}

func TestObjectPaths_BlobPath_SHA512(t *testing.T) {
	p := testPaths(t)
	encoded := "cf83e1357eefb8bdf1542850d66d8007d620e4050b5715dc83f4a921d36ce9ce47d0d13c5d85f2b0ff8318d2877eec2f63b931bd47417a81a538327af927da3e"
	dgst := digest.Digest("sha512:" + encoded)
	got := p.BlobPath(dgst)
	assert.Equal(t, "acme/blobs/sha512/"+encoded, got)
}

// ---------------------------------------------------------------------------
// Folder
// ---------------------------------------------------------------------------

func TestObjectPaths_Folder_NoArgs(t *testing.T) {
	p := testPaths(t)
	assert.Equal(t, "acme", p.Folder())
}

func TestObjectPaths_Folder_WithParts(t *testing.T) {
	p := testPaths(t)
	assert.Equal(t, "acme/blobs/sha256", p.Folder("blobs", "sha256"))
}

// ---------------------------------------------------------------------------
// TrimBlobPrefix
// ---------------------------------------------------------------------------

func TestObjectPaths_TrimBlobPrefix(t *testing.T) {
	p := testPaths(t)
	got := p.TrimBlobPrefix("acme/blobs/sha256/abcdef")
	assert.Equal(t, "sha256/abcdef", got)
}

func TestObjectPaths_TrimBlobPrefix_NoMatch(t *testing.T) {
	p := testPaths(t)
	got := p.TrimBlobPrefix("other/blobs/sha256/abcdef")
	// No prefix match, returns unchanged.
	assert.Equal(t, "other/blobs/sha256/abcdef", got)
}

// ---------------------------------------------------------------------------
// digestFromPath
// ---------------------------------------------------------------------------

func TestDigestFromPath_Valid(t *testing.T) {
	p := testPaths(t)
	key := "acme/blobs/sha256/abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"
	dgst, err := digestFromPath(key, p)
	require.NoError(t, err)
	assert.Equal(t, digest.Digest("sha256:abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"), dgst)
}

func TestDigestFromPath_EmptyAfterTrim(t *testing.T) {
	p := testPaths(t)
	_, err := digestFromPath("acme/blobs/", p)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot extract digest")
}

func TestDigestFromPath_NoSlashAfterTrim(t *testing.T) {
	p := testPaths(t)
	_, err := digestFromPath("acme/blobs/sha256only", p)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "malformed blob key")
}

func TestDigestFromPath_InvalidDigest(t *testing.T) {
	p := testPaths(t)
	_, err := digestFromPath("acme/blobs/badAlgo/encoded", p)
	require.Error(t, err)
}

func TestDigestFromPath_NoTenantMatch(t *testing.T) {
	p := testPaths(t)
	_, err := digestFromPath("other-tenant/blobs/sha256/abc", p)
	// TrimBlobPrefix won't match, the full key is passed through as "trimmed".
	// It'll try to parse "other-tenant/blobs/sha256/abc" which will fail.
	require.Error(t, err)
}
