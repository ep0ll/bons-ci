package reactdag

import (
	"fmt"
	"sort"
	"strings"
)

// ---------------------------------------------------------------------------
// GraphAnalyser — structural metrics and optimisation insights
// ---------------------------------------------------------------------------

// GraphAnalysis is the result of analysing a sealed DAG's structure.
type GraphAnalysis struct {
	VertexCount    int
	EdgeCount      int
	RootCount      int      // vertices with no parents
	LeafCount      int      // vertices with no children
	MaxDepth       int      // longest path from any root to any leaf (vertex hops)
	MaxFanIn       int      // highest number of parents any single vertex has
	MaxFanOut      int      // highest number of children any single vertex has
	CriticalPath   []string // longest path from root to any leaf
	Bottlenecks    []string // vertices that lie on many critical paths (high betweenness)
	ParallelLevels int      // number of distinct topological levels (parallelism potential)
	IsolatedNodes  []string // vertices with no edges at all
	PerVertex      map[string]VertexAnalysis
}

// VertexAnalysis holds per-vertex structural metrics.
type VertexAnalysis struct {
	VertexID    string
	Depth       int      // distance from nearest root
	FanIn       int      // number of direct parents
	FanOut      int      // number of direct children
	AncestorCount   int  // transitive ancestor count
	DescendantCount int  // transitive descendant count
	IsRoot      bool
	IsLeaf      bool
	IsOnCriticalPath bool
}

// Analyse computes structural metrics for the DAG.
func Analyse(d *DAG) (*GraphAnalysis, error) {
	sorted, err := d.TopologicalSort()
	if err != nil {
		return nil, fmt.Errorf("graph analysis: %w", err)
	}

	a := &GraphAnalysis{
		VertexCount: len(sorted),
		PerVertex:   make(map[string]VertexAnalysis, len(sorted)),
	}

	// Compute depths (longest path from any root to each vertex).
	depth := computeDepths(sorted)

	// Identify critical path (longest chain root → leaf).
	a.CriticalPath = longestPath(sorted)
	criticalSet := make(map[string]bool, len(a.CriticalPath))
	for _, id := range a.CriticalPath {
		criticalSet[id] = true
	}
	if len(a.CriticalPath) > 0 {
		a.MaxDepth = len(a.CriticalPath) - 1
	}

	// Compute topological levels for parallelism potential.
	a.ParallelLevels = maxDepthValue(depth) + 1

	// Collect per-vertex stats.
	for _, v := range sorted {
		fanIn := len(v.Parents())
		fanOut := len(v.Children())
		a.EdgeCount += fanOut

		isRoot := fanIn == 0
		isLeaf := fanOut == 0
		if isRoot {
			a.RootCount++
		}
		if isLeaf {
			a.LeafCount++
		}
		if isRoot && isLeaf {
			a.IsolatedNodes = append(a.IsolatedNodes, v.ID())
		}
		if fanIn > a.MaxFanIn {
			a.MaxFanIn = fanIn
		}
		if fanOut > a.MaxFanOut {
			a.MaxFanOut = fanOut
		}

		ancs, _ := d.Ancestors(v.ID())
		descs, _ := d.Descendants(v.ID())

		a.PerVertex[v.ID()] = VertexAnalysis{
			VertexID:         v.ID(),
			Depth:            depth[v.ID()],
			FanIn:            fanIn,
			FanOut:           fanOut,
			AncestorCount:    len(ancs),
			DescendantCount:  len(descs),
			IsRoot:           isRoot,
			IsLeaf:           isLeaf,
			IsOnCriticalPath: criticalSet[v.ID()],
		}
	}

	// Bottleneck: vertices on the critical path with many dependents.
	a.Bottlenecks = detectBottlenecks(a.PerVertex, criticalSet)

	return a, nil
}

// RenderAnalysis formats a GraphAnalysis as a human-readable report.
func RenderAnalysis(a *GraphAnalysis) string {
	var b strings.Builder

	fmt.Fprintln(&b, "Graph Analysis")
	fmt.Fprintln(&b, strings.Repeat("─", 50))
	fmt.Fprintf(&b, "  %-28s %d\n", "Vertices:", a.VertexCount)
	fmt.Fprintf(&b, "  %-28s %d\n", "Edges:", a.EdgeCount)
	fmt.Fprintf(&b, "  %-28s %d\n", "Roots:", a.RootCount)
	fmt.Fprintf(&b, "  %-28s %d\n", "Leaves:", a.LeafCount)
	fmt.Fprintf(&b, "  %-28s %d\n", "Max depth:", a.MaxDepth)
	fmt.Fprintf(&b, "  %-28s %d\n", "Max fan-in:", a.MaxFanIn)
	fmt.Fprintf(&b, "  %-28s %d\n", "Max fan-out:", a.MaxFanOut)
	fmt.Fprintf(&b, "  %-28s %d\n", "Parallel levels:", a.ParallelLevels)

	if len(a.CriticalPath) > 0 {
		fmt.Fprintf(&b, "  %-28s %s\n", "Critical path:",
			strings.Join(a.CriticalPath, " → "))
	}
	if len(a.Bottlenecks) > 0 {
		fmt.Fprintf(&b, "  %-28s %s\n", "Bottlenecks:",
			strings.Join(a.Bottlenecks, ", "))
	}
	if len(a.IsolatedNodes) > 0 {
		fmt.Fprintf(&b, "  %-28s %s\n", "Isolated nodes:",
			strings.Join(a.IsolatedNodes, ", "))
	}

	// Per-vertex table (sorted by depth desc, then fan-out desc).
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "Per-Vertex Analysis")
	fmt.Fprintln(&b, strings.Repeat("─", 70))
	fmt.Fprintf(&b, "  %-20s %5s %6s %7s %9s %11s %s\n",
		"Vertex", "Depth", "Fan-in", "Fan-out", "Ancestors", "Descendants", "Flags")
	fmt.Fprintln(&b, "  "+strings.Repeat("─", 68))

	// Sort: critical path first, then by descendant count desc.
	vas := make([]VertexAnalysis, 0, len(a.PerVertex))
	for _, va := range a.PerVertex {
		vas = append(vas, va)
	}
	sort.Slice(vas, func(i, j int) bool {
		if vas[i].IsOnCriticalPath != vas[j].IsOnCriticalPath {
			return vas[i].IsOnCriticalPath
		}
		return vas[i].DescendantCount > vas[j].DescendantCount
	})

	for _, va := range vas {
		flags := vertexFlags(va)
		fmt.Fprintf(&b, "  %-20s %5d %6d %7d %9d %11d %s\n",
			truncate(va.VertexID, 19),
			va.Depth, va.FanIn, va.FanOut,
			va.AncestorCount, va.DescendantCount, flags)
	}

	return b.String()
}

// ---------------------------------------------------------------------------
// Parallelism potential analysis
// ---------------------------------------------------------------------------

// ParallelismReport analyses how much of the build can run concurrently
// at each topological level.
type ParallelismReport struct {
	Levels     []TopologicalLevel
	MaxWidth   int     // widest level (most parallel)
	AvgWidth   float64 // average vertices per level
	SerialFraction float64 // fraction of levels that are serial (width=1)
}

// TopologicalLevel describes one "wave" of parallel execution.
type TopologicalLevel struct {
	Level    int
	Vertices []string
	Width    int // = len(Vertices)
}

// AnalyseParallelism breaks the DAG into topological levels and reports
// how much work can run concurrently at each wave.
func AnalyseParallelism(d *DAG) (*ParallelismReport, error) {
	sorted, err := d.TopologicalSort()
	if err != nil {
		return nil, fmt.Errorf("parallelism analysis: %w", err)
	}

	depth := computeDepths(sorted)
	maxD := maxDepthValue(depth)

	levelMap := make(map[int][]string, maxD+1)
	for _, v := range sorted {
		d := depth[v.ID()]
		levelMap[d] = append(levelMap[d], v.ID())
	}

	report := &ParallelismReport{
		Levels: make([]TopologicalLevel, 0, maxD+1),
	}
	serial := 0
	for l := 0; l <= maxD; l++ {
		vs := levelMap[l]
		sort.Strings(vs)
		report.Levels = append(report.Levels, TopologicalLevel{
			Level:    l,
			Vertices: vs,
			Width:    len(vs),
		})
		if len(vs) > report.MaxWidth {
			report.MaxWidth = len(vs)
		}
		if len(vs) == 1 {
			serial++
		}
	}
	total := maxD + 1
	if total > 0 {
		var widthSum int
		for _, l := range report.Levels {
			widthSum += l.Width
		}
		report.AvgWidth = float64(widthSum) / float64(total)
		report.SerialFraction = float64(serial) / float64(total)
	}
	return report, nil
}

// RenderParallelismReport formats a ParallelismReport as ASCII.
func RenderParallelismReport(r *ParallelismReport) string {
	var b strings.Builder
	fmt.Fprintln(&b, "Parallelism Analysis")
	fmt.Fprintln(&b, strings.Repeat("─", 50))
	fmt.Fprintf(&b, "  %-28s %d\n", "Levels:", len(r.Levels))
	fmt.Fprintf(&b, "  %-28s %d\n", "Max width (concurrent):", r.MaxWidth)
	fmt.Fprintf(&b, "  %-28s %.1f\n", "Avg width:", r.AvgWidth)
	fmt.Fprintf(&b, "  %-28s %.0f%%\n", "Serial fraction:", r.SerialFraction*100)
	fmt.Fprintln(&b)
	for _, l := range r.Levels {
		bar := strings.Repeat("█", l.Width)
		fmt.Fprintf(&b, "  L%-3d [%2d] %s\n", l.Level, l.Width, bar)
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Internal algorithms
// ---------------------------------------------------------------------------

// computeDepths assigns each vertex the length of the longest path from any
// root to that vertex (measured in edges).
func computeDepths(sorted []*Vertex) map[string]int {
	depth := make(map[string]int, len(sorted))
	for _, v := range sorted {
		for _, p := range v.Parents() {
			if d := depth[p.ID()] + 1; d > depth[v.ID()] {
				depth[v.ID()] = d
			}
		}
	}
	return depth
}

// maxDepthValue returns the maximum value in a depth map.
func maxDepthValue(depth map[string]int) int {
	max := 0
	for _, d := range depth {
		if d > max {
			max = d
		}
	}
	return max
}

// longestPath returns the vertex ID sequence of the longest chain (most edges)
// from any root to any leaf using DP on the topo-sorted list.
func longestPath(sorted []*Vertex) []string {
	if len(sorted) == 0 {
		return nil
	}
	dist := make(map[string]int, len(sorted))
	prev := make(map[string]string, len(sorted))
	for _, v := range sorted {
		dist[v.ID()] = 1
		for _, p := range v.Parents() {
			if d := dist[p.ID()] + 1; d > dist[v.ID()] {
				dist[v.ID()] = d
				prev[v.ID()] = p.ID()
			}
		}
	}
	// Find the leaf with maximum distance.
	var end string
	maxD := 0
	for _, v := range sorted {
		if d := dist[v.ID()]; d > maxD {
			maxD = d
			end = v.ID()
		}
	}
	// Trace back.
	var path []string
	for cur := end; cur != ""; cur = prev[cur] {
		path = append([]string{cur}, path...)
	}
	return path
}

// detectBottlenecks returns vertex IDs that are on the critical path and have
// a high descendant count (they block many downstream vertices).
func detectBottlenecks(perVertex map[string]VertexAnalysis, criticalSet map[string]bool) []string {
	if len(criticalSet) == 0 {
		return nil
	}
	// Threshold: descendant count in the top quartile of critical-path vertices.
	var descs []int
	for id := range criticalSet {
		descs = append(descs, perVertex[id].DescendantCount)
	}
	sort.Ints(descs)
	threshold := 1
	if len(descs) >= 4 {
		threshold = descs[len(descs)*3/4]
	}

	var bottlenecks []string
	for id := range criticalSet {
		if va := perVertex[id]; va.DescendantCount >= threshold && va.DescendantCount > 0 {
			bottlenecks = append(bottlenecks, id)
		}
	}
	sort.Strings(bottlenecks)
	return bottlenecks
}

// vertexFlags returns a compact string of single-letter flags for a vertex.
func vertexFlags(va VertexAnalysis) string {
	flags := ""
	if va.IsRoot {
		flags += "R"
	}
	if va.IsLeaf {
		flags += "L"
	}
	if va.IsOnCriticalPath {
		flags += "C"
	}
	return flags
}
