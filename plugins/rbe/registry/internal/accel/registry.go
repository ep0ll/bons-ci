// Package accel provides the central registry of AccelHandlers and a
// multi-stage detection pipeline that identifies the acceleration type
// of any OCI manifest.
//
// New acceleration technologies are added by calling Register() with a
// concrete AccelHandler implementation — no other code needs to change.
package accel

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/bons/bons-ci/plugins/rbe/registry/internal/logger"
	"github.com/bons/bons-ci/plugins/rbe/registry/pkg/types"
)

// ────────────────────────────────────────────────────────────────────────────
// Handler registry
// ────────────────────────────────────────────────────────────────────────────

// Registry holds all registered AccelHandlers and orchestrates detection.
// It is safe for concurrent use after the initial call to Register().
type Registry struct {
	mu       sync.RWMutex
	handlers map[types.AccelType]types.AccelHandler
	order    []types.AccelType // insertion-ordered list for deterministic detection
	log      *logger.Logger
}

// NewRegistry returns an empty handler registry.
// Call Register() for each supported AccelType before using it.
func NewRegistry(log *logger.Logger) *Registry {
	return &Registry{
		handlers: make(map[types.AccelType]types.AccelHandler),
		log:      log.With(logger.String("component", "accel_registry")),
	}
}

// Register adds a handler. Panics if a handler for the same AccelType is
// already registered (programming error caught at startup).
func (r *Registry) Register(h types.AccelHandler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t := h.Name()
	if _, exists := r.handlers[t]; exists {
		panic(fmt.Sprintf("accel: handler for type %q already registered", t))
	}
	r.handlers[t] = h
	r.order = append(r.order, t)
	r.log.Info("registered accel handler", logger.String("type", string(t)))
}

// Get returns the handler for a given AccelType, or nil if not registered.
func (r *Registry) Get(t types.AccelType) types.AccelHandler {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.handlers[t]
}

// Detect runs all registered handlers' Detect methods in registration order
// and returns the first matching AccelType. Returns AccelUnknown if none match.
func (r *Registry) Detect(ctx context.Context, manifest ocispec.Manifest, configBlob []byte) (types.AccelType, error) {
	r.mu.RLock()
	order := r.order
	handlers := r.handlers
	r.mu.RUnlock()

	for _, t := range order {
		h := handlers[t]
		accelType, ok, err := h.Detect(ctx, manifest, configBlob)
		if err != nil {
			r.log.Warn("accel detection error",
				logger.String("type", string(t)), logger.Error(err))
			continue
		}
		if ok {
			r.log.Debug("accel type detected", logger.String("type", string(accelType)))
			return accelType, nil
		}
	}
	return types.AccelUnknown, nil
}

// ExtractSourceRefs runs the handler for the given accelType and returns
// all SourceRefs extracted from the manifest and config blob.
func (r *Registry) ExtractSourceRefs(
	ctx context.Context,
	accelType types.AccelType,
	manifest ocispec.Manifest,
	configBlob []byte,
) ([]types.SourceRef, error) {
	r.mu.RLock()
	h := r.handlers[accelType]
	r.mu.RUnlock()

	if h == nil {
		return nil, fmt.Errorf("accel: no handler registered for type %q", accelType)
	}
	return h.ExtractSourceRefs(ctx, manifest, configBlob)
}

// Validate runs the handler's Validate method for the given accelType.
func (r *Registry) Validate(ctx context.Context, accelType types.AccelType, manifest ocispec.Manifest) error {
	r.mu.RLock()
	h := r.handlers[accelType]
	r.mu.RUnlock()

	if h == nil {
		return fmt.Errorf("accel: no handler registered for type %q", accelType)
	}
	return h.Validate(ctx, manifest)
}

// RegisteredTypes returns all currently registered AccelTypes.
func (r *Registry) RegisteredTypes() []types.AccelType {
	r.mu.RLock()
	defer r.mu.RUnlock()
	cp := make([]types.AccelType, len(r.order))
	copy(cp, r.order)
	return cp
}

// ────────────────────────────────────────────────────────────────────────────
// Shared helpers used by all handler implementations
// ────────────────────────────────────────────────────────────────────────────

// ParseManifest unmarshals raw bytes into an ocispec.Manifest.
func ParseManifest(raw []byte) (ocispec.Manifest, error) {
	var m ocispec.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return ocispec.Manifest{}, fmt.Errorf("accel: parsing manifest: %w", err)
	}
	return m, nil
}

// AnnotationSourceRef builds a SourceRef from the canonical
// org.accelregistry.source.digest annotation on a manifest or config.
func AnnotationSourceRef(annotations map[string]string) (types.SourceRef, bool) {
	dgstStr, ok := annotations[types.AnnotationSourceDigest]
	if !ok || dgstStr == "" {
		return types.SourceRef{}, false
	}
	dgst, err := parseDigest(dgstStr)
	if err != nil {
		return types.SourceRef{}, false
	}
	kind := types.SourceRefManifest
	if mt, ok := annotations["org.accelregistry.source.mediatype"]; ok {
		switch mt {
		case ocispec.MediaTypeImageIndex, "application/vnd.docker.distribution.manifest.list.v2+json":
			kind = types.SourceRefIndex
		case ocispec.MediaTypeImageConfig:
			kind = types.SourceRefConfig
		}
	}
	return types.SourceRef{
		Digest:      dgst,
		Kind:        kind,
		Annotations: annotations,
	}, true
}

// SubjectSourceRef builds a SourceRef from the OCI 1.1 subject field.
func SubjectSourceRef(subject *ocispec.Descriptor) (types.SourceRef, bool) {
	if subject == nil {
		return types.SourceRef{}, false
	}
	return types.SourceRef{
		Digest:    subject.Digest,
		MediaType: subject.MediaType,
		Kind:      types.SourceRefManifest,
	}, true
}

// parseDigest wraps digest.Parse with a friendly error.
func parseDigest(s string) (digest.Digest, error) {
	return digest.Parse(s)
}

// LayerSourceRefs builds SourceRefs for every layer in a manifest, where
// the layer annotation key contains a source digest.
func LayerSourceRefs(manifest ocispec.Manifest, annotationKey string) []types.SourceRef {
	var refs []types.SourceRef
	for _, layer := range manifest.Layers {
		if dgstStr, ok := layer.Annotations[annotationKey]; ok && dgstStr != "" {
			refs = append(refs, types.SourceRef{
				Digest: mustParseDigestStr(dgstStr),
				Kind:   types.SourceRefLayer,
			})
		}
	}
	return refs
}

// mustParseDigestStr parses a digest string; returns zero value on failure.
func mustParseDigestStr(s string) digest.Digest {
	d, _ := digest.Parse(s)
	return d
}
