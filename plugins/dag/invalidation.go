package reactdag

import (
	"context"
	"fmt"
)

// ---------------------------------------------------------------------------
// InvalidationEngine
// ---------------------------------------------------------------------------

// InvalidationEngine computes which vertices must be re-evaluated given a set
// of changed files. It respects fine-grained FileDependency declarations so
// that a vertex is only invalidated when a file it actually reads has changed.
//
// Key optimization: if vertex A depends only on file1 of vertex B, and only
// file2 of B changed, A does not need re-evaluation even though B's output set
// changed.
type InvalidationEngine struct {
	dag      *DAG
	eventBus *EventBus
}

// NewInvalidationEngine constructs an engine bound to the given DAG.
func NewInvalidationEngine(dag *DAG, bus *EventBus) *InvalidationEngine {
	return &InvalidationEngine{dag: dag, eventBus: bus}
}

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

// Invalidate determines which vertices are affected by changedFiles, resets
// them to StateInitial, and emits EventInvalidated for each.
//
// It also updates the hash of each changed file in whichever parent vertex
// currently holds that path in its OutputFiles. This ensures that downstream
// cache-key computation reflects the new content, not the stale hash.
func (e *InvalidationEngine) Invalidate(ctx context.Context, changedFiles []FileRef) ([]*Vertex, error) {
	sorted, err := e.dag.TopologicalSort()
	if err != nil {
		return nil, fmt.Errorf("invalidation: topological sort: %w", err)
	}

	// Build a path→FileRef map of the incoming changes.
	changedMap := make(map[string]FileRef, len(changedFiles))
	for _, f := range changedFiles {
		changedMap[f.Path] = f
	}

	// Propagate new hashes into parent output files so downstream cache keys
	// are computed with the updated content hashes.
	e.propagateChangedHashes(sorted, changedMap)

	changedPaths := make(map[string]bool, len(changedFiles))
	for p := range changedMap {
		changedPaths[p] = true
	}

	var invalidated []*Vertex
	for _, v := range sorted {
		if e.isAffected(v, changedPaths) {
			invalidated = append(invalidated, v)
			e.addOutputPathsToChanged(v, changedPaths)
			e.resetAndNotify(ctx, v)
		}
	}
	return invalidated, nil
}

// propagateChangedHashes updates the OutputFiles of any vertex that owns a
// changed path, replacing the old FileRef with the new one (which carries
// the updated hash). This ensures downstream cache-key computation uses
// fresh content hashes.
func (e *InvalidationEngine) propagateChangedHashes(sorted []*Vertex, changedMap map[string]FileRef) {
	for _, v := range sorted {
		outs := v.OutputFiles()
		if len(outs) == 0 {
			continue
		}
		updated := false
		for i, f := range outs {
			if newRef, changed := changedMap[f.Path]; changed {
				outs[i] = newRef
				updated = true
			}
		}
		if updated {
			v.SetOutputFiles(outs)
		}
	}
}

// IsAffectedByFiles reports whether v consumes any of the listed file paths
// from its parents, without mutating any state. Useful for dry-run analysis.
func (e *InvalidationEngine) IsAffectedByFiles(v *Vertex, changedPaths map[string]bool) bool {
	return e.isAffected(v, changedPaths)
}

// ---------------------------------------------------------------------------
// Internal logic
// ---------------------------------------------------------------------------

// isAffected checks whether vertex v would be affected by the current
// changedPaths set, respecting fine-grained file dependency declarations.
func (e *InvalidationEngine) isAffected(v *Vertex, changedPaths map[string]bool) bool {
	for _, parent := range v.Parents() {
		declaredPaths, hasDep := v.FileDependencyForParent(parent.ID())

		if !hasDep {
			// No fine-grained declaration → assume all parent outputs are consumed.
			if e.anyParentOutputChanged(parent, changedPaths) {
				return true
			}
			continue
		}

		// Fine-grained: check only the declared subset.
		if anyPathInSet(declaredPaths, changedPaths) {
			return true
		}
	}

	// Also check top-level input files (e.g., source files not from parents).
	for _, f := range v.InputFiles() {
		if changedPaths[f.Path] {
			return true
		}
	}
	return false
}

// anyParentOutputChanged reports whether any of parent's produced output files
// appear in changedPaths.
func (e *InvalidationEngine) anyParentOutputChanged(parent *Vertex, changedPaths map[string]bool) bool {
	for _, f := range parent.OutputFiles() {
		if changedPaths[f.Path] {
			return true
		}
	}
	return false
}

// addOutputPathsToChanged inserts all of v's output file paths into the set,
// so that its children will be evaluated in the same pass.
func (e *InvalidationEngine) addOutputPathsToChanged(v *Vertex, changedPaths map[string]bool) {
	for _, f := range v.OutputFiles() {
		changedPaths[f.Path] = true
	}
}

// resetAndNotify resets a vertex and emits EventInvalidated.
func (e *InvalidationEngine) resetAndNotify(ctx context.Context, v *Vertex) {
	v.Reset()
	if e.eventBus != nil {
		e.eventBus.PublishInvalidated(ctx, v, "file change")
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func fileRefsToPathSet(refs []FileRef) map[string]bool {
	m := make(map[string]bool, len(refs))
	for _, f := range refs {
		m[f.Path] = true
	}
	return m
}

func anyPathInSet(paths []string, set map[string]bool) bool {
	for _, p := range paths {
		if set[p] {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Dependency-level analysis (higher-level insights)
// ---------------------------------------------------------------------------

// EffectiveAncestorLevel returns the "level" at which the given vertex actually
// depends on ancestorID based on real file consumption, not just graph topology.
//
// Returns:
//   - 0  → direct dependency (parent)
//   - N  → N hops through intermediate vertices that consume from each other
//   - -1 → not an effective dependency at all
//
// This is used by the scheduler to identify vertices that can be "skipped"
// when an ancestor changes files that they do not transitively consume.
func (e *InvalidationEngine) EffectiveAncestorLevel(v *Vertex, ancestorID string) int {
	return e.dag.AncestorDepth(ancestorID, v.ID())
}

// ComputeChangedOutputs derives which of a vertex's output files would be
// considered changed if the listed input paths changed. This is used for
// forward propagation analysis without a full build.
func ComputeChangedOutputs(v *Vertex, changedInputPaths map[string]bool) map[string]bool {
	affected := false
	for _, f := range v.InputFiles() {
		if changedInputPaths[f.Path] {
			affected = true
			break
		}
	}
	if !affected {
		return nil
	}
	out := make(map[string]bool, len(v.OutputFiles()))
	for _, f := range v.OutputFiles() {
		out[f.Path] = true
	}
	return out
}
