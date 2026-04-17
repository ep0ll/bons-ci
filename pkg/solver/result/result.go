// Package result provides a generic, multi-platform result container
// matching BuildKit's result.Result[T]. It supports single refs, named
// multi-platform refs, metadata, and attestations.
package result

import (
	"maps"
	"sync"

	"github.com/pkg/errors"
)

// Result is a generic container for solve outputs. It supports:
//   - Single reference (Ref) for simple single-platform builds
//   - Named references (Refs) for multi-platform builds
//   - Arbitrary metadata (key → bytes)
//   - Per-platform attestations
type Result[T comparable] struct {
	mu           sync.Mutex
	Ref          T
	Refs         map[string]T
	Metadata     map[string][]byte
	Attestations map[string][]Attestation[T]
}

// Clone creates a shallow copy of the result.
func (r *Result[T]) Clone() *Result[T] {
	r.mu.Lock()
	defer r.mu.Unlock()
	return &Result[T]{
		Ref:          r.Ref,
		Refs:         maps.Clone(r.Refs),
		Metadata:     maps.Clone(r.Metadata),
		Attestations: maps.Clone(r.Attestations),
	}
}

// AddMeta adds a metadata key-value pair.
func (r *Result[T]) AddMeta(k string, v []byte) {
	r.mu.Lock()
	if r.Metadata == nil {
		r.Metadata = map[string][]byte{}
	}
	r.Metadata[k] = v
	r.mu.Unlock()
}

// AddRef adds a named reference (e.g., for a specific platform).
func (r *Result[T]) AddRef(k string, ref T) {
	r.mu.Lock()
	if r.Refs == nil {
		r.Refs = map[string]T{}
	}
	r.Refs[k] = ref
	r.mu.Unlock()
}

// AddAttestation adds an attestation for a named platform.
func (r *Result[T]) AddAttestation(k string, v Attestation[T]) {
	r.mu.Lock()
	if r.Attestations == nil {
		r.Attestations = map[string][]Attestation[T]{}
	}
	r.Attestations[k] = append(r.Attestations[k], v)
	r.mu.Unlock()
}

// SetRef sets the single reference.
func (r *Result[T]) SetRef(ref T) {
	r.Ref = ref
}

// SingleRef returns the single reference. Returns an error if this is
// a multi-platform result (Refs is set but Ref is zero).
func (r *Result[T]) SingleRef() (T, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var zero T
	if r.Refs != nil && r.Ref == zero {
		return zero, errors.Errorf("invalid map result")
	}
	return r.Ref, nil
}

// FindRef looks up a named reference. If a single-element Refs map exists
// and the key doesn't match, it returns the sole element.
func (r *Result[T]) FindRef(key string) (T, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.Refs != nil {
		if ref, ok := r.Refs[key]; ok {
			return ref, true
		}
		if len(r.Refs) == 1 {
			for _, ref := range r.Refs {
				return ref, true
			}
		}
		var zero T
		return zero, false
	}
	return r.Ref, true
}

// EachRef calls fn for every non-zero reference (Ref, Refs, and attestation refs).
func (r *Result[T]) EachRef(fn func(T) error) (err error) {
	var zero T
	if r.Ref != zero {
		err = fn(r.Ref)
	}
	for _, ref := range r.Refs {
		if ref != zero {
			if err1 := fn(ref); err1 != nil && err == nil {
				err = err1
			}
		}
	}
	for _, as := range r.Attestations {
		for _, a := range as {
			if a.Ref != zero {
				if err1 := fn(a.Ref); err1 != nil && err == nil {
					err = err1
				}
			}
		}
	}
	return err
}

// IsEmpty returns true if this result has no references.
func (r *Result[T]) IsEmpty() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.Refs) > 0 {
		return false
	}
	var zero T
	return r.Ref == zero
}

// EachRef iterates over references in both a and b in parallel.
// Both results must map to the same set of keys.
func EachRef[U comparable, V comparable](a *Result[U], b *Result[V], fn func(U, V) error) (err error) {
	var (
		zeroU U
		zeroV V
	)
	if a.Ref != zeroU && b.Ref != zeroV {
		err = fn(a.Ref, b.Ref)
	}
	for k, r := range a.Refs {
		r2, ok := b.Refs[k]
		if !ok {
			continue
		}
		if r != zeroU && r2 != zeroV {
			if err1 := fn(r, r2); err1 != nil && err == nil {
				err = err1
			}
		}
	}
	for k, atts := range a.Attestations {
		atts2, ok := b.Attestations[k]
		if !ok {
			continue
		}
		for i, att := range atts {
			if i >= len(atts2) {
				break
			}
			att2 := atts2[i]
			if att.Ref != zeroU && att2.Ref != zeroV {
				if err1 := fn(att.Ref, att2.Ref); err1 != nil && err == nil {
					err = err1
				}
			}
		}
	}
	return err
}

// ConvertResult transforms a Result[U] into a Result[V] using a converter.
// Zero values are mapped to zero values without calling fn.
func ConvertResult[U comparable, V comparable](r *Result[U], fn func(U) (V, error)) (*Result[V], error) {
	var zero U
	r2 := &Result[V]{}
	var err error

	if r.Ref != zero {
		r2.Ref, err = fn(r.Ref)
		if err != nil {
			return nil, err
		}
	}

	if r.Refs != nil {
		r2.Refs = map[string]V{}
	}
	for k, ref := range r.Refs {
		if ref == zero {
			var zeroV V
			r2.Refs[k] = zeroV
			continue
		}
		r2.Refs[k], err = fn(ref)
		if err != nil {
			return nil, err
		}
	}

	if r.Attestations != nil {
		r2.Attestations = map[string][]Attestation[V]{}
	}
	for k, as := range r.Attestations {
		for _, a := range as {
			a2, err := ConvertAttestation(&a, fn)
			if err != nil {
				return nil, err
			}
			r2.Attestations[k] = append(r2.Attestations[k], *a2)
		}
	}

	r2.Metadata = r.Metadata
	return r2, nil
}
