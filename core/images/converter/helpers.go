package converter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images/converter"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

var (
	ClearGCLabels = converter.ClearGCLabels
)

func copyDesc(desc ocispec.Descriptor) *ocispec.Descriptor {
	descCopy := desc
	return &descCopy
}

func ReadJSON(ctx context.Context, cs content.Store, x interface{}, desc ocispec.Descriptor) (map[string]string, error) {
	info, err := cs.Info(ctx, desc.Digest)
	if err != nil {
		return nil, err
	}
	labels := info.Labels
	b, err := content.ReadBlob(ctx, cs, desc)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, x); err != nil {
		return nil, err
	}
	return labels, nil
}

func WriteJSON(ctx context.Context, cs content.Store, x interface{}, oldDesc ocispec.Descriptor, labels map[string]string) (*ocispec.Descriptor, error) {
	b, err := json.Marshal(x)
	if err != nil {
		return nil, err
	}
	dgst := digest.SHA256.FromBytes(b)
	ref := fmt.Sprintf("converter-write-json-%s", dgst.String())
	w, err := content.OpenWriter(ctx, cs, content.WithRef(ref))
	if err != nil {
		return nil, err
	}
	if err := content.Copy(ctx, w, bytes.NewReader(b), int64(len(b)), dgst, content.WithLabels(labels)); err != nil {
		w.Close()
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	newDesc := oldDesc
	newDesc.Size = int64(len(b))
	newDesc.Digest = dgst
	return &newDesc, nil
}

// clearDockerV1DummyID clears the dummy values for legacy `.config.Image` and `.container_config.Image`.
// Returns true if the cfg was modified.
func clearDockerV1DummyID(cfg DualConfig) (bool, error) {
	var modified bool
	f := func(k string) error {
		if configX, ok := cfg[k]; ok && configX != nil {
			var configField map[string]*json.RawMessage
			if err := json.Unmarshal(*configX, &configField); err != nil {
				return err
			}
			delete(configField, "Image")
			b, err := json.Marshal(configField)
			if err != nil {
				return err
			}
			cfg[k] = (*json.RawMessage)(&b)
			modified = true
		}
		return nil
	}
	if err := f("config"); err != nil {
		return modified, err
	}
	if err := f("container_config"); err != nil {
		return modified, err
	}
	return modified, nil
}
