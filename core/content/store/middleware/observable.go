package middleware

import (
	"context"
	"time"

	"github.com/bons/bons-ci/core/content/event"
	"github.com/containerd/containerd/v2/core/content"
	digest "github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

// Observable returns a Middleware that publishes store lifecycle events to bus.
// source is a human-readable label identifying the store (e.g., "local", "registry").
// Events are published asynchronously so that observers never slow the write path.
func Observable(bus *event.Bus, source string) Middleware {
	return func(next content.Store) content.Store {
		return &observableStore{Store: next, bus: bus, source: source}
	}
}

type observableStore struct {
	content.Store
	bus    *event.Bus
	source string
}

// ReaderAt publishes a hit or miss event depending on the outcome.
func (o *observableStore) ReaderAt(ctx context.Context, desc v1.Descriptor) (content.ReaderAt, error) {
	r, err := o.Store.ReaderAt(ctx, desc)
	if err != nil {
		o.emit(ctx, event.Event{
			Kind:       event.KindReadMiss,
			Source:     o.source,
			Digest:     desc.Digest,
			Err:        err,
			OccurredAt: time.Now(),
		})
		return nil, err
	}
	o.emit(ctx, event.Event{
		Kind:       event.KindReadHit,
		Source:     o.source,
		Digest:     desc.Digest,
		OccurredAt: time.Now(),
	})
	return r, nil
}

// Writer publishes a started event on success, or a failed event on error.
func (o *observableStore) Writer(ctx context.Context, opts ...content.WriterOpt) (content.Writer, error) {
	w, err := o.Store.Writer(ctx, opts...)
	if err != nil {
		o.emit(ctx, event.Event{
			Kind:       event.KindWriteFailed,
			Source:     o.source,
			Err:        err,
			OccurredAt: time.Now(),
		})
		return nil, err
	}

	o.emit(ctx, event.Event{
		Kind:       event.KindWriteStarted,
		Source:     o.source,
		OccurredAt: time.Now(),
	})
	return &observableWriter{Writer: w, bus: o.bus, source: o.source, ctx: ctx}, nil
}

// Delete publishes a deleted event on success.
func (o *observableStore) Delete(ctx context.Context, dgst digest.Digest) error {
	err := o.Store.Delete(ctx, dgst)
	if err == nil {
		o.emit(ctx, event.Event{
			Kind:       event.KindDeleted,
			Source:     o.source,
			Digest:     dgst,
			OccurredAt: time.Now(),
		})
	}
	return err
}

// emit dispatches an event asynchronously. Observers must never block the
// hot write path.
func (o *observableStore) emit(ctx context.Context, evt event.Event) {
	o.bus.PublishAsync(ctx, evt)
}

// observableWriter wraps a content.Writer and emits commit/fail events.
type observableWriter struct {
	content.Writer
	bus    *event.Bus
	source string
	ctx    context.Context //nolint:containedctx — stored for Commit notification only
}

// Commit forwards the commit and emits a committed or failed event.
func (ow *observableWriter) Commit(ctx context.Context, size int64, expected digest.Digest, opts ...content.Opt) error {
	err := ow.Writer.Commit(ctx, size, expected, opts...)

	kind := event.KindWriteCommitted
	if err != nil {
		kind = event.KindWriteFailed
	}
	ow.bus.PublishAsync(ctx, event.Event{
		Kind:       kind,
		Source:     ow.source,
		Digest:     expected,
		Err:        err,
		OccurredAt: time.Now(),
	})
	return err
}
