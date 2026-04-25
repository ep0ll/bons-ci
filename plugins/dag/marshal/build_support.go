package marshal

import (
	"fmt"

	"github.com/bons/bons-ci/plugins/dag/ops"
	"github.com/bons/bons-ci/plugins/dag/vertex"
)

// BuildPayload is the wire form of a BuildOp.
type BuildPayload struct {
	// SourceRef points to the op whose output contains the definition file.
	SourceRef *InputRef `json:"source,omitempty"`
	// DefinitionFile is the path within the source layer to the LLB definition.
	DefinitionFile string `json:"definition_file,omitempty"`
	// Platform is the build target platform.
	Platform *ops.Platform `json:"platform,omitempty"`
}

func init() {
	// Register the BuildPayload field on OpPayload by extending the serializer.
	// This is done via an init() side-effect so that adding BuildOp support
	// does not require modifying marshal.go directly (open/closed principle).
	//
	// In practice the marshal.go serializeVertex switch already has a default
	// case that produces an empty payload for unknown kinds; extending it here
	// via a registered handler is the clean way to add new op kinds without
	// modifying the core serializer file.
	registerKindSerializer(vertex.KindBuild, serializeBuild)
}

// kindSerializers maps vertex.Kind to a serialization function.
// Populated by init() functions in extension files.
var kindSerializers = map[vertex.Kind]func(vertex.Vertex, map[string]int) (OpPayload, error){}

func registerKindSerializer(kind vertex.Kind, fn func(vertex.Vertex, map[string]int) (OpPayload, error)) {
	kindSerializers[kind] = fn
}

func serializeBuild(v vertex.Vertex, posMap map[string]int) (OpPayload, error) {
	b, ok := v.(*ops.BuildOp)
	if !ok {
		return OpPayload{}, fmt.Errorf("expected *ops.BuildOp, got %T", v)
	}

	payload := &BuildPayload{
		DefinitionFile: b.DefinitionFile(),
		Platform:       b.Constraints().Platform,
	}

	if !b.Source().IsZero() {
		pos, ok := posMap[b.Source().Vertex.ID()]
		if !ok {
			return OpPayload{}, fmt.Errorf("build source %q not in position map", b.Source().Vertex.ID())
		}
		payload.SourceRef = &InputRef{OpIndex: pos, OutputIndex: b.Source().Index}
	}

	// We embed the payload as JSON in the generic Op field.
	// Since OpPayload uses omitempty on all fields, an unknown kind just
	// carries a nil payload. We store it in a custom extension point.
	return OpPayload{Build: payload}, nil
}
