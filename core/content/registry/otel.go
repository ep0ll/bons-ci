package registry

import (
	"context"

	"github.com/containerd/containerd/v2/core/content"
	digest "github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "github.com/bons/bons-ci/core/content/registry"

// TracedStore is an OpenTelemetry-instrumented decorator for content.Store.
// Each method creates a span with relevant attributes (digest, size, ref).
//
// This follows the Open/Closed Principle: tracing is added without
// modifying the Store implementation.
type TracedStore struct {
	inner  content.Store
	tracer trace.Tracer
}

// NewTracedStore wraps a content.Store with OpenTelemetry tracing.
func NewTracedStore(store content.Store, tp trace.TracerProvider) *TracedStore {
	if tp == nil {
		tp = otel.GetTracerProvider()
	}
	return &TracedStore{
		inner:  store,
		tracer: tp.Tracer(tracerName),
	}
}

// compile-time check
var _ content.Store = (*TracedStore)(nil)

func (t *TracedStore) Info(ctx context.Context, dgst digest.Digest) (content.Info, error) {
	ctx, span := t.tracer.Start(ctx, "registry.Info",
		trace.WithAttributes(attribute.String("digest", dgst.String())),
	)
	defer span.End()

	info, err := t.inner.Info(ctx, dgst)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return info, err
	}
	span.SetAttributes(attribute.Int64("size", info.Size))
	return info, nil
}

func (t *TracedStore) Update(ctx context.Context, info content.Info, fieldpaths ...string) (content.Info, error) {
	ctx, span := t.tracer.Start(ctx, "registry.Update",
		trace.WithAttributes(attribute.String("digest", info.Digest.String())),
	)
	defer span.End()

	result, err := t.inner.Update(ctx, info, fieldpaths...)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	return result, err
}

func (t *TracedStore) Walk(ctx context.Context, fn content.WalkFunc, fs ...string) error {
	ctx, span := t.tracer.Start(ctx, "registry.Walk",
		trace.WithAttributes(attribute.StringSlice("filters", fs)),
	)
	defer span.End()

	count := 0
	wrapped := func(info content.Info) error {
		count++
		return fn(info)
	}

	err := t.inner.Walk(ctx, wrapped, fs...)
	span.SetAttributes(attribute.Int("walked_count", count))
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	return err
}

func (t *TracedStore) Delete(ctx context.Context, dgst digest.Digest) error {
	ctx, span := t.tracer.Start(ctx, "registry.Delete",
		trace.WithAttributes(attribute.String("digest", dgst.String())),
	)
	defer span.End()

	if err := t.inner.Delete(ctx, dgst); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}
	return nil
}

func (t *TracedStore) ReaderAt(ctx context.Context, desc v1.Descriptor) (content.ReaderAt, error) {
	ctx, span := t.tracer.Start(ctx, "registry.ReaderAt",
		trace.WithAttributes(
			attribute.String("digest", desc.Digest.String()),
			attribute.Int64("size", desc.Size),
		),
	)
	defer span.End()

	ra, err := t.inner.ReaderAt(ctx, desc)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}
	return ra, nil
}

func (t *TracedStore) Writer(ctx context.Context, opts ...content.WriterOpt) (content.Writer, error) {
	ctx, span := t.tracer.Start(ctx, "registry.Writer")
	defer span.End()

	w, err := t.inner.Writer(ctx, opts...)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}
	return w, nil
}

func (t *TracedStore) Abort(ctx context.Context, ref string) error {
	ctx, span := t.tracer.Start(ctx, "registry.Abort",
		trace.WithAttributes(attribute.String("ref", ref)),
	)
	defer span.End()

	if err := t.inner.Abort(ctx, ref); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}
	return nil
}

func (t *TracedStore) Status(ctx context.Context, ref string) (content.Status, error) {
	ctx, span := t.tracer.Start(ctx, "registry.Status",
		trace.WithAttributes(attribute.String("ref", ref)),
	)
	defer span.End()

	st, err := t.inner.Status(ctx, ref)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return st, err
	}
	return st, nil
}

func (t *TracedStore) ListStatuses(ctx context.Context, fs ...string) ([]content.Status, error) {
	ctx, span := t.tracer.Start(ctx, "registry.ListStatuses",
		trace.WithAttributes(attribute.StringSlice("filters", fs)),
	)
	defer span.End()

	statuses, err := t.inner.ListStatuses(ctx, fs...)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return statuses, err
	}
	span.SetAttributes(attribute.Int("count", len(statuses)))
	return statuses, nil
}
