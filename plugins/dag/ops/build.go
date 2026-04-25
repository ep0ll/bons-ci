package ops

import (
	"context"
	"fmt"

	"github.com/bons/bons-ci/plugins/dag/vertex"
)

// BuildOp triggers a nested build inside the solver.
//
// In BuildKit's LLB model, a BuildOp allows one build definition to include
// another as a nested operation — the solver receives a sub-definition and
// builds it recursively. This is how Dockerfiles that invoke other Dockerfiles
// (e.g. via COPY --from) can be expressed as a single coherent DAG.
//
// The source ref must point to a filesystem layer that contains the LLB
// definition file (identified by DefinitionFile).
type BuildOp struct {
	id             string
	source         vertex.Ref
	definitionFile string
	constraints    Constraints
	inputs         []vertex.Vertex
}

var _ vertex.Vertex = (*BuildOp)(nil)
var _ vertex.Named = (*BuildOp)(nil)

// BuildInfo carries options for a BuildOp.
type BuildInfo struct {
	Constraints
	// DefinitionFile is the path within the source layer where the nested
	// LLB definition file lives. If empty, the solver uses its default convention.
	DefinitionFile string
}

// NewBuildOp constructs a BuildOp that executes a nested build.
//
// source is the vertex whose output contains the definition file.
// opts are functional options for configuring the build.
func NewBuildOp(source vertex.Ref, opts ...func(*BuildInfo)) *BuildOp {
	info := &BuildInfo{}
	for _, o := range opts {
		o(info)
	}

	b := &BuildOp{
		source:         source,
		definitionFile: info.DefinitionFile,
		constraints:    info.Constraints,
	}

	if !source.IsZero() {
		b.inputs = []vertex.Vertex{source.Vertex}
	}

	b.id = b.computeID()
	return b
}

func (b *BuildOp) computeID() string {
	srcID := ""
	if !b.source.IsZero() {
		srcID = fmt.Sprintf("%s:%d", b.source.Vertex.ID(), b.source.Index)
	}
	return idOf(struct {
		Kind           string    `json:"kind"`
		SourceID       string    `json:"source_id,omitempty"`
		DefinitionFile string    `json:"definition_file,omitempty"`
		Platform       *Platform `json:"platform,omitempty"`
	}{
		Kind:           string(vertex.KindBuild),
		SourceID:       srcID,
		DefinitionFile: b.definitionFile,
		Platform:       b.constraints.Platform,
	})
}

func (b *BuildOp) ID() string               { return b.id }
func (b *BuildOp) Kind() vertex.Kind        { return vertex.KindBuild }
func (b *BuildOp) Inputs() []vertex.Vertex  { return b.inputs }
func (b *BuildOp) Source() vertex.Ref       { return b.source }
func (b *BuildOp) DefinitionFile() string   { return b.definitionFile }
func (b *BuildOp) Constraints() Constraints { return b.constraints }

func (b *BuildOp) Name() string {
	if b.definitionFile != "" {
		return "build:" + b.definitionFile
	}
	return "build"
}

func (b *BuildOp) Validate(_ context.Context) error {
	if b.source.IsZero() {
		return fmt.Errorf("build: source ref must not be scratch — a definition file is required")
	}
	return nil
}

// Ref returns a reference to the output of this nested build.
func (b *BuildOp) Ref() vertex.Ref { return vertex.Ref{Vertex: b, Index: 0} }

// WithDefinitionFile sets the path to the LLB definition file within the source.
func WithDefinitionFile(path string) func(*BuildInfo) {
	return func(b *BuildInfo) { b.DefinitionFile = path }
}

// WithBuildConstraints sets the build constraints for the BuildOp.
func WithBuildConstraints(c Constraints) func(*BuildInfo) {
	return func(b *BuildInfo) { b.Constraints = c }
}
