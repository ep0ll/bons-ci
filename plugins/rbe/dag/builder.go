package dagstore

import (
	"fmt"
	"time"
)

// ——— VertexSpec ——————————————————————————————————————————————————————————————

// VertexSpec is the input to the builder when adding a vertex.
type VertexSpec struct {
	// OperationHash is the hash of this vertex's own operation/definition,
	// excluding inputs.  Required.
	OperationHash string

	// ID is the optional human-readable identifier for this vertex.
	// Must be unique within the store if set.
	ID string

	// Inputs lists parent vertex specs in declaration order.
	// The builder resolves hashes automatically from previously added vertices.
	Inputs []InputSpec

	// Labels are arbitrary key-value metadata.
	Labels map[string]string
}

// InputSpec describes one parent-vertex dependency as seen from the builder.
type InputSpec struct {
	// Vertex is the parent vertex spec as returned by Builder.AddVertex.
	// Either Vertex or VertexHash must be set.
	Vertex *BuiltVertex

	// VertexHash allows referencing an external vertex (not added to this
	// builder) by its pre-computed hash.
	VertexHash string

	// VertexID is the optional ID alias for the referenced vertex.
	VertexID string

	// Files are the specific files from the parent vertex consumed here.
	Files []FileRef
}

// BuiltVertex is the handle returned by Builder.AddVertex.  Pass it as
// InputSpec.Vertex to wire up edges.
type BuiltVertex struct {
	meta *VertexMeta
}

// Hash returns the content-addressed hash of the built vertex.
func (bv *BuiltVertex) Hash() string { return bv.meta.Hash }

// ID returns the optional human-readable ID of the built vertex.
func (bv *BuiltVertex) ID() string { return bv.meta.ID }

// Meta returns a shallow copy of the underlying VertexMeta.
func (bv *BuiltVertex) Meta() VertexMeta { return *bv.meta }

// ——— Builder ————————————————————————————————————————————————————————————————

// Builder constructs a DAG by accumulating vertex specs and computing all
// content hashes before the DAG is committed to a Store.
//
// Usage:
//
//	b := dagstore.NewBuilder("my-dag", hasher)
//	root  := b.MustAddVertex(dagstore.VertexSpec{OperationHash: "abc"})
//	child := b.MustAddVertex(dagstore.VertexSpec{
//	    OperationHash: "def",
//	    Inputs: []dagstore.InputSpec{{Vertex: root, Files: ...}},
//	})
//	dag, vertices, err := b.Build()
type Builder struct {
	dagID    string
	hasher   Hasher
	vertices []*BuiltVertex
	labels   map[string]string
	now      func() time.Time // injectable for tests
}

// NewBuilder creates a Builder for a DAG with the given ID.
func NewBuilder(dagID string, h Hasher) *Builder {
	return &Builder{
		dagID:  dagID,
		hasher: h,
		now:    time.Now,
	}
}

// WithLabels attaches labels to the DAG being built.
func (b *Builder) WithLabels(labels map[string]string) *Builder {
	b.labels = labels
	return b
}

// AddVertex computes the vertex hash from its spec, records it, and returns
// the handle.  It returns an error if hashing fails or the spec is invalid.
func (b *Builder) AddVertex(spec VertexSpec) (*BuiltVertex, error) {
	if spec.OperationHash == "" {
		return nil, &InvalidArgumentError{Field: "OperationHash", Reason: "must not be empty"}
	}

	inputHashes := make([]string, 0, len(spec.Inputs))
	builtInputs := make([]VertexInput, 0, len(spec.Inputs))

	for i, inp := range spec.Inputs {
		vh, vid, err := b.resolveInput(i, inp)
		if err != nil {
			return nil, err
		}
		inputHashes = append(inputHashes, vh)
		builtInputs = append(builtInputs, VertexInput{
			VertexHash: vh,
			VertexID:   vid,
			Files:      inp.Files,
		})
	}

	vertexHash, err := ComputeVertexHash(b.hasher, spec.OperationHash, inputHashes)
	if err != nil {
		return nil, fmt.Errorf("add vertex: %w", err)
	}

	now := b.now()
	meta := &VertexMeta{
		Hash:          vertexHash,
		ID:            spec.ID,
		DAGID:         b.dagID,
		OperationHash: spec.OperationHash,
		TreeHash:      vertexHash, // see types.go — TreeHash == Hash
		Inputs:        builtInputs,
		Labels:        spec.Labels,
		CreatedAt:     now,
		UpdatedAt:     now,
	}

	bv := &BuiltVertex{meta: meta}
	b.vertices = append(b.vertices, bv)
	return bv, nil
}

// MustAddVertex is like AddVertex but panics on error.  Suitable for tests and
// init-time construction where errors are programmer mistakes.
func (b *Builder) MustAddVertex(spec VertexSpec) *BuiltVertex {
	bv, err := b.AddVertex(spec)
	if err != nil {
		panic(fmt.Sprintf("dagstore builder: %v", err))
	}
	return bv
}

// Build finalises the DAG: computes its hash from the leaf vertices and returns
// the DAGMeta plus the full list of VertexMeta records ready to be stored.
//
// A leaf vertex is one that no other vertex in this builder depends on.
func (b *Builder) Build() (*DAGMeta, []*VertexMeta, error) {
	if len(b.vertices) == 0 {
		return nil, nil, &InvalidArgumentError{Reason: "dag has no vertices"}
	}

	// Identify roots (no inputs) and leaves (not referenced by any other vertex).
	childSet := make(map[string]struct{}, len(b.vertices))
	for _, bv := range b.vertices {
		for _, inp := range bv.meta.Inputs {
			childSet[inp.VertexHash] = struct{}{}
		}
	}

	var rootHashes, leafHashes []string
	metas := make([]*VertexMeta, 0, len(b.vertices))

	for _, bv := range b.vertices {
		metas = append(metas, bv.meta)
		if len(bv.meta.Inputs) == 0 {
			rootHashes = append(rootHashes, bv.meta.Hash)
		}
		if _, isParent := childSet[bv.meta.Hash]; !isParent {
			leafHashes = append(leafHashes, bv.meta.Hash)
		}
	}

	dagHash, err := ComputeDAGHash(b.hasher, leafHashes)
	if err != nil {
		return nil, nil, fmt.Errorf("build dag hash: %w", err)
	}

	now := b.now()
	dag := &DAGMeta{
		ID:          b.dagID,
		Hash:        dagHash,
		RootHashes:  rootHashes,
		LeafHashes:  leafHashes,
		VertexCount: len(metas),
		Labels:      b.labels,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	return dag, metas, nil
}

// ——— internal helpers ————————————————————————————————————————————————————————

func (b *Builder) resolveInput(idx int, inp InputSpec) (hash, id string, err error) {
	if inp.Vertex != nil {
		return inp.Vertex.meta.Hash, inp.Vertex.meta.ID, nil
	}
	if inp.VertexHash != "" {
		return inp.VertexHash, inp.VertexID, nil
	}
	return "", "", &InvalidArgumentError{
		Field:  fmt.Sprintf("Inputs[%d]", idx),
		Reason: "either Vertex or VertexHash must be set",
	}
}
