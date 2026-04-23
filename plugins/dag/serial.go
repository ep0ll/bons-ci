package reactdag

import (
	"encoding/json"
	"fmt"
	"time"
)

// ---------------------------------------------------------------------------
// DAGSchema — JSON-serialisable representation of a DAG
// ---------------------------------------------------------------------------

// DAGSchema is the top-level JSON envelope for a serialised DAG.
type DAGSchema struct {
	Version  int             `json:"version"`
	SealedAt time.Time       `json:"sealed_at"`
	Vertices []VertexSchema  `json:"vertices"`
	Edges    []EdgeSchema    `json:"edges"`
	FileDeps []FileDepSchema `json:"file_deps,omitempty"`
}

// VertexSchema is the JSON representation of one vertex (without runtime state).
type VertexSchema struct {
	ID     string            `json:"id"`
	OpID   string            `json:"op_id"`
	Labels map[string]string `json:"labels,omitempty"`
}

// EdgeSchema is a directed edge from ParentID → ChildID.
type EdgeSchema struct {
	ParentID string `json:"parent_id"`
	ChildID  string `json:"child_id"`
}

// FileDepSchema records fine-grained file dependency declarations.
type FileDepSchema struct {
	ChildID  string   `json:"child_id"`
	ParentID string   `json:"parent_id"`
	Paths    []string `json:"paths"`
}

const currentSchemaVersion = 1

// ---------------------------------------------------------------------------
// MarshalDAG — DAG → JSON bytes
// ---------------------------------------------------------------------------

// MarshalDAG serialises the sealed DAG's structure to JSON.
// Runtime state (vertex execution state, output files, metrics) is not included.
// The returned JSON can later be used with UnmarshalDAGSchema to reconstruct
// the graph structure (but not the operations — callers must re-register those).
func MarshalDAG(d *DAG) ([]byte, error) {
	schema, err := DAGToSchema(d)
	if err != nil {
		return nil, err
	}
	return json.MarshalIndent(schema, "", "  ")
}

// DAGToSchema converts a DAG to its schema representation without marshalling.
func DAGToSchema(d *DAG) (*DAGSchema, error) {
	sorted, err := d.TopologicalSort()
	if err != nil {
		return nil, fmt.Errorf("marshal dag: %w", err)
	}

	schema := &DAGSchema{
		Version:  currentSchemaVersion,
		SealedAt: time.Now(),
	}

	for _, v := range sorted {
		schema.Vertices = append(schema.Vertices, VertexSchema{
			ID:     v.ID(),
			OpID:   v.OpID(),
			Labels: v.Labels(),
		})
		for _, child := range v.Children() {
			schema.Edges = append(schema.Edges, EdgeSchema{
				ParentID: v.ID(),
				ChildID:  child.ID(),
			})
		}
		for _, dep := range v.FileDependencies() {
			schema.FileDeps = append(schema.FileDeps, FileDepSchema{
				ChildID:  v.ID(),
				ParentID: dep.ParentID,
				Paths:    dep.Paths,
			})
		}
	}
	return schema, nil
}

// ---------------------------------------------------------------------------
// UnmarshalDAGSchema — JSON → DAGSchema (structural only)
// ---------------------------------------------------------------------------

// UnmarshalDAGSchema parses JSON into a DAGSchema.
// Use RebuildDAG to reconstruct a runnable DAG from the schema.
func UnmarshalDAGSchema(data []byte) (*DAGSchema, error) {
	var schema DAGSchema
	if err := json.Unmarshal(data, &schema); err != nil {
		return nil, fmt.Errorf("unmarshal dag schema: %w", err)
	}
	if schema.Version != currentSchemaVersion {
		return nil, fmt.Errorf("unmarshal dag: unsupported schema version %d (want %d)",
			schema.Version, currentSchemaVersion)
	}
	return &schema, nil
}

// ---------------------------------------------------------------------------
// RebuildDAG — reconstruct a DAG from a schema + operation registry
// ---------------------------------------------------------------------------

// OperationRegistry maps opID strings to Operation factories.
// Callers populate this map before calling RebuildDAG.
type OperationRegistry map[string]func() Operation

// RebuildDAG reconstructs a sealed DAG from a schema and an operation registry.
// For each vertex, it looks up the vertex's OpID in the registry and calls the
// factory to obtain an Operation. Vertices with unknown OpIDs cause an error
// unless a fallback "unknown" factory is registered under the key "*".
func RebuildDAG(schema *DAGSchema, registry OperationRegistry) (*DAG, error) {
	d := NewDAG()

	for _, vs := range schema.Vertices {
		op, err := lookupOp(vs.OpID, registry)
		if err != nil {
			return nil, fmt.Errorf("rebuild dag: vertex %q: %w", vs.ID, err)
		}
		v := NewVertex(vs.ID, op)
		for k, val := range vs.Labels {
			v.SetLabel(k, val)
		}
		if err := d.AddVertex(v); err != nil {
			return nil, fmt.Errorf("rebuild dag: add vertex %q: %w", vs.ID, err)
		}
	}

	for _, e := range schema.Edges {
		if err := d.LinkVertices(e.ParentID, e.ChildID); err != nil {
			return nil, fmt.Errorf("rebuild dag: link %q→%q: %w", e.ParentID, e.ChildID, err)
		}
	}

	for _, fd := range schema.FileDeps {
		if err := d.AddFileDependency(fd.ChildID, fd.ParentID, fd.Paths); err != nil {
			return nil, fmt.Errorf("rebuild dag: file dep %q→%q: %w", fd.ChildID, fd.ParentID, err)
		}
	}

	if err := d.Seal(); err != nil {
		return nil, fmt.Errorf("rebuild dag: seal: %w", err)
	}
	return d, nil
}

func lookupOp(opID string, registry OperationRegistry) (Operation, error) {
	if factory, ok := registry[opID]; ok {
		return factory(), nil
	}
	if factory, ok := registry["*"]; ok {
		return factory(), nil
	}
	return nil, fmt.Errorf("operation %q not found in registry (register a \"*\" fallback for unknown ops)", opID)
}

// ---------------------------------------------------------------------------
// StateSnapshot — serialise runtime vertex state for warm restarts
// ---------------------------------------------------------------------------

// VertexStateSnapshot records the runtime state of one vertex.
type VertexStateSnapshot struct {
	VertexID    string    `json:"vertex_id"`
	State       string    `json:"state"`
	CacheKey    string    `json:"cache_key,omitempty"`
	OutputFiles []FileRef `json:"output_files,omitempty"`
	ErrMsg      string    `json:"error,omitempty"`
}

// StateSnapshot holds the runtime state of every vertex in the DAG.
type StateSnapshot struct {
	CapturedAt time.Time             `json:"captured_at"`
	Vertices   []VertexStateSnapshot `json:"vertices"`
}

// CaptureStateSnapshot serialises the current runtime state of every vertex.
// This can be used to persist build state across process restarts, avoiding
// re-execution of already-completed vertices.
func CaptureStateSnapshot(d *DAG) *StateSnapshot {
	snap := &StateSnapshot{CapturedAt: time.Now()}
	for _, v := range d.All() {
		vs := VertexStateSnapshot{
			VertexID:    v.ID(),
			State:       v.State().String(),
			OutputFiles: v.OutputFiles(),
		}
		var key [32]byte
		k := v.CacheKey()
		key = k
		if key != ([32]byte{}) {
			vs.CacheKey = fmt.Sprintf("%x", key)
		}
		if err := v.Err(); err != nil {
			vs.ErrMsg = err.Error()
		}
		snap.Vertices = append(snap.Vertices, vs)
	}
	return snap
}

// MarshalStateSnapshot serialises the state snapshot to JSON.
func MarshalStateSnapshot(snap *StateSnapshot) ([]byte, error) {
	return json.MarshalIndent(snap, "", "  ")
}

// UnmarshalStateSnapshot parses a JSON state snapshot.
func UnmarshalStateSnapshot(data []byte) (*StateSnapshot, error) {
	var snap StateSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, fmt.Errorf("unmarshal state snapshot: %w", err)
	}
	return &snap, nil
}

// RestoreStateSnapshot applies a serialised state snapshot to the DAG,
// restoring vertex states and output files without re-executing operations.
// Vertices present in the snapshot but not in the DAG are silently skipped.
func RestoreStateSnapshot(d *DAG, snap *StateSnapshot) error {
	stateMap := map[string]State{
		"initial":    StateInitial,
		"fast_cache": StateFastCache,
		"slow_cache": StateSlowCache,
		"completed":  StateCompleted,
		"failed":     StateFailed,
	}

	for _, vs := range snap.Vertices {
		v, ok := d.Vertex(vs.VertexID)
		if !ok {
			continue
		}
		st, known := stateMap[vs.State]
		if !known || st == StateInitial {
			continue
		}
		// Restore output files before transitioning state.
		if len(vs.OutputFiles) > 0 {
			v.SetOutputFiles(vs.OutputFiles)
		}
		if vs.ErrMsg != "" {
			if err := v.SetFailed(fmt.Errorf("%s", vs.ErrMsg), "state restore"); err != nil {
				return fmt.Errorf("restore state: vertex %q: %w", vs.VertexID, err)
			}
			continue
		}
		if err := v.SetState(st, "state restore"); err != nil {
			return fmt.Errorf("restore state: vertex %q: %w", vs.VertexID, err)
		}
	}
	return nil
}
