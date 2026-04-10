package registry

import (
	"context"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// TracedStore is a transparent decorator over content.Store that emits
// OpenTelemetry spans for all store operations.
type TracedStore struct {
	inner  content.Store
	tracer trace.Tracer
}

// NewTracedStore wraps a store with OTel tracing capabilities.
// This preserves the implementation of Provider, Manager, and Ingester.
func NewTracedStore(store content.Store, tp trace.TracerProvider) content.Store {
	return &TracedStore{
		inner:  store,
		tracer: tp.Tracer("registry"),
	}
}

func (s *TracedStore) Info(ctx context.Context, dgst digest.Digest) (content.Info, error) {
	ctx, span := s.tracer.Start(ctx, "registry.Info", trace.WithAttributes(
		attribute.String("digest", dgst.String()),
	))
	defer span.End()

	info, err := s.inner.Info(ctx, dgst)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	return info, err
}

func (s *TracedStore) Update(ctx context.Context, info content.Info, fieldpaths ...string) (content.Info, error) {
	ctx, span := s.tracer.Start(ctx, "registry.Update", trace.WithAttributes(
		attribute.String("digest", info.Digest.String()),
	))
	defer span.End()

	res, err := s.inner.Update(ctx, info, fieldpaths...)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	return res, err
}

func (s *TracedStore) Walk(ctx context.Context, fn content.WalkFunc, filters ...string) error {
	ctx, span := s.tracer.Start(ctx, "registry.Walk")
	defer span.End()

	err := s.inner.Walk(ctx, fn, filters...)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	return err
}

func (s *TracedStore) Delete(ctx context.Context, dgst digest.Digest) error {
	ctx, span := s.tracer.Start(ctx, "registry.Delete", trace.WithAttributes(
		attribute.String("digest", dgst.String()),
	))
	defer span.End()

	err := s.inner.Delete(ctx, dgst)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	return err
}

func (s *TracedStore) ReaderAt(ctx context.Context, desc v1.Descriptor) (content.ReaderAt, error) {
	ctx, span := s.tracer.Start(ctx, "registry.ReaderAt", trace.WithAttributes(
		attribute.String("digest", desc.Digest.String()),
		attribute.Int64("size", desc.Size),
	))
	defer span.End()

	ra, err := s.inner.ReaderAt(ctx, desc)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	return ra, err
}

func (s *TracedStore) Status(ctx context.Context, ref string) (content.Status, error) {
	ctx, span := s.tracer.Start(ctx, "registry.Status", trace.WithAttributes(
		attribute.String("ref", ref),
	))
	defer span.End()

	status, err := s.inner.Status(ctx, ref)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	return status, err
}

func (s *TracedStore) ListStatuses(ctx context.Context, filters ...string) ([]content.Status, error) {
	ctx, span := s.tracer.Start(ctx, "registry.ListStatuses")
	defer span.End()

	statuses, err := s.inner.ListStatuses(ctx, filters...)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	return statuses, err
}

func (s *TracedStore) Abort(ctx context.Context, ref string) error {
	ctx, span := s.tracer.Start(ctx, "registry.Abort", trace.WithAttributes(
		attribute.String("ref", ref),
	))
	defer span.End()

	err := s.inner.Abort(ctx, ref)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	return err
}

func (s *TracedStore) Writer(ctx context.Context, opts ...content.WriterOpt) (content.Writer, error) {
	var wrOpt content.WriterOpts
	for _, o := range opts {
		_ = o(&wrOpt)
	}

	ctx, span := s.tracer.Start(ctx, "registry.Writer", trace.WithAttributes(
		attribute.String("ref", wrOpt.Ref),
		attribute.String("digest", wrOpt.Desc.Digest.String()),
	))
	defer span.End()

	w, err := s.inner.Writer(ctx, opts...)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	return w, err
}

var _ content.Store = &TracedStore{}
