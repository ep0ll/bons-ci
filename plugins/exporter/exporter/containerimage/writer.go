package containerimage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/bons/bons-ci/plugins/exporter/core"
)

// ─── Wire types (OCI Image Spec) ───────────────────────────────────────────
// These are minimal local representations. In a full implementation these
// would come from github.com/opencontainers/image-spec/specs-go/v1.

type ociDescriptor struct {
	MediaType   string            `json:"mediaType"`
	Digest      string            `json:"digest"`
	Size        int64             `json:"size"`
	Annotations map[string]string `json:"annotations,omitempty"`
	Platform    *ociPlatform      `json:"platform,omitempty"`
}

type ociPlatform struct {
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
	Variant      string `json:"variant,omitempty"`
	OSVersion    string `json:"os.version,omitempty"`
}

type ociManifest struct {
	SchemaVersion int               `json:"schemaVersion"`
	MediaType     string            `json:"mediaType"`
	Config        ociDescriptor     `json:"config"`
	Layers        []ociDescriptor   `json:"layers"`
	Annotations   map[string]string `json:"annotations,omitempty"`
}

type ociIndex struct {
	SchemaVersion int               `json:"schemaVersion"`
	MediaType     string            `json:"mediaType"`
	Manifests     []ociDescriptor   `json:"manifests"`
	Annotations   map[string]string `json:"annotations,omitempty"`
}

type ociImageConfig struct {
	Architecture string       `json:"architecture"`
	OS           string       `json:"os"`
	Created      *time.Time   `json:"created,omitempty"`
	RootFS       ociRootFS    `json:"rootfs"`
	History      []ociHistory `json:"history,omitempty"`
	Config       *ociConfig   `json:"config,omitempty"`
}

type ociRootFS struct {
	Type    string   `json:"type"`
	DiffIDs []string `json:"diff_ids"`
}

type ociHistory struct {
	Created    *time.Time `json:"created,omitempty"`
	CreatedBy  string     `json:"created_by,omitempty"`
	Comment    string     `json:"comment,omitempty"`
	EmptyLayer bool       `json:"empty_layer,omitempty"`
}

type ociConfig struct {
	Env        []string          `json:"Env,omitempty"`
	Entrypoint []string          `json:"Entrypoint,omitempty"`
	Cmd        []string          `json:"Cmd,omitempty"`
	WorkingDir string            `json:"WorkingDir,omitempty"`
	Labels     map[string]string `json:"Labels,omitempty"`
}

// ─── manifestWriter ────────────────────────────────────────────────────────

// manifestWriter contains the stateless logic for committing image content
// to a ContentStore. All methods are pure transformations — no side-effects
// other than writing to the store.
type manifestWriter struct {
	store core.ContentStore
}

func newManifestWriter(store core.ContentStore) *manifestWriter {
	return &manifestWriter{store: store}
}

// commitLayers persists each layer blob and returns its descriptor.
func (w *manifestWriter) commitLayers(
	ctx context.Context,
	layers []core.Layer,
	cfg Config,
) ([]core.BlobDescriptor, error) {
	descs := make([]core.BlobDescriptor, 0, len(layers))
	for idx, layer := range layers {
		// In production: apply compression if needed here.
		// For this framework, we store the descriptor as-is.
		if layer.Descriptor.Digest.IsZero() {
			return nil, fmt.Errorf("layer[%d]: descriptor has zero digest", idx)
		}
		descs = append(descs, layer.Descriptor)
	}
	return descs, nil
}

// commitConfig builds and persists the OCI image config blob.
func (w *manifestWriter) commitConfig(
	ctx context.Context,
	rawConfig []byte,
	layers []core.BlobDescriptor,
	epoch *time.Time,
	cfg Config,
) (core.BlobDescriptor, error) {
	var imgCfg ociImageConfig

	if len(rawConfig) > 0 {
		if err := json.Unmarshal(rawConfig, &imgCfg); err != nil {
			return core.BlobDescriptor{}, fmt.Errorf("unmarshal image config: %w", err)
		}
	} else {
		imgCfg = defaultImageConfig()
	}

	// Rebuild rootfs DiffIDs from committed layers.
	imgCfg.RootFS = ociRootFS{Type: "layers"}
	for _, l := range layers {
		diffID := l.Digest.String() // simplified; real code uses uncompressed digest
		imgCfg.RootFS.DiffIDs = append(imgCfg.RootFS.DiffIDs, diffID)
	}

	// Clamp creation time to epoch.
	if epoch != nil {
		if imgCfg.Created == nil || imgCfg.Created.After(*epoch) {
			imgCfg.Created = epoch
		}
		for i := range imgCfg.History {
			h := &imgCfg.History[i]
			if h.Created == nil || h.Created.After(*epoch) {
				h.Created = epoch
			}
		}
	}

	configBytes, err := json.MarshalIndent(imgCfg, "", "  ")
	if err != nil {
		return core.BlobDescriptor{}, fmt.Errorf("marshal image config: %w", err)
	}

	mediaType := core.MediaTypeOCIImageConfig
	if !cfg.OCITypes {
		mediaType = "application/vnd.docker.container.image.v1+json"
	}

	dgst := core.NewDigest(configBytes)
	desc := core.BlobDescriptor{
		Digest:    dgst,
		Size:      int64(len(configBytes)),
		MediaType: mediaType,
	}

	if err := w.writeBlob(ctx, configBytes, desc); err != nil {
		return core.BlobDescriptor{}, fmt.Errorf("write config blob: %w", err)
	}
	return desc, nil
}

// commitManifest builds and persists the OCI image manifest.
func (w *manifestWriter) commitManifest(
	ctx context.Context,
	config core.BlobDescriptor,
	layers []core.BlobDescriptor,
	annotations map[string]string,
	cfg Config,
) (core.BlobDescriptor, error) {
	mediaType := string(core.MediaTypeOCIImageManifest)
	if !cfg.OCITypes {
		mediaType = string(core.MediaTypeDockerManifest)
	}

	mfst := ociManifest{
		SchemaVersion: 2,
		MediaType:     mediaType,
		Config:        toOCIDescriptor(config),
		Annotations:   annotations,
	}
	for _, l := range layers {
		mfst.Layers = append(mfst.Layers, toOCIDescriptor(l))
	}

	data, err := json.MarshalIndent(mfst, "", "  ")
	if err != nil {
		return core.BlobDescriptor{}, fmt.Errorf("marshal manifest: %w", err)
	}

	dgst := core.NewDigest(data)
	desc := core.BlobDescriptor{
		Digest:      dgst,
		Size:        int64(len(data)),
		MediaType:   core.MediaType(mediaType),
		Annotations: annotations,
	}

	if err := w.writeBlob(ctx, data, desc); err != nil {
		return core.BlobDescriptor{}, fmt.Errorf("write manifest blob: %w", err)
	}
	return desc, nil
}

// commitIndex builds and persists an OCI image index (multi-platform manifest).
func (w *manifestWriter) commitIndex(
	ctx context.Context,
	manifests []core.BlobDescriptor,
	annotations map[string]string,
	cfg Config,
) (core.BlobDescriptor, error) {
	mediaType := string(core.MediaTypeOCIImageIndex)
	if !cfg.OCITypes {
		mediaType = string(core.MediaTypeDockerManifestList)
	}

	idx := ociIndex{
		SchemaVersion: 2,
		MediaType:     mediaType,
		Annotations:   annotations,
	}
	for _, m := range manifests {
		d := toOCIDescriptor(m)
		// Extract platform from annotation set by instance.go.
		if p, ok := m.Annotations["platform"]; ok {
			d.Platform = parsePlatformString(p)
		}
		idx.Manifests = append(idx.Manifests, d)
	}

	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return core.BlobDescriptor{}, fmt.Errorf("marshal index: %w", err)
	}

	dgst := core.NewDigest(data)
	desc := core.BlobDescriptor{
		Digest:    dgst,
		Size:      int64(len(data)),
		MediaType: core.MediaType(mediaType),
	}

	if err := w.writeBlob(ctx, data, desc); err != nil {
		return core.BlobDescriptor{}, fmt.Errorf("write index blob: %w", err)
	}
	return desc, nil
}

// commitAttestationManifest builds and persists an attestation manifest.
// The attestation manifest is linked to the subject manifest via the
// Docker annotation "vnd.docker.reference.digest".
func (w *manifestWriter) commitAttestationManifest(
	ctx context.Context,
	subject core.BlobDescriptor,
	attestations []core.AttestationRecord,
	cfg Config,
) (core.BlobDescriptor, error) {
	mediaType := string(core.MediaTypeOCIImageManifest)
	if !cfg.OCITypes {
		mediaType = string(core.MediaTypeDockerManifest)
	}

	layers := make([]ociDescriptor, 0, len(attestations))
	for _, att := range attestations {
		data := att.Payload
		dgst := core.NewDigest(data)
		desc := core.BlobDescriptor{
			Digest:    dgst,
			Size:      int64(len(data)),
			MediaType: core.MediaTypeInTotoStatement,
			Annotations: map[string]string{
				"in-toto.io/predicate-type": att.PredicateType,
			},
		}
		if err := w.writeBlob(ctx, data, desc); err != nil {
			return core.BlobDescriptor{}, err
		}
		layers = append(layers, toOCIDescriptor(desc))
	}

	// Minimal empty config for the attestation manifest.
	emptyCfgData := []byte("{}")
	emptyCfgDgst := core.NewDigest(emptyCfgData)
	emptyCfgDesc := core.BlobDescriptor{
		Digest:    emptyCfgDgst,
		Size:      int64(len(emptyCfgData)),
		MediaType: core.MediaTypeOCIImageConfig,
	}
	if err := w.writeBlob(ctx, emptyCfgData, emptyCfgDesc); err != nil {
		return core.BlobDescriptor{}, err
	}

	mfst := ociManifest{
		SchemaVersion: 2,
		MediaType:     mediaType,
		Config:        toOCIDescriptor(emptyCfgDesc),
		Layers:        layers,
		Annotations: map[string]string{
			"vnd.docker.reference.type":   "attestation-manifest",
			"vnd.docker.reference.digest": subject.Digest.String(),
		},
	}

	data, err := json.MarshalIndent(mfst, "", "  ")
	if err != nil {
		return core.BlobDescriptor{}, fmt.Errorf("marshal attestation manifest: %w", err)
	}

	dgst := core.NewDigest(data)
	desc := core.BlobDescriptor{
		Digest:    dgst,
		Size:      int64(len(data)),
		MediaType: core.MediaType(mediaType),
	}

	return desc, w.writeBlob(ctx, data, desc)
}

// ─── helpers ───────────────────────────────────────────────────────────────

func (w *manifestWriter) writeBlob(ctx context.Context, data []byte, desc core.BlobDescriptor) error {
	if w.store == nil {
		return fmt.Errorf("manifestWriter: no ContentStore configured")
	}
	// Deduplicate: skip write if already present.
	if has, err := w.store.Has(ctx, desc.Digest); err == nil && has {
		return nil
	}
	written, err := w.store.WriteBlob(ctx, data, string(desc.MediaType))
	if err != nil {
		return err
	}
	if !bytes.Equal([]byte(written.String()), []byte(desc.Digest.String())) {
		return fmt.Errorf("content store digest mismatch: wrote %s, expected %s",
			written, desc.Digest)
	}
	return nil
}

func toOCIDescriptor(d core.BlobDescriptor) ociDescriptor {
	return ociDescriptor{
		MediaType:   string(d.MediaType),
		Digest:      d.Digest.String(),
		Size:        d.Size,
		Annotations: d.Annotations,
	}
}

func defaultImageConfig() ociImageConfig {
	return ociImageConfig{
		Architecture: "amd64",
		OS:           "linux",
		RootFS:       ociRootFS{Type: "layers"},
	}
}

func parsePlatformString(s string) *ociPlatform {
	// "linux/amd64" or "linux/arm64/v8"
	parts := splitMax(s, '/', 3)
	p := &ociPlatform{}
	if len(parts) > 0 {
		p.OS = parts[0]
	}
	if len(parts) > 1 {
		p.Architecture = parts[1]
	}
	if len(parts) > 2 {
		p.Variant = parts[2]
	}
	return p
}

func splitMax(s string, sep byte, n int) []string {
	var parts []string
	start := 0
	for i := 0; i < len(s) && len(parts) < n-1; i++ {
		if s[i] == sep {
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	parts = append(parts, s[start:])
	return parts
}
