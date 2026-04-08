// Package resolver provides Resolver implementations for different source kinds.
package resolver

import (
	"context"
	"fmt"
	"strings"

	"go.uber.org/zap"

	"github.com/bons/bons-ci/pkg/sbomkit/core/domain"
	"github.com/bons/bons-ci/pkg/sbomkit/core/event"
	"github.com/bons/bons-ci/pkg/sbomkit/core/ports"
)

// ImageResolver resolves container image sources (domain.SourceImage).
//
// Responsibilities:
//   - Validate and normalise the image reference.
//   - Warn if private registry credentials appear missing.
//   - Apply configured mirror substitutions.
//   - Emit lifecycle events.
//
// Actual image pulling is delegated to Syft / Stereoscope so that the resolver
// stays lightweight and does not duplicate credential or cache logic.
type ImageResolver struct {
	logger           *zap.Logger
	bus              *event.Bus
	insecureRegistry bool
	// mirrorMap maps source registry host → mirror host (e.g. "docker.io" → "mirror.corp/proxy").
	mirrorMap map[string]string
}

// ImageResolverOption configures an ImageResolver.
type ImageResolverOption func(*ImageResolver)

// WithInsecureRegistry allows resolving images from registries without TLS.
func WithInsecureRegistry() ImageResolverOption {
	return func(r *ImageResolver) { r.insecureRegistry = true }
}

// WithMirror registers a registry mirror.
// e.g. WithMirror("docker.io", "registry-mirror.corp")
func WithMirror(registry, mirror string) ImageResolverOption {
	return func(r *ImageResolver) {
		if r.mirrorMap == nil {
			r.mirrorMap = make(map[string]string)
		}
		r.mirrorMap[registry] = mirror
	}
}

// NewImageResolver constructs an ImageResolver.
//
// Both logger and bus may be nil; the resolver will use a no-op logger and a
// closed (no-op) bus in those cases, making direct construction always safe.
// Production callers should inject a real bus via SetBus after engine creation.
func NewImageResolver(logger *zap.Logger, bus *event.Bus, opts ...ImageResolverOption) ports.Resolver {
	if logger == nil {
		logger = zap.NewNop()
	}
	if bus == nil {
		bus = event.NewBus(0) // synchronous no-op bus; replaced by SetBus in production
	}
	r := &ImageResolver{logger: logger, bus: bus}
	for _, o := range opts {
		o(r)
	}
	return r
}

// Accepts implements ports.Resolver.
func (r *ImageResolver) Accepts(kind domain.SourceKind) bool {
	return kind == domain.SourceImage
}

// Resolve implements ports.Resolver.
//
// For image sources, Resolve:
//  1. Validates the image reference is non-empty and syntactically plausible.
//  2. Normalises the reference (trims whitespace, rejects embedded whitespace).
//  3. Applies any configured mirror substitutions.
//  4. Warns if private registry credentials appear missing.
func (r *ImageResolver) Resolve(ctx context.Context, src domain.Source) (domain.Source, error) {
	if src.Identifier == "" {
		return src, domain.New(domain.ErrKindValidation, "image identifier must not be empty", nil)
	}

	r.bus.Publish(ctx, event.TopicResolveStarted, event.ScanProgressPayload{
		Stage:   "resolving",
		Message: fmt.Sprintf("validating image reference %q", src.Identifier),
	}, "")

	// Normalise the reference.
	ref, err := normaliseImageRef(src.Identifier)
	if err != nil {
		return src, domain.Newf(domain.ErrKindValidation, err, "invalid image reference %q", src.Identifier)
	}

	// Apply mirror substitution.
	ref = r.applyMirror(ref)

	// Warn on private registry without credentials.
	if isPrivateRegistry(ref) && src.Credentials == nil {
		r.logger.Warn("image appears to be in a private registry but no credentials provided",
			zap.String("image", ref),
		)
	}

	enriched := src
	enriched.Identifier = ref

	r.logger.Info("image reference resolved",
		zap.String("original", src.Identifier),
		zap.String("resolved", ref),
	)

	r.bus.PublishAsync(ctx, event.TopicResolveCompleted, event.ResolveCompletedPayload{
		ResolvedIdentity: ref,
	}, "")

	return enriched, nil
}

// ── helpers ──────────────────────────────────────────────────────────────────

// normaliseImageRef performs minimal normalisation on an image reference.
// It does not parse the full OCI reference grammar to avoid importing a heavy
// library; the final parsing is delegated to syft / stereoscope.
func normaliseImageRef(ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("empty reference")
	}
	// Reject references with embedded whitespace.
	if strings.ContainsAny(ref, " \t\n") {
		return "", fmt.Errorf("reference contains whitespace")
	}
	return ref, nil
}

// applyMirror replaces the registry host in ref with a configured mirror.
func (r *ImageResolver) applyMirror(ref string) string {
	for registry, mirror := range r.mirrorMap {
		if strings.HasPrefix(ref, registry+"/") {
			return mirror + "/" + strings.TrimPrefix(ref, registry+"/")
		}
	}
	return ref
}

// isPrivateRegistry heuristically decides whether ref points to a private
// registry.
//
// Logic:
//  1. Short refs (no "/") are Docker Hub short names — always public.
//  2. Refs beginning with a known public registry host are public.
//  3. Refs whose first path component looks like a hostname (contains "." or ":")
//     and is not in the known-public set are treated as private.
func isPrivateRegistry(ref string) bool {
	// Short ref like "ubuntu:22.04" or "nginx" — Docker Hub implicit, public.
	if !strings.Contains(ref, "/") {
		return false
	}

	publicPrefixes := []string{
		"docker.io/", "index.docker.io/",
		"ghcr.io/", "quay.io/", "gcr.io/",
		"registry.k8s.io/", "public.ecr.aws/",
	}
	for _, p := range publicPrefixes {
		if strings.HasPrefix(ref, p) {
			return false
		}
	}

	// If the first path component contains a "." or ":" it looks like a
	// hostname (e.g. "corp.registry.io/app", "localhost:5000/app").
	host := strings.SplitN(ref, "/", 2)[0]
	return strings.ContainsAny(host, ".:")
}

// Compile-time interface satisfaction check.
var _ ports.Resolver = (*ImageResolver)(nil)
