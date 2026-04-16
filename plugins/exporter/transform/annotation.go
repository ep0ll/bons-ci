package transform

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bons/bons-ci/plugins/exporter/core"
)

// Standard OCI annotation keys (https://github.com/opencontainers/image-spec/blob/main/annotations.md).
const (
	AnnotationCreated       = "org.opencontainers.image.created"
	AnnotationAuthors       = "org.opencontainers.image.authors"
	AnnotationURL           = "org.opencontainers.image.url"
	AnnotationDocumentation = "org.opencontainers.image.documentation"
	AnnotationSource        = "org.opencontainers.image.source"
	AnnotationVersion       = "org.opencontainers.image.version"
	AnnotationRevision      = "org.opencontainers.image.revision"
	AnnotationVendor        = "org.opencontainers.image.vendor"
	AnnotationLicenses      = "org.opencontainers.image.licenses"
	AnnotationTitle         = "org.opencontainers.image.title"
	AnnotationDescription   = "org.opencontainers.image.description"
)

// MergeStrategy controls how incoming annotations interact with existing ones.
type MergeStrategy int

const (
	// MergeStrategyOverwrite replaces existing keys with incoming values.
	MergeStrategyOverwrite MergeStrategy = iota

	// MergeStrategyPreserve keeps existing keys; only fills absent ones.
	MergeStrategyPreserve

	// MergeStrategyError returns an error on key collision.
	MergeStrategyError
)

// AnnotationTransformerOptions configures AnnotationTransformer.
type AnnotationTransformerOptions struct {
	// StaticAnnotations are merged verbatim into every artifact.
	StaticAnnotations map[string]string

	// InjectCreatedAt, when true, sets the AnnotationCreated key to
	// the artifact's epoch (or time.Now() if no epoch is present).
	InjectCreatedAt bool

	// Strategy governs collision resolution when merging.
	Strategy MergeStrategy

	// AllowedPrefixes, when non-empty, silently drops any annotation whose
	// key does not start with one of the listed prefixes. Useful for
	// enforcing annotation namespace policies.
	AllowedPrefixes []string
}

// AnnotationTransformerOption is a functional option for AnnotationTransformer.
type AnnotationTransformerOption func(*AnnotationTransformerOptions)

// WithStaticAnnotation adds a single static annotation.
func WithStaticAnnotation(key, value string) AnnotationTransformerOption {
	return func(o *AnnotationTransformerOptions) {
		if o.StaticAnnotations == nil {
			o.StaticAnnotations = make(map[string]string)
		}
		o.StaticAnnotations[key] = value
	}
}

// WithInjectCreatedAt enables automatic creation-time annotation injection.
func WithInjectCreatedAt(v bool) AnnotationTransformerOption {
	return func(o *AnnotationTransformerOptions) { o.InjectCreatedAt = v }
}

// WithMergeStrategy sets the collision resolution strategy.
func WithMergeStrategy(s MergeStrategy) AnnotationTransformerOption {
	return func(o *AnnotationTransformerOptions) { o.Strategy = s }
}

// WithAllowedAnnotationPrefix restricts which annotation key prefixes survive.
func WithAllowedAnnotationPrefix(prefix string) AnnotationTransformerOption {
	return func(o *AnnotationTransformerOptions) {
		o.AllowedPrefixes = append(o.AllowedPrefixes, prefix)
	}
}

// AnnotationTransformer merges static and dynamic OCI annotations into the
// Artifact. It is deliberately decoupled from manifest construction so that
// the same transformer can be reused by multiple exporter backends.
type AnnotationTransformer struct {
	BaseTransformer
	opts AnnotationTransformerOptions
}

// NewAnnotationTransformer creates an AnnotationTransformer.
func NewAnnotationTransformer(options ...AnnotationTransformerOption) *AnnotationTransformer {
	opts := AnnotationTransformerOptions{
		Strategy:          MergeStrategyOverwrite,
		StaticAnnotations: make(map[string]string),
	}
	for _, o := range options {
		o(&opts)
	}
	return &AnnotationTransformer{
		BaseTransformer: NewBase("annotation-injector", PriorityAnnotation),
		opts:            opts,
	}
}

// Transform merges configured annotations into a clone of the artifact.
func (t *AnnotationTransformer) Transform(ctx context.Context, a *core.Artifact) (*core.Artifact, error) {
	incoming := t.buildIncoming(a)
	if len(incoming) == 0 {
		return a, nil
	}

	// Filter by allowed prefixes.
	if len(t.opts.AllowedPrefixes) > 0 {
		filtered := make(map[string]string, len(incoming))
		for k, v := range incoming {
			if t.allowed(k) {
				filtered[k] = v
			}
		}
		incoming = filtered
	}
	if len(incoming) == 0 {
		return a, nil
	}

	clone := a.Clone()
	if clone.Annotations == nil {
		clone.Annotations = make(map[string]string)
	}

	for k, v := range incoming {
		existing, exists := clone.Annotations[k]
		switch {
		case !exists:
			clone.Annotations[k] = v
		case t.opts.Strategy == MergeStrategyPreserve:
			// keep existing; discard incoming
		case t.opts.Strategy == MergeStrategyError:
			return nil, fmt.Errorf(
				"annotation-injector: collision on key %q (existing=%q, incoming=%q)",
				k, existing, v)
		default: // MergeStrategyOverwrite
			clone.Annotations[k] = v
		}
	}

	return clone, nil
}

func (t *AnnotationTransformer) buildIncoming(a *core.Artifact) map[string]string {
	result := make(map[string]string, len(t.opts.StaticAnnotations))
	for k, v := range t.opts.StaticAnnotations {
		result[k] = v
	}
	if t.opts.InjectCreatedAt {
		if _, already := result[AnnotationCreated]; !already {
			ts := t.resolveCreatedAt(a)
			result[AnnotationCreated] = ts.UTC().Format(time.RFC3339)
		}
	}
	return result
}

func (t *AnnotationTransformer) resolveCreatedAt(a *core.Artifact) time.Time {
	if raw, ok := a.Metadata[MetaKeySourceDateEpoch]; ok && len(raw) > 0 {
		// reuse epoch transformer's written key
		if secs := parseUnixSecs(string(raw)); secs > 0 {
			return time.Unix(secs, 0).UTC()
		}
	}
	// fall back to most recent layer history
	for i := len(a.Layers) - 1; i >= 0; i-- {
		if h := a.Layers[i].History; h != nil && h.CreatedAt != nil {
			return *h.CreatedAt
		}
	}
	return time.Now().UTC()
}

func (t *AnnotationTransformer) allowed(key string) bool {
	for _, prefix := range t.opts.AllowedPrefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

func parseUnixSecs(s string) int64 {
	var n int64
	for _, c := range s {
		if c < '0' || c > '9' {
			return -1
		}
		n = n*10 + int64(c-'0')
	}
	return n
}
