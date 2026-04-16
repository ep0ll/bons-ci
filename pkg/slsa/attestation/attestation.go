// Package attestation provides in-toto statement construction, filtering, and
// metadata helpers. Zero external dependencies.
package attestation

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/bons/bons-ci/pkg/slsa/types"
)

// ─────────────────────────────────────────────────────────────────────────────
// Statement
// ─────────────────────────────────────────────────────────────────────────────

// Statement is an in-toto v0.1 Statement.
type Statement struct {
	Type          string          `json:"_type"`
	PredicateType string          `json:"predicateType"`
	Subject       []types.Subject `json:"subject"`
	Predicate     json.RawMessage `json:"predicate"`
}

// NewStatement creates a Statement wrapping the given predicate.
// At least one valid subject is required.
func NewStatement(predicateType string, subjects []types.Subject, predicate any) (*Statement, error) {
	if predicateType == "" {
		return nil, errors.New("statement: predicateType must not be empty")
	}
	if len(subjects) == 0 {
		return nil, errors.New("statement: at least one subject is required")
	}
	for i, s := range subjects {
		if s.Name == "" {
			return nil, fmt.Errorf("statement: subject[%d].Name is empty", i)
		}
		if len(s.Digest) == 0 {
			return nil, fmt.Errorf("statement: subject[%d].Digest is empty", i)
		}
	}
	raw, err := json.Marshal(predicate)
	if err != nil {
		return nil, fmt.Errorf("statement: marshal predicate: %w", err)
	}
	return &Statement{
		Type:          types.InTotoStatementTypeV01,
		PredicateType: predicateType,
		Subject:       subjects,
		Predicate:     raw,
	}, nil
}

// Marshal serialises the statement to indented JSON.
func (s *Statement) Marshal() ([]byte, error) {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("statement: marshal: %w", err)
	}
	return b, nil
}

// UnmarshalStatement parses a JSON in-toto statement and validates _type.
func UnmarshalStatement(data []byte) (*Statement, error) {
	var s Statement
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("statement: unmarshal: %w", err)
	}
	if s.Type != types.InTotoStatementTypeV01 {
		return nil, fmt.Errorf("statement: unexpected _type %q", s.Type)
	}
	return &s, nil
}

// DecodePredicate unmarshals the predicate JSON into v.
func (s *Statement) DecodePredicate(v any) error {
	return json.Unmarshal(s.Predicate, v)
}

// ─────────────────────────────────────────────────────────────────────────────
// Attestation[T]
// ─────────────────────────────────────────────────────────────────────────────

// Kind classifies the source format of an attestation.
type Kind int

const (
	// KindInToto is a standalone in-toto statement.
	KindInToto Kind = iota
	// KindBundle is a directory of in-toto statements (must be unbundled before use).
	KindBundle
)

// Attestation is a generic container for attestation content. The type
// parameter T is the reference type used to lazily load content.
type Attestation[T any] struct {
	Kind          Kind
	Metadata      map[string][]byte
	Ref           T
	Path          string
	ContentFunc   func() ([]byte, error)
	PredicateType string
	Subjects      []types.Subject
}

// HasContent reports whether content can be produced by this attestation.
func (a *Attestation[T]) HasContent() bool {
	if a.ContentFunc != nil {
		return true
	}
	var zero T
	return fmt.Sprint(a.Ref) != fmt.Sprint(zero)
}

// ReadContent calls ContentFunc to get the raw predicate bytes.
func (a *Attestation[T]) ReadContent() ([]byte, error) {
	if a.ContentFunc == nil {
		return nil, errors.New("attestation: no ContentFunc set")
	}
	return a.ContentFunc()
}

// Clone returns a shallow copy with an independent Metadata map.
func (a *Attestation[T]) Clone() *Attestation[T] {
	cp := *a
	if a.Metadata != nil {
		cp.Metadata = make(map[string][]byte, len(a.Metadata))
		for k, v := range a.Metadata {
			cp.Metadata[k] = v
		}
	}
	return &cp
}

// ─────────────────────────────────────────────────────────────────────────────
// Metadata keys & values
// ─────────────────────────────────────────────────────────────────────────────

const (
	MetaKeyReason     = "reason"
	MetaKeyInlineOnly = "inline-only"
	MetaKeySBOMCore   = "sbom-core"
	MetaKeySignedAt   = "signed-at"

	ReasonSBOM       = "sbom"
	ReasonProvenance = "provenance"
)

// TimestampMetadata returns a Metadata map with MetaKeySignedAt set.
func TimestampMetadata(t time.Time) map[string][]byte {
	return map[string][]byte{
		MetaKeySignedAt: []byte(t.UTC().Format(time.RFC3339)),
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Filter
// ─────────────────────────────────────────────────────────────────────────────

// Filter returns the subset of attestations satisfying the include/exclude criteria.
//
//   - include: every key→value pair must be present and equal.
//   - exclude: no key→value pair may be present and equal.
func Filter[T any](attestations []Attestation[T], include, exclude map[string][]byte) []Attestation[T] {
	if len(include) == 0 && len(exclude) == 0 {
		return attestations
	}
	out := make([]Attestation[T], 0, len(attestations))
	for _, att := range attestations {
		meta := att.Metadata
		if meta == nil {
			meta = map[string][]byte{}
		}
		if !matchAll(meta, include) || matchAny(meta, exclude) {
			continue
		}
		out = append(out, att)
	}
	return out
}

// FilterByReason returns attestations whose "reason" metadata equals reason.
func FilterByReason[T any](attestations []Attestation[T], reason string) []Attestation[T] {
	return Filter(attestations, map[string][]byte{MetaKeyReason: []byte(reason)}, nil)
}

func matchAll(meta map[string][]byte, criteria map[string][]byte) bool {
	for k, v := range criteria {
		if !bytes.Equal(meta[k], v) {
			return false
		}
	}
	return true
}

func matchAny(meta map[string][]byte, criteria map[string][]byte) bool {
	for k, v := range criteria {
		if bytes.Equal(meta[k], v) {
			return true
		}
	}
	return false
}
