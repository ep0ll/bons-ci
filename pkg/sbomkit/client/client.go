// Package client provides a high-level, ergonomic API for SBOM generation.
//
// The Client is the single entry-point most callers should use. It wires all
// default infrastructure (Syft scanner, image + filesystem resolvers,
// CycloneDX/SPDX exporters, in-memory cache) with a fluent option system.
//
// Quick start:
//
//	c, err := client.New()
//	result, err := c.GenerateFromImage(ctx, "ubuntu:22.04", client.WithFormat(domain.FormatSPDXJSON))
//	os.Stdout.Write(result.Encoded)
//
// For full control, construct an Engine directly using the core package.
package client

import (
	"context"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bons/bons-ci/pkg/sbomkit/core"
	"github.com/bons/bons-ci/pkg/sbomkit/core/cache"
	"github.com/bons/bons-ci/pkg/sbomkit/core/domain"
	"github.com/bons/bons-ci/pkg/sbomkit/core/event"
	"github.com/bons/bons-ci/pkg/sbomkit/core/exporter"
	"github.com/bons/bons-ci/pkg/sbomkit/core/pipeline"
	"github.com/bons/bons-ci/pkg/sbomkit/core/ports"
	"github.com/bons/bons-ci/pkg/sbomkit/core/resolver"
	"github.com/bons/bons-ci/pkg/sbomkit/core/scanner"
)

// Client wraps the core Engine and exposes a fluent, source-typed API.
// All methods are safe for concurrent use.
type Client struct {
	engine        *core.Engine
	logger        *zap.Logger
	defaultFormat domain.Format
}

// Result is the output of any Generate* call.
type Result struct {
	// SBOM is the in-memory domain SBOM.
	SBOM *domain.SBOM
	// Encoded is the wire-format bytes in the requested format.
	Encoded []byte
	// Format is the format in which Encoded was produced.
	Format domain.Format
	// CacheHit is true when the result was served from the cache.
	CacheHit bool
	// Duration is the wall-clock time taken for the full operation.
	Duration time.Duration
}

// ClientOption configures the Client at construction time.
type ClientOption func(*clientConfig)

// clientConfig holds the full resolved Client configuration.
type clientConfig struct {
	logger             *zap.Logger
	engineCfg          core.Config
	scannerOpts        scanner.SyftOptions
	cacheTTL           time.Duration
	cacheEnabled       bool
	cacheShards        int
	imageResolverOpts  []resolver.ImageResolverOption
	fsResolverOpts     []resolver.FilesystemResolverOption
	defaultFormat      domain.Format
	eventSubscriptions []eventSubscription
	extraExporters     []ports.Exporter
	extraResolvers     []ports.Resolver
}

type eventSubscription struct {
	topic   event.Topic
	handler event.Handler
}

// ── ClientOption constructors ─────────────────────────────────────────────────

// WithLogger attaches a structured logger to the client.
func WithLogger(l *zap.Logger) ClientOption {
	return func(c *clientConfig) { c.logger = l }
}

// WithCacheTTL sets the TTL for cached SBOMs (default: 24 h).
// Pass 0 to disable expiry.
func WithCacheTTL(ttl time.Duration) ClientOption {
	return func(c *clientConfig) { c.cacheTTL = ttl }
}

// WithCacheDisabled disables the SBOM cache entirely.
func WithCacheDisabled() ClientOption {
	return func(c *clientConfig) { c.cacheEnabled = false }
}

// WithMaxRetries sets the number of retry attempts on transient scan errors (default: 3).
func WithMaxRetries(n int) ClientOption {
	return func(c *clientConfig) { c.engineCfg.MaxRetries = n }
}

// WithDefaultFormat sets the output format used when a Generate call does not
// explicitly specify one (default: CycloneDX JSON).
func WithDefaultFormat(f domain.Format) ClientOption {
	return func(c *clientConfig) { c.defaultFormat = f }
}

// WithImagePullSource configures where Syft pulls images from:
// "registry" (default, direct OCI registry) or "docker" (Docker daemon).
func WithImagePullSource(src string) ClientOption {
	return func(c *clientConfig) { c.scannerOpts.DefaultImagePullSource = src }
}

// WithScanParallelism controls the number of concurrent Syft catalogers (default: NumCPU).
func WithScanParallelism(n int) ClientOption {
	return func(c *clientConfig) { c.scannerOpts.Parallelism = n }
}

// WithRegistryMirror adds a registry mirror mapping.
// e.g. WithRegistryMirror("docker.io", "mirror.corp:5000")
func WithRegistryMirror(registry, mirror string) ClientOption {
	return func(c *clientConfig) {
		c.imageResolverOpts = append(c.imageResolverOpts,
			resolver.WithMirror(registry, mirror))
	}
}

// WithInsecureRegistry allows pulling from registries without TLS.
func WithInsecureRegistry() ClientOption {
	return func(c *clientConfig) {
		c.imageResolverOpts = append(c.imageResolverOpts,
			resolver.WithInsecureRegistry())
	}
}

// WithAllowedScanRoots restricts filesystem scanning to the given path prefixes.
// Attempts to scan paths outside these roots return an auth error.
func WithAllowedScanRoots(roots ...string) ClientOption {
	return func(c *clientConfig) {
		c.fsResolverOpts = append(c.fsResolverOpts,
			resolver.WithAllowedRoots(roots...))
	}
}

// WithEventHandler registers a handler for a specific event topic.
// The handler is called synchronously from the event delivery goroutine;
// avoid blocking operations or spawn a goroutine inside the handler.
func WithEventHandler(topic event.Topic, h event.Handler) ClientOption {
	return func(c *clientConfig) {
		c.eventSubscriptions = append(c.eventSubscriptions, eventSubscription{topic: topic, handler: h})
	}
}

// WithExtraExporter registers an additional Exporter implementation.
// If the format is already registered by a built-in exporter, the extra
// exporter takes precedence.
func WithExtraExporter(ex ports.Exporter) ClientOption {
	return func(c *clientConfig) { c.extraExporters = append(c.extraExporters, ex) }
}

// WithExtraResolver registers an additional Resolver implementation.
// Extra resolvers take precedence over built-in ones for the source kinds
// they accept.
func WithExtraResolver(r ports.Resolver) ClientOption {
	return func(c *clientConfig) { c.extraResolvers = append(c.extraResolvers, r) }
}

// ── Constructor ──────────────────────────────────────────────────────────────

// New constructs a Client with the provided options.
// All infrastructure (scanner, resolvers, exporters, cache) is wired automatically.
//
// Returns an error only if configuration validation fails.
func New(opts ...ClientOption) (*Client, error) {
	cfg := defaultClientConfig()
	for _, o := range opts {
		o(cfg)
	}

	logger := cfg.logger
	if logger == nil {
		logger = zap.NewNop()
	}

	// ── Assemble infrastructure ───────────────────────────────────────────────

	// Scanner.
	syftScanner := scanner.NewSyftScanner(logger, cfg.scannerOpts)

	// Resolvers.
	// Constructed with nil bus here; the real bus is injected after engine
	// creation via resolver.BusInjector.SetBus. The resolvers default to a
	// synchronous no-op bus until injection, so they are always safe to call.
	imgResolver := resolver.NewImageResolver(logger, nil, cfg.imageResolverOpts...)
	fsResolver := resolver.NewFilesystemResolver(logger, nil, cfg.fsResolverOpts...)

	// Extra resolvers take precedence over built-ins (listed first in the slice
	// so buildResolverMap's last-write-wins picks built-ins for uncontested kinds).
	resolvers := make([]ports.Resolver, 0, 2+len(cfg.extraResolvers))
	resolvers = append(resolvers, cfg.extraResolvers...) // extra first → higher priority
	resolvers = append(resolvers, imgResolver, fsResolver)

	// Exporters (extra exporters override built-ins).
	exporters := make([]ports.Exporter, 0, 4+len(cfg.extraExporters))
	exporters = append(exporters, cfg.extraExporters...)
	exporters = append(exporters,
		exporter.NewCycloneDXJSONExporter(logger),
		exporter.NewCycloneDXXMLExporter(logger),
		exporter.NewSPDXJSONExporter(logger),
		exporter.NewSPDXTagValueExporter(logger),
	)

	// Cache.
	var c ports.Cache
	if cfg.cacheEnabled {
		cacheOpts := []cache.MemoryCacheOption{cache.WithShards(cfg.cacheShards)}
		if cfg.cacheTTL > 0 {
			cacheOpts = append(cacheOpts, cache.WithTTL(cfg.cacheTTL))
		}
		c = cache.NewMemoryCache(cacheOpts...)
	}

	// Engine.
	engine := core.New(syftScanner, resolvers, exporters, c, logger, cfg.engineCfg)

	// Event subscriptions — must be registered before any Generate call.
	for _, sub := range cfg.eventSubscriptions {
		engine.Bus().Subscribe(sub.topic, sub.handler)
	}

	// Inject the real event bus into the resolvers now that the engine (and
	// therefore the bus) exists.
	//
	// This uses the exported resolver.BusInjector interface.  An unexported
	// interface method would silently fail the type assertion across packages —
	// the exact bug this design avoids.
	injectResolverBus(imgResolver, engine.Bus())
	injectResolverBus(fsResolver, engine.Bus())

	return &Client{
		engine:        engine,
		logger:        logger,
		defaultFormat: cfg.defaultFormat,
	}, nil
}

// ── Generate API ─────────────────────────────────────────────────────────────

// Option applies a per-request override.
type Option func(*requestConfig)

// requestConfig holds per-call overrides.
type requestConfig struct {
	format      domain.Format
	credentials *domain.Credentials
	platform    *domain.Platform
	scanOpts    ports.ScanOptions
	labels      map[string]string
}

// WithFormat overrides the output format for a single call.
func WithFormat(f domain.Format) Option {
	return func(r *requestConfig) { r.format = f }
}

// WithCredentials provides registry credentials for a single call.
func WithCredentials(creds domain.Credentials) Option {
	return func(r *requestConfig) { r.credentials = &creds }
}

// WithPlatform pins the target platform for multi-arch images.
func WithPlatform(p domain.Platform) Option {
	return func(r *requestConfig) { r.platform = &p }
}

// WithCatalogers sets an explicit cataloger allowlist for a single scan.
func WithCatalogers(ids ...string) Option {
	return func(r *requestConfig) { r.scanOpts.Catalogers = ids }
}

// WithExcludePatterns adds glob exclusion patterns for a single scan.
func WithExcludePatterns(patterns ...string) Option {
	return func(r *requestConfig) { r.scanOpts.ExcludePatterns = patterns }
}

// WithScanLayers enables per-layer component attribution (containers only).
func WithScanLayers() Option {
	return func(r *requestConfig) { r.scanOpts.ScanLayers = true }
}

// WithLabels attaches arbitrary key/value metadata to the source.
func WithLabels(labels map[string]string) Option {
	return func(r *requestConfig) { r.labels = labels }
}

// GenerateFromImage generates an SBOM for a container image reference.
//
//	result, err := c.GenerateFromImage(ctx, "docker.io/ubuntu:22.04")
//	result, err := c.GenerateFromImage(ctx, "ubuntu@sha256:…", WithFormat(domain.FormatSPDXJSON))
//	result, err := c.GenerateFromImage(ctx, "corp.registry/app:latest",
//	    WithCredentials(domain.Credentials{Token: os.Getenv("REG_TOKEN")}),
//	)
func (c *Client) GenerateFromImage(ctx context.Context, imageRef string, opts ...Option) (*Result, error) {
	return c.generate(ctx, domain.Source{
		Kind:       domain.SourceImage,
		Identifier: imageRef,
	}, opts...)
}

// GenerateFromSnapshot generates an SBOM for a container rootfs snapshot
// written to a local directory (e.g. a BuildKit snapshot or overlayfs merge dir).
//
//	result, err := c.GenerateFromSnapshot(ctx, "/var/lib/buildkit/snapshots/abc123")
func (c *Client) GenerateFromSnapshot(ctx context.Context, rootfsPath string, opts ...Option) (*Result, error) {
	return c.generate(ctx, domain.Source{
		Kind:       domain.SourceSnapshot,
		Identifier: rootfsPath,
	}, opts...)
}

// GenerateFromDirectory generates an SBOM for a local filesystem directory.
//
//	result, err := c.GenerateFromDirectory(ctx, "/path/to/project")
func (c *Client) GenerateFromDirectory(ctx context.Context, dirPath string, opts ...Option) (*Result, error) {
	return c.generate(ctx, domain.Source{
		Kind:       domain.SourceDirectory,
		Identifier: dirPath,
	}, opts...)
}

// GenerateFromArchive generates an SBOM for a local tarball.
// Supported extensions: .tar, .tar.gz, .tgz, .tar.bz2, .tar.xz, .tar.zst
//
//	result, err := c.GenerateFromArchive(ctx, "/tmp/image.tar")
func (c *Client) GenerateFromArchive(ctx context.Context, archivePath string, opts ...Option) (*Result, error) {
	return c.generate(ctx, domain.Source{
		Kind:       domain.SourceArchive,
		Identifier: archivePath,
	}, opts...)
}

// GenerateFromOCILayout generates an SBOM from an OCI image layout directory.
// The directory must contain an oci-layout file and an index.json.
//
//	result, err := c.GenerateFromOCILayout(ctx, "/tmp/oci-image-dir")
func (c *Client) GenerateFromOCILayout(ctx context.Context, layoutPath string, opts ...Option) (*Result, error) {
	return c.generate(ctx, domain.Source{
		Kind:       domain.SourceOCILayout,
		Identifier: layoutPath,
	}, opts...)
}

// GenerateFromSource is the escape hatch for callers that build a domain.Source
// directly (e.g. when SourceKind is provided at runtime from configuration).
func (c *Client) GenerateFromSource(ctx context.Context, src domain.Source, opts ...Option) (*Result, error) {
	return c.generate(ctx, src, opts...)
}

// SubscribeEvents registers a handler for the given topic on the engine's bus.
// Returns a subscription ID that can be passed to UnsubscribeEvents.
func (c *Client) SubscribeEvents(topic event.Topic, h event.Handler) string {
	return c.engine.Bus().Subscribe(topic, h)
}

// UnsubscribeEvents removes a previously registered event handler.
func (c *Client) UnsubscribeEvents(subscriptionID string) {
	c.engine.Bus().Unsubscribe(subscriptionID)
}

// Close shuts down all client resources. Must be called when the client is
// no longer needed to prevent goroutine leaks.
func (c *Client) Close() error {
	return c.engine.Close()
}

// ── internal ─────────────────────────────────────────────────────────────────

func (c *Client) generate(ctx context.Context, src domain.Source, opts ...Option) (*Result, error) {
	rcfg := &requestConfig{}
	for _, o := range opts {
		o(rcfg)
	}

	// Apply per-request overrides to source.
	if rcfg.credentials != nil {
		src.Credentials = rcfg.credentials
	}
	if rcfg.platform != nil {
		src.Platform = rcfg.platform
	}
	if len(rcfg.labels) > 0 {
		src.Labels = rcfg.labels
	}

	// Resolve format: per-request > client default > hard-coded fallback.
	format := rcfg.format
	if format == "" {
		format = c.defaultFormat
	}

	req := pipeline.Request{
		ID:     uuid.NewString(),
		Source: src,
		Format: format,
		Opts:   rcfg.scanOpts,
	}

	start := time.Now()
	engineResult, err := c.engine.Generate(ctx, req)
	if err != nil {
		return nil, err
	}

	return &Result{
		SBOM:     engineResult.SBOM,
		Encoded:  engineResult.Encoded,
		Format:   format,
		CacheHit: engineResult.CacheHit,
		Duration: time.Since(start),
	}, nil
}

// defaultClientConfig returns the baseline configuration values.
func defaultClientConfig() *clientConfig {
	return &clientConfig{
		engineCfg: core.DefaultConfig(),
		scannerOpts: scanner.SyftOptions{
			DefaultImagePullSource: "registry",
		},
		cacheTTL:      24 * time.Hour,
		cacheEnabled:  true,
		cacheShards:   16,
		defaultFormat: domain.FormatCycloneDXJSON,
	}
}

// injectResolverBus calls SetBus on r if it implements resolver.BusInjector.
//
// Using the exported resolver.BusInjector interface (with exported method SetBus)
// is required because Go's interface satisfaction for unexported methods is
// restricted to the defining package.  An unexported interface method in package
// client can never be satisfied by a type defined in package resolver, so the
// type assertion would silently always return false.
func injectResolverBus(r ports.Resolver, b *event.Bus) {
	if inj, ok := r.(resolver.BusInjector); ok {
		inj.SetBus(b)
	}
}
