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

// Semantic span attribute keys — defined as package-level vars to avoid a
// string allocation on every span creation (attribute.String interns the key).
var (
	attrDigest      = "registry.digest"
	attrSizeBytes   = "registry.size_bytes"
	attrRef         = "registry.ref"
	attrFilters     = "registry.filters"
	attrCount       = "registry.count"
	attrWalkedCount = "registry.walked_count"
)

// TracedStore is an OpenTelemetry-instrumented decorator for any content.Store.
//
// Every method creates a span with standard registry attributes (digest, size,
// ref) and records errors with span.RecordError / SetStatus(codes.Error).
//
// Follows the Open/Closed Principle: tracing behaviour is added without
// modifying the underlying Store.
type TracedStore struct {
	inner  content.Store
	tracer trace.Tracer
}

// NewTracedStore wraps store with OpenTelemetry tracing spans.
// If tp is nil the global tracer provider is used (otel.GetTracerProvider()).
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
		trace.WithAttributes(attribute.String(attrDigest, dgst.String())),
	)
	defer span.End()

	info, err := t.inner.Info(ctx, dgst)
	if err != nil {
		spanErr(span, err)
		return info, err
	}
	span.SetAttributes(attribute.Int64(attrSizeBytes, info.Size))
	return info, nil
}

func (t *TracedStore) Update(ctx context.Context, info content.Info, fieldpaths ...string) (content.Info, error) {
	ctx, span := t.tracer.Start(ctx, "registry.Update",
		trace.WithAttributes(attribute.String(attrDigest, info.Digest.String())),
	)
	defer span.End()

	result, err := t.inner.Update(ctx, info, fieldpaths...)
	if err != nil {
		spanErr(span, err)
	}
	return result, err
}

func (t *TracedStore) Walk(ctx context.Context, fn content.WalkFunc, fs ...string) error {
	ctx, span := t.tracer.Start(ctx, "registry.Walk",
		trace.WithAttributes(attribute.StringSlice(attrFilters, fs)),
	)
	defer span.End()

	count := 0
	err := t.inner.Walk(ctx, func(info content.Info) error {
		count++
		return fn(info)
	}, fs...)
	span.SetAttributes(attribute.Int(attrWalkedCount, count))
	if err != nil {
		spanErr(span, err)
	}
	return err
}

func (t *TracedStore) Delete(ctx context.Context, dgst digest.Digest) error {
	ctx, span := t.tracer.Start(ctx, "registry.Delete",
		trace.WithAttributes(attribute.String(attrDigest, dgst.String())),
	)
	defer span.End()

	if err := t.inner.Delete(ctx, dgst); err != nil {
		spanErr(span, err)
		return err
	}
	return nil
}

func (t *TracedStore) ReaderAt(ctx context.Context, desc v1.Descriptor) (content.ReaderAt, error) {
	ctx, span := t.tracer.Start(ctx, "registry.ReaderAt",
		trace.WithAttributes(
			attribute.String(attrDigest, desc.Digest.String()),
			attribute.Int64(attrSizeBytes, desc.Size),
		),
	)
	defer span.End()

	ra, err := t.inner.ReaderAt(ctx, desc)
	if err != nil {
		spanErr(span, err)
		return nil, err
	}
	return ra, nil
}

func (t *TracedStore) Writer(ctx context.Context, opts ...content.WriterOpt) (content.Writer, error) {
	ctx, span := t.tracer.Start(ctx, "registry.Writer")
	defer span.End()

	w, err := t.inner.Writer(ctx, opts...)
	if err != nil {
		spanErr(span, err)
		return nil, err
	}
	return w, nil
}

func (t *TracedStore) Abort(ctx context.Context, ref string) error {
	ctx, span := t.tracer.Start(ctx, "registry.Abort",
		trace.WithAttributes(attribute.String(attrRef, ref)),
	)
	defer span.End()

	if err := t.inner.Abort(ctx, ref); err != nil {
		spanErr(span, err)
		return err
	}
	return nil
}

func (t *TracedStore) Status(ctx context.Context, ref string) (content.Status, error) {
	ctx, span := t.tracer.Start(ctx, "registry.Status",
		trace.WithAttributes(attribute.String(attrRef, ref)),
	)
	defer span.End()

	st, err := t.inner.Status(ctx, ref)
	if err != nil {
		spanErr(span, err)
	}
	return st, err
}

func (t *TracedStore) ListStatuses(ctx context.Context, fs ...string) ([]content.Status, error) {
	ctx, span := t.tracer.Start(ctx, "registry.ListStatuses",
		trace.WithAttributes(attribute.StringSlice(attrFilters, fs)),
	)
	defer span.End()

	ss, err := t.inner.ListStatuses(ctx, fs...)
	if err != nil {
		spanErr(span, err)
		return ss, err
	}
	span.SetAttributes(attribute.Int(attrCount, len(ss)))
	return ss, nil
}

// spanErr records err on span and sets span status to Error.
func spanErr(span trace.Span, err error) {
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
}
