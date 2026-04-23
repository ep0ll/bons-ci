package reactdag

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// BuildPlan — dry-run analysis
// ---------------------------------------------------------------------------

// VertexPlan is the predicted disposition of a single vertex in a build.
type VertexPlan struct {
	VertexID     string
	Action       PlanAction
	CacheTier    string // "fast", "slow", or "" for execute/skip
	Dependencies []string
	// EstimatedDurationMS is derived from the last cached DurationMS, or 0.
	EstimatedDurationMS int64
}

// PlanAction is what the scheduler would do for a vertex.
type PlanAction uint8

const (
	// PlanExecute: vertex will be run.
	PlanExecute PlanAction = iota
	// PlanFastCache: result will be served from fast cache.
	PlanFastCache
	// PlanSlowCache: result will be served from slow cache.
	PlanSlowCache
	// PlanSkip: vertex is already terminal (target short-circuit).
	PlanSkip
	// PlanFail: vertex is predicted to fail (cached failure will be replayed).
	PlanFail
)

var planActionNames = [...]string{"execute", "fast_cache", "slow_cache", "skip", "fail"}

func (a PlanAction) String() string {
	if int(a) < len(planActionNames) {
		return planActionNames[a]
	}
	return fmt.Sprintf("action(%d)", a)
}

// BuildPlan is the complete dry-run analysis for one Build call.
type BuildPlan struct {
	TargetID                string
	Steps                   []VertexPlan // topological order
	TotalVertices           int
	WillExecute             int
	FastCacheHits           int
	SlowCacheHits           int
	WillFail                int
	EstimatedCriticalPathMS int64
	CriticalPath            []string
}

// Summary returns a human-readable one-line summary of the plan.
func (p *BuildPlan) Summary() string {
	return fmt.Sprintf(
		"target=%s total=%d execute=%d fast_cache=%d slow_cache=%d will_fail=%d est_critical_path=%dms",
		p.TargetID, p.TotalVertices, p.WillExecute,
		p.FastCacheHits, p.SlowCacheHits, p.WillFail,
		p.EstimatedCriticalPathMS,
	)
}

// ---------------------------------------------------------------------------
// Planner
// ---------------------------------------------------------------------------

// Planner analyses a DAG without executing any operations.
// It queries both cache tiers and predicts the action the Scheduler would take
// for each vertex.
type Planner struct {
	dag       *DAG
	fastCache CacheStore
	slowCache CacheStore
	keyComp   CacheKeyComputer
}

// NewPlanner constructs a Planner using the same configuration as the Scheduler.
func NewPlanner(dag *DAG, fastCache, slowCache CacheStore, keyComp CacheKeyComputer) *Planner {
	if fastCache == nil {
		fastCache = NoopCacheStore{}
	}
	if slowCache == nil {
		slowCache = NoopCacheStore{}
	}
	if keyComp == nil {
		keyComp = DefaultKeyComputer{}
	}
	return &Planner{dag: dag, fastCache: fastCache, slowCache: slowCache, keyComp: keyComp}
}

// Plan computes the BuildPlan for a given target without executing anything.
// changedFiles should match what would be passed to Scheduler.Build.
func (p *Planner) Plan(ctx context.Context, targetID string, changedFiles []FileRef) (*BuildPlan, error) {
	target, ok := p.dag.Vertex(targetID)
	if !ok {
		return nil, fmt.Errorf("planner: target %q not found", targetID)
	}

	// Apply invalidations to a scratch state (we must not mutate the real DAG).
	snapshots := p.snapshotStates()
	defer p.restoreStates(snapshots)

	if len(changedFiles) > 0 {
		eng := NewInvalidationEngine(p.dag, nil)
		if _, err := eng.Invalidate(ctx, changedFiles); err != nil {
			return nil, fmt.Errorf("planner: invalidation: %w", err)
		}
	}

	// Short-circuit if already terminal.
	if target.State().IsTerminal() {
		return p.shortCircuitPlan(ctx, target, targetID)
	}

	sorted, err := p.dag.TopologicalSortFrom(targetID)
	if err != nil {
		return nil, fmt.Errorf("planner: topo sort: %w", err)
	}

	plan := &BuildPlan{
		TargetID:      targetID,
		TotalVertices: len(sorted),
	}

	// Simulate the scheduler's resolution logic.
	for _, v := range sorted {
		step := p.planVertex(ctx, v)
		plan.Steps = append(plan.Steps, step)
		switch step.Action {
		case PlanExecute:
			plan.WillExecute++
		case PlanFastCache:
			plan.FastCacheHits++
		case PlanSlowCache:
			plan.SlowCacheHits++
		case PlanFail:
			plan.WillFail++
		}
	}

	// Critical path: sum estimated durations along the longest chain.
	critPath, _ := p.dag.CriticalPath(targetID)
	plan.CriticalPath = critPath
	plan.EstimatedCriticalPathMS = p.estimateCriticalPath(plan.Steps, critPath)

	return plan, nil
}

// planVertex determines what the Scheduler would do for v.
func (p *Planner) planVertex(ctx context.Context, v *Vertex) VertexPlan {
	step := VertexPlan{
		VertexID: v.ID(),
		Action:   PlanExecute,
	}
	for _, parent := range v.Parents() {
		step.Dependencies = append(step.Dependencies, parent.ID())
	}

	inputs := p.resolveInputFiles(v)
	key, err := p.keyComp.Compute(v, inputs)
	if err != nil {
		return step
	}

	// Fast cache lookup.
	if entry, _ := p.fastCache.Get(ctx, key); entry != nil {
		step.EstimatedDurationMS = entry.DurationMS
		if entry.IsFailed() {
			step.Action = PlanFail
			step.CacheTier = "fast"
		} else {
			step.Action = PlanFastCache
			step.CacheTier = "fast"
		}
		return step
	}

	// Slow cache lookup.
	if entry, _ := p.slowCache.Get(ctx, key); entry != nil {
		step.EstimatedDurationMS = entry.DurationMS
		if entry.IsFailed() {
			step.Action = PlanFail
			step.CacheTier = "slow"
		} else {
			step.Action = PlanSlowCache
			step.CacheTier = "slow"
		}
		return step
	}

	return step
}

// shortCircuitPlan mirrors Scheduler.shortCircuit for the planner.
func (p *Planner) shortCircuitPlan(ctx context.Context, target *Vertex, targetID string) (*BuildPlan, error) {
	ancestors, _ := p.dag.Ancestors(targetID)
	plan := &BuildPlan{
		TargetID:      targetID,
		TotalVertices: len(ancestors) + 1,
	}
	for _, anc := range ancestors {
		plan.Steps = append(plan.Steps, VertexPlan{VertexID: anc.ID(), Action: PlanSkip})
	}
	action := PlanSkip
	if target.State() == StateFailed {
		action = PlanFail
		plan.WillFail++
	}
	plan.Steps = append(plan.Steps, VertexPlan{VertexID: target.ID(), Action: action})
	return plan, nil
}

// resolveInputFiles mirrors Scheduler.resolveInputFiles.
func (p *Planner) resolveInputFiles(v *Vertex) []FileRef {
	var inputs []FileRef
	for _, parent := range v.Parents() {
		declared, hasDep := v.FileDependencyForParent(parent.ID())
		if !hasDep {
			inputs = append(inputs, parent.OutputFiles()...)
		} else {
			inputs = append(inputs, filterFilesByPath(parent.OutputFiles(), declared)...)
		}
	}
	return inputs
}

// estimateCriticalPath sums estimated durations along the critical path.
func (p *Planner) estimateCriticalPath(steps []VertexPlan, critPath []string) int64 {
	durByID := make(map[string]int64, len(steps))
	for _, s := range steps {
		durByID[s.VertexID] = s.EstimatedDurationMS
	}
	var total int64
	for _, id := range critPath {
		total += durByID[id]
	}
	return total
}

// ---------------------------------------------------------------------------
// State snapshot / restore (so Plan never mutates real vertex state)
// ---------------------------------------------------------------------------

type vertexSnapshot struct {
	id    string
	state State
	err   error
}

func (p *Planner) snapshotStates() []vertexSnapshot {
	all := p.dag.All()
	snaps := make([]vertexSnapshot, len(all))
	for i, v := range all {
		snaps[i] = vertexSnapshot{id: v.ID(), state: v.State(), err: v.Err()}
	}
	return snaps
}

func (p *Planner) restoreStates(snaps []vertexSnapshot) {
	for _, snap := range snaps {
		v, ok := p.dag.Vertex(snap.id)
		if !ok {
			continue
		}
		v.Reset()
		if snap.state != StateInitial {
			// Force the state back (Reset puts it to initial).
			_ = v.SetState(snap.state, "plan restore")
			if snap.err != nil {
				_ = v.SetFailed(snap.err, "plan restore")
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Plan renderer — pretty-prints a BuildPlan to a string
// ---------------------------------------------------------------------------

// RenderPlan formats a BuildPlan as a human-readable ASCII table.
func RenderPlan(plan *BuildPlan) string {
	var b strings.Builder

	fmt.Fprintf(&b, "Build Plan — target: %s\n", plan.TargetID)
	fmt.Fprintf(&b, "%-6s %-12s %-12s %s\n", "Step", "Action", "Cache", "Vertex")
	fmt.Fprintln(&b, strings.Repeat("─", 60))

	for i, step := range plan.Steps {
		cache := step.CacheTier
		if cache == "" {
			cache = "─"
		}
		estDur := ""
		if step.EstimatedDurationMS > 0 {
			estDur = fmt.Sprintf(" (~%dms)", step.EstimatedDurationMS)
		}
		fmt.Fprintf(&b, "%-6d %-12s %-12s %s%s\n",
			i+1, step.Action, cache, step.VertexID, estDur)
	}

	fmt.Fprintln(&b, strings.Repeat("─", 60))
	fmt.Fprintf(&b, "Total: %d  Execute: %d  FastCache: %d  SlowCache: %d  WillFail: %d\n",
		plan.TotalVertices, plan.WillExecute, plan.FastCacheHits, plan.SlowCacheHits, plan.WillFail)

	if len(plan.CriticalPath) > 0 {
		fmt.Fprintf(&b, "Critical path: %s  (est. %dms)\n",
			strings.Join(plan.CriticalPath, " → "), plan.EstimatedCriticalPathMS)
	}

	return b.String()
}

// ---------------------------------------------------------------------------
// Dummy time reference (suppress unused import)
// ---------------------------------------------------------------------------
var _ = time.Duration(0)
