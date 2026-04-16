package transform_test

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/bons/bons-ci/plugins/exporter/core"
	"github.com/bons/bons-ci/plugins/exporter/internal/testutil"
	"github.com/bons/bons-ci/plugins/exporter/transform"
)

func epochArtifact(epoch time.Time) *core.Artifact {
	future := epoch.Add(48 * time.Hour)
	return &core.Artifact{
		Kind: core.ArtifactKindContainerImage,
		Layers: []core.Layer{
			{History: &core.LayerHistory{CreatedAt: &future, CreatedBy: "test"}},
		},
		Metadata: map[string][]byte{
			transform.MetaKeySourceDateEpoch: []byte(strconv.FormatInt(epoch.Unix(), 10)),
		},
	}
}

// ════════════════════════════════════════════════════════════════════
// EPOCH TRANSFORMER
// ════════════════════════════════════════════════════════════════════

func TestEpoch_ClampsTimestamp(t *testing.T) {
	t.Parallel()
	epoch := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	result, err := transform.NewEpochTransformer().Transform(context.Background(), epochArtifact(epoch))
	testutil.NoError(t, err)
	got := *result.Layers[0].History.CreatedAt
	if !got.Equal(epoch.UTC()) {
		t.Errorf("timestamp not clamped: got %v, want %v", got, epoch.UTC())
	}
}

func TestEpoch_DoesNotClampPastTimestamp(t *testing.T) {
	t.Parallel()
	epoch := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	earlier := epoch.Add(-time.Hour * 24)
	a := &core.Artifact{
		Kind: core.ArtifactKindContainerImage,
		Layers: []core.Layer{
			{History: &core.LayerHistory{CreatedAt: &earlier}},
		},
		Metadata: map[string][]byte{
			transform.MetaKeySourceDateEpoch: []byte(strconv.FormatInt(epoch.Unix(), 10)),
		},
	}
	result, err := transform.NewEpochTransformer().Transform(context.Background(), a)
	testutil.NoError(t, err)
	if !result.Layers[0].History.CreatedAt.Equal(earlier) {
		t.Error("timestamp before epoch must not be changed")
	}
}

func TestEpoch_NoEpochInMetadata_ReturnsUnchanged(t *testing.T) {
	t.Parallel()
	a := &core.Artifact{Kind: core.ArtifactKindContainerImage, Metadata: map[string][]byte{}}
	result, err := transform.NewEpochTransformer().Transform(context.Background(), a)
	testutil.NoError(t, err)
	if result != a {
		t.Error("no-op must return original pointer")
	}
}

func TestEpoch_FallbackToNow(t *testing.T) {
	t.Parallel()
	before := time.Now().Add(-time.Second)
	a := &core.Artifact{Kind: core.ArtifactKindContainerImage, Metadata: map[string][]byte{}}
	result, err := transform.NewEpochTransformer(transform.WithFallbackToNow(true)).
		Transform(context.Background(), a)
	testutil.NoError(t, err)
	raw := result.Metadata[transform.MetaKeySourceDateEpoch]
	if raw == nil {
		t.Fatal("FallbackToNow must set epoch metadata")
	}
	secs, err := strconv.ParseInt(string(raw), 10, 64)
	testutil.NoError(t, err)
	applied := time.Unix(secs, 0)
	if !applied.After(before) {
		t.Errorf("fallback epoch %v not after test start %v", applied, before)
	}
}

func TestEpoch_InvalidEpochReturnsError(t *testing.T) {
	t.Parallel()
	a := &core.Artifact{Metadata: map[string][]byte{
		transform.MetaKeySourceDateEpoch: []byte("not-a-number"),
	}}
	_, err := transform.NewEpochTransformer().Transform(context.Background(), a)
	testutil.Error(t, err, "invalid epoch must error")
	if !errors.Is(err, core.ErrValidation) {
		t.Errorf("wrong error type: %v", err)
	}
}

func TestEpoch_SetsAppliedKey(t *testing.T) {
	t.Parallel()
	epoch := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	result, err := transform.NewEpochTransformer().Transform(context.Background(), epochArtifact(epoch))
	testutil.NoError(t, err)
	testutil.Equal(t, string(result.Metadata[transform.MetaKeyEpochApplied]), "true", "applied key")
}

func TestEpoch_DoesNotMutateOriginal(t *testing.T) {
	t.Parallel()
	epoch := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	a := epochArtifact(epoch)
	origTime := *a.Layers[0].History.CreatedAt
	_, err := transform.NewEpochTransformer().Transform(context.Background(), a)
	testutil.NoError(t, err)
	if !a.Layers[0].History.CreatedAt.Equal(origTime) {
		t.Error("original artifact must not be mutated")
	}
}

// ════════════════════════════════════════════════════════════════════
// ATTESTATION TRANSFORMER
// ════════════════════════════════════════════════════════════════════

func attArtifact(atts ...core.AttestationRecord) *core.Artifact {
	return &core.Artifact{Kind: core.ArtifactKindContainerImage, Attestations: atts}
}

func TestAttestation_PassthroughWhenEmpty(t *testing.T) {
	t.Parallel()
	a := &core.Artifact{Kind: core.ArtifactKindContainerImage}
	result, err := transform.NewAttestationTransformer().Transform(context.Background(), a)
	testutil.NoError(t, err)
	if result != a {
		t.Error("empty attestations: must return original pointer")
	}
}

func TestAttestation_FiltersByAllowedKinds(t *testing.T) {
	t.Parallel()
	a := attArtifact(
		core.AttestationRecord{Kind: core.AttestationKindInToto, Path: "sbom.json", Payload: []byte("{}")},
		core.AttestationRecord{Kind: core.AttestationKindVuln, Path: "vuln.json", Payload: []byte("{}")},
	)
	result, err := transform.NewAttestationTransformer(
		transform.WithAllowedKinds(core.AttestationKindInToto),
	).Transform(context.Background(), a)
	testutil.NoError(t, err)
	testutil.Len(t, result.Attestations, 1, "filtered attestations")
	testutil.Equal(t, result.Attestations[0].Kind, core.AttestationKindInToto, "kind")
}

func TestAttestation_DenyPredicateType(t *testing.T) {
	t.Parallel()
	a := attArtifact(
		core.AttestationRecord{
			Kind: core.AttestationKindInToto, Path: "prov.json",
			PredicateType: "https://slsa.dev/provenance/v0.2", Payload: []byte("{}"),
		},
		core.AttestationRecord{
			Kind: core.AttestationKindInToto, Path: "sbom.json",
			PredicateType: "https://spdx.dev/Document", Payload: []byte("{}"),
		},
	)
	result, err := transform.NewAttestationTransformer(
		transform.WithDeniedPredicateType("https://slsa.dev/provenance/"),
	).Transform(context.Background(), a)
	testutil.NoError(t, err)
	testutil.Len(t, result.Attestations, 1, "denied predicate filtered")
	testutil.Equal(t, result.Attestations[0].PredicateType, "https://spdx.dev/Document", "predicate type")
}

func TestAttestation_DeduplicateByPath(t *testing.T) {
	t.Parallel()
	a := attArtifact(
		core.AttestationRecord{Kind: core.AttestationKindInToto, Path: "dup.json", Payload: []byte("{}")},
		core.AttestationRecord{Kind: core.AttestationKindInToto, Path: "dup.json", Payload: []byte("{}")},
		core.AttestationRecord{Kind: core.AttestationKindInToto, Path: "unique.json", Payload: []byte("{}")},
	)
	result, err := transform.NewAttestationTransformer(transform.WithDeduplicateByPath(true)).
		Transform(context.Background(), a)
	testutil.NoError(t, err)
	testutil.Len(t, result.Attestations, 2, "deduplication count")
}

func TestAttestation_RequirePayloadRejectsEmpty(t *testing.T) {
	t.Parallel()
	a := attArtifact(core.AttestationRecord{
		Kind: core.AttestationKindInToto, Path: "empty.json", Payload: []byte(""),
	})
	_, err := transform.NewAttestationTransformer(transform.WithRequirePayload(true)).
		Transform(context.Background(), a)
	testutil.Error(t, err, "empty payload must fail when RequirePayload=true")
}

func TestAttestation_MissingPathRejected(t *testing.T) {
	t.Parallel()
	a := attArtifact(core.AttestationRecord{
		Kind: core.AttestationKindInToto, Path: "", Payload: []byte("{}"),
	})
	_, err := transform.NewAttestationTransformer().Transform(context.Background(), a)
	testutil.Error(t, err, "empty path must fail")
}

// ════════════════════════════════════════════════════════════════════
// ANNOTATION TRANSFORMER
// ════════════════════════════════════════════════════════════════════

func TestAnnotation_InjectsStatic(t *testing.T) {
	t.Parallel()
	a := &core.Artifact{Kind: core.ArtifactKindContainerImage, Metadata: make(map[string][]byte)}
	result, err := transform.NewAnnotationTransformer(
		transform.WithStaticAnnotation("org.opencontainers.image.version", "1.2.3"),
	).Transform(context.Background(), a)
	testutil.NoError(t, err)
	testutil.Equal(t, result.Annotations["org.opencontainers.image.version"], "1.2.3", "static annotation")
}

func TestAnnotation_OverwriteStrategy(t *testing.T) {
	t.Parallel()
	a := &core.Artifact{Kind: core.ArtifactKindContainerImage,
		Annotations: map[string]string{"key": "old"}, Metadata: make(map[string][]byte)}
	result, err := transform.NewAnnotationTransformer(
		transform.WithStaticAnnotation("key", "new"),
		transform.WithMergeStrategy(transform.MergeStrategyOverwrite),
	).Transform(context.Background(), a)
	testutil.NoError(t, err)
	testutil.Equal(t, result.Annotations["key"], "new", "overwrite")
}

func TestAnnotation_PreserveStrategy(t *testing.T) {
	t.Parallel()
	a := &core.Artifact{Kind: core.ArtifactKindContainerImage,
		Annotations: map[string]string{"key": "original"}, Metadata: make(map[string][]byte)}
	result, err := transform.NewAnnotationTransformer(
		transform.WithStaticAnnotation("key", "incoming"),
		transform.WithMergeStrategy(transform.MergeStrategyPreserve),
	).Transform(context.Background(), a)
	testutil.NoError(t, err)
	testutil.Equal(t, result.Annotations["key"], "original", "preserve")
}

func TestAnnotation_ErrorStrategy(t *testing.T) {
	t.Parallel()
	a := &core.Artifact{Kind: core.ArtifactKindContainerImage,
		Annotations: map[string]string{"key": "exists"}, Metadata: make(map[string][]byte)}
	_, err := transform.NewAnnotationTransformer(
		transform.WithStaticAnnotation("key", "clash"),
		transform.WithMergeStrategy(transform.MergeStrategyError),
	).Transform(context.Background(), a)
	testutil.Error(t, err, "error strategy collision must fail")
	testutil.Contains(t, err.Error(), "collision", "error message")
}

func TestAnnotation_AllowedPrefixFilters(t *testing.T) {
	t.Parallel()
	a := &core.Artifact{Kind: core.ArtifactKindContainerImage, Metadata: make(map[string][]byte)}
	result, err := transform.NewAnnotationTransformer(
		transform.WithStaticAnnotation("org.opencontainers.image.version", "1.0"),
		transform.WithStaticAnnotation("com.example.internal", "secret"),
		transform.WithAllowedAnnotationPrefix("org.opencontainers."),
	).Transform(context.Background(), a)
	testutil.NoError(t, err)
	if _, ok := result.Annotations["org.opencontainers.image.version"]; !ok {
		t.Error("allowed annotation missing")
	}
	if _, ok := result.Annotations["com.example.internal"]; ok {
		t.Error("denied annotation must be filtered")
	}
}

func TestAnnotation_InjectCreatedAt(t *testing.T) {
	t.Parallel()
	a := &core.Artifact{Kind: core.ArtifactKindContainerImage, Metadata: make(map[string][]byte)}
	result, err := transform.NewAnnotationTransformer(
		transform.WithInjectCreatedAt(true),
	).Transform(context.Background(), a)
	testutil.NoError(t, err)
	if result.Annotations[transform.AnnotationCreated] == "" {
		t.Error("created-at annotation must be injected")
	}
}

func TestAnnotation_NoAnnotations_IsNoop(t *testing.T) {
	t.Parallel()
	a := &core.Artifact{Kind: core.ArtifactKindContainerImage, Metadata: make(map[string][]byte)}
	result, err := transform.NewAnnotationTransformer().Transform(context.Background(), a)
	testutil.NoError(t, err)
	if result != a {
		t.Error("no annotations: must return original pointer")
	}
}

// ════════════════════════════════════════════════════════════════════
// TRANSFORMER CHAIN
// ════════════════════════════════════════════════════════════════════

type wordAppender struct {
	name     string
	priority int
	word     string
}

func (w *wordAppender) Name() string  { return w.name }
func (w *wordAppender) Priority() int { return w.priority }
func (w *wordAppender) Transform(_ context.Context, a *core.Artifact) (*core.Artifact, error) {
	clone := a.Clone()
	if clone.Labels == nil {
		clone.Labels = make(map[string]string)
	}
	if v := clone.Labels["words"]; v == "" {
		clone.Labels["words"] = w.word
	} else {
		clone.Labels["words"] = v + " " + w.word
	}
	return clone, nil
}

func TestChain_RunsChildrenInOrder(t *testing.T) {
	t.Parallel()
	chain := transform.NewChain("root", 0)
	chain.MustAdd(&wordAppender{name: "b", priority: 20, word: "B"})
	chain.MustAdd(&wordAppender{name: "a", priority: 10, word: "A"})
	chain.MustAdd(&wordAppender{name: "c", priority: 30, word: "C"})
	result, err := chain.Transform(context.Background(), &core.Artifact{})
	testutil.NoError(t, err)
	testutil.Equal(t, result.Labels["words"], "A B C", "chain order")
}

func TestChain_DuplicateNameRejected(t *testing.T) {
	t.Parallel()
	chain := transform.NewChain("test", 0)
	testutil.NoError(t, chain.Add(&wordAppender{name: "dup", priority: 0}))
	testutil.Error(t, chain.Add(&wordAppender{name: "dup", priority: 1}), "duplicate name must fail")
}

func TestChain_NestedChains(t *testing.T) {
	t.Parallel()
	inner := transform.NewChain("inner", 10)
	inner.MustAdd(&wordAppender{name: "ia", priority: 0, word: "IA"})
	inner.MustAdd(&wordAppender{name: "ib", priority: 1, word: "IB"})
	outer := transform.NewChain("outer", 0)
	outer.MustAdd(&wordAppender{name: "oa", priority: 0, word: "OA"})
	outer.MustAdd(inner)
	result, err := outer.Transform(context.Background(), &core.Artifact{})
	testutil.NoError(t, err)
	testutil.Equal(t, result.Labels["words"], "OA IA IB", "nested chain order")
}

func TestChain_ChildFailurePropagates(t *testing.T) {
	t.Parallel()
	failing := transform.NewFuncTransformer("fail", 0,
		func(_ context.Context, _ *core.Artifact) (*core.Artifact, error) {
			return nil, errors.New("boom")
		})
	chain := transform.NewChain("test", 0)
	chain.MustAdd(failing)
	_, err := chain.Transform(context.Background(), &core.Artifact{})
	testutil.Error(t, err, "child failure must propagate")
	if !errors.Is(err, core.ErrTransformFailed) {
		t.Errorf("wrong error type: %v", err)
	}
}

func TestChain_ChildrenReturnsCopy(t *testing.T) {
	t.Parallel()
	chain := transform.NewChain("test", 0)
	chain.MustAdd(&wordAppender{name: "child", priority: 0})
	children := chain.Children()
	children[0] = nil
	if chain.Children()[0] == nil {
		t.Error("Children must return independent copy")
	}
}

// ════════════════════════════════════════════════════════════════════
// FUNC TRANSFORMER
// ════════════════════════════════════════════════════════════════════

func TestFuncTransformer_NameAndPriority(t *testing.T) {
	t.Parallel()
	tr := transform.NewFuncTransformer("my-func", 42,
		func(_ context.Context, a *core.Artifact) (*core.Artifact, error) { return a, nil })
	testutil.Equal(t, tr.Name(), "my-func", "Name")
	testutil.Equal(t, tr.Priority(), 42, "Priority")
}

func TestFuncTransformer_DelegatesTransform(t *testing.T) {
	t.Parallel()
	called := false
	tr := transform.NewFuncTransformer("probe", 0,
		func(_ context.Context, a *core.Artifact) (*core.Artifact, error) {
			called = true
			return a, nil
		})
	_, err := tr.Transform(context.Background(), &core.Artifact{})
	testutil.NoError(t, err)
	testutil.True(t, called, "transform func must be called")
}
