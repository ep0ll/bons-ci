package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/errdefs"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// readImageConfig reads and unmarshals an OCI image config from the content store.
func readImageConfig(ctx context.Context, cs content.Store, desc ocispec.Descriptor) (*ocispec.Image, error) {
	var config ocispec.Image
	p, err := content.ReadBlob(ctx, cs, desc)
	if err != nil {
		return nil, fmt.Errorf("read image config %s: %w", desc.Digest, err)
	}
	if err := json.Unmarshal(p, &config); err != nil {
		return nil, fmt.Errorf("unmarshal image config: %w", err)
	}
	return &config, nil
}

// readManifest reads and unmarshals an OCI manifest from the content store.
func readManifest(ctx context.Context, cs content.Store, desc ocispec.Descriptor) (*ocispec.Manifest, error) {
	var manifest ocispec.Manifest
	p, err := content.ReadBlob(ctx, cs, desc)
	if err != nil {
		return nil, fmt.Errorf("read manifest %s: %w", desc.Digest, err)
	}
	if err := json.Unmarshal(p, &manifest); err != nil {
		return nil, fmt.Errorf("unmarshal manifest: %w", err)
	}
	return &manifest, nil
}

// readIndex reads and unmarshals an OCI index from the content store.
func readIndex(ctx context.Context, cs content.Store, desc ocispec.Descriptor) (*ocispec.Index, error) {
	var index ocispec.Index
	p, err := content.ReadBlob(ctx, cs, desc)
	if err != nil {
		return nil, fmt.Errorf("read index %s: %w", desc.Digest, err)
	}
	if err := json.Unmarshal(p, &index); err != nil {
		return nil, fmt.Errorf("unmarshal index: %w", err)
	}
	return &index, nil
}

// writeJSON marshals v as JSON, writes it to the content store, and returns
// a descriptor for the written content. If base has a non-empty MediaType,
// that is preserved on the returned descriptor.
func writeJSON(ctx context.Context, cs content.Store, v interface{}, base ocispec.Descriptor) (*ocispec.Descriptor, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal json: %w", err)
	}

	dgst := digest.SHA256.FromBytes(data)
	ref := fmt.Sprintf("json-write-%s", dgst)

	w, err := content.OpenWriter(ctx, cs, content.WithRef(ref))
	if err != nil {
		return nil, fmt.Errorf("open writer for json: %w", err)
	}
	defer w.Close()

	if err := content.Copy(ctx, w, bytes.NewReader(data), int64(len(data)), dgst); err != nil {
		if !errdefs.IsAlreadyExists(err) {
			return nil, fmt.Errorf("copy json: %w", err)
		}
	}

	desc := ocispec.Descriptor{
		Digest:    dgst,
		Size:      int64(len(data)),
		MediaType: base.MediaType,
	}

	if desc.MediaType == "" {
		desc.MediaType = ocispec.MediaTypeImageManifest
	}

	return &desc, nil
}
