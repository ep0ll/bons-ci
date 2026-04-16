package core_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bons/bons-ci/plugins/exporter/core"
	"github.com/bons/bons-ci/plugins/exporter/internal/testutil"
)

// ════════════════════════════════════════════════════════════════════
// shared test doubles
// ════════════════════════════════════════════════════════════════════

type noopReporter struct{}

func (n *noopReporter) Start(_ context.Context, _, _ string)          {}
func (n *noopReporter) Update(_ context.Context, _ string, _ int)     {}
func (n *noopReporter) Complete(_ context.Context, _ string, _ error) {}
func (n *noopReporter) Close() error                                  { return nil }

var _ core.ProgressReporter = &noopReporter{}

type stubExporter struct {
	t    core.ExporterType
	opts core.Options
}

func (s *stubExporter) Type() core.ExporterType { return s.t }
func (s *stubExporter) Resolve(_ context.Context, opts core.Options) (core.ExporterInstance, error) {
	s.opts = opts.Clone()
	return &stubInstance{}, nil
}

type stubInstance struct {
	result   *core.ExportResult
	finalize core.FinalizeFunc
	err      error
}

func (s *stubInstance) Export(_ context.Context, _ *core.ExportRequest) (*core.ExportResult, core.FinalizeFunc, error) {
	if s.err != nil {
		return nil, nil, s.err
	}
	r := s.result
	if r == nil {
		r = &core.ExportResult{}
	}
	return r, s.finalize, nil
}

// recordingTransformer appends its name to artifact.Labels["order"].
type recordingTransformer struct {
	name     string
	priority int
	failWith error
}

func (r *recordingTransformer) Name() string  { return r.name }
func (r *recordingTransformer) Priority() int { return r.priority }
func (r *recordingTransformer) Transform(_ context.Context, a *core.Artifact) (*core.Artifact, error) {
	if r.failWith != nil {
		return nil, r.failWith
	}
	clone := a.Clone()
	if clone.Labels == nil {
		clone.Labels = make(map[string]string)
	}
	if v := clone.Labels["order"]; v == "" {
		clone.Labels["order"] = r.name
	} else {
		clone.Labels["order"] = v + "," + r.name
	}
	return clone, nil
}

// capturingInstance records the last received request.
type capturingInstance struct {
	received *core.ExportRequest
	err      error
	finalize core.FinalizeFunc
}

func (c *capturingInstance) Export(_ context.Context, req *core.ExportRequest) (*core.ExportResult, core.FinalizeFunc, error) {
	c.received = req
	if c.err != nil {
		return nil, nil, c.err
	}
	return &core.ExportResult{}, c.finalize, nil
}

func validRequest(t *testing.T) *core.ExportRequest {
	t.Helper()
	req, err := core.NewExportRequest().
		WithSessionID("test-session").
		WithArtifact(&core.Artifact{Kind: core.ArtifactKindContainerImage}).
		WithReporter(&noopReporter{}).
		Build()
	if err != nil {
		t.Fatalf("validRequest: %v", err)
	}
	return req
}

// ════════════════════════════════════════════════════════════════════
// DIGEST
// ════════════════════════════════════════════════════════════════════

func TestNewDigest_IsDeterministic(t *testing.T) {
	t.Parallel()
	data := []byte("hello world")
	d1, d2 := core.NewDigest(data), core.NewDigest(data)
	testutil.Equal(t, d1, d2, "same data must produce same digest")
	testutil.False(t, d1.IsZero(), "digest of non-empty data must not be zero")
}

func TestNewDigest_DifferentDataProducesDifferentDigest(t *testing.T) {
	t.Parallel()
	testutil.NotEqual(t, core.NewDigest([]byte("a")), core.NewDigest([]byte("b")),
		"different data must not produce identical digests")
}

func TestDigest_StringFormat(t *testing.T) {
	t.Parallel()
	s := core.NewDigest([]byte("test")).String()
	if !strings.HasPrefix(s, "sha256:") {
		t.Errorf("digest must start with 'sha256:', got %q", s)
	}
	testutil.Equal(t, len(s), len("sha256:")+64, "digest string length")
}

func TestParseDigest_RoundTrip(t *testing.T) {
	t.Parallel()
	orig := core.NewDigest([]byte("round-trip"))
	parsed, err := core.ParseDigest(orig.String())
	testutil.NoError(t, err)
	testutil.Equal(t, parsed, orig, "round-trip digest")
}

func TestParseDigest_InvalidFormat(t *testing.T) {
	t.Parallel()
	for _, bad := range []string{"", "nocolon", ":nohex", "algo:"} {
		if _, err := core.ParseDigest(bad); err == nil {
			t.Errorf("ParseDigest(%q): expected error", bad)
		}
	}
}

func TestDigest_IsZero(t *testing.T) {
	t.Parallel()
	var z core.Digest
	testutil.True(t, z.IsZero(), "zero Digest.IsZero() must be true")
	testutil.Equal(t, z.String(), "", "zero Digest.String() must be empty")
}

// ════════════════════════════════════════════════════════════════════
// PLATFORM
// ════════════════════════════════════════════════════════════════════

func TestPlatform_String(t *testing.T) {
	t.Parallel()
	cases := []struct {
		p    core.Platform
		want string
	}{
		{core.Platform{OS: "linux", Architecture: "amd64"}, "linux/amd64"},
		{core.Platform{OS: "linux", Architecture: "arm64", Variant: "v8"}, "linux/arm64/v8"},
	}
	for _, tc := range cases {
		testutil.Equal(t, tc.p.String(), tc.want, "Platform.String")
	}
}

func TestPlatform_Normalize(t *testing.T) {
	t.Parallel()
	p := core.Platform{OS: "Linux", Architecture: "AMD64"}.Normalize()
	testutil.Equal(t, p.OS, "linux", "OS normalized")
	testutil.Equal(t, p.Architecture, "amd64", "Arch normalized")
}

// ════════════════════════════════════════════════════════════════════
// ARTIFACT CLONE
// ════════════════════════════════════════════════════════════════════

func TestArtifact_CloneIsDeep(t *testing.T) {
	t.Parallel()
	ts := time.Now()
	orig := &core.Artifact{
		Kind:        core.ArtifactKindContainerImage,
		Layers:      []core.Layer{{History: &core.LayerHistory{CreatedAt: &ts, CreatedBy: "cmd"}}},
		Config:      []byte(`{"architecture":"amd64"}`),
		Annotations: map[string]string{"key": "value"},
		Labels:      map[string]string{"l": "v"},
		Metadata:    map[string][]byte{"m": []byte("data")},
		Attestations: []core.AttestationRecord{
			{Kind: core.AttestationKindInToto, Path: "sbom.json"},
		},
	}
	clone := orig.Clone()
	clone.Annotations["key"] = "mutated"
	clone.Labels["l"] = "mutated"
	clone.Metadata["m"] = []byte("mutated")
	clone.Config[0] = '!'
	clone.Attestations[0].Path = "mutated.json"
	clone.Layers[0].History.CreatedBy = "mutated"

	testutil.Equal(t, orig.Annotations["key"], "value", "Annotations immutable")
	testutil.Equal(t, orig.Labels["l"], "v", "Labels immutable")
	testutil.Equal(t, string(orig.Metadata["m"]), "data", "Metadata immutable")
	testutil.Equal(t, orig.Config[0], byte('{'), "Config immutable")
	testutil.Equal(t, orig.Attestations[0].Path, "sbom.json", "Attestations immutable")
	testutil.Equal(t, orig.Layers[0].History.CreatedBy, "cmd", "Layers immutable")
}

func TestArtifact_CloneOfNilReturnsNil(t *testing.T) {
	t.Parallel()
	var a *core.Artifact
	if a.Clone() != nil {
		t.Error("Clone of nil must return nil")
	}
}

// ════════════════════════════════════════════════════════════════════
// OPTIONS
// ════════════════════════════════════════════════════════════════════

func TestOptions_GetDefault(t *testing.T) {
	t.Parallel()
	opts := core.Options{"key": "val"}
	testutil.Equal(t, opts.GetDefault("key", "default"), "val", "existing key")
	testutil.Equal(t, opts.GetDefault("missing", "default"), "default", "missing key")
}

func TestOptions_Clone_Isolation(t *testing.T) {
	t.Parallel()
	orig := core.Options{"a": "1"}
	clone := orig.Clone()
	clone["a"] = "mutated"
	testutil.Equal(t, orig["a"], "1", "Options.Clone must be independent")
}

// ════════════════════════════════════════════════════════════════════
// EXPORT REQUEST BUILDER
// ════════════════════════════════════════════════════════════════════

func TestExportRequestBuilder_RequiredFields_Empty(t *testing.T) {
	t.Parallel()
	testutil.Error(t, func() error { _, err := core.NewExportRequest().Build(); return err }(),
		"empty builder must fail")
}

func TestExportRequestBuilder_MissingArtifact(t *testing.T) {
	t.Parallel()
	_, err := core.NewExportRequest().WithSessionID("s").WithReporter(&noopReporter{}).Build()
	testutil.Error(t, err, "missing artifact must fail")
}

func TestExportRequestBuilder_MissingReporter(t *testing.T) {
	t.Parallel()
	_, err := core.NewExportRequest().WithSessionID("s").WithArtifact(&core.Artifact{}).Build()
	testutil.Error(t, err, "missing reporter must fail")
}

func TestExportRequestBuilder_ValidRequest(t *testing.T) {
	t.Parallel()
	ts := time.Now()
	req, err := core.NewExportRequest().
		WithSessionID("sess-abc").
		WithArtifact(&core.Artifact{}).
		WithImageName("reg.io/app:v1").
		WithPush(true).
		WithEpoch(&ts).
		WithAnnotation("key", "val").
		WithLabel("env", "prod").
		WithStore(false).
		WithReporter(&noopReporter{}).
		Build()
	testutil.NoError(t, err)
	testutil.Equal(t, req.SessionID, "sess-abc", "SessionID")
	testutil.Equal(t, req.ImageName, "reg.io/app:v1", "ImageName")
	testutil.True(t, req.Push, "Push")
	testutil.Equal(t, req.Annotations["key"], "val", "Annotation")
	testutil.Equal(t, req.Labels["env"], "prod", "Label")
	testutil.False(t, req.Store, "Store")
}

// ════════════════════════════════════════════════════════════════════
// EXPORT RESULT
// ════════════════════════════════════════════════════════════════════

func TestExportResult_SetAndGet(t *testing.T) {
	t.Parallel()
	r := &core.ExportResult{}
	r.Set(core.ResultKeyImageDigest, "sha256:abc")
	testutil.Equal(t, r.Get(core.ResultKeyImageDigest), "sha256:abc", "Set/Get")
	testutil.Equal(t, r.Get(core.ResultKeyImageName), "", "absent key returns empty")
}

// ════════════════════════════════════════════════════════════════════
// REGISTRY
// ════════════════════════════════════════════════════════════════════

func TestRegistry_RegisterAndGet(t *testing.T) {
	t.Parallel()
	reg := core.NewRegistry()
	exp := &stubExporter{t: "test-exporter"}
	testutil.NoError(t, reg.Register(exp))
	got, err := reg.Get("test-exporter")
	testutil.NoError(t, err)
	if got != exp {
		t.Error("Get returned different exporter")
	}
}

func TestRegistry_RegisterDuplicate(t *testing.T) {
	t.Parallel()
	reg := core.NewRegistry()
	reg.MustRegister(&stubExporter{t: "dup"})
	err := reg.Register(&stubExporter{t: "dup"})
	testutil.Error(t, err, "duplicate registration must fail")
	if !errors.Is(err, core.ErrAlreadyRegistered) {
		t.Errorf("wrong error: %v", err)
	}
}

func TestRegistry_GetUnregistered(t *testing.T) {
	t.Parallel()
	reg := core.NewRegistry()
	_, err := reg.Get("ghost")
	testutil.Error(t, err, "get unknown type must fail")
	if !errors.Is(err, core.ErrNotRegistered) {
		t.Errorf("wrong error: %v", err)
	}
}

func TestRegistry_RegisterNil(t *testing.T) {
	t.Parallel()
	testutil.Error(t, core.NewRegistry().Register(nil), "register nil must fail")
}

func TestRegistry_RegisterEmptyType(t *testing.T) {
	t.Parallel()
	testutil.Error(t, core.NewRegistry().Register(&stubExporter{t: ""}), "empty type must fail")
}

func TestRegistry_MustRegisterPanicsOnDuplicate(t *testing.T) {
	t.Parallel()
	reg := core.NewRegistry()
	reg.MustRegister(&stubExporter{t: "once"})
	defer func() {
		if recover() == nil {
			t.Error("MustRegister duplicate must panic")
		}
	}()
	reg.MustRegister(&stubExporter{t: "once"})
}

func TestRegistry_Types_IsSorted(t *testing.T) {
	t.Parallel()
	reg := core.NewRegistry()
	for _, n := range []string{"zzz", "aaa", "mmm"} {
		reg.MustRegister(&stubExporter{t: core.ExporterType(n)})
	}
	types := reg.Types()
	testutil.Len(t, types, 3, "type count")
	testutil.Equal(t, types[0], core.ExporterType("aaa"), "sorted[0]")
	testutil.Equal(t, types[1], core.ExporterType("mmm"), "sorted[1]")
	testutil.Equal(t, types[2], core.ExporterType("zzz"), "sorted[2]")
}

func TestRegistry_Resolve_ForwardsOptions(t *testing.T) {
	t.Parallel()
	reg := core.NewRegistry()
	exp := &stubExporter{t: "forward"}
	reg.MustRegister(exp)
	_, err := reg.Resolve(context.Background(), "forward", core.Options{"key": "value"})
	testutil.NoError(t, err)
	testutil.Equal(t, exp.opts["key"], "value", "options forwarded")
}

func TestRegistry_SealedAfterFirstResolve(t *testing.T) {
	t.Parallel()
	reg := core.NewRegistry()
	reg.MustRegister(&stubExporter{t: "first"})
	_, err := reg.Resolve(context.Background(), "first", nil)
	testutil.NoError(t, err)
	err = reg.Register(&stubExporter{t: "late"})
	testutil.Error(t, err, "registration after resolve must fail")
	if !errors.Is(err, core.ErrAlreadyRegistered) {
		t.Errorf("wrong error: %v", err)
	}
}

func TestRegistry_ConcurrentRegistration(t *testing.T) {
	t.Parallel()
	reg := core.NewRegistry()
	var wg sync.WaitGroup
	errs := make([]error, 20)
	for i := range 20 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = reg.Register(&stubExporter{t: core.ExporterType(string(rune('a' + i)))})
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		testutil.NoError(t, err, "concurrent unique registration")
	}
	testutil.Len(t, reg.Types(), 20, "all types registered")
}

// ════════════════════════════════════════════════════════════════════
// PIPELINE
// ════════════════════════════════════════════════════════════════════

func TestPipeline_TransformersRunInPriorityOrder(t *testing.T) {
	t.Parallel()
	cap := &capturingInstance{}
	p := core.NewPipeline(cap)
	p.MustAddTransformer(&recordingTransformer{name: "C", priority: 300})
	p.MustAddTransformer(&recordingTransformer{name: "A", priority: 100})
	p.MustAddTransformer(&recordingTransformer{name: "B", priority: 200})

	_, _, err := p.Execute(context.Background(), validRequest(t))
	testutil.NoError(t, err)
	testutil.Equal(t, cap.received.Artifact.Labels["order"], "A,B,C", "transformer order")
}

func TestPipeline_DuplicateTransformerNameRejected(t *testing.T) {
	t.Parallel()
	p := core.NewPipeline(&capturingInstance{})
	p.MustAddTransformer(&recordingTransformer{name: "x", priority: 0})
	testutil.Error(t, p.AddTransformer(&recordingTransformer{name: "x", priority: 1}),
		"duplicate name must be rejected")
}

func TestPipeline_NilTransformerRejected(t *testing.T) {
	t.Parallel()
	testutil.Error(t, core.NewPipeline(&capturingInstance{}).AddTransformer(nil),
		"nil transformer must fail")
}

func TestPipeline_MustAddPanicsOnDuplicate(t *testing.T) {
	t.Parallel()
	p := core.NewPipeline(&capturingInstance{})
	p.MustAddTransformer(&recordingTransformer{name: "once", priority: 0})
	defer func() {
		if recover() == nil {
			t.Error("MustAddTransformer duplicate must panic")
		}
	}()
	p.MustAddTransformer(&recordingTransformer{name: "once", priority: 1})
}

func TestPipeline_TransformerFailurePropagates(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("transformer error")
	p := core.NewPipeline(&capturingInstance{})
	p.MustAddTransformer(&recordingTransformer{name: "ok", priority: 10})
	p.MustAddTransformer(&recordingTransformer{name: "fail", priority: 20, failWith: sentinel})
	_, _, err := p.Execute(context.Background(), validRequest(t))
	testutil.Error(t, err, "transformer error must propagate")
	if !errors.Is(err, core.ErrTransformFailed) {
		t.Errorf("wrong error type: %v", err)
	}
}

func TestPipeline_OriginalArtifactIsImmutable(t *testing.T) {
	t.Parallel()
	cap := &capturingInstance{}
	p := core.NewPipeline(cap)
	p.MustAddTransformer(&recordingTransformer{name: "mutator", priority: 0})

	req := validRequest(t)
	origLabelLen := len(req.Artifact.Labels)
	_, _, err := p.Execute(context.Background(), req)
	testutil.NoError(t, err)
	// If the pipeline mutated the original, new keys would have been added.
	if len(req.Artifact.Labels) != origLabelLen {
		t.Error("original request artifact must not be mutated by pipeline")
	}
}

func TestPipeline_FinalizeFuncPassedThrough(t *testing.T) {
	t.Parallel()
	called := false
	cap := &capturingInstance{finalize: func(_ context.Context) error { called = true; return nil }}
	p := core.NewPipeline(cap)
	_, fin, err := p.Execute(context.Background(), validRequest(t))
	testutil.NoError(t, err)
	if fin == nil {
		t.Fatal("finalize func must not be nil")
	}
	testutil.NoError(t, fin(context.Background()))
	testutil.True(t, called, "finalize func must be called")
}

func TestPipeline_ExportErrorPropagates(t *testing.T) {
	t.Parallel()
	exportErr := errors.New("export failed")
	p := core.NewPipeline(&capturingInstance{err: exportErr})
	_, _, err := p.Execute(context.Background(), validRequest(t))
	testutil.Error(t, err, "export error must propagate")
	if !errors.Is(err, exportErr) {
		t.Errorf("wrong error: %v", err)
	}
}

func TestPipeline_TransformersReturnsCopy(t *testing.T) {
	t.Parallel()
	p := core.NewPipeline(&capturingInstance{})
	p.MustAddTransformer(&recordingTransformer{name: "t1", priority: 0})
	ts := p.Transformers()
	ts[0] = nil
	if p.Transformers()[0] == nil {
		t.Error("Transformers must return independent copy")
	}
}

func TestPipeline_InvalidRequestRejected(t *testing.T) {
	t.Parallel()
	p := core.NewPipeline(&capturingInstance{})
	_, _, err := p.Execute(context.Background(), &core.ExportRequest{})
	testutil.Error(t, err, "invalid request must be rejected at Execute")
}

func TestPipeline_MiddlewareOutermostFirst(t *testing.T) {
	t.Parallel()
	var order []string
	make := func(name string) core.Middleware {
		return func(next core.ExportFunc) core.ExportFunc {
			return func(ctx context.Context, req *core.ExportRequest) (*core.ExportResult, core.FinalizeFunc, error) {
				order = append(order, name+":before")
				r, f, e := next(ctx, req)
				order = append(order, name+":after")
				return r, f, e
			}
		}
	}
	p := core.NewPipeline(&capturingInstance{}, make("outer"), make("inner"))
	_, _, err := p.Execute(context.Background(), validRequest(t))
	testutil.NoError(t, err)
	testutil.Len(t, order, 4, "middleware call count")
	testutil.Equal(t, order[0], "outer:before", "outer before")
	testutil.Equal(t, order[1], "inner:before", "inner before")
	testutil.Equal(t, order[2], "inner:after", "inner after")
	testutil.Equal(t, order[3], "outer:after", "outer after")
}

// ════════════════════════════════════════════════════════════════════
// PIPELINE BUILDER
// ════════════════════════════════════════════════════════════════════

func TestPipelineBuilder_BuildsCorrectly(t *testing.T) {
	t.Parallel()
	reg := core.NewRegistry()
	reg.MustRegister(&stubExporter{t: "builder-test"})
	p, err := core.NewPipelineBuilder(reg).
		ForExporter("builder-test", core.Options{"k": "v"}).
		WithTransformer(&recordingTransformer{name: "t1", priority: 0}).
		Build(context.Background())
	testutil.NoError(t, err)
	testutil.Len(t, p.Transformers(), 1, "transformer count")
}

func TestPipelineBuilder_MissingExporterType(t *testing.T) {
	t.Parallel()
	_, err := core.NewPipelineBuilder(core.NewRegistry()).Build(context.Background())
	testutil.Error(t, err, "missing exporter type must fail")
}

func TestPipelineBuilder_UnknownExporterType(t *testing.T) {
	t.Parallel()
	_, err := core.NewPipelineBuilder(core.NewRegistry()).
		ForExporter("unknown", nil).
		Build(context.Background())
	testutil.Error(t, err, "unknown exporter type must fail")
	if !errors.Is(err, core.ErrNotRegistered) {
		t.Errorf("wrong error: %v", err)
	}
}

func TestPipelineBuilder_DuplicateTransformer(t *testing.T) {
	t.Parallel()
	reg := core.NewRegistry()
	reg.MustRegister(&stubExporter{t: "dup-t"})
	_, err := core.NewPipelineBuilder(reg).
		ForExporter("dup-t", nil).
		WithTransformer(&recordingTransformer{name: "same", priority: 0}).
		WithTransformer(&recordingTransformer{name: "same", priority: 1}).
		Build(context.Background())
	testutil.Error(t, err, "duplicate transformer in builder must fail")
}
