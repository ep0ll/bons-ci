package transform

import (
	"bytes"
	"context"
	"fmt"

	"github.com/bons/bons-ci/plugins/exporter/core"
)

// AttestationTransformerOptions controls filtering and validation behaviour.
type AttestationTransformerOptions struct {
	// AllowedKinds, when non-empty, retains only attestations whose Kind is
	// in the set. An empty set means "allow all".
	AllowedKinds map[core.AttestationKind]struct{}

	// DeniedPredicateTypes is a set of predicate type prefixes that will be
	// dropped (e.g. "https://slsa.dev/provenance/" from untrusted frontends).
	DeniedPredicateTypes []string

	// RequirePayload, when true, rejects attestations with empty payloads.
	RequirePayload bool

	// DeduplicateByPath removes duplicate attestation records with the same Path.
	DeduplicateByPath bool
}

// AttestationTransformerOption is a functional option for AttestationTransformer.
type AttestationTransformerOption func(*AttestationTransformerOptions)

// WithAllowedKinds restricts attestations to only the listed kinds.
func WithAllowedKinds(kinds ...core.AttestationKind) AttestationTransformerOption {
	return func(o *AttestationTransformerOptions) {
		o.AllowedKinds = make(map[core.AttestationKind]struct{}, len(kinds))
		for _, k := range kinds {
			o.AllowedKinds[k] = struct{}{}
		}
	}
}

// WithDeniedPredicateType adds a predicate-type prefix to the deny list.
func WithDeniedPredicateType(prefix string) AttestationTransformerOption {
	return func(o *AttestationTransformerOptions) {
		o.DeniedPredicateTypes = append(o.DeniedPredicateTypes, prefix)
	}
}

// WithRequirePayload enforces non-empty attestation payloads.
func WithRequirePayload(v bool) AttestationTransformerOption {
	return func(o *AttestationTransformerOptions) { o.RequirePayload = v }
}

// WithDeduplicateByPath enables path-based deduplication.
func WithDeduplicateByPath(v bool) AttestationTransformerOption {
	return func(o *AttestationTransformerOptions) { o.DeduplicateByPath = v }
}

// AttestationTransformer filters, validates, and deduplicates AttestationRecords
// in an Artifact. It is intentionally independent of any specific attestation
// format (in-toto, SLSA, SBOM) so that format-specific logic lives in
// dedicated downstream transformers.
type AttestationTransformer struct {
	BaseTransformer
	opts AttestationTransformerOptions
}

// NewAttestationTransformer creates an AttestationTransformer.
func NewAttestationTransformer(options ...AttestationTransformerOption) *AttestationTransformer {
	opts := AttestationTransformerOptions{}
	for _, o := range options {
		o(&opts)
	}
	return &AttestationTransformer{
		BaseTransformer: NewBase("attestation-filter", PriorityAttestation),
		opts:            opts,
	}
}

// Transform applies kind filtering, predicate-type denial, payload validation,
// and optional path-based deduplication to the artifact's AttestationRecords.
func (t *AttestationTransformer) Transform(_ context.Context, a *core.Artifact) (*core.Artifact, error) {
	if len(a.Attestations) == 0 {
		return a, nil
	}

	kept := make([]core.AttestationRecord, 0, len(a.Attestations))
	seen := make(map[string]struct{})

	for _, att := range a.Attestations {
		if err := t.validate(att); err != nil {
			return nil, err
		}
		if !t.allowKind(att.Kind) {
			continue
		}
		if t.denyPredicate(att.PredicateType) {
			continue
		}
		if t.opts.DeduplicateByPath {
			if _, dup := seen[att.Path]; dup {
				continue
			}
			seen[att.Path] = struct{}{}
		}
		kept = append(kept, att.Clone())
	}

	clone := a.Clone()
	clone.Attestations = kept
	return clone, nil
}

func (t *AttestationTransformer) validate(att core.AttestationRecord) error {
	if att.Path == "" && att.Kind != core.AttestationKindBundle {
		return fmt.Errorf("attestation with kind=%q has empty path", att.Kind)
	}
	if t.opts.RequirePayload && len(bytes.TrimSpace(att.Payload)) == 0 {
		return fmt.Errorf("attestation %q has empty payload", att.Path)
	}
	return nil
}

func (t *AttestationTransformer) allowKind(k core.AttestationKind) bool {
	if len(t.opts.AllowedKinds) == 0 {
		return true
	}
	_, ok := t.opts.AllowedKinds[k]
	return ok
}

func (t *AttestationTransformer) denyPredicate(pt string) bool {
	for _, prefix := range t.opts.DeniedPredicateTypes {
		if len(pt) >= len(prefix) && pt[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}
