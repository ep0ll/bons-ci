package schedule

// Policy computes the scheduling priority for a vertex. Lower values run first.
// Implementations should be stateless and safe for concurrent use.
type Policy interface {
	// Priority computes the scheduling priority from the vertex's depth in the
	// DAG and an optional estimated execution cost hint.
	Priority(depth int, estimatedCost float64) int
}

// CriticalPathPolicy prioritises vertices with the highest depth (longest path
// from root) first, minimising overall solve latency by tackling the bottleneck
// chain early. This mirrors BuildKit's concept of starting the deepest
// dependency chains first.
//
// Priority = MaxDepth - depth (lower number = higher urgency = runs first).
type CriticalPathPolicy struct {
	// MaxDepth is the maximum depth in the current graph. Set this to
	// Graph.MaxDepth() before submitting tasks.
	MaxDepth int
}

// Priority returns MaxDepth - depth, so deeper (root-adjacent) vertices get
// lower (earlier) priority values. EstimatedCost is ignored.
func (p *CriticalPathPolicy) Priority(depth int, _ float64) int {
	return p.MaxDepth - depth
}

// DepthFirstPolicy schedules deeper vertices first without requiring MaxDepth.
// Uses -depth as the priority (works even when MaxDepth is unknown up front).
type DepthFirstPolicy struct{}

// Priority returns the negated depth so deeper vertices run first.
func (DepthFirstPolicy) Priority(depth int, _ float64) int { return -depth }

// BreadthFirstPolicy schedules shallower vertices first (the opposite of
// CriticalPath). Useful when you want to produce partial results quickly.
type BreadthFirstPolicy struct{}

// Priority uses raw depth so shallower vertices execute first.
func (BreadthFirstPolicy) Priority(depth int, _ float64) int { return depth }

// CostWeightedPolicy combines dependency depth and estimated execution cost.
// More expensive deeper vertices are prioritised highest, which is useful when
// vertex costs vary widely (e.g. image pulls vs cheap file operations).
type CostWeightedPolicy struct {
	MaxDepth int
}

// Priority computes a combined score: depth component + cost component.
// Higher cost → more negative cost score → runs earlier.
func (p CostWeightedPolicy) Priority(depth int, estimatedCost float64) int {
	depthScore := p.MaxDepth - depth
	costScore := int(-estimatedCost * 1000)
	return depthScore + costScore
}
