// Package integration tests the full AccelRegistry HTTP API end-to-end.
//
// Tests exercise the complete request lifecycle:
//  1. Upload blobs
//  2. Push OCI manifests (with accel annotations)
//  3. Query accel index via /accel/v1/query/{digest}
//  4. Pull specific accel types via /accel/v1/pull
//  5. Traverse the OCI DAG via /accel/v1/dag/{name}/{digest}
//  6. Fetch image metadata via /accel/v1/metadata/{name}/{digest}
//  7. OCI 1.1 referrers via /v2/{name}/referrers/{digest}
//  8. Tag listing via /v2/{name}/tags/list
//  9. Manifest delete via DELETE /v2/{name}/manifests/{reference}
package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	apiv1 "github.com/bons/bons-ci/plugins/rbe/registry/api/v1"
	"github.com/bons/bons-ci/plugins/rbe/registry/internal/logger"
	"github.com/bons/bons-ci/plugins/rbe/registry/internal/registry"
	"github.com/bons/bons-ci/plugins/rbe/registry/pkg/types"
)

// ────────────────────────────────────────────────────────────────────────────
// Test server factory
// ────────────────────────────────────────────────────────────────────────────

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	reg, err := registry.New(registry.Config{
		ExpectedSources: 1000,
		Log:             logger.NewNop(),
	})
	if err != nil {
		t.Fatalf("creating registry: %v", err)
	}
	h := apiv1.New(reg, logger.NewNop())
	r := chi.NewRouter()
	r.Use(chimiddleware.RequestID)
	r.Mount("/", h.Router())
	return httptest.NewServer(r)
}

// ────────────────────────────────────────────────────────────────────────────
// Helper: push a full image (blobs + manifest) to the test server
// ────────────────────────────────────────────────────────────────────────────

type pushedImage struct {
	Repo         string
	Tag          string
	ManifestDgst digest.Digest
	ConfigDgst   digest.Digest
	LayerDgst    digest.Digest
}

func pushImage(t *testing.T, server *httptest.Server, repo, tag string, annotations map[string]string) *pushedImage {
	t.Helper()
	client := server.Client()

	// 1. Upload config blob
	configData := []byte(`{"architecture":"amd64","os":"linux","config":{}}`)
	configDgst := digest.Canonical.FromBytes(configData)
	uploadBlob(t, client, server.URL, repo, configData, configDgst)

	// 2. Upload layer blob
	layerData := []byte("fake-layer-content-for-testing")
	layerDgst := digest.Canonical.FromBytes(layerData)
	uploadBlob(t, client, server.URL, repo, layerData, layerDgst)

	// 3. Build manifest
	manifest := ocispec.Manifest{
		MediaType: ocispec.MediaTypeImageManifest,
		Config: ocispec.Descriptor{
			MediaType: ocispec.MediaTypeImageConfig,
			Digest:    configDgst,
			Size:      int64(len(configData)),
		},
		Layers: []ocispec.Descriptor{
			{
				MediaType: ocispec.MediaTypeImageLayerGzip,
				Digest:    layerDgst,
				Size:      int64(len(layerData)),
			},
		},
		Annotations: annotations,
	}
	manifest.SchemaVersion = 2
	manifestData, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}

	// 4. Push manifest
	url := fmt.Sprintf("%s/v2/%s/manifests/%s", server.URL, repo, tag)
	req, _ := http.NewRequest(http.MethodPut, url, bytes.NewReader(manifestData))
	req.Header.Set("Content-Type", ocispec.MediaTypeImageManifest)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("push manifest: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("push manifest: status %d, body: %s", resp.StatusCode, body)
	}

	return &pushedImage{
		Repo:         repo,
		Tag:          tag,
		ManifestDgst: digest.Canonical.FromBytes(manifestData),
		ConfigDgst:   configDgst,
		LayerDgst:    layerDgst,
	}
}

func uploadBlob(t *testing.T, client *http.Client, baseURL, repo string, data []byte, dgst digest.Digest) {
	t.Helper()
	// Initiate upload
	url := fmt.Sprintf("%s/v2/%s/blobs/uploads/", baseURL, repo)
	resp, err := client.Post(url, "", nil)
	if err != nil {
		t.Fatalf("initiate upload: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("initiate upload: status %d", resp.StatusCode)
	}
	uuid := resp.Header.Get("Docker-Upload-UUID")
	if uuid == "" {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("no upload UUID in response. body: %s", body)
	}

	// Finalize with the blob data
	url = fmt.Sprintf("%s/v2/%s/blobs/uploads/%s?digest=%s", baseURL, repo, uuid, dgst.String())
	req, _ := http.NewRequest(http.MethodPut, url, bytes.NewReader(data))
	req.ContentLength = int64(len(data))
	resp2, err := client.Do(req)
	if err != nil {
		t.Fatalf("finalize upload: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp2.Body)
		t.Fatalf("finalize upload: status %d, body: %s", resp2.StatusCode, body)
	}
}

// ────────────────────────────────────────────────────────────────────────────
// OCI Distribution Spec tests
// ────────────────────────────────────────────────────────────────────────────

func TestAPI_VersionCheck(t *testing.T) {
	server := newTestServer(t)
	defer server.Close()

	resp, err := server.Client().Get(server.URL + "/v2/")
	if err != nil {
		t.Fatalf("GET /v2/: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestAPI_BlobUploadAndDownload(t *testing.T) {
	server := newTestServer(t)
	defer server.Close()

	data := []byte("integration test blob data")
	dgst := digest.Canonical.FromBytes(data)
	uploadBlob(t, server.Client(), server.URL, "library/test", data, dgst)

	// Download via GET
	resp, err := server.Client().Get(fmt.Sprintf("%s/v2/library/test/blobs/%s", server.URL, dgst))
	if err != nil {
		t.Fatalf("GET blob: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	got, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(got, data) {
		t.Errorf("blob content mismatch")
	}
}

func TestAPI_ManifestPushAndPull(t *testing.T) {
	server := newTestServer(t)
	defer server.Close()

	img := pushImage(t, server, "library/node", "20-alpine", nil)

	// GET by tag
	resp, err := server.Client().Get(fmt.Sprintf("%s/v2/%s/manifests/%s", server.URL, img.Repo, img.Tag))
	if err != nil {
		t.Fatalf("GET manifest by tag: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	// GET by digest
	resp2, err := server.Client().Get(fmt.Sprintf("%s/v2/%s/manifests/%s", server.URL, img.Repo, img.ManifestDgst))
	if err != nil {
		t.Fatalf("GET manifest by digest: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for digest lookup, got %d", resp2.StatusCode)
	}
}

func TestAPI_TagList(t *testing.T) {
	server := newTestServer(t)
	defer server.Close()

	pushImage(t, server, "library/python", "3.12", nil)
	pushImage(t, server, "library/python", "3.11", nil)

	resp, err := server.Client().Get(fmt.Sprintf("%s/v2/library/python/tags/list", server.URL))
	if err != nil {
		t.Fatalf("GET tags: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var result struct {
		Name string   `json:"name"`
		Tags []string `json:"tags"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&result)
	if len(result.Tags) < 2 {
		t.Errorf("expected ≥2 tags, got %d: %v", len(result.Tags), result.Tags)
	}
}

func TestAPI_ManifestDelete(t *testing.T) {
	server := newTestServer(t)
	defer server.Close()

	img := pushImage(t, server, "library/alpine", "3.19", nil)

	url := fmt.Sprintf("%s/v2/%s/manifests/%s", server.URL, img.Repo, img.ManifestDgst)
	req, _ := http.NewRequest(http.MethodDelete, url, nil)
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("DELETE manifest: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("expected 202, got %d", resp.StatusCode)
	}

	// Verify gone
	resp2, _ := server.Client().Get(url)
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Errorf("after delete: expected 404, got %d", resp2.StatusCode)
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Accel API tests
// ────────────────────────────────────────────────────────────────────────────

func TestAPI_QueryAccel_Nydus(t *testing.T) {
	server := newTestServer(t)
	defer server.Close()

	// Push source image
	sourceImg := pushImage(t, server, "library/node", "20", nil)

	// Push nydus variant with annotation pointing back to source
	nydusAnnotations := map[string]string{
		types.NydusAnnotationSourceDigest: sourceImg.ManifestDgst.String(),
		types.AnnotationSourceDigest:      sourceImg.ManifestDgst.String(),
		types.AnnotationAccelType:         string(types.AccelNydus),
	}

	// Upload nydus-specific layers
	nydusLayerData := []byte("fake-nydus-blob-content")
	nydusLayerDgst := digest.Canonical.FromBytes(nydusLayerData)
	uploadBlob(t, server.Client(), server.URL, "library/node", nydusLayerData, nydusLayerDgst)

	cfgData := []byte(`{"architecture":"amd64","os":"linux"}`)
	cfgDgst := digest.Canonical.FromBytes(cfgData)
	uploadBlob(t, server.Client(), server.URL, "library/node", cfgData, cfgDgst)

	manifest := ocispec.Manifest{
		MediaType: ocispec.MediaTypeImageManifest,
		Config: ocispec.Descriptor{
			MediaType: ocispec.MediaTypeImageConfig,
			Digest:    cfgDgst,
			Size:      int64(len(cfgData)),
		},
		Layers: []ocispec.Descriptor{
			{
				MediaType:   types.NydusLayerMediaType, // nydus-specific media type
				Digest:      nydusLayerDgst,
				Size:        int64(len(nydusLayerData)),
				Annotations: map[string]string{types.NydusAnnotationSourceDigest: sourceImg.LayerDgst.String()},
			},
		},
		Annotations: nydusAnnotations,
	}
	manifest.SchemaVersion = 2
	manifestData, _ := json.Marshal(manifest)

	req, _ := http.NewRequest(http.MethodPut,
		fmt.Sprintf("%s/v2/library/node/manifests/20-nydus", server.URL),
		bytes.NewReader(manifestData))
	req.Header.Set("Content-Type", ocispec.MediaTypeImageManifest)
	resp, _ := server.Client().Do(req)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("push nydus manifest: status %d: %s", resp.StatusCode, body)
	}

	// Query accel index using the source manifest digest
	qURL := fmt.Sprintf("%s/accel/v1/query/%s", server.URL, sourceImg.ManifestDgst)
	qResp, err := server.Client().Get(qURL)
	if err != nil {
		t.Fatalf("query accel: %v", err)
	}
	defer qResp.Body.Close()

	var result types.AccelQueryResult
	if err := json.NewDecoder(qResp.Body).Decode(&result); err != nil {
		t.Fatalf("decode query result: %v", err)
	}

	if !result.Found {
		t.Error("expected Found=true for nydus variant")
	}
	if result.TotalVariants == 0 {
		t.Error("expected TotalVariants > 0")
	}
	if _, ok := result.Variants[types.AccelNydus]; !ok {
		t.Errorf("expected nydus variant, got types: %v", result.SupportedTypes)
	}
}

func TestAPI_QueryAccel_NotFound(t *testing.T) {
	server := newTestServer(t)
	defer server.Close()

	absent := digest.Canonical.FromString("no-accel-for-this")
	resp, err := server.Client().Get(fmt.Sprintf("%s/accel/v1/query/%s", server.URL, absent))
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

func TestAPI_PullAccel(t *testing.T) {
	server := newTestServer(t)
	defer server.Close()

	// Push source + nydus variant (same as above but inline)
	src := pushImage(t, server, "test/pull", "v1", nil)

	nydusCfg := []byte(`{"os":"linux"}`)
	nydusCfgDgst := digest.Canonical.FromBytes(nydusCfg)
	uploadBlob(t, server.Client(), server.URL, "test/pull", nydusCfg, nydusCfgDgst)

	nydusLayer := []byte("nydus-layer-pull-test")
	nydusLayerDgst := digest.Canonical.FromBytes(nydusLayer)
	uploadBlob(t, server.Client(), server.URL, "test/pull", nydusLayer, nydusLayerDgst)

	nydusManifest := ocispec.Manifest{
		MediaType: ocispec.MediaTypeImageManifest,
		Config:    ocispec.Descriptor{MediaType: ocispec.MediaTypeImageConfig, Digest: nydusCfgDgst, Size: int64(len(nydusCfg))},
		Layers: []ocispec.Descriptor{
			{MediaType: types.NydusLayerMediaType, Digest: nydusLayerDgst, Size: int64(len(nydusLayer))},
		},
		Annotations: map[string]string{
			types.AnnotationSourceDigest:      src.ManifestDgst.String(),
			types.NydusAnnotationSourceDigest: src.ManifestDgst.String(),
		},
	}
	nydusManifest.SchemaVersion = 2
	nmData, _ := json.Marshal(nydusManifest)
	req, _ := http.NewRequest(http.MethodPut,
		fmt.Sprintf("%s/v2/test/pull/manifests/v1-nydus", server.URL),
		bytes.NewReader(nmData))
	req.Header.Set("Content-Type", ocispec.MediaTypeImageManifest)
	resp, _ := server.Client().Do(req)
	resp.Body.Close()

	// Issue pull request
	pullReq := types.PullRequest{
		SourceDigest: src.ManifestDgst,
		AccelTypes:   []types.AccelType{types.AccelNydus},
	}
	pullBody, _ := json.Marshal(pullReq)
	pullResp, err := server.Client().Post(
		server.URL+"/accel/v1/pull",
		"application/json",
		bytes.NewReader(pullBody),
	)
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	defer pullResp.Body.Close()
	if pullResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(pullResp.Body)
		t.Fatalf("pull: status %d: %s", pullResp.StatusCode, body)
	}

	var pullResult types.PullResult
	_ = json.NewDecoder(pullResp.Body).Decode(&pullResult)
	if len(pullResult.Pulled) == 0 {
		t.Error("expected at least one pulled variant")
	}
}

func TestAPI_DAGTraversal(t *testing.T) {
	server := newTestServer(t)
	defer server.Close()

	img := pushImage(t, server, "library/ubuntu", "22.04", nil)

	resp, err := server.Client().Get(
		fmt.Sprintf("%s/accel/v1/dag/%s/%s", server.URL, img.Repo, img.ManifestDgst))
	if err != nil {
		t.Fatalf("DAG: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("DAG: status %d: %s", resp.StatusCode, body)
	}

	var dagResult types.DAGQueryResult
	_ = json.NewDecoder(resp.Body).Decode(&dagResult)

	if dagResult.TotalNodes == 0 {
		t.Error("expected TotalNodes > 0")
	}
	if dagResult.Root == nil {
		t.Error("expected non-nil Root node")
	}
}

func TestAPI_ImageMetadata(t *testing.T) {
	server := newTestServer(t)
	defer server.Close()

	img := pushImage(t, server, "library/redis", "7.2", nil)

	resp, err := server.Client().Get(
		fmt.Sprintf("%s/accel/v1/metadata/%s/%s", server.URL, img.Repo, img.ManifestDgst))
	if err != nil {
		t.Fatalf("metadata: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("metadata: status %d: %s", resp.StatusCode, body)
	}

	var meta types.ImageMetadata
	_ = json.NewDecoder(resp.Body).Decode(&meta)
	if meta.Digest != img.ManifestDgst {
		t.Errorf("metadata digest mismatch: got %s, want %s", meta.Digest, img.ManifestDgst)
	}
}

func TestAPI_Stats(t *testing.T) {
	server := newTestServer(t)
	defer server.Close()

	resp, err := server.Client().Get(server.URL + "/accel/v1/stats")
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var stats types.IndexStats
	if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
		t.Fatalf("decode stats: %v", err)
	}
	if stats.ShardCount != 256 {
		t.Errorf("expected 256 shards, got %d", stats.ShardCount)
	}
}

func TestAPI_OCI11Referrers(t *testing.T) {
	server := newTestServer(t)
	defer server.Close()

	// Push subject image
	subjectImg := pushImage(t, server, "library/busybox", "latest", nil)

	// Push SOCI index as a referrer (artifact with subject)
	sociCfg := []byte(`{}`)
	sociCfgDgst := digest.Canonical.FromBytes(sociCfg)
	uploadBlob(t, server.Client(), server.URL, "library/busybox", sociCfg, sociCfgDgst)

	sociManifest := ocispec.Manifest{
		MediaType:    ocispec.MediaTypeImageManifest,
		ArtifactType: types.SOCIArtifactType,
		Config: ocispec.Descriptor{
			MediaType: types.SOCIArtifactType,
			Digest:    sociCfgDgst,
			Size:      int64(len(sociCfg)),
		},
		Layers: []ocispec.Descriptor{},
		Subject: &ocispec.Descriptor{
			MediaType: ocispec.MediaTypeImageManifest,
			Digest:    subjectImg.ManifestDgst,
			Size:      1,
		},
	}
	sociManifest.SchemaVersion = 2
	sociData, _ := json.Marshal(sociManifest)
	sociManifestDgst := digest.Canonical.FromBytes(sociData)
	_ = sociManifestDgst

	req, _ := http.NewRequest(http.MethodPut,
		fmt.Sprintf("%s/v2/library/busybox/manifests/soci-index", server.URL),
		bytes.NewReader(sociData))
	req.Header.Set("Content-Type", types.SOCIArtifactType)
	resp, _ := server.Client().Do(req)
	resp.Body.Close()

	// Query referrers for subject
	refResp, err := server.Client().Get(
		fmt.Sprintf("%s/v2/library/busybox/referrers/%s", server.URL, subjectImg.ManifestDgst))
	if err != nil {
		t.Fatalf("referrers: %v", err)
	}
	defer refResp.Body.Close()
	if refResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(refResp.Body)
		t.Fatalf("referrers: status %d: %s", refResp.StatusCode, body)
	}

	var refResult struct {
		Manifests []ocispec.Descriptor `json:"manifests"`
	}
	_ = json.NewDecoder(refResp.Body).Decode(&refResult)
	if len(refResult.Manifests) == 0 {
		t.Error("expected at least one referrer (SOCI index)")
	}
}

func TestAPI_ExistsAccel(t *testing.T) {
	server := newTestServer(t)
	defer server.Close()

	absent := digest.Canonical.FromString("no-accel-exists")
	resp, _ := server.Client().Get(fmt.Sprintf("%s/accel/v1/exists/%s", server.URL, absent))
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 for absent digest, got %d", resp.StatusCode)
	}
}

func TestAPI_HealthChecks(t *testing.T) {
	server := newTestServer(t)
	defer server.Close()

	for _, path := range []string{"/healthz", "/readyz"} {
		resp, err := server.Client().Get(server.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s: expected 200, got %d", path, resp.StatusCode)
		}
	}
}
