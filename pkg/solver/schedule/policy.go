package schedule

// Policy computes the scheduling priority for a vertex. Lower priority
// values are executed first.
type Policy interface {
	// Priority computes the scheduling priority given the vertex's depth
	// in the DAG and its estimated execution cost.
	Priority(depth int, estimatedCost float64) int
}

// CriticalPathPolicy prioritizes deeper vertices first. Vertices closer
// to the roots of the DAG (higher depth) are scheduled with lower priority
// values so they execute first. This minimizes overall solve latency by
// tackling the longest dependency chains early.
type CriticalPathPolicy struct {
	// MaxDepth is the maximum depth in the current graph. Must be set
	// before use so that priority = maxDepth - depth.
	MaxDepth int
}

// Priority returns maxDepth - depth, so deeper vertices get lower (earlier)
// priority values. EstimatedCost is used as a tiebreaker in the queue.
func (p *CriticalPathPolicy) Priority(depth int, _ float64) int {
	return p.MaxDepth - depth
}

// DepthFirstPolicy schedules deeper vertices first (same as CriticalPath
// but without requiring MaxDepth). Uses negative depth as priority.
type DepthFirstPolicy struct{}

// Priority returns -depth so deeper vertices have lower (earlier) priority.
func (DepthFirstPolicy) Priority(depth int, _ float64) int {
	return -depth
}

// BreadthFirstPolicy schedules shallow vertices first.
type BreadthFirstPolicy struct{}

// Priority uses depth directly, so shallower vertices execute first.
func (BreadthFirstPolicy) Priority(depth int, _ float64) int {
	return depth
}

// CostWeightedPolicy combines depth and estimated cost. More expensive
// deeper vertices are prioritized highest.
type CostWeightedPolicy struct {
	MaxDepth int
}

// Priority computes a combined score from depth and cost.
func (p CostWeightedPolicy) Priority(depth int, estimatedCost float64) int {
	// Depth component: deeper = lower priority value = earlier execution.
	depthScore := p.MaxDepth - depth
	// Cost component: more expensive = lower priority value.
	costScore := int(-estimatedCost * 1000)
	return depthScore + costScore
}
