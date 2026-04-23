package writer

import (
	"context"

	"github.com/bons/bons-ci/core/content/store/composite/content/util"
	"github.com/containerd/containerd/content"
	v2content "github.com/containerd/containerd/v2/core/content"
	digest "github.com/opencontainers/go-digest"
)

func V1Writer(wr v2content.Writer) content.Writer {
	return &v1Writer{wr}
}

type v1Writer struct {
	wr v2content.Writer
}

// Close implements content.Writer.
func (v *v1Writer) Close() error {
	return v.wr.Close()
}

// Commit implements content.Writer.
func (v *v1Writer) Commit(ctx context.Context, size int64, expected digest.Digest, opts ...content.Opt) error {
	return v.wr.Commit(ctx, size, expected, util.V2Opt(opts...)...)
}

// Digest implements content.Writer.
func (v *v1Writer) Digest() digest.Digest {
	return v.wr.Digest()
}

// Status implements content.Writer.
func (v *v1Writer) Status() (content.Status, error) {
	st, err := v.wr.Status()
	return content.Status(st), err
}

// Truncate implements content.Writer.
func (v *v1Writer) Truncate(size int64) error {
	return v.wr.Truncate(size)
}

// Write implements content.Writer.
func (v *v1Writer) Write(p []byte) (n int, err error) {
	return v.wr.Write(p)
}

var _ content.Writer = (*v1Writer)(nil)
