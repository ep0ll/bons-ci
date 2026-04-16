package containerimage_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bons/bons-ci/plugins/exporter/core"
	"github.com/bons/bons-ci/plugins/exporter/exporter/containerimage"
	"github.com/bons/bons-ci/plugins/exporter/internal/testutil"
	"github.com/bons/bons-ci/plugins/exporter/progress"
)

// ════════════════════════════════════════════════════════════════════
// CONFIG PARSING
// ════════════════════════════════════════════════════════════════════

func TestParseConfig_Defaults(t *testing.T) {
	t.Parallel()
	cfg, unknown, err := containerimage.ParseConfig(core.Options{})
	testutil.NoError(t, err)
	if len(unknown) > 0 {
		t.Errorf("unexpected unknown keys: %v", unknown)
	}
	testutil.True(t, cfg.Store, "Store default")
	testutil.True(t, cfg.OCITypes, "OCITypes default")
	testutil.Equal(t, cfg.Compression, containerimage.CompressionGzip, "Compression default")
	testutil.True(t, cfg.ForceInlineAttestations, "ForceInlineAttestations default")
}

func TestParseConfig_AllKnownOptions(t *testing.T) {
	t.Parallel()
	opts := core.Options{
		containerimage.OptKeyName:                "registry.example.com/app:v1",
		containerimage.OptKeyPush:                "true",
		containerimage.OptKeyPushByDigest:        "true",
		containerimage.OptKeyInsecure:            "false",
		containerimage.OptKeyStore:               "false",
		containerimage.OptKeyOCITypes:            "true",
		containerimage.OptKeyOCIArtifact:         "false",
		containerimage.OptKeyCompression:         "zstd",
		containerimage.OptKeyCompressionLevel:    "9",
		containerimage.OptKeyForceCompression:    "true",
		containerimage.OptKeyRewriteTimestamp:    "true",
		containerimage.OptKeyForceInlineAttest:   "false",
		containerimage.OptKeyPreferNondistLayers: "true",
		containerimage.OptKeyNameCanonical:       "true",
		containerimage.OptKeyDanglingPrefix:      "dangling/",
		containerimage.OptKeyUnpack:              "true",
	}
	cfg, unknown, err := containerimage.ParseConfig(opts)
	testutil.NoError(t, err)
	if len(unknown) > 0 {
		t.Errorf("unexpected unknown keys: %v", unknown)
	}
	testutil.Equal(t, cfg.ImageName, "registry.example.com/app:v1", "ImageName")
	testutil.True(t, cfg.Push, "Push")
	testutil.True(t, cfg.PushByDigest, "PushByDigest")
	testutil.False(t, cfg.Insecure, "Insecure")
	testutil.False(t, cfg.Store, "Store")
	testutil.Equal(t, cfg.Compression, containerimage.CompressionZstd, "Compression")
	testutil.Equal(t, cfg.CompressionLevel, 9, "CompressionLevel")
	testutil.True(t, cfg.ForceCompression, "ForceCompression")
	testutil.True(t, cfg.RewriteTimestamp, "RewriteTimestamp")
	testutil.False(t, cfg.ForceInlineAttestations, "ForceInlineAttestations")
	testutil.True(t, cfg.PreferNondistributableLayers, "PreferNondistLayers")
	testutil.True(t, cfg.NameCanonical, "NameCanonical")
	testutil.Equal(t, cfg.DanglingPrefix, "dangling/", "DanglingPrefix")
	testutil.True(t, cfg.Unpack, "Unpack")
}

func TestParseConfig_EmptyBoolMeansTrue(t *testing.T) {
	t.Parallel()
	cfg, _, err := containerimage.ParseConfig(core.Options{containerimage.OptKeyPush: ""})
	testutil.NoError(t, err)
	testutil.True(t, cfg.Push, "empty bool must default to true")
}

func TestParseConfig_UnknownKeyInUnknownMap(t *testing.T) {
	t.Parallel()
	_, unknown, err := containerimage.ParseConfig(core.Options{"my-custom-key": "val"})
	testutil.NoError(t, err)
	testutil.Equal(t, unknown["my-custom-key"], "val", "unknown key forwarded")
}

func TestParseConfig_InvalidBoolErrors(t *testing.T) {
	t.Parallel()
	_, _, err := containerimage.ParseConfig(core.Options{containerimage.OptKeyPush: "maybe"})
	testutil.Error(t, err, "invalid bool must error")
}

func TestParseConfig_InvalidCompressionErrors(t *testing.T) {
	t.Parallel()
	_, _, err := containerimage.ParseConfig(core.Options{containerimage.OptKeyCompression: "lz77"})
	testutil.Error(t, err, "invalid compression must error")
}

func TestParseConfig_OCIArtifactForcesOCITypes(t *testing.T) {
	t.Parallel()
	cfg, _, err := containerimage.ParseConfig(core.Options{
		containerimage.OptKeyOCIArtifact: "true",
		containerimage.OptKeyOCITypes:    "false",
	})
	testutil.NoError(t, err)
	testutil.True(t, cfg.OCITypes, "oci-artifact must force oci-types")
}

func TestParseConfig_InvalidCompressionLevelErrors(t *testing.T) {
	t.Parallel()
	_, _, err := containerimage.ParseConfig(core.Options{containerimage.OptKeyCompressionLevel: "x"})
	testutil.Error(t, err, "non-integer compression level must error")
}

// ════════════════════════════════════════════════════════════════════
// RESOLVE
// ════════════════════════════════════════════════════════════════════

func TestResolve_RejectsUnknownOptions(t *testing.T) {
	t.Parallel()
	exp := containerimage.New(containerimage.WithContentStore(testutil.NewMemoryContentStore()))
	_, err := exp.Resolve(context.Background(), core.Options{"definitely-unknown": "val"})
	testutil.Error(t, err, "unknown option must be rejected")
	if !errors.Is(err, core.ErrUnsupportedOption) {
		t.Errorf("wrong error type: %v", err)
	}
}

func TestResolve_ReturnsInstanceOnValidOpts(t *testing.T) {
	t.Parallel()
	exp := containerimage.New(containerimage.WithContentStore(testutil.NewMemoryContentStore()))
	inst, err := exp.Resolve(context.Background(), core.Options{containerimage.OptKeyName: "app:latest"})
	testutil.NoError(t, err)
	if inst == nil {
		t.Error("Resolve must return non-nil instance")
	}
}

func TestExporter_TypeIsContainerImage(t *testing.T) {
	t.Parallel()
	testutil.Equal(t, containerimage.New().Type(), core.ExporterTypeContainerImage, "exporter type")
}

// ════════════════════════════════════════════════════════════════════
// EXPORT — single platform
// ════════════════════════════════════════════════════════════════════

func TestExport_SinglePlatform_WritesBlobs(t *testing.T) {
	t.Parallel()
	store := testutil.NewMemoryContentStore()
	inst := resolve(t, store, nil, nil, core.Options{containerimage.OptKeyStore: "false"})

	result, finalize, err := inst.Export(context.Background(), buildReq(t, testutil.MinimalArtifact(), false))
	testutil.NoError(t, err)
	if finalize != nil {
		t.Error("no-push export must not return finalize func")
	}
	if result == nil {
		t.Fatal("result must not be nil")
	}
	if result.ImageDigest.IsZero() {
		t.Error("result digest must not be zero")
	}
	if store.Count() == 0 {
		t.Error("blobs must be written to content store")
	}
}

func TestExport_RequestImageNameOverridesConfig(t *testing.T) {
	t.Parallel()
	store := testutil.NewMemoryContentStore()
	inst := resolve(t, store, nil, nil, core.Options{
		containerimage.OptKeyName:  "config-name:v1",
		containerimage.OptKeyStore: "false",
	})
	req := buildReq(t, testutil.MinimalArtifact(), false)
	req.ImageName = "override:v3"

	result, _, err := inst.Export(context.Background(), req)
	testutil.NoError(t, err)
	testutil.Equal(t, result.ImageName, "override:v3", "request name overrides config")
}

func TestExport_StoresImageWhenStoreTrue(t *testing.T) {
	t.Parallel()
	store := testutil.NewMemoryContentStore()
	storer := testutil.NewSpyImageStorer()
	inst := resolve(t, store, nil, storer, core.Options{
		containerimage.OptKeyName:  "stored:v1",
		containerimage.OptKeyStore: "true",
	})
	req := buildReq(t, testutil.MinimalArtifact(), false)
	req.ImageName = "stored:v1"

	_, _, err := inst.Export(context.Background(), req)
	testutil.NoError(t, err)
	testutil.Len(t, storer.StoreCalls, 1, "store calls")
	testutil.Equal(t, storer.StoreCalls[0].Name, "stored:v1", "stored name")
}

func TestExport_StoreErrorPropagates(t *testing.T) {
	t.Parallel()
	storeErr := errors.New("disk full")
	storer := testutil.NewSpyImageStorer()
	storer.FailStoreWith(storeErr)
	inst := resolve(t, testutil.NewMemoryContentStore(), nil, storer, core.Options{
		containerimage.OptKeyName:  "fail:v1",
		containerimage.OptKeyStore: "true",
	})
	req := buildReq(t, testutil.MinimalArtifact(), false)
	req.ImageName = "fail:v1"

	_, _, err := inst.Export(context.Background(), req)
	testutil.Error(t, err, "store error must propagate")
	if !errors.Is(err, storeErr) {
		t.Errorf("wrong error: %v", err)
	}
}

func TestExport_MissingContentStore_Errors(t *testing.T) {
	t.Parallel()
	exp := containerimage.New() // no content store
	inst, err := exp.Resolve(context.Background(), core.Options{containerimage.OptKeyStore: "false"})
	testutil.NoError(t, err)
	_, _, err = inst.Export(context.Background(), buildReq(t, testutil.MinimalArtifact(), false))
	testutil.Error(t, err, "missing content store must error")
	testutil.Contains(t, err.Error(), "ContentStore", "error message")
}

// ════════════════════════════════════════════════════════════════════
// EXPORT — push
// ════════════════════════════════════════════════════════════════════

func TestExport_PushReturnsFinalizeFunc(t *testing.T) {
	t.Parallel()
	store := testutil.NewMemoryContentStore()
	pusher := testutil.NewSpyPusher()
	inst := resolve(t, store, pusher, nil, core.Options{
		containerimage.OptKeyName:  "registry.io/app:v1",
		containerimage.OptKeyPush:  "true",
		containerimage.OptKeyStore: "false",
	})
	req := buildReq(t, testutil.MinimalArtifact(), true)
	req.ImageName = "registry.io/app:v1"

	_, fin, err := inst.Export(context.Background(), req)
	testutil.NoError(t, err)
	if fin == nil {
		t.Fatal("push must return finalize func")
	}
	testutil.NoError(t, fin(context.Background()))
	calls := pusher.Calls()
	testutil.Len(t, calls, 1, "push calls")
	testutil.Equal(t, calls[0].Ref, "registry.io/app:v1", "pushed ref")
}

func TestExport_PushErrorInFinalize(t *testing.T) {
	t.Parallel()
	pushErr := errors.New("registry unreachable")
	pusher := testutil.NewSpyPusher()
	pusher.FailWith(pushErr)
	inst := resolve(t, testutil.NewMemoryContentStore(), pusher, nil, core.Options{
		containerimage.OptKeyName:  "registry.io/app:v1",
		containerimage.OptKeyPush:  "true",
		containerimage.OptKeyStore: "false",
	})
	req := buildReq(t, testutil.MinimalArtifact(), true)
	req.ImageName = "registry.io/app:v1"

	_, fin, err := inst.Export(context.Background(), req)
	testutil.NoError(t, err)
	err = fin(context.Background())
	testutil.Error(t, err, "push error must propagate in finalize")
	if !errors.Is(err, pushErr) {
		t.Errorf("wrong error: %v", err)
	}
}

func TestExport_PushWithNoPusher_Errors(t *testing.T) {
	t.Parallel()
	inst := resolve(t, testutil.NewMemoryContentStore(), nil, nil, core.Options{
		containerimage.OptKeyPush:  "true",
		containerimage.OptKeyStore: "false",
	})
	req := buildReq(t, testutil.MinimalArtifact(), true)
	_, _, err := inst.Export(context.Background(), req)
	testutil.Error(t, err, "push with no Pusher must error")
	testutil.Contains(t, err.Error(), "Pusher", "error message")
}

// ════════════════════════════════════════════════════════════════════
// EXPORT — multi-platform
// ════════════════════════════════════════════════════════════════════

func TestExport_MultiPlatform_WritesIndex(t *testing.T) {
	t.Parallel()
	store := testutil.NewMemoryContentStore()
	inst := resolve(t, store, nil, nil, core.Options{containerimage.OptKeyStore: "false"})

	result, _, err := inst.Export(context.Background(), buildReq(t, testutil.MultiPlatformArtifact(), false))
	testutil.NoError(t, err)
	if result.ImageDigest.IsZero() {
		t.Error("multi-platform result digest must not be zero")
	}
	if store.Count() <= 2 {
		t.Errorf("multi-platform must write more blobs than single; got %d", store.Count())
	}
}

// ════════════════════════════════════════════════════════════════════
// EXPORT — epoch
// ════════════════════════════════════════════════════════════════════

func TestExport_EpochSet_ExportSucceeds(t *testing.T) {
	t.Parallel()
	store := testutil.NewMemoryContentStore()
	inst := resolve(t, store, nil, nil, core.Options{containerimage.OptKeyStore: "false"})
	epoch := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	req := buildReq(t, testutil.MinimalArtifact(), false)
	req.Epoch = &epoch

	result, _, err := inst.Export(context.Background(), req)
	testutil.NoError(t, err)
	if result.ImageDigest.IsZero() {
		t.Error("result digest must not be zero")
	}
}

// ════════════════════════════════════════════════════════════════════
// PROGRESS REPORTING
// ════════════════════════════════════════════════════════════════════

func TestExport_ReportsProgress(t *testing.T) {
	t.Parallel()
	store := testutil.NewMemoryContentStore()
	inst := resolve(t, store, nil, nil, core.Options{containerimage.OptKeyStore: "false"})

	rep := progress.NewCapturing()
	req := buildReq(t, testutil.MinimalArtifact(), false)
	req.Reporter = rep

	_, _, err := inst.Export(context.Background(), req)
	testutil.NoError(t, err)

	started := rep.StartedIDs()
	mustContain := func(id string) {
		t.Helper()
		for _, s := range started {
			if s == id {
				return
			}
		}
		t.Errorf("expected progress event %q; got %v", id, started)
	}
	mustContain("export")
	mustContain("layers")
	mustContain("config")
	mustContain("manifest")
	if rep.CompletedWithError() {
		t.Error("no progress events must have errored")
	}
}

// ════════════════════════════════════════════════════════════════════
// RESULT KEYS
// ════════════════════════════════════════════════════════════════════

func TestExport_ResultContainsDigestAndDescriptorKeys(t *testing.T) {
	t.Parallel()
	store := testutil.NewMemoryContentStore()
	inst := resolve(t, store, nil, nil, core.Options{containerimage.OptKeyStore: "false"})

	result, _, err := inst.Export(context.Background(), buildReq(t, testutil.MinimalArtifact(), false))
	testutil.NoError(t, err)

	digest := result.Get(core.ResultKeyImageDigest)
	if digest == "" {
		t.Error("ResultKeyImageDigest must be set")
	}
	testutil.Contains(t, digest, "sha256:", "digest format")

	descriptor := result.Get(core.ResultKeyDescriptor)
	if descriptor == "" {
		t.Error("ResultKeyDescriptor must be set")
	}
}

// ════════════════════════════════════════════════════════════════════
// helpers
// ════════════════════════════════════════════════════════════════════

func resolve(
	t *testing.T,
	store core.ContentStore,
	pusher containerimage.Pusher,
	storer containerimage.ImageStorer,
	opts core.Options,
) core.ExporterInstance {
	t.Helper()
	exp := containerimage.New(
		containerimage.WithContentStore(store),
		containerimage.WithPusher(pusher),
		containerimage.WithImageStorer(storer),
	)
	inst, err := exp.Resolve(context.Background(), opts)
	testutil.NoError(t, err, "Resolve")
	return inst
}

func buildReq(t *testing.T, artifact *core.Artifact, push bool) *core.ExportRequest {
	t.Helper()
	req, err := core.NewExportRequest().
		WithSessionID("test-session").
		WithArtifact(artifact).
		WithPush(push).
		WithReporter(progress.NewCapturing()).
		Build()
	testutil.NoError(t, err, "buildReq")
	return req
}
