package core_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"

	"github.com/bons/bons-ci/pkg/sbomkit/core"
	"github.com/bons/bons-ci/pkg/sbomkit/core/domain"
	"github.com/bons/bons-ci/pkg/sbomkit/core/event"
	"github.com/bons/bons-ci/pkg/sbomkit/core/pipeline"
	"github.com/bons/bons-ci/pkg/sbomkit/core/ports"
)

// ── Fakes ─────────────────────────────────────────────────────────────────────

// fakeScanner is a configurable ports.Scanner test double.
type fakeScanner struct {
	name   string
	result *domain.SBOM
	err    error
	calls  atomic.Int32
	closed bool
}

func (f *fakeScanner) Name() string { return f.name }
func (f *fakeScanner) Scan(_ context.Context, _ domain.Source, _ ports.ScanOptions) (*domain.SBOM, error) {
	f.calls.Add(1)
	return f.result, f.err
}
func (f *fakeScanner) Close() error { f.closed = true; return nil }

// fakeResolver is a configurable ports.Resolver test double.
type fakeResolver struct {
	accepts domain.SourceKind
	mutate  func(domain.Source) (domain.Source, error) // nil → identity
}

func (f *fakeResolver) Accepts(k domain.SourceKind) bool { return k == f.accepts }
func (f *fakeResolver) Resolve(_ context.Context, src domain.Source) (domain.Source, error) {
	if f.mutate != nil {
		return f.mutate(src)
	}
	return src, nil
}

// fakeExporter is a configurable ports.Exporter test double.
type fakeExporter struct {
	format domain.Format
	err    error
	calls  atomic.Int32
}

func (f *fakeExporter) Format() domain.Format { return f.format }
func (f *fakeExporter) Export(_ context.Context, _ *domain.SBOM, w io.Writer) error {
	f.calls.Add(1)
	if f.err != nil {
		return f.err
	}
	_, err := w.Write([]byte(`{"fake":"sbom"}`))
	return err
}

// fakeCache is a thread-safe in-memory cache test double.
type fakeCache struct {
	mu      sync.Mutex
	entries map[string]*domain.SBOM
	getErr  error
	setErr  error
	hits    atomic.Int32
	misses  atomic.Int32
}

func newFakeCache() *fakeCache { return &fakeCache{entries: make(map[string]*domain.SBOM)} }

func (c *fakeCache) Get(_ context.Context, key string) (*domain.SBOM, error) {
	if c.getErr != nil {
		return nil, c.getErr
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if v, ok := c.entries[key]; ok {
		c.hits.Add(1)
		return v, nil
	}
	c.misses.Add(1)
	return nil, nil
}

func (c *fakeCache) Set(_ context.Context, key string, sbom *domain.SBOM) error {
	if c.setErr != nil {
		return c.setErr
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = sbom
	return nil
}

func (c *fakeCache) Delete(_ context.Context, key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, key)
	return nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

func buildEngine(t *testing.T,
	sc ports.Scanner,
	res []ports.Resolver,
	ex []ports.Exporter,
	ca ports.Cache,
	cfg core.Config,
) *core.Engine {
	t.Helper()
	logger := zaptest.NewLogger(t)
	eng := core.New(sc, res, ex, ca, logger, cfg)
	t.Cleanup(func() { _ = eng.Close() })
	return eng
}

func minimalRequest(kind domain.SourceKind, id, format string) pipeline.Request {
	return pipeline.Request{
		ID:     id,
		Source: domain.Source{Kind: kind, Identifier: id},
		Format: domain.Format(format),
	}
}

func makeSBOM(components int) *domain.SBOM {
	sbom := &domain.SBOM{ID: "test-sbom"}
	for i := 0; i < components; i++ {
		sbom.Components = append(sbom.Components, domain.Component{
			Name:    "pkg",
			Version: "1.0.0",
		})
	}
	return sbom
}

// ── Engine tests ──────────────────────────────────────────────────────────────

func TestEngine_Generate_HappyPath(t *testing.T) {
	sc := &fakeScanner{name: "fake", result: makeSBOM(5)}
	ex := &fakeExporter{format: domain.FormatCycloneDXJSON}
	res := []ports.Resolver{&fakeResolver{accepts: domain.SourceImage}}

	cfg := core.DefaultConfig()
	cfg.MaxRetries = 0
	cfg.EnableCache = false

	eng := buildEngine(t, sc, res, []ports.Exporter{ex}, nil, cfg)

	result, err := eng.Generate(context.Background(), minimalRequest(
		domain.SourceImage, "docker.io/ubuntu:22.04", string(domain.FormatCycloneDXJSON),
	))

	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if result == nil || result.SBOM == nil {
		t.Fatal("expected non-nil result and SBOM")
	}
	if len(result.Encoded) == 0 {
		t.Error("expected non-empty encoded output")
	}
	if sc.calls.Load() != 1 {
		t.Errorf("expected scanner called once, got %d", sc.calls.Load())
	}
	if ex.calls.Load() != 1 {
		t.Errorf("expected exporter called once, got %d", ex.calls.Load())
	}
}

func TestEngine_Generate_ScannerError_ReturnsError(t *testing.T) {
	scanErr := domain.New(domain.ErrKindScanning, "simulated scan failure", nil)
	sc := &fakeScanner{name: "fake", err: scanErr}
	res := []ports.Resolver{&fakeResolver{accepts: domain.SourceDirectory}}

	cfg := core.DefaultConfig()
	cfg.MaxRetries = 0
	cfg.EnableCache = false

	eng := buildEngine(t, sc, res, []ports.Exporter{
		&fakeExporter{format: domain.FormatCycloneDXJSON},
	}, nil, cfg)

	_, err := eng.Generate(context.Background(), minimalRequest(
		domain.SourceDirectory, "/app", string(domain.FormatCycloneDXJSON),
	))

	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !domain.IsKind(err, domain.ErrKindScanning) {
		t.Errorf("expected scanning error, got: %v", err)
	}
}

func TestEngine_Generate_ValidationError_EmptyRequestID(t *testing.T) {
	sc := &fakeScanner{name: "fake", result: makeSBOM(1)}
	eng := buildEngine(t, sc, nil, []ports.Exporter{
		&fakeExporter{format: domain.FormatCycloneDXJSON},
	}, nil, core.DefaultConfig())

	req := pipeline.Request{
		ID:     "", // invalid
		Source: domain.Source{Kind: domain.SourceImage, Identifier: "ubuntu:22.04"},
		Format: domain.FormatCycloneDXJSON,
	}
	_, err := eng.Generate(context.Background(), req)

	if err == nil {
		t.Fatal("expected validation error")
	}
	if !domain.IsKind(err, domain.ErrKindValidation) {
		t.Errorf("expected validation error kind, got: %v", err)
	}
}

func TestEngine_Generate_UnknownFormat_ReturnsError(t *testing.T) {
	sc := &fakeScanner{name: "fake", result: makeSBOM(1)}
	res := []ports.Resolver{&fakeResolver{accepts: domain.SourceImage}}
	eng := buildEngine(t, sc, res, []ports.Exporter{
		&fakeExporter{format: domain.FormatCycloneDXJSON},
	}, nil, core.DefaultConfig())

	req := pipeline.Request{
		ID:     "req-1",
		Source: domain.Source{Kind: domain.SourceImage, Identifier: "ubuntu:22.04"},
		Format: "application/unknown-format", // not a known domain.Format
	}
	_, err := eng.Generate(context.Background(), req)

	if err == nil {
		t.Fatal("expected error for unknown format")
	}
	if !domain.IsKind(err, domain.ErrKindValidation) {
		t.Errorf("expected validation error for unknown format, got: %v", err)
	}
}

func TestEngine_Generate_NoResolverForKind_ReturnsError(t *testing.T) {
	sc := &fakeScanner{name: "fake", result: makeSBOM(1)}
	// Resolver only accepts directories; request is for an image.
	res := []ports.Resolver{&fakeResolver{accepts: domain.SourceDirectory}}
	cfg := core.DefaultConfig()
	cfg.EnableCache = false

	eng := buildEngine(t, sc, res, []ports.Exporter{
		&fakeExporter{format: domain.FormatCycloneDXJSON},
	}, nil, cfg)

	req := minimalRequest(domain.SourceImage, "ubuntu:22.04", string(domain.FormatCycloneDXJSON))
	_, err := eng.Generate(context.Background(), req)

	if err == nil {
		t.Fatal("expected error when no resolver matches")
	}
	if !domain.IsKind(err, domain.ErrKindResolving) {
		t.Errorf("expected resolving error, got: %v", err)
	}
}

func TestEngine_Generate_ResolverError_Propagates(t *testing.T) {
	sc := &fakeScanner{name: "fake", result: makeSBOM(1)}
	resolveErr := domain.New(domain.ErrKindNotFound, "image not found", nil)
	res := []ports.Resolver{&fakeResolver{
		accepts: domain.SourceImage,
		mutate:  func(_ domain.Source) (domain.Source, error) { return domain.Source{}, resolveErr },
	}}

	cfg := core.DefaultConfig()
	cfg.EnableCache = false
	cfg.MaxRetries = 0

	eng := buildEngine(t, sc, res, []ports.Exporter{
		&fakeExporter{format: domain.FormatCycloneDXJSON},
	}, nil, cfg)

	_, err := eng.Generate(context.Background(), minimalRequest(
		domain.SourceImage, "missing:latest", string(domain.FormatCycloneDXJSON),
	))

	if !domain.IsKind(err, domain.ErrKindNotFound) {
		t.Errorf("expected not-found error, got: %v", err)
	}
}

// ── Cache tests ───────────────────────────────────────────────────────────────

func TestEngine_Generate_CacheHit_SkipsScanner(t *testing.T) {
	sc := &fakeScanner{name: "fake", result: makeSBOM(3)}
	res := []ports.Resolver{&fakeResolver{accepts: domain.SourceImage}}
	ex := &fakeExporter{format: domain.FormatCycloneDXJSON}
	fc := newFakeCache()

	cfg := core.DefaultConfig()
	cfg.EnableCache = true
	cfg.MaxRetries = 0

	eng := buildEngine(t, sc, res, []ports.Exporter{ex}, fc, cfg)
	req := minimalRequest(domain.SourceImage, "ubuntu:22.04", string(domain.FormatCycloneDXJSON))

	// First call — populates cache.
	r1, err := eng.Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("first generate failed: %v", err)
	}
	if r1.CacheHit {
		t.Error("first call should not be a cache hit")
	}
	if sc.calls.Load() != 1 {
		t.Errorf("scanner should have been called once, got %d", sc.calls.Load())
	}

	// Second call — should hit cache.
	r2, err := eng.Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("second generate failed: %v", err)
	}
	if !r2.CacheHit {
		t.Error("second call should be a cache hit")
	}
	if sc.calls.Load() != 1 {
		t.Errorf("scanner should still have been called only once, got %d", sc.calls.Load())
	}
}

func TestEngine_Generate_CacheSetError_DoesNotFailScan(t *testing.T) {
	sc := &fakeScanner{name: "fake", result: makeSBOM(2)}
	res := []ports.Resolver{&fakeResolver{accepts: domain.SourceImage}}
	ex := &fakeExporter{format: domain.FormatCycloneDXJSON}
	fc := &fakeCache{
		entries: make(map[string]*domain.SBOM),
		setErr:  errors.New("redis timeout"),
	}

	cfg := core.DefaultConfig()
	cfg.EnableCache = true
	cfg.MaxRetries = 0

	eng := buildEngine(t, sc, res, []ports.Exporter{ex}, fc, cfg)
	req := minimalRequest(domain.SourceImage, "ubuntu:22.04", string(domain.FormatCycloneDXJSON))

	result, err := eng.Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("cache write error should not propagate: %v", err)
	}
	if result == nil || result.SBOM == nil {
		t.Error("expected valid SBOM despite cache write failure")
	}
}

// ── Retry tests ───────────────────────────────────────────────────────────────

func TestEngine_Generate_RetryOnTransientError(t *testing.T) {
	var callCount atomic.Int32
	sc := &fakeScanner{name: "fake"}

	// Fail twice, succeed on third attempt.
	realScan := func(_ context.Context, _ domain.Source, _ ports.ScanOptions) (*domain.SBOM, error) {
		n := callCount.Add(1)
		if n < 3 {
			return nil, domain.New(domain.ErrKindScanning, "transient error", nil)
		}
		return makeSBOM(2), nil
	}
	_ = realScan // used via override below

	sc.result = makeSBOM(2) // final success value

	// We use a counting wrapper scanner to simulate transient failure.
	wrapper := &countingScanner{
		inner: sc,
		failN: 2, // fail first 2 attempts
	}

	res := []ports.Resolver{&fakeResolver{accepts: domain.SourceImage}}
	ex := &fakeExporter{format: domain.FormatCycloneDXJSON}

	cfg := core.DefaultConfig()
	cfg.MaxRetries = 3
	cfg.EnableCache = false

	eng := buildEngine(t, wrapper, res, []ports.Exporter{ex}, nil, cfg)
	req := minimalRequest(domain.SourceImage, "ubuntu:22.04", string(domain.FormatCycloneDXJSON))

	result, err := eng.Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("expected retry to succeed: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if wrapper.calls.Load() != 3 {
		t.Errorf("expected 3 scan attempts (2 failures + 1 success), got %d", wrapper.calls.Load())
	}
}

// countingScanner wraps a scanner and injects failures for the first N calls.
type countingScanner struct {
	inner ports.Scanner
	failN int32
	calls atomic.Int32
}

func (s *countingScanner) Name() string { return s.inner.Name() }
func (s *countingScanner) Close() error { return s.inner.Close() }
func (s *countingScanner) Scan(ctx context.Context, src domain.Source, opts ports.ScanOptions) (*domain.SBOM, error) {
	n := s.calls.Add(1)
	if n <= s.failN {
		return nil, domain.New(domain.ErrKindScanning, "injected transient failure", nil)
	}
	return s.inner.Scan(ctx, src, opts)
}

// ── Event bus tests ───────────────────────────────────────────────────────────

func TestEngine_Generate_EmitsLifecycleEvents(t *testing.T) {
	sc := &fakeScanner{name: "fake", result: makeSBOM(2)}
	res := []ports.Resolver{&fakeResolver{accepts: domain.SourceImage}}
	ex := &fakeExporter{format: domain.FormatCycloneDXJSON}

	cfg := core.DefaultConfig()
	cfg.EnableCache = false
	cfg.MaxRetries = 0
	cfg.AsyncEventBufferSize = 0 // synchronous for test determinism

	eng := buildEngine(t, sc, res, []ports.Exporter{ex}, nil, cfg)

	var (
		mu     sync.Mutex
		topics []event.Topic
	)
	record := func(e event.Event) error {
		mu.Lock()
		topics = append(topics, e.Topic)
		mu.Unlock()
		return nil
	}

	for _, topic := range []event.Topic{
		event.TopicScanRequested,
		event.TopicScanStarted,
		event.TopicScanCompleted,
		event.TopicExportStarted,
		event.TopicExportCompleted,
	} {
		eng.Bus().Subscribe(topic, record)
	}

	req := minimalRequest(domain.SourceImage, "ubuntu:22.04", string(domain.FormatCycloneDXJSON))
	_, err := eng.Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}

	// Allow async events to drain.
	time.Sleep(20 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()

	want := map[event.Topic]bool{
		event.TopicScanRequested:   false,
		event.TopicScanStarted:     false,
		event.TopicScanCompleted:   false,
		event.TopicExportStarted:   false,
		event.TopicExportCompleted: false,
	}
	for _, got := range topics {
		want[got] = true
	}
	for topic, seen := range want {
		if !seen {
			t.Errorf("expected event %q to have been emitted", topic)
		}
	}
}

func TestBus_Subscribe_Unsubscribe(t *testing.T) {
	bus := event.NewBus(0)
	defer bus.Close()

	var count atomic.Int32
	id := bus.Subscribe(event.TopicScanCompleted, func(_ event.Event) error {
		count.Add(1)
		return nil
	})

	bus.Publish(context.Background(), event.TopicScanCompleted, nil, "corr-1")
	if count.Load() != 1 {
		t.Errorf("expected 1 delivery, got %d", count.Load())
	}

	bus.Unsubscribe(id)
	bus.Publish(context.Background(), event.TopicScanCompleted, nil, "corr-2")
	if count.Load() != 1 {
		t.Errorf("expected still 1 delivery after unsubscribe, got %d", count.Load())
	}
}

func TestBus_Predicate_FiltersEvents(t *testing.T) {
	bus := event.NewBus(0)
	defer bus.Close()

	var count atomic.Int32
	// Only handle events whose payload has RequestID == "target".
	bus.Subscribe(event.TopicScanCompleted, func(_ event.Event) error {
		count.Add(1)
		return nil
	}, func(e event.Event) bool {
		p, ok := e.Payload.(event.ScanCompletedPayload)
		return ok && p.RequestID == "target"
	})

	bus.Publish(context.Background(), event.TopicScanCompleted,
		event.ScanCompletedPayload{RequestID: "other"}, "corr-1")
	if count.Load() != 0 {
		t.Errorf("predicate should have filtered the event")
	}

	bus.Publish(context.Background(), event.TopicScanCompleted,
		event.ScanCompletedPayload{RequestID: "target"}, "corr-2")
	if count.Load() != 1 {
		t.Errorf("expected 1 delivery for matching predicate, got %d", count.Load())
	}
}

func TestBus_Close_DropsSubsequentPublishes(t *testing.T) {
	bus := event.NewBus(0)
	var count atomic.Int32
	bus.Subscribe(event.TopicScanCompleted, func(_ event.Event) error {
		count.Add(1)
		return nil
	})
	bus.Close()
	bus.Publish(context.Background(), event.TopicScanCompleted, nil, "after-close")
	if count.Load() != 0 {
		t.Errorf("expected no events after close, got %d", count.Load())
	}
}

func TestBus_Close_Idempotent(t *testing.T) {
	bus := event.NewBus(64)
	bus.Close()
	bus.Close() // must not panic
}

// ── Pipeline middleware tests ─────────────────────────────────────────────────

func TestPipeline_Chain_OrderIsPreserved(t *testing.T) {
	var order []string
	make := func(name string) pipeline.Processor {
		return func(ctx context.Context, req pipeline.Request, next pipeline.Handler) (pipeline.Response, error) {
			order = append(order, name+":before")
			resp, err := next(ctx, req)
			order = append(order, name+":after")
			return resp, err
		}
	}

	final := pipeline.Handler(func(_ context.Context, _ pipeline.Request) (pipeline.Response, error) {
		order = append(order, "final")
		return pipeline.Response{SBOM: &domain.SBOM{}}, nil
	})

	p := pipeline.New(final, make("A"), make("B"), make("C"))
	_, _ = p.Execute(context.Background(), pipeline.Request{
		ID:     "x",
		Source: domain.Source{Kind: domain.SourceImage, Identifier: "ubuntu:22.04"},
		Format: domain.FormatCycloneDXJSON,
	})

	want := []string{
		"A:before", "B:before", "C:before",
		"final",
		"C:after", "B:after", "A:after",
	}
	if len(order) != len(want) {
		t.Fatalf("execution order mismatch\nwant: %v\ngot:  %v", want, order)
	}
	for i, w := range want {
		if order[i] != w {
			t.Errorf("step %d: want %q, got %q", i, w, order[i])
		}
	}
}

func TestPipeline_WithCache_ShortCircuitsOnHit(t *testing.T) {
	cachedSBOM := makeSBOM(7)
	fc := newFakeCache()
	// Pre-populate cache. Key derivation must match the processor's cacheKey function.
	// We call Generate twice; on second call the cache should be warm.

	logger := zaptest.NewLogger(t)
	bus := event.NewBus(0)
	defer bus.Close()

	var scanCalls atomic.Int32
	coreHandler := pipeline.Handler(func(_ context.Context, _ pipeline.Request) (pipeline.Response, error) {
		scanCalls.Add(1)
		return pipeline.Response{SBOM: cachedSBOM}, nil
	})

	p := pipeline.New(coreHandler,
		pipeline.WithCache(fc, bus, logger),
	)

	req := pipeline.Request{
		ID:     "r1",
		Source: domain.Source{Kind: domain.SourceImage, Identifier: "ubuntu:22.04"},
		Format: domain.FormatCycloneDXJSON,
	}

	// First call: miss → scan → store.
	r1, err := p.Execute(context.Background(), req)
	if err != nil || r1.CacheHit {
		t.Fatalf("first call: err=%v cacheHit=%v", err, r1.CacheHit)
	}

	// Second call with same request: should hit cache.
	req.ID = "r2" // different request ID, same cache key
	r2, err := p.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("second call failed: %v", err)
	}
	if !r2.CacheHit {
		t.Error("expected cache hit on second call")
	}
	if scanCalls.Load() != 1 {
		t.Errorf("expected scanner called only once, got %d", scanCalls.Load())
	}
}

// ── Domain error tests ────────────────────────────────────────────────────────

func TestDomainError_IsKind(t *testing.T) {
	err := domain.Newf(domain.ErrKindAuth, nil, "unauthorized: %s", "registry.corp")

	if !domain.IsKind(err, domain.ErrKindAuth) {
		t.Error("expected IsKind(auth) == true")
	}
	if domain.IsKind(err, domain.ErrKindScanning) {
		t.Error("expected IsKind(scanning) == false")
	}
}

func TestDomainError_Wrapping(t *testing.T) {
	cause := errors.New("network timeout")
	wrapped := domain.New(domain.ErrKindScanning, "scan failed", cause)

	if !errors.Is(wrapped, cause) {
		t.Error("errors.Is should find the wrapped cause")
	}
}

// ── Engine.Close tests ────────────────────────────────────────────────────────

func TestEngine_Close_ClosesScanner(t *testing.T) {
	sc := &fakeScanner{name: "fake", result: makeSBOM(1)}
	logger, _ := zap.NewDevelopment()
	eng := core.New(sc, nil, nil, nil, logger, core.DefaultConfig())

	if err := eng.Close(); err != nil {
		t.Fatalf("Close() returned error: %v", err)
	}
	if !sc.closed {
		t.Error("expected scanner to be closed")
	}
}

// ── Byte output integrity ─────────────────────────────────────────────────────

func TestFakeExporter_WritesExpectedBytes(t *testing.T) {
	ex := &fakeExporter{format: domain.FormatCycloneDXJSON}
	var buf bytes.Buffer
	err := ex.Export(context.Background(), &domain.SBOM{}, &buf)
	if err != nil {
		t.Fatalf("unexpected export error: %v", err)
	}
	if !bytes.Contains(buf.Bytes(), []byte("sbom")) {
		t.Errorf("expected output to contain 'sbom', got: %s", buf.String())
	}
}

// ── Bus nil-safety ────────────────────────────────────────────────────────────

func TestBus_NilReceiver_PublishIsNoOp(t *testing.T) {
	var b *event.Bus // nil
	// Must not panic.
	b.Publish(context.Background(), event.TopicScanCompleted, nil, "corr")
}

func TestBus_NilReceiver_PublishAsyncIsNoOp(t *testing.T) {
	var b *event.Bus // nil
	if got := b.PublishAsync(context.Background(), event.TopicScanCompleted, nil, "corr"); got {
		t.Error("expected false from nil bus PublishAsync")
	}
}
