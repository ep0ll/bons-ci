package reactdag

import (
	"fmt"
	"strings"
)

// ---------------------------------------------------------------------------
// DOT Exporter — Graphviz visualisation
// ---------------------------------------------------------------------------

// DOTOptions controls the appearance of the exported graph.
type DOTOptions struct {
	// Title is the graph label rendered at the top.
	Title string
	// ShowState colours vertices according to their current State.
	ShowState bool
	// ShowMetrics annotates vertices with their execution duration.
	ShowMetrics bool
	// ShowFileDeps renders fine-grained file dependency edges in a different style.
	ShowFileDeps bool
	// RankDir controls graph direction: "TB" (top-bottom), "LR" (left-right).
	// Defaults to "TB".
	RankDir string
}

var stateColors = map[State]string{
	StateInitial:   "#E8E8E8", // light grey
	StateFastCache: "#C8E6C9", // green  (fast hit)
	StateSlowCache: "#B3E5FC", // blue   (slow hit)
	StateCompleted: "#A5D6A7", // darker green
	StateFailed:    "#FFCDD2", // red
}

// ExportDOT serialises the DAG as a Graphviz DOT string.
// The output can be piped to `dot -Tsvg -o graph.svg` to produce a diagram.
func ExportDOT(d *DAG, opts DOTOptions) string {
	if opts.RankDir == "" {
		opts.RankDir = "TB"
	}

	var b strings.Builder

	b.WriteString("digraph reactdag {\n")
	b.WriteString("  graph [")
	if opts.Title != "" {
		fmt.Fprintf(&b, `label=%q `, opts.Title)
	}
	fmt.Fprintf(&b, "rankdir=%q fontname=\"Helvetica\" bgcolor=\"#FAFAFA\"];\n", opts.RankDir)
	b.WriteString("  node  [fontname=\"Helvetica\" fontsize=11 style=filled shape=box];\n")
	b.WriteString("  edge  [fontname=\"Helvetica\" fontsize=9];\n\n")

	vertices := d.All()
	for _, v := range vertices {
		b.WriteString(dotVertexNode(v, opts))
	}

	b.WriteString("\n")

	// Structural edges.
	for _, v := range vertices {
		for _, parent := range v.Parents() {
			// Check if this edge has a file dep declaration.
			deps, hasDep := v.FileDependencyForParent(parent.ID())
			if opts.ShowFileDeps && hasDep {
				edgeLabel := strings.Join(deps, "\\n")
				fmt.Fprintf(&b, "  %s -> %s [label=%q style=dashed color=\"#1565C0\" fontcolor=\"#1565C0\"];\n",
					dotID(parent.ID()), dotID(v.ID()), edgeLabel)
			} else {
				fmt.Fprintf(&b, "  %s -> %s;\n", dotID(parent.ID()), dotID(v.ID()))
			}
		}
	}

	b.WriteString("}\n")
	return b.String()
}

// dotVertexNode generates the DOT node declaration for a vertex.
func dotVertexNode(v *Vertex, opts DOTOptions) string {
	var b strings.Builder

	label := v.ID()
	if opts.ShowMetrics {
		m := v.Metrics()
		if dur := m.Duration(); dur > 0 {
			label += fmt.Sprintf("\\n(%dms)", dur.Milliseconds())
		}
	}

	fillColor := "#FFFFFF"
	if opts.ShowState {
		if c, ok := stateColors[v.State()]; ok {
			fillColor = c
		}
	}

	tooltip := fmt.Sprintf("id=%s op=%s state=%s", v.ID(), v.OpID(), v.State())
	if err := v.Err(); err != nil {
		tooltip += " err=" + err.Error()
	}

	fmt.Fprintf(&b, "  %s [label=%q fillcolor=%q tooltip=%q];\n",
		dotID(v.ID()), label, fillColor, tooltip)

	return b.String()
}

// dotID sanitises a vertex ID for use as a DOT node identifier.
// DOT identifiers must not contain special characters.
func dotID(id string) string {
	r := strings.NewReplacer(
		"-", "_", ".", "_", "/", "_", ":", "_", " ", "_",
	)
	return "v_" + r.Replace(id)
}
