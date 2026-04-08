// Package pipeline provides a composable, ordered middleware chain for SBOM
// generation. The design mirrors the classic HTTP middleware pattern adapted
// for domain scan operations.
//
// Usage:
//
//	handler := pipeline.Chain(
//	    coreHandler,          // innermost: performs the actual scan
//	    pipeline.WithLogging(logger),
//	    pipeline.WithEvents(bus),
//	    pipeline.WithCache(cache),
//	    pipeline.WithRetry(3, logger),
//	)
//	resp, err := handler(ctx, req)
package pipeline

import (
	"context"

	"github.com/bons/bons-ci/pkg/sbomkit/core/domain"
	"github.com/bons/bons-ci/pkg/sbomkit/core/ports"
)

// Request is the value passed through the processor chain.
// All fields should be treated as read-only inside processors.
type Request struct {
	// ID is a correlation identifier that ties all events to this scan operation.
	ID string
	// Source is the resolved (post-Resolver) source descriptor.
	Source domain.Source
	// Opts tunes the underlying scanner.
	Opts ports.ScanOptions
	// Format is the requested output encoding.
	Format domain.Format
}

// Response is the value produced by the innermost handler and propagated
// outward through each processor.
type Response struct {
	// SBOM is the generated SBOM aggregate.
	SBOM *domain.SBOM
	// CacheHit is true when the response was served from cache.
	CacheHit bool
}

// Handler is a function that produces a Response from a Request.
// The innermost Handler performs the actual scan; outer Handlers are Processors.
type Handler func(ctx context.Context, req Request) (Response, error)

// Processor is a middleware step that wraps a Handler.
// Each Processor must call next exactly once to continue the chain, unless it
// intentionally short-circuits (e.g. a cache hit).
type Processor func(ctx context.Context, req Request, next Handler) (Response, error)

// Chain composes processors around a final Handler.
// Processors execute in the order given: processors[0] is the outermost
// (first to receive the call) and processors[n-1] is innermost before final.
//
//	Chain(final, A, B, C)  ≡  A → B → C → final
func Chain(final Handler, processors ...Processor) Handler {
	return build(final, processors)
}

// build recursively wraps the handler list. It is the recursive kernel of
// Chain; extracted to keep closure captures correct.
func build(final Handler, processors []Processor) Handler {
	if len(processors) == 0 {
		return final
	}
	proc := processors[0]
	rest := build(final, processors[1:])
	return func(ctx context.Context, req Request) (Response, error) {
		return proc(ctx, req, rest)
	}
}
