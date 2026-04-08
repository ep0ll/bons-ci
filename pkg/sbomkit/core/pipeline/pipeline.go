package pipeline

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"go.uber.org/zap"

	"github.com/bons/bons-ci/pkg/sbomkit/core/domain"
	"github.com/bons/bons-ci/pkg/sbomkit/core/event"
	"github.com/bons/bons-ci/pkg/sbomkit/core/ports"
)

// Pipeline is the composed, ready-to-execute scan handler.
type Pipeline struct {
	handler Handler
}

// New constructs a Pipeline from a core handler and an ordered list of processors.
// The core handler performs the actual scan; processors add cross-cutting concerns.
func New(core Handler, processors ...Processor) *Pipeline {
	return &Pipeline{handler: Chain(core, processors...)}
}

// Execute runs the full processor chain for the given request.
func (p *Pipeline) Execute(ctx context.Context, req Request) (Response, error) {
	return p.handler(ctx, req)
}

// ── Built-in processors ──────────────────────────────────────────────────────

// WithLogging logs the start and outcome of every request.
func WithLogging(logger *zap.Logger) Processor {
	return func(ctx context.Context, req Request, next Handler) (Response, error) {
		logger.Info("scan started",
			zap.String("request_id", req.ID),
			zap.String("source", req.Source.Identifier),
			zap.String("kind", string(req.Source.Kind)),
			zap.String("format", string(req.Format)),
		)
		start := time.Now()

		resp, err := next(ctx, req)

		elapsed := time.Since(start)
		if err != nil {
			logger.Error("scan failed",
				zap.String("request_id", req.ID),
				zap.Duration("elapsed", elapsed),
				zap.Error(err),
			)
			return resp, err
		}

		componentCount := 0
		if resp.SBOM != nil {
			componentCount = len(resp.SBOM.Components)
		}
		logger.Info("scan completed",
			zap.String("request_id", req.ID),
			zap.Duration("elapsed", elapsed),
			zap.Int("components", componentCount),
			zap.Bool("cache_hit", resp.CacheHit),
		)
		return resp, nil
	}
}

// WithEvents emits lifecycle events to the bus at key pipeline boundaries.
func WithEvents(bus *event.Bus) Processor {
	return func(ctx context.Context, req Request, next Handler) (Response, error) {
		bus.Publish(ctx, event.TopicScanStarted, event.ScanProgressPayload{
			RequestID: req.ID,
			Stage:     "started",
			Percent:   0,
			Message:   fmt.Sprintf("scanning %s", req.Source.Identifier),
		}, req.ID)

		start := time.Now()
		resp, err := next(ctx, req)
		elapsed := time.Since(start)

		if err != nil {
			bus.PublishAsync(ctx, event.TopicScanFailed, event.ScanFailedPayload{
				RequestID: req.ID,
				Stage:     "pipeline",
				Err:       err,
			}, req.ID)
			return resp, err
		}

		componentCount := 0
		if resp.SBOM != nil {
			componentCount = len(resp.SBOM.Components)
		}
		bus.PublishAsync(ctx, event.TopicScanCompleted, event.ScanCompletedPayload{
			RequestID:      req.ID,
			ComponentCount: componentCount,
			Format:         string(req.Format),
			DurationMs:     elapsed.Milliseconds(),
			CacheHit:       resp.CacheHit,
		}, req.ID)

		return resp, nil
	}
}

// WithCache wraps the pipeline with a cache-aside lookup.
// A cache miss passes the request to next; the result is stored before return.
// Cache storage failures are logged but do not fail the request.
func WithCache(cache ports.Cache, bus *event.Bus, logger *zap.Logger) Processor {
	return func(ctx context.Context, req Request, next Handler) (Response, error) {
		key := cacheKey(req)

		cached, err := cache.Get(ctx, key)
		if err != nil {
			// Storage error on read; log and proceed without cache.
			logger.Warn("cache read error; proceeding without cache",
				zap.String("key", key),
				zap.Error(err),
			)
		} else if cached != nil {
			bus.PublishAsync(ctx, event.TopicCacheHit, event.CacheHitPayload{
				RequestID: req.ID,
				CacheKey:  key,
			}, req.ID)
			return Response{SBOM: cached, CacheHit: true}, nil
		} else {
			bus.PublishAsync(ctx, event.TopicCacheMiss, event.CacheMissPayload{
				RequestID: req.ID,
				CacheKey:  key,
			}, req.ID)
		}

		resp, err := next(ctx, req)
		if err != nil {
			return resp, err
		}

		if setErr := cache.Set(ctx, key, resp.SBOM); setErr != nil {
			logger.Warn("cache write error; result still valid",
				zap.String("key", key),
				zap.Error(setErr),
			)
		}
		return resp, nil
	}
}

// WithRetry retries the inner pipeline on transient errors.
// Non-retryable domain errors (validation, auth, not-found) are returned immediately.
// Exponential back-off with a 10-second cap is applied between attempts.
func WithRetry(maxAttempts int, logger *zap.Logger) Processor {
	if maxAttempts <= 0 {
		maxAttempts = 1
	}
	return func(ctx context.Context, req Request, next Handler) (Response, error) {
		var (
			resp Response
			err  error
		)
		for attempt := 1; attempt <= maxAttempts; attempt++ {
			resp, err = next(ctx, req)
			if err == nil {
				return resp, nil
			}
			if !isRetryable(err) || attempt == maxAttempts {
				break
			}
			wait := backoff(attempt)
			logger.Warn("scan failed; retrying",
				zap.String("request_id", req.ID),
				zap.Int("attempt", attempt),
				zap.Int("max_attempts", maxAttempts),
				zap.Duration("wait", wait),
				zap.Error(err),
			)
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return Response{}, ctx.Err()
			}
		}
		return resp, err
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

// cacheKey builds a stable SHA-256 key from the request's identity fields.
func cacheKey(req Request) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s|%s|%s", req.Source.Kind, req.Source.Identifier, req.Format)
	if req.Source.Platform != nil {
		fmt.Fprintf(h, "|%s", req.Source.Platform.String())
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// isRetryable returns true for errors that are worth retrying.
func isRetryable(err error) bool {
	// Context cancellation / deadline exceeded: never retry.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	// Domain errors: only retry transient infrastructure errors.
	if domain.IsKind(err, domain.ErrKindValidation) ||
		domain.IsKind(err, domain.ErrKindNotFound) ||
		domain.IsKind(err, domain.ErrKindAuth) {
		return false
	}
	return true
}

// backoff returns the wait duration for a given attempt (1-indexed).
// Uses exponential back-off capped at 10 seconds.
func backoff(attempt int) time.Duration {
	ms := 100 * (1 << uint(attempt-1)) // 100, 200, 400, 800, …
	d := time.Duration(ms) * time.Millisecond
	const cap = 10 * time.Second
	if d > cap {
		return cap
	}
	return d
}
