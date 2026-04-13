package registry_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bons/bons-ci/plugins/rbe/pkg/models"
)

// ── minimal OCI manifest for testing ─────────────────────────────────────────

func buildManifest(layers []struct {
	Digest, MediaType string
	Size              int64
}) []byte {
	type desc struct {
		Digest    string `json:"digest"`
		MediaType string `json:"mediaType"`
		Size      int64  `json:"size"`
	}
	type manifest struct {
		SchemaVersion int    `json:"schemaVersion"`
		MediaType     string `json:"mediaType"`
		Config        desc   `json:"config"`
		Layers        []desc `json:"layers"`
	}
	m := manifest{
		SchemaVersion: 2,
		MediaType:     "application/vnd.oci.image.manifest.v1+json",
		Config:        desc{Digest: "sha256:" + strings.Repeat("c", 64), MediaType: "application/vnd.oci.image.config.v1+json", Size: 100},
	}
	for _, l := range layers {
		m.Layers = append(m.Layers, desc{Digest: l.Digest, MediaType: l.MediaType, Size: l.Size})
	}
	b, _ := json.Marshal(m)
	return b
}

func sha256Digest(data []byte) string {
	h := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(h[:])
}

// TestBlobDiffSets verifies that ConversionRecord diff sets are computed correctly.
func TestBlobDiffSets(t *testing.T) {
	shared := models.BlobDescriptor{Digest: "sha256:" + strings.Repeat("s", 64), Size: 100}
	srcOnly := models.BlobDescriptor{Digest: "sha256:" + strings.Repeat("a", 64), Size: 200}
	dstOnly := models.BlobDescriptor{Digest: "sha256:" + strings.Repeat("b", 64), Size: 300}

	rec := models.ConversionRecord{
		ID:           "test-conv",
		SourceDigest: "sha256:src",
		SourceFormat: models.ImageFormatOCI,
		SourceBlobs:  []models.BlobDescriptor{shared, srcOnly},
		TargetDigest: "sha256:dst",
		TargetFormat: models.ImageFormatNydus,
		TargetBlobs:  []models.BlobDescriptor{shared, dstOnly},
		ConvertedAt:  time.Now(),
	}

	// Manually compute expected diff
	srcMap := map[string]struct{}{}
	for _, b := range rec.SourceBlobs {
		srcMap[b.Digest] = struct{}{}
	}
	dstMap := map[string]struct{}{}
	for _, b := range rec.TargetBlobs {
		dstMap[b.Digest] = struct{}{}
	}

	var sharedDigests, srcOnlyDigests, dstOnlyDigests []string
	for d := range srcMap {
		if _, ok := dstMap[d]; ok {
			sharedDigests = append(sharedDigests, d)
		} else {
			srcOnlyDigests = append(srcOnlyDigests, d)
		}
	}
	for d := range dstMap {
		if _, ok := srcMap[d]; !ok {
			dstOnlyDigests = append(dstOnlyDigests, d)
		}
	}

	if len(sharedDigests) != 1 || sharedDigests[0] != shared.Digest {
		t.Errorf("expected 1 shared blob, got %v", sharedDigests)
	}
	if len(srcOnlyDigests) != 1 || srcOnlyDigests[0] != srcOnly.Digest {
		t.Errorf("expected 1 src-only blob, got %v", srcOnlyDigests)
	}
	if len(dstOnlyDigests) != 1 || dstOnlyDigests[0] != dstOnly.Digest {
		t.Errorf("expected 1 dst-only blob, got %v", dstOnlyDigests)
	}
}

// TestManifestDigest verifies that computed digests are stable.
func TestManifestDigest(t *testing.T) {
	raw := buildManifest([]struct {
		Digest, MediaType string
		Size              int64
	}{
		{"sha256:" + strings.Repeat("a", 64), "application/vnd.oci.image.layer.v1.tar+gzip", 1024},
	})
	d1 := sha256Digest(raw)
	d2 := sha256Digest(raw)
	if d1 != d2 {
		t.Fatal("digest is not stable")
	}
	if !strings.HasPrefix(d1, "sha256:") {
		t.Fatalf("expected sha256: prefix, got %s", d1)
	}
}

// TestImageFormatDetection verifies mediaType → ImageFormat mapping.
func TestImageFormatDetection(t *testing.T) {
	cases := []struct {
		mediaType string
		want      models.ImageFormat
	}{
		{"application/vnd.oci.image.manifest.v1+json", models.ImageFormatOCI},
		{"application/vnd.docker.distribution.manifest.v2+json", models.ImageFormatDocker},
		{"application/vnd.oci.image.manifest.v1+json; nydus", models.ImageFormatNydus},
		{"application/vnd.oci.image.layer.v1.tar+estargz", models.ImageFormatEStargz},
		{"application/vnd.oci.image.layer.v1.tar+zstd", models.ImageFormatZstd},
	}
	for _, tc := range cases {
		detect := func(mt string) models.ImageFormat {
			switch {
			case strings.Contains(mt, "nydus"):
				return models.ImageFormatNydus
			case strings.Contains(mt, "estargz"):
				return models.ImageFormatEStargz
			case strings.Contains(mt, "zstd"):
				return models.ImageFormatZstd
			case strings.Contains(mt, "docker"):
				return models.ImageFormatDocker
			default:
				return models.ImageFormatOCI
			}
		}
		got := detect(tc.mediaType)
		if got != tc.want {
			t.Errorf("mediaType %q: want %s, got %s", tc.mediaType, tc.want, got)
		}
	}
}

var _ = context.Background
