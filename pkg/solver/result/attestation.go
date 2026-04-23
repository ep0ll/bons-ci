package result

// Attestation associates a reference with attestation metadata.
type Attestation[T comparable] struct {
	Ref    T
	Kind   AttestationKind
	Path   string
	InToto InTotoAttestation
}

// AttestationKind distinguishes different types of attestations.
type AttestationKind int

const (
	// AttestationKindInToto is an in-toto attestation.
	AttestationKindInToto AttestationKind = iota
	// AttestationKindBundle is a signed attestation bundle.
	AttestationKindBundle
)

// InTotoAttestation holds in-toto specific attestation fields.
type InTotoAttestation struct {
	PredicateType string
	Subjects      []InTotoSubject
}

// InTotoSubject is a subject within an in-toto attestation.
type InTotoSubject struct {
	Kind string
	Name string
}

// ConvertAttestation transforms an Attestation[U] to Attestation[V].
func ConvertAttestation[U comparable, V comparable](a *Attestation[U], fn func(U) (V, error)) (*Attestation[V], error) {
	var zero U
	a2 := &Attestation[V]{
		Kind:   a.Kind,
		Path:   a.Path,
		InToto: a.InToto,
	}
	if a.Ref != zero {
		var err error
		a2.Ref, err = fn(a.Ref)
		if err != nil {
			return nil, err
		}
	}
	return a2, nil
}
