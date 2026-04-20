package reactdag

import (
	"context"
	"fmt"
	"sync"
)

// ---------------------------------------------------------------------------
// Group — a named set of vertices that can be built together
// ---------------------------------------------------------------------------

// Group is a named collection of vertex IDs that can be treated as a single
// build target. Building a group builds all member vertices concurrently up
// to the scheduler's worker limit. Groups support:
//
//   - Selective rebuilds: build only "test" or only "lint" without building
//     the full graph.
//   - Tagging: tag vertices with semantic roles and query by tag.
//   - Virtual targets: a group has no Operation; it simply collects results.
type Group struct {
	name      string
	vertexIDs []string
	labels    map[string]string
}

// NewGroup creates a named Group containing the given vertex IDs.
func NewGroup(name string, vertexIDs ...string) *Group {
	return &Group{
		name:      name,
		vertexIDs: vertexIDs,
		labels:    make(map[string]string),
	}
}

// Name returns the group's name.
func (g *Group) Name() string { return g.name }

// Members returns the vertex IDs belonging to this group.
func (g *Group) Members() []string {
	cp := make([]string, len(g.vertexIDs))
	copy(cp, g.vertexIDs)
	return cp
}

// SetLabel attaches a label to the group.
func (g *Group) SetLabel(key, value string) { g.labels[key] = value }

// Label returns a label value.
func (g *Group) Label(key string) (string, bool) {
	v, ok := g.labels[key]
	return v, ok
}

// ---------------------------------------------------------------------------
// GroupRegistry — manages named groups
// ---------------------------------------------------------------------------

// GroupRegistry maps group names to Groups.
type GroupRegistry struct {
	mu     sync.RWMutex
	groups map[string]*Group
}

// NewGroupRegistry constructs an empty registry.
func NewGroupRegistry() *GroupRegistry {
	return &GroupRegistry{groups: make(map[string]*Group)}
}

// Register adds or replaces a group.
func (r *GroupRegistry) Register(g *Group) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.groups[g.name] = g
}

// Get returns a group by name.
func (r *GroupRegistry) Get(name string) (*Group, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	g, ok := r.groups[name]
	return g, ok
}

// All returns all registered groups.
func (r *GroupRegistry) All() []*Group {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Group, 0, len(r.groups))
	for _, g := range r.groups {
		out = append(out, g)
	}
	return out
}

// ByLabel returns all groups that have a specific label key-value pair.
func (r *GroupRegistry) ByLabel(key, value string) []*Group {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []*Group
	for _, g := range r.groups {
		if v, ok := g.labels[key]; ok && v == value {
			out = append(out, g)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// GroupScheduler — builds a group's members concurrently
// ---------------------------------------------------------------------------

// GroupBuildResult holds the per-member outcome of a group build.
type GroupBuildResult struct {
	GroupName string
	Results   map[string]GroupMemberResult // vertex ID → result
}

// GroupMemberResult is the outcome for one vertex within a group build.
type GroupMemberResult struct {
	Metrics *BuildMetrics
	Err     error
}

// Succeeded reports whether all members completed without error.
func (r GroupBuildResult) Succeeded() bool {
	for _, m := range r.Results {
		if m.Err != nil {
			return false
		}
	}
	return true
}

// Errors returns a map of vertexID → error for all failed members.
func (r GroupBuildResult) Errors() map[string]error {
	out := make(map[string]error)
	for id, m := range r.Results {
		if m.Err != nil {
			out[id] = m.Err
		}
	}
	return out
}

// GroupScheduler extends a Scheduler with group-aware build methods.
type GroupScheduler struct {
	*Scheduler
	registry *GroupRegistry
}

// NewGroupScheduler constructs a GroupScheduler.
func NewGroupScheduler(dag *DAG, registry *GroupRegistry, opts ...Option) *GroupScheduler {
	return &GroupScheduler{
		Scheduler: NewScheduler(dag, opts...),
		registry:  registry,
	}
}

// BuildGroup builds all members of the named group concurrently.
// changedFiles is passed to each member build.
func (gs *GroupScheduler) BuildGroup(
	ctx context.Context,
	groupName string,
	changedFiles []FileRef,
) (GroupBuildResult, error) {
	g, ok := gs.registry.Get(groupName)
	if !ok {
		return GroupBuildResult{}, fmt.Errorf("group %q not found", groupName)
	}
	return gs.buildMembers(ctx, g, changedFiles)
}

// BuildByLabel builds all groups matching the given label concurrently.
func (gs *GroupScheduler) BuildByLabel(
	ctx context.Context,
	labelKey, labelValue string,
	changedFiles []FileRef,
) ([]GroupBuildResult, error) {
	groups := gs.registry.ByLabel(labelKey, labelValue)
	if len(groups) == 0 {
		return nil, fmt.Errorf("no groups with label %s=%s", labelKey, labelValue)
	}
	results := make([]GroupBuildResult, len(groups))
	errCh := make(chan error, len(groups))
	var wg sync.WaitGroup
	for i, grp := range groups {
		wg.Add(1)
		go func(idx int, g *Group) {
			defer wg.Done()
			res, err := gs.buildMembers(ctx, g, changedFiles)
			results[idx] = res
			if err != nil {
				errCh <- err
			}
		}(i, grp)
	}
	wg.Wait()
	close(errCh)
	var firstErr error
	for e := range errCh {
		if firstErr == nil {
			firstErr = e
		}
	}
	return results, firstErr
}

// buildMembers runs each member vertex as an independent build target.
func (gs *GroupScheduler) buildMembers(
	ctx context.Context,
	g *Group,
	changedFiles []FileRef,
) (GroupBuildResult, error) {
	result := GroupBuildResult{
		GroupName: g.name,
		Results:   make(map[string]GroupMemberResult, len(g.vertexIDs)),
	}
	var mu sync.Mutex
	errCh := make(chan error, len(g.vertexIDs))
	var wg sync.WaitGroup

	for _, id := range g.vertexIDs {
		wg.Add(1)
		go func(vertexID string) {
			defer wg.Done()
			m, err := gs.Build(ctx, vertexID, changedFiles)
			mu.Lock()
			result.Results[vertexID] = GroupMemberResult{Metrics: m, Err: err}
			mu.Unlock()
			if err != nil {
				errCh <- fmt.Errorf("group %q member %q: %w", g.name, vertexID, err)
			}
		}(id)
	}
	wg.Wait()
	close(errCh)

	var firstErr error
	for e := range errCh {
		if firstErr == nil {
			firstErr = e
		}
	}
	return result, firstErr
}
