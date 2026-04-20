package reactdag

import (
	"fmt"
	"strings"
)

// ---------------------------------------------------------------------------
// DAGDiff — compare two DAG state snapshots
// ---------------------------------------------------------------------------

// VertexDiff describes what changed for a single vertex between two snapshots.
type VertexDiff struct {
	VertexID  string
	ChangeType DiffChangeType

	// State fields (populated for StateChanged).
	PrevState State
	NextState State

	// Output file changes (populated for OutputsChanged).
	AddedFiles   []string
	RemovedFiles []string
	ModifiedFiles []string
}

// DiffChangeType classifies the nature of a vertex change.
type DiffChangeType string

const (
	DiffAdded         DiffChangeType = "added"         // vertex only in next snapshot
	DiffRemoved       DiffChangeType = "removed"        // vertex only in prev snapshot
	DiffStateChanged  DiffChangeType = "state_changed"  // state transitioned
	DiffOutputsChanged DiffChangeType = "outputs_changed" // output file hashes changed
	DiffUnchanged     DiffChangeType = "unchanged"      // no diff
)

// DAGSnapshot captures vertex states and output files at a point in time.
type DAGSnapshot struct {
	States  map[string]State
	Outputs map[string][]FileRef // vertex ID → output files
	Labels  map[string]map[string]string
}

// TakeSnapshot captures the current DAG state.
func TakeSnapshot(d *DAG) DAGSnapshot {
	snap := DAGSnapshot{
		States:  make(map[string]State),
		Outputs: make(map[string][]FileRef),
		Labels:  make(map[string]map[string]string),
	}
	for _, v := range d.All() {
		snap.States[v.ID()] = v.State()
		outs := v.OutputFiles()
		if len(outs) > 0 {
			cp := make([]FileRef, len(outs))
			copy(cp, outs)
			snap.Outputs[v.ID()] = cp
		}
		snap.Labels[v.ID()] = v.Labels()
	}
	return snap
}

// Diff compares two DAGSnapshots and returns per-vertex changes.
// prev is the "before" snapshot; next is "after".
func Diff(prev, next DAGSnapshot) []VertexDiff {
	var diffs []VertexDiff

	// Vertices present in both snapshots.
	for id, nextState := range next.States {
		prevState, inPrev := prev.States[id]
		if !inPrev {
			diffs = append(diffs, VertexDiff{
				VertexID:   id,
				ChangeType: DiffAdded,
				NextState:  nextState,
			})
			continue
		}

		if prevState != nextState {
			diffs = append(diffs, VertexDiff{
				VertexID:   id,
				ChangeType: DiffStateChanged,
				PrevState:  prevState,
				NextState:  nextState,
			})
		}

		// Check output file changes.
		outDiff := diffOutputFiles(prev.Outputs[id], next.Outputs[id])
		if outDiff != nil {
			outDiff.VertexID = id
			diffs = append(diffs, *outDiff)
		}
	}

	// Vertices removed in next.
	for id, prevState := range prev.States {
		if _, inNext := next.States[id]; !inNext {
			diffs = append(diffs, VertexDiff{
				VertexID:   id,
				ChangeType: DiffRemoved,
				PrevState:  prevState,
			})
		}
	}

	return diffs
}

// DiffSummary returns a single-line human-readable summary of a diff slice.
func DiffSummary(diffs []VertexDiff) string {
	counts := make(map[DiffChangeType]int)
	for _, d := range diffs {
		counts[d.ChangeType]++
	}
	if len(counts) == 0 {
		return "no changes"
	}
	parts := []string{}
	order := []DiffChangeType{DiffAdded, DiffRemoved, DiffStateChanged, DiffOutputsChanged}
	for _, t := range order {
		if n := counts[t]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, t))
		}
	}
	return strings.Join(parts, ", ")
}

// RenderDiff formats a diff as a human-readable table.
func RenderDiff(diffs []VertexDiff) string {
	if len(diffs) == 0 {
		return "No changes between snapshots.\n"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%-6s %-20s %-15s %-15s\n", "Change", "Vertex", "Prev State", "Next State")
	fmt.Fprintln(&b, strings.Repeat("─", 60))
	for _, d := range diffs {
		prev := "─"
		if d.PrevState != 0 || d.ChangeType == DiffRemoved {
			prev = d.PrevState.String()
		}
		next := "─"
		if d.NextState != 0 || d.ChangeType == DiffAdded {
			next = d.NextState.String()
		}
		fmt.Fprintf(&b, "%-6s %-20s %-15s %-15s\n",
			changeTypeSymbol(d.ChangeType),
			truncate(d.VertexID, 19),
			prev, next)
		if d.ChangeType == DiffOutputsChanged {
			for _, f := range d.AddedFiles {
				fmt.Fprintf(&b, "       %-19s + %s\n", "", f)
			}
			for _, f := range d.RemovedFiles {
				fmt.Fprintf(&b, "       %-19s - %s\n", "", f)
			}
			for _, f := range d.ModifiedFiles {
				fmt.Fprintf(&b, "       %-19s ~ %s\n", "", f)
			}
		}
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// diffOutputFiles compares two output file lists and returns a VertexDiff (without VertexID set)
// if there are changes, or nil if the files are identical.
func diffOutputFiles(prev, next []FileRef) *VertexDiff {
	prevMap := fileRefMap(prev)
	nextMap := fileRefMap(next)

	var added, removed, modified []string

	for path, nextRef := range nextMap {
		prevRef, ok := prevMap[path]
		if !ok {
			added = append(added, path)
		} else if prevRef.Hash != nextRef.Hash {
			modified = append(modified, path)
		}
	}
	for path := range prevMap {
		if _, ok := nextMap[path]; !ok {
			removed = append(removed, path)
		}
	}

	if len(added)+len(removed)+len(modified) == 0 {
		return nil
	}
	return &VertexDiff{
		ChangeType:    DiffOutputsChanged,
		AddedFiles:    added,
		RemovedFiles:  removed,
		ModifiedFiles: modified,
	}
}

func fileRefMap(refs []FileRef) map[string]FileRef {
	m := make(map[string]FileRef, len(refs))
	for _, r := range refs {
		m[r.Path] = r
	}
	return m
}

func changeTypeSymbol(t DiffChangeType) string {
	switch t {
	case DiffAdded:
		return "+"
	case DiffRemoved:
		return "-"
	case DiffStateChanged:
		return "~"
	case DiffOutputsChanged:
		return "Δ"
	default:
		return "="
	}
}
