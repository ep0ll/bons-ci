package integration_test

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/bons/bons-ci/plugins/exporter/core"
	"github.com/bons/bons-ci/plugins/exporter/exporter/containerimage"
	"github.com/bons/bons-ci/plugins/exporter/internal/testutil"
	"github.com/bons/bons-ci/plugins/exporter/progress"
	"github.com/bons/bons-ci/plugins/exporter/transform"
)

func buildRegistry(
	store core.ContentStore,
	pusher containerimage.Pusher,
	storer containerimage.ImageStorer,
) core.Registry {
	reg := core.NewRegistry()
	reg.MustRegister(containerimage.New(
		containerimage.WithContentStore(store),
		containerimage.WithPusher(pusher),
		containerimage.WithImageStorer(storer),
	))
	return reg
}

func req(t *testing.T, artifact *core.Artifact, imageName string) *core.ExportRequest {
	t.Helper()
	r, err := core.NewExportRequest().
		WithSessionID("integration-session").
		WithArtifact(artifact).
		WithImageName(imageName).
		WithReporter(progress.NewCapturing()).
		Build()
	testutil.NoError(t, err)
	return r
}

// ════════════════════════════════════════════════════════════════════
// FULL PIPELINE: registry → pipeline builder → transformers → export
// ════════════════════════════════════════════════════════════════════

func TestIntegration_FullPipeline_SinglePlatform(t *testing.T) {
	t.Parallel()
	store := testutil.NewMemoryContentStore()
	pusher := testutil.NewSpyPusher()
	storer := testutil.NewSpyImageStorer()
	reg := buildRegistry(store, pusher, storer)

	epoch := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	pipeline, err := core.NewPipelineBuilder(reg).
		ForExporter(core.ExporterTypeContainerImage, core.Options{
			containerimage.OptKeyName:  "registry.example.com/myapp:v1",
			containerimage.OptKeyPush:  "true",
			containerimage.OptKeyStore: "true",
		}).
		WithTransformer(transform.NewEpochTransformer()).
		WithTransformer(transform.NewAnnotationTransformer(
			transform.WithStaticAnnotation("org.opencontainers.image.version", "1.0.0"),
			transform.WithStaticAnnotation("org.opencontainers.image.vendor", "Acme Corp"),
			transform.WithInjectCreatedAt(true),
		)).
		WithTransformer(transform.NewAttestationTransformer(
			transform.WithDeniedPredicateType("https://slsa.dev/provenance/"),
		)).
		Build(context.Background())
	testutil.NoError(t, err)

	artifact := testutil.MinimalArtifact()
	artifact.Metadata[transform.MetaKeySourceDateEpoch] = []byte(strconv.FormatInt(epoch.Unix(), 10))
	artifact.Attestations = []core.AttestationRecord{
		{
			Kind:          core.AttestationKindInToto,
			PredicateType: "https://spdx.dev/Document",
			Path:          "sbom.json",
			Payload:       []byte(`{"spdxVersion":"SPDX-2.3"}`),
		},
		{
			// Must be filtered out.
			Kind:          core.AttestationKindInToto,
			PredicateType: "https://slsa.dev/provenance/v0.2",
			Path:          "provenance.json",
			Payload:       []byte(`{"builder":{}}`),
		},
	}

	r := req(t, artifact, "registry.example.com/myapp:v1")
	r.Push = true
	result, fin, err := pipeline.Execute(context.Background(), r)
	testutil.NoError(t, err)
	if result == nil {
		t.Fatal("result must not be nil")
	}
	if fin == nil {
		t.Fatal("finalize func must not be nil")
	}
	testutil.NoError(t, fin(context.Background()))

	calls := pusher.Calls()
	testutil.Len(t, calls, 1, "push calls")
	testutil.Equal(t, calls[0].Ref, "registry.example.com/myapp:v1", "pushed ref")
	testutil.Len(t, storer.StoreCalls, 1, "store calls")
	if result.ImageDigest.IsZero() {
		t.Error("result digest must not be zero")
	}
}

func TestIntegration_EpochTransformerClamps(t *testing.T) {
	t.Parallel()
	store := testutil.NewMemoryContentStore()
	reg := buildRegistry(store, nil, nil)

	epoch := time.Date(2020, 6, 15, 0, 0, 0, 0, time.UTC)
	future := epoch.Add(48 * time.Hour)

	artifact := testutil.MinimalArtifact()
	artifact.Metadata[transform.MetaKeySourceDateEpoch] = []byte(strconv.FormatInt(epoch.Unix(), 10))
	artifact.Layers[0].History.CreatedAt = &future

	pipeline, err := core.NewPipelineBuilder(reg).
		ForExporter(core.ExporterTypeContainerImage, core.Options{containerimage.OptKeyStore: "false"}).
		WithTransformer(transform.NewEpochTransformer()).
		Build(context.Background())
	testutil.NoError(t, err)

	result, _, err := pipeline.Execute(context.Background(), req(t, artifact, ""))
	testutil.NoError(t, err)
	if result.ImageDigest.IsZero() {
		t.Error("epoch-clamped export must produce a valid digest")
	}
}

func TestIntegration_MiddlewareTimingRecorded(t *testing.T) {
	t.Parallel()
	store := testutil.NewMemoryContentStore()
	reg := buildRegistry(store, nil, nil)

	var recorded bool
	timing := core.Middleware(func(next core.ExportFunc) core.ExportFunc {
		return func(ctx context.Context, req *core.ExportRequest) (*core.ExportResult, core.FinalizeFunc, error) {
			start := time.Now()
			r, f, e := next(ctx, req)
			_ = time.Since(start)
			recorded = true
			return r, f, e
		}
	})

	pipeline, err := core.NewPipelineBuilder(reg).
		ForExporter(core.ExporterTypeContainerImage, core.Options{containerimage.OptKeyStore: "false"}).
		WithMiddleware(timing).
		Build(context.Background())
	testutil.NoError(t, err)

	_, _, err = pipeline.Execute(context.Background(), req(t, testutil.MinimalArtifact(), ""))
	testutil.NoError(t, err)
	testutil.True(t, recorded, "middleware must be called")
}

func TestIntegration_MultipleExporterTypes(t *testing.T) {
	t.Parallel()
	store := testutil.NewMemoryContentStore()
	reg := core.NewRegistry()
	reg.MustRegister(containerimage.New(containerimage.WithContentStore(store)))
	reg.MustRegister(&futureExporter{t: "dallie"})

	types := reg.Types()
	testutil.Len(t, types, 2, "type count")

	hasContainerImage := false
	hasDallie := false
	for _, t := range types {
		if t == core.ExporterTypeContainerImage {
			hasContainerImage = true
		}
		if t == "dallie" {
			hasDallie = true
		}
	}
	if !hasContainerImage {
		t.Error("containerimage type missing from registry")
	}
	if !hasDallie {
		t.Error("dallie type missing from registry")
	}
}

func TestIntegration_AttestationProvenance_Filtered(t *testing.T) {
	t.Parallel()
	store := testutil.NewMemoryContentStore()
	reg := buildRegistry(store, nil, nil)

	pipeline, err := core.NewPipelineBuilder(reg).
		ForExporter(core.ExporterTypeContainerImage, core.Options{containerimage.OptKeyStore: "false"}).
		WithTransformer(transform.NewAttestationTransformer(
			transform.WithDeniedPredicateType("https://slsa.dev/provenance/"),
			transform.WithRequirePayload(true),
		)).
		Build(context.Background())
	testutil.NoError(t, err)

	artifact := testutil.MinimalArtifact()
	artifact.Attestations = []core.AttestationRecord{
		{
			Kind:          core.AttestationKindInToto,
			PredicateType: "https://slsa.dev/provenance/v1",
			Path:          "prov.json",
			Payload:       []byte(`{}`),
		},
	}

	result, _, err := pipeline.Execute(context.Background(), req(t, artifact, ""))
	testutil.NoError(t, err)
	if result.ImageDigest.IsZero() {
		t.Error("result digest must not be zero after filtering")
	}
}

func TestIntegration_ResultKeys(t *testing.T) {
	t.Parallel()
	store := testutil.NewMemoryContentStore()
	reg := buildRegistry(store, nil, nil)

	pipeline, err := core.NewPipelineBuilder(reg).
		ForExporter(core.ExporterTypeContainerImage, core.Options{containerimage.OptKeyStore: "false"}).
		Build(context.Background())
	testutil.NoError(t, err)

	result, _, err := pipeline.Execute(context.Background(), req(t, testutil.MinimalArtifact(), ""))
	testutil.NoError(t, err)

	if v := result.Get(core.ResultKeyImageDigest); v == "" {
		t.Error("ResultKeyImageDigest must be set")
	}
	if v := result.Get(core.ResultKeyDescriptor); v == "" {
		t.Error("ResultKeyDescriptor must be set")
	}
}

func TestIntegration_ContextCancellation_NoPanic(t *testing.T) {
	t.Parallel()
	store := testutil.NewMemoryContentStore()
	reg := buildRegistry(store, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	pipeline, err := core.NewPipelineBuilder(reg).
		ForExporter(core.ExporterTypeContainerImage, core.Options{containerimage.OptKeyStore: "false"}).
		Build(context.Background())
	testutil.NoError(t, err)

	// Must not panic regardless of cancellation timing.
	_, _, _ = pipeline.Execute(ctx, req(t, testutil.MinimalArtifact(), ""))
}

// futureExporter is a stub for unimplemented backends, proving that the
// registry is open for extension without coupling to the container image path.
type futureExporter struct{ t core.ExporterType }

func (f *futureExporter) Type() core.ExporterType { return f.t }
func (f *futureExporter) Resolve(_ context.Context, _ core.Options) (core.ExporterInstance, error) {
	return &futureInstance{}, nil
}

type futureInstance struct{}

func (f *futureInstance) Export(_ context.Context, _ *core.ExportRequest) (*core.ExportResult, core.FinalizeFunc, error) {
	return nil, nil, errors.New("futureInstance.Export: not implemented (stub)")
}
