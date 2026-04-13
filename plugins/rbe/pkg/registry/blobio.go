package registry

import (
	"context"
	"io"

	"github.com/bons/bons-ci/plugins/rbe/pkg/storage"
)

// GetBlobReader returns a ReadCloser for a blob's raw bytes.
// This is the data path (separate from StatBlob which is metadata-only).
func (r *Registry) GetBlobReader(ctx context.Context, digest string, opts storage.GetOptions) (io.ReadCloser, int64, error) {
	return r.store.Get(ctx, digest, opts)
}

// GetUploadParts returns the assembled part list for completing an upload.
func (r *Registry) GetUploadParts(ctx context.Context, uploadID string) ([]storage.Part, error) {
	status, err := r.store.GetUploadStatus(ctx, uploadID)
	if err != nil {
		return nil, err
	}
	parts := make([]storage.Part, len(status.Parts))
	copy(parts, status.Parts)
	return parts, nil
}
