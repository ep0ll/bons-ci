package registry

import (
	"context"
	"io"

	"github.com/containerd/containerd/v2/core/content"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

// RegistryBackend abstracts all remote OCI registry operations.
//
// Implementations may back this with containerd's transfer/registry, ORAS,
// or any OCI-distribution-spec-compliant client. Store depends exclusively on
// this interface — never on a concrete registry client — making backends
// fully pluggable and independently testable.
type RegistryBackend interface {
	// Resolve returns the canonical name and descriptor for a reference.
	// The reference may be a tag or digest-qualified string.
	Resolve(ctx context.Context, ref string) (name string, desc v1.Descriptor, err error)

	// Fetch opens a readable stream for the content identified by desc
	// under the given reference. The caller MUST close the returned reader.
	Fetch(ctx context.Context, ref string, desc v1.Descriptor) (io.ReadCloser, error)

	// Push returns a content.Writer that uploads data for desc under ref.
	// The caller MUST Commit or Close the returned writer.
	Push(ctx context.Context, ref string, desc v1.Descriptor) (content.Writer, error)
}
