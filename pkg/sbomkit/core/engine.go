// Package core provides the Engine, which is the composition root for the
// SBOM generation system. It wires the event bus, resolvers, pipeline, and
// exporters into a single, coherent orchestrator.
package core

import (
	"bytes"
	"context"
	"fmt"

	"go.uber.org/zap"

	"github.com/bons/bons-ci/pkg/sbomkit/core/domain"
	"github.com/bons/bons-ci/pkg/sbomkit/core/event"
	"github.com/bons/bons-ci/pkg/sbomkit/core/pipeline"
	"github.com/bons/bons-ci/pkg/sbomkit/core/ports"
)

// Config holds top-level Engine tuning parameters.
type Config struct {
	// MaxRetries is the number of additional scan attempts on transient failure.
	// 0 means no retries (single attempt).
	MaxRetries int

	// EnableCache enables the cache-aside processor in the pipeline.
	// Requires a non-nil Cache to be provided at construction.
	EnableCache bool

	// AsyncEventBufferSize is the number of events that can be queued for
	// async delivery. 0 uses synchronous delivery for all events.
	AsyncEventBufferSize int
}

// DefaultConfig returns sensible production defaults.
func DefaultConfig() Config {
	return Config{
		MaxRetries:           3,
		EnableCache:          true,
		AsyncEventBufferSize: 256,
	}
}

// GenerateResult is the successful output of Engine.Generate.
type GenerateResult struct {
	// SBOM is the domain representation of the generated SBOM.
	SBOM *domain.SBOM
	// Encoded holds the serialised SBOM in the requested wire format.
	Encoded []byte
	// CacheHit is true when the result was served from cache.
	CacheHit bool
}

// Engine orchestrates source resolution, scanning, and export.
// It owns the event bus and the pipeline; callers interact with it via Generate.
//
// Construct with New; do not copy after first use.
type Engine struct {
	scanner   ports.Scanner
	resolvers map[domain.SourceKind]ports.Resolver
	exporters map[domain.Format]ports.Exporter
	cache     ports.Cache // may be nil
	bus       *event.Bus
	pipeline  *pipeline.Pipeline
	logger    *zap.Logger
	cfg       Config
}

// New constructs an Engine.
//
// resolvers: each Resolver is registered for every source kind it Accepts.
// exporters: each Exporter is registered for the Format it produces.
// cache:     may be nil; when nil EnableCache is effectively false.
// logger:    may be nil; falls back to zap.NewNop().
func New(
	scanner ports.Scanner,
	resolvers []ports.Resolver,
	exporters []ports.Exporter,
	cache ports.Cache,
	logger *zap.Logger,
	cfg Config,
) *Engine {
	if logger == nil {
		logger = zap.NewNop()
	}

	bus := event.NewBus(
		cfg.AsyncEventBufferSize,
		event.WithBusLogger(logger),
	)

	resolverMap := buildResolverMap(resolvers)
	exporterMap := buildExporterMap(exporters)

	e := &Engine{
		scanner:   scanner,
		resolvers: resolverMap,
		exporters: exporterMap,
		cache:     cache,
		bus:       bus,
		logger:    logger,
		cfg:       cfg,
	}
	e.pipeline = e.buildPipeline()
	return e
}

// Generate orchestrates the full SBOM generation pipeline for req:
//  1. Resolve the source (validate, normalise, pull if needed).
//  2. Emit TopicScanRequested.
//  3. Execute the pipeline (cache-check → retry → scan).
//  4. Export to the requested format.
//  5. Emit TopicExportCompleted.
func (e *Engine) Generate(ctx context.Context, req pipeline.Request) (*GenerateResult, error) {
	if err := e.validateRequest(req); err != nil {
		return nil, err
	}

	// ── 1. Resolve source ────────────────────────────────────────────────────
	resolver, ok := e.resolvers[req.Source.Kind]
	if !ok {
		return nil, domain.Newf(domain.ErrKindResolving, nil,
			"no resolver registered for source kind %q", req.Source.Kind)
	}
	resolved, err := resolver.Resolve(ctx, req.Source)
	if err != nil {
		return nil, err // already a domain.Error
	}
	req.Source = resolved

	// ── 2. Announce ──────────────────────────────────────────────────────────
	e.bus.Publish(ctx, event.TopicScanRequested, event.ScanRequestedPayload{
		RequestID:  req.ID,
		Source:     req.Source.Identifier,
		SourceKind: string(req.Source.Kind),
		Format:     string(req.Format),
	}, req.ID)

	// ── 3. Execute pipeline ──────────────────────────────────────────────────
	resp, err := e.pipeline.Execute(ctx, req)
	if err != nil {
		return nil, err
	}

	// ── 4. Export ────────────────────────────────────────────────────────────
	exporter, ok := e.exporters[req.Format]
	if !ok {
		return nil, domain.Newf(domain.ErrKindExporting, nil,
			"no exporter registered for format %q", req.Format)
	}

	e.bus.Publish(ctx, event.TopicExportStarted, event.ScanProgressPayload{
		RequestID: req.ID,
		Stage:     "exporting",
		Message:   fmt.Sprintf("serialising to %s", req.Format),
	}, req.ID)

	var buf bytes.Buffer
	if err := exporter.Export(ctx, resp.SBOM, &buf); err != nil {
		e.bus.PublishAsync(ctx, event.TopicExportFailed, event.ScanFailedPayload{
			RequestID: req.ID,
			Stage:     "export",
			Err:       err,
		}, req.ID)
		return nil, err
	}
	encoded := buf.Bytes()

	// ── 5. Export completed ──────────────────────────────────────────────────
	e.bus.PublishAsync(ctx, event.TopicExportCompleted, event.ExportCompletedPayload{
		RequestID: req.ID,
		Format:    string(req.Format),
		ByteCount: len(encoded),
	}, req.ID)

	return &GenerateResult{
		SBOM:     resp.SBOM,
		Encoded:  encoded,
		CacheHit: resp.CacheHit,
	}, nil
}

// Bus exposes the event bus for external subscriber registration.
// Call this before Generate to avoid missing early lifecycle events.
func (e *Engine) Bus() *event.Bus { return e.bus }

// Close shuts down the engine's resources (event bus async queue, scanner).
// It is safe to call more than once.
func (e *Engine) Close() error {
	e.bus.Close()
	if err := e.scanner.Close(); err != nil {
		return fmt.Errorf("closing scanner: %w", err)
	}
	return nil
}

// ── internal ─────────────────────────────────────────────────────────────────

func (e *Engine) buildPipeline() *pipeline.Pipeline {
	// Innermost handler: the scanner.
	coreHandler := pipeline.Handler(func(ctx context.Context, req pipeline.Request) (pipeline.Response, error) {
		sbom, err := e.scanner.Scan(ctx, req.Source, req.Opts)
		if err != nil {
			return pipeline.Response{}, err
		}
		return pipeline.Response{SBOM: sbom}, nil
	})

	// Build the processor stack (outermost listed first).
	var processors []pipeline.Processor
	processors = append(processors, pipeline.WithLogging(e.logger))
	processors = append(processors, pipeline.WithEvents(e.bus))
	if e.cfg.EnableCache && e.cache != nil {
		processors = append(processors, pipeline.WithCache(e.cache, e.bus, e.logger))
	}
	if e.cfg.MaxRetries > 0 {
		processors = append(processors, pipeline.WithRetry(e.cfg.MaxRetries, e.logger))
	}

	return pipeline.New(coreHandler, processors...)
}

func (e *Engine) validateRequest(req pipeline.Request) error {
	if req.ID == "" {
		return domain.New(domain.ErrKindValidation, "request ID must not be empty", nil)
	}
	if req.Source.Identifier == "" {
		return domain.New(domain.ErrKindValidation, "source identifier must not be empty", nil)
	}
	if !domain.IsKnownFormat(req.Format) {
		return domain.Newf(domain.ErrKindValidation, nil, "unknown format %q", req.Format)
	}
	return nil
}

// buildResolverMap indexes resolvers by all source kinds they accept.
func buildResolverMap(resolvers []ports.Resolver) map[domain.SourceKind]ports.Resolver {
	m := make(map[domain.SourceKind]ports.Resolver, len(resolvers)*2)
	all := []domain.SourceKind{
		domain.SourceImage,
		domain.SourceSnapshot,
		domain.SourceDirectory,
		domain.SourceArchive,
		domain.SourceOCILayout,
	}
	for _, r := range resolvers {
		for _, k := range all {
			if r.Accepts(k) {
				m[k] = r
			}
		}
	}
	return m
}

// buildExporterMap indexes exporters by their format.
func buildExporterMap(exporters []ports.Exporter) map[domain.Format]ports.Exporter {
	m := make(map[domain.Format]ports.Exporter, len(exporters))
	for _, ex := range exporters {
		m[ex.Format()] = ex
	}
	return m
}
