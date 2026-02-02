package content

import (
	"context"
	"errors"

	"github.com/containerd/containerd/v2/core/content"
	digest "github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

type localFirstContentStore struct {
	local, registry content.Store
}

// Abort implements content.Store.
func (l *localFirstContentStore) Abort(ctx context.Context, ref string) error {
	if err := l.local.Abort(ctx, ref); err != nil {
		return err
	}

	l.registry.Abort(ctx, ref)

	return nil
}

// Delete implements content.Store.
func (l *localFirstContentStore) Delete(ctx context.Context, dgst digest.Digest) error {
	return l.local.Delete(ctx, dgst)
}

// Info implements content.Store.
func (l *localFirstContentStore) Info(ctx context.Context, dgst digest.Digest) (content.Info, error) {
	if info, err := l.local.Info(ctx, dgst); err == nil {
		return info, err
	}

	return l.registry.Info(ctx, dgst)
}

// ListStatuses implements content.Store.
func (l *localFirstContentStore) ListStatuses(ctx context.Context, filters ...string) ([]content.Status, error) {
	return l.local.ListStatuses(ctx, filters...)
}

// ReaderAt implements content.Store.
func (l *localFirstContentStore) ReaderAt(ctx context.Context, desc v1.Descriptor) (content.ReaderAt, error) {
	readerAt, err := l.local.ReaderAt(ctx, desc)
	if err == nil {
		return readerAt, nil
	}

	return l.registry.ReaderAt(ctx, desc)
}

// Status implements content.Store.
func (l *localFirstContentStore) Status(ctx context.Context, ref string) (content.Status, error) {
	return l.local.Status(ctx, ref)
}

// Update implements content.Store.
func (l *localFirstContentStore) Update(ctx context.Context, info content.Info, fieldpaths ...string) (content.Info, error) {
	return l.local.Update(ctx, info, fieldpaths...)
}

// Walk implements content.Store.
func (l *localFirstContentStore) Walk(ctx context.Context, fn content.WalkFunc, filters ...string) error {
	return l.local.Walk(ctx, fn, filters...)
}

// Writer implements content.Store.
func (l *localFirstContentStore) Writer(ctx context.Context, opts ...content.WriterOpt) (content.Writer, error) {
	var writers []content.Writer
	lw, lerr := l.local.Writer(ctx, opts...)
	if lerr == nil {
		writers = append(writers, lw)
	}

	rw, rerr := l.registry.Writer(ctx, opts...)
	if rerr == nil {
		writers = append(writers, rw)
	}

	return &multiWriter{writers: writers}, errors.Join(lerr, rerr)
}

var _ content.Store = &localFirstContentStore{}
