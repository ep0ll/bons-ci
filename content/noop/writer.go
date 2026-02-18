package noop

import (
	"context"
	"github.com/containerd/containerd/v2/core/content"
	digest "github.com/opencontainers/go-digest"
)

type noopWriter struct{}

var _ content.Writer = &noopWriter{}

// Close implements content.Writer.
func (n *noopWriter) Close() error {
	return nil
}

// Commit implements content.Writer.
func (n *noopWriter) Commit(ctx context.Context, size int64, expected digest.Digest, opts ...content.Opt) error {
	return nil
}

// Digest implements content.Writer.
func (n *noopWriter) Digest() digest.Digest {
	return digest.Digest("")
}

// Status implements content.Writer.
func (n *noopWriter) Status() (content.Status, error) {
	return content.Status{}, nil
}

// Truncate implements content.Writer.
func (n *noopWriter) Truncate(size int64) error {
	return nil
}

// Write implements content.Writer.
func (*noopWriter) Write(p []byte) (n int, err error) {
	return len(p), nil
}

func NoopWriter() content.Writer {
	return &noopWriter{}
}
