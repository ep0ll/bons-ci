package writer

import (
	"context"

	"github.com/bons/bons-ci/core/content/store/composite/content/util"
	"github.com/containerd/containerd/content"
	v2content "github.com/containerd/containerd/v2/core/content"
	digest "github.com/opencontainers/go-digest"
)

func V2Writer(wr content.Writer) v2content.Writer {
	return &v2Writer{wr}
}

type v2Writer struct {
	wr content.Writer
}

// Close implements content.Writer.
func (v *v2Writer) Close() error {
	return v.wr.Close()
}

// Commit implements content.Writer.
func (v *v2Writer) Commit(ctx context.Context, size int64, expected digest.Digest, opts ...v2content.Opt) error {
	return v.wr.Commit(ctx, size, expected, util.V1Opt(opts...)...)
}

// Digest implements content.Writer.
func (v *v2Writer) Digest() digest.Digest {
	return v.wr.Digest()
}

// Status implements content.Writer.
func (v *v2Writer) Status() (v2content.Status, error) {
	st, err := v.wr.Status()
	return v2content.Status(st), err
}

// Truncate implements content.Writer.
func (v *v2Writer) Truncate(size int64) error {
	return v.wr.Truncate(size)
}

// Write implements content.Writer.
func (v *v2Writer) Write(p []byte) (n int, err error) {
	return v.wr.Write(p)
}

var _ v2content.Writer = (*v2Writer)(nil)
