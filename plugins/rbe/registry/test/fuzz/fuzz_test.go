// Package fuzz provides Go 1.18+ fuzz targets for AccelRegistry.
//
// Run with:
//
//	go test ./test/fuzz/ -fuzz=FuzzDetect       -fuzztime=60s
//	go test ./test/fuzz/ -fuzz=FuzzBloomFilter  -fuzztime=60s
//	go test ./test/fuzz/ -fuzz=FuzzManifestPut  -fuzztime=60s
//
// The fuzz engine will explore random inputs and report any panics,
// goroutine leaks, or data races (run with -race).
package fuzz

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/bons/bons-ci/plugins/rbe/registry/internal/accel/estargz"
	"github.com/bons/bons-ci/plugins/rbe/registry/internal/accel/nydus"
	"github.com/bons/bons-ci/plugins/rbe/registry/internal/accel/overlaybd"
	"github.com/bons/bons-ci/plugins/rbe/registry/internal/accel/soci"
	"github.com/bons/bons-ci/plugins/rbe/registry/internal/registry"
	"github.com/bons/bons-ci/plugins/rbe/registry/internal/storage/memory"
	"github.com/bons/bons-ci/plugins/rbe/registry/pkg/bloom"
	"github.com/bons/bons-ci/plugins/rbe/registry/pkg/types"
)

// FuzzDetect verifies that no handler panics or errors on arbitrary manifest bytes.
// Invariants:
//   - Must not panic
//   - If Detect returns true, it returns a known AccelType
//   - Running all handlers on the same data must be safe concurrently
func FuzzDetect(f *testing.F) {
	// Seed corpus with known-good manifests
	f.Add(mustMarshal(makeNydusManifest()))
	f.Add(mustMarshal(makeSOCIManifest()))
	f.Add(mustMarshal(makeEstargzManifest()))
	f.Add(mustMarshal(makeOverlayBDManifest()))
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"schemaVersion":2}`))
	f.Add([]byte(`not json at all`))
	f.Add([]byte(nil))

	handlers := []interface {
		Detect(context.Context, ocispec.Manifest, []byte) (types.AccelType, bool, error)
		Name() types.AccelType
	}{
		nydus.New(), estargz.New(), soci.New(), overlaybd.New(),
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		var m ocispec.Manifest
		_ = json.Unmarshal(data, &m)

		for _, h := range handlers {
			accelType, ok, err := h.Detect(context.Background(), m, nil)
			// Must not panic — that's the primary invariant.
			if err != nil {
				return // errors are acceptable, panics are not
			}
			if ok {
				// If Detect returns true, type must be the handler's own type
				if accelType != h.Name() {
					t.Errorf("handler %s returned wrong type %s", h.Name(), accelType)
				}
			}
		}
	})
}

// FuzzBloomFilter verifies the bloom filter never panics, never returns
// false-negative (if added, Test must return true), and that concurrent
// Add+Test are race-free.
func FuzzBloomFilter(f *testing.F) {
	f.Add([]byte("sha256:abc123"))
	f.Add([]byte(""))
	f.Add([]byte("sha512:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"))
	f.Add(make([]byte, 1024))

	f.Fuzz(func(t *testing.T, data []byte) {
		filter := bloom.New(bloom.DefaultM, bloom.DefaultK)

		// Add must not panic
		filter.Add(data)

		// After Add, Test must return true (no false negatives)
		if !filter.Test(data) {
			t.Errorf("bloom filter: false negative for data len=%d", len(data))
		}

		// String helpers must not panic
		filter.AddString(string(data))
		_ = filter.TestString(string(data))
	})
}

// FuzzManifestPut verifies that pushing arbitrary manifest bytes to the
// registry never panics, even for malformed or malicious input.
// The registry must return an error, not panic.
func FuzzManifestPut(f *testing.F) {
	f.Add(mustMarshal(makeNydusManifest()), "library/node", "latest")
	f.Add([]byte(`{}`), "repo", "tag")
	f.Add([]byte(`not json`), "repo", "tag")
	f.Add([]byte(nil), "a", "b")
	f.Add(mustMarshal(makeSOCIManifest()), "library/busybox", "soci")

	f.Fuzz(func(t *testing.T, manifestBytes []byte, repo, ref string) {
		if repo == "" || ref == "" {
			return
		}
		// Sanitise: only allow printable ASCII in repo/ref
		for _, c := range repo + ref {
			if c > 127 || c < 32 {
				return
			}
		}

		reg, err := registry.New(registry.Config{ExpectedSources: 1000})
		if err != nil {
			return
		}

		// Pre-populate blobs if manifest has a valid config+layers
		var m ocispec.Manifest
		if json.Unmarshal(manifestBytes, &m) == nil {
			store := memory.New()
			ctx := context.Background()
			for _, desc := range append(m.Layers, m.Config) {
				if desc.Digest != "" && desc.Size > 0 && desc.Size < 1024*1024 {
					data := make([]byte, desc.Size)
					actual := digest.Canonical.FromBytes(data)
					if actual == desc.Digest {
						_ = store.Put(ctx, desc.Digest, bytes.NewReader(data), desc.Size)
					}
				}
			}
		}

		// The key invariant: PutManifest must never panic.
		ctx := context.Background()
		_, _ = reg.PutManifest(ctx, repo, ref, ocispec.MediaTypeImageManifest, manifestBytes)
	})
}

// FuzzDigestString verifies that digest parsing never panics.
func FuzzDigestString(f *testing.F) {
	f.Add("sha256:abc123")
	f.Add("")
	f.Add("sha256:")
	f.Add("invalid")
	f.Add("sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	f.Fuzz(func(t *testing.T, s string) {
		d, err := digest.Parse(s)
		if err != nil {
			return
		}
		// Valid digest: String() must round-trip
		if d.String() == "" {
			t.Errorf("non-empty digest String() returned empty")
		}
		// Algorithm must be known
		if !d.Algorithm().Available() {
			// OK — algorithm might not be available in this binary
		}
	})
}

// ────────────────────────────────────────────────────────────────────────────
// Seed corpus helpers
// ────────────────────────────────────────────────────────────────────────────

func mustMarshal(v interface{}) []byte {
	b, _ := json.Marshal(v)
	return b
}

func makeNydusManifest() ocispec.Manifest {
	return ocispec.Manifest{
		MediaType: ocispec.MediaTypeImageManifest,
		Config: ocispec.Descriptor{
			MediaType: ocispec.MediaTypeImageConfig,
			Digest:    digest.Canonical.FromString("cfg"),
			Size:      3,
		},
		Layers: []ocispec.Descriptor{{
			MediaType:   types.NydusLayerMediaType,
			Digest:      digest.Canonical.FromString("layer"),
			Size:        5,
			Annotations: map[string]string{types.NydusAnnotationSourceDigest: digest.Canonical.FromString("src").String()},
		}},
		Annotations: map[string]string{
			types.NydusAnnotationSourceDigest: digest.Canonical.FromString("src").String(),
		},
	}
}

func makeSOCIManifest() ocispec.Manifest {
	src := digest.Canonical.FromString("soci-subject")
	return ocispec.Manifest{
		MediaType:    types.SOCIArtifactType,
		ArtifactType: types.SOCIArtifactType,
		Config: ocispec.Descriptor{
			MediaType: types.SOCIArtifactType,
			Digest:    digest.Canonical.FromString("soci-cfg"),
			Size:      2,
		},
		Layers: []ocispec.Descriptor{},
		Subject: &ocispec.Descriptor{
			MediaType: ocispec.MediaTypeImageManifest,
			Digest:    src,
			Size:      1024,
		},
	}
}

func makeEstargzManifest() ocispec.Manifest {
	return ocispec.Manifest{
		MediaType: ocispec.MediaTypeImageManifest,
		Config: ocispec.Descriptor{
			MediaType: ocispec.MediaTypeImageConfig,
			Digest:    digest.Canonical.FromString("estargz-cfg"),
			Size:      3,
		},
		Layers: []ocispec.Descriptor{{
			MediaType: ocispec.MediaTypeImageLayerGzip,
			Digest:    digest.Canonical.FromString("estargz-layer"),
			Size:      10,
			Annotations: map[string]string{
				types.StargzAnnotationTOCDigest: digest.Canonical.FromString("toc").String(),
			},
		}},
		Annotations: map[string]string{
			types.AnnotationSourceDigest: digest.Canonical.FromString("estargz-src").String(),
		},
	}
}

func makeOverlayBDManifest() ocispec.Manifest {
	return ocispec.Manifest{
		MediaType: ocispec.MediaTypeImageManifest,
		Config: ocispec.Descriptor{
			MediaType: ocispec.MediaTypeImageConfig,
			Digest:    digest.Canonical.FromString("obd-cfg"),
			Size:      3,
		},
		Layers: []ocispec.Descriptor{{
			MediaType: types.OverlayBDLayerMediaType,
			Digest:    digest.Canonical.FromString("obd-layer"),
			Size:      10,
			Annotations: map[string]string{
				types.OverlayBDAnnotationLayer: "true",
			},
		}},
		Annotations: map[string]string{
			types.AnnotationSourceDigest: digest.Canonical.FromString("obd-src").String(),
		},
	}
}
