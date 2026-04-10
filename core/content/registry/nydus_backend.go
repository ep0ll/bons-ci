//go:build !windows

package registry

import (
	"context"
	"fmt"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/nydus-snapshotter/pkg/converter"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// S3Backend implements converter.Backend by delegating to a content.Store
// backed by S3/B2 (typically b2.S3ContentStore). This bridges the Nydus
// converter's push interface with the existing object storage infrastructure.
type S3Backend struct {
	store content.Store
}

// NewS3Backend creates a new S3/B2 backend for Nydus converter operations.
// The given store must be an S3-compatible content.Store (e.g., b2.S3ContentStore).
func NewS3Backend(store content.Store) converter.Backend {
	return &S3Backend{store: store}
}

// Push reads a Nydus blob from the source content store and writes it to
// the S3/B2 backend store.
func (b *S3Backend) Push(ctx context.Context, cs content.Store, desc ocispec.Descriptor) error {
	// Check if already in S3/B2
	if _, err := b.store.Info(ctx, desc.Digest); err == nil {
		return nil
	}

	ra, err := cs.ReaderAt(ctx, desc)
	if err != nil {
		return fmt.Errorf("backend: open source reader for %s: %w", desc.Digest, err)
	}
	defer ra.Close()

	ref := fmt.Sprintf("backend-push-%s", desc.Digest)
	w, err := b.store.Writer(ctx, content.WithDescriptor(desc), content.WithRef(ref))
	if err != nil {
		return fmt.Errorf("backend: open destination writer for %s: %w", desc.Digest, err)
	}
	defer w.Close()

	if err := content.Copy(ctx, w, content.NewReader(ra), desc.Size, desc.Digest); err != nil {
		return fmt.Errorf("backend: copy blob %s: %w", desc.Digest, err)
	}

	return nil
}

// Check verifies whether a blob with the given digest exists in the S3/B2 store.
func (b *S3Backend) Check(blobDigest digest.Digest) (string, error) {
	info, err := b.store.Info(context.Background(), blobDigest)
	if err != nil {
		return "", fmt.Errorf("backend: blob %s not found: %w", blobDigest, err)
	}
	return info.Digest.Hex(), nil
}

// Type returns the backend type identifier.
func (b *S3Backend) Type() string {
	return "s3"
}

var _ converter.Backend = &S3Backend{}
