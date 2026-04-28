package overlay

import (
	"context"

	"github.com/bons/bons-ci/pkg/fshash/internal/core"
)

// Interpreter bridges raw access events (from fanotify) to high-level Mutation events
// aware of OverlayFS semantics.
type Interpreter struct {
	parser *Parser
	hooks  InterpreterHooks
}

// NewInterpreter creates a new overlay interpreter.
func NewInterpreter(opts ...Option) *Interpreter {
	cfg := defaultConfig()
	for _, opt := range opts {
		opt(&cfg)
	}

	return &Interpreter{
		parser: NewParser(opts...),
		hooks:  cfg.hooks,
	}
}

// Interpret processes an access event and yields one or more Mutations.
// It translates overlayfs metadata markers into logical actions.
func (i *Interpreter) Interpret(ctx context.Context, event core.AccessEvent) []Mutation {
	entry := i.parser.Parse(event.Path, event.LayerID)

	var mutations []Mutation

	switch entry.Kind {
	case EntryWhiteout:
		i.hooks.fireWhiteout(ctx, entry)
		// A whiteout marker means the target file is deleted
		mutations = append(mutations, Mutation{
			Kind:    MutationDeleted,
			Path:    entry.TargetPath,
			LayerID: entry.LayerID,
		})

	case EntryOpaque:
		i.hooks.fireOpaque(ctx, entry)
		// An opaque marker means the directory is opaque
		mutations = append(mutations, Mutation{
			Kind:    MutationOpaqued,
			Path:    entry.TargetPath, // TargetPath is the dir for an opaque marker
			LayerID: entry.LayerID,
		})

	case EntryRegular:
		// Not an overlay marker - it's a modified/accessed file
		mutations = append(mutations, Mutation{
			Kind:    MutationModified,
			Path:    entry.TargetPath,
			LayerID: entry.LayerID,
		})
	}

	for _, m := range mutations {
		i.hooks.fireMutation(ctx, m)
	}

	return mutations
}
