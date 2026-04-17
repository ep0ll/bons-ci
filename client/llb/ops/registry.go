package ops

import (
	"fmt"
	"sync"

	"github.com/bons/bons-ci/client/llb/core"
)

// Factory is a plugin that can handle a particular vertex type.
type Factory interface {
	CanHandle(vt core.VertexType) bool
}

// Registry is a thread-safe map from VertexType to registered factories.
type Registry struct {
	mu        sync.RWMutex
	factories map[core.VertexType][]Factory
}

func NewRegistry() *Registry {
	return &Registry{factories: make(map[core.VertexType][]Factory)}
}

func (r *Registry) Register(vt core.VertexType, f Factory) {
	if f == nil {
		panic("ops.Registry.Register: factory must not be nil")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.factories[vt] = append(r.factories[vt], f)
}

func (r *Registry) Factories(vt core.VertexType) ([]Factory, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	fs, ok := r.factories[vt]
	if !ok {
		return nil, fmt.Errorf("no factory registered for vertex type %q", vt)
	}
	return fs, nil
}

func (r *Registry) RegisteredTypes() []core.VertexType {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]core.VertexType, 0, len(r.factories))
	for vt := range r.factories {
		out = append(out, vt)
	}
	return out
}

// DefaultRegistry is the global registry pre-populated with all built-in ops.
var DefaultRegistry = NewRegistry()
