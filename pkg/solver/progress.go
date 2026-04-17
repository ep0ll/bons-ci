package solver

import (
	"sort"
	"sync"
	"time"

	digest "github.com/opencontainers/go-digest"
)

// SolveStatus holds progress information for a solve operation, including
// vertex states, status updates, logs, and warnings. This matches
// BuildKit's SolveStatus pattern.
type SolveStatus struct {
	Vertexes []*VertexInfo
	Statuses []*VertexStatus
	Logs     []*VertexLog
	Warnings []*VertexWarning
}

// VertexInfo describes the current state of a vertex in the solve.
type VertexInfo struct {
	Digest    digest.Digest
	Inputs    []digest.Digest
	Name      string
	Cached    bool
	Started   *time.Time
	Completed *time.Time
	Error     string
}

// ─── Vertex stream (dedup + ordering) ────────────────────────────────────────

// vertexStream deduplicates and orders vertex status updates for streaming
// to clients. It matches BuildKit's vertexStream in progress.go.
type vertexStream struct {
	mu        sync.Mutex
	cache     map[digest.Digest]*VertexInfo
	wasCached map[digest.Digest]struct{}
}

func newVertexStream() *vertexStream {
	return &vertexStream{
		cache:     make(map[digest.Digest]*VertexInfo),
		wasCached: make(map[digest.Digest]struct{}),
	}
}

// append adds a vertex info and returns any new/updated entries to emit.
// When a vertex starts, any unstarted inputs are automatically marked as cached.
func (vs *vertexStream) append(v VertexInfo) []*VertexInfo {
	vs.mu.Lock()
	defer vs.mu.Unlock()

	var out []*VertexInfo
	vs.cache[v.Digest] = &v

	if v.Started != nil {
		for _, inp := range v.Inputs {
			if inpv, ok := vs.cache[inp]; ok {
				if !inpv.Cached && inpv.Completed == nil {
					inpv.Cached = true
					inpv.Started = v.Started
					inpv.Completed = v.Started
					copy := *inpv
					out = append(out, &copy)
					delete(vs.cache, inp)
				}
			}
		}
	}

	if v.Cached {
		vs.markCached(v.Digest)
	}

	copy := v
	return append(out, &copy)
}

func (vs *vertexStream) markCached(dgst digest.Digest) {
	if v, ok := vs.cache[dgst]; ok {
		if _, ok := vs.wasCached[dgst]; !ok {
			for _, inp := range v.Inputs {
				vs.markCached(inp)
			}
		}
		vs.wasCached[dgst] = struct{}{}
	}
}

// encore returns any vertices that started but never completed (cancelled).
func (vs *vertexStream) encore() []*VertexInfo {
	vs.mu.Lock()
	defer vs.mu.Unlock()

	var out []*VertexInfo
	for _, v := range vs.cache {
		if v.Started != nil && v.Completed == nil {
			now := time.Now()
			v.Completed = &now
			if _, ok := vs.wasCached[v.Digest]; !ok && v.Error == "" {
				v.Error = "context canceled"
			}
			out = append(out, v)
		}
	}
	return out
}

// SortStatus sorts a SolveStatus by timestamps for consistent ordering.
func SortStatus(ss *SolveStatus) {
	sort.Slice(ss.Vertexes, func(i, j int) bool {
		a, b := ss.Vertexes[i], ss.Vertexes[j]
		if a.Started == nil {
			return true
		}
		if b.Started == nil {
			return false
		}
		return a.Started.Before(*b.Started)
	})
	sort.Slice(ss.Statuses, func(i, j int) bool {
		return ss.Statuses[i].Timestamp.Before(ss.Statuses[j].Timestamp)
	})
	sort.Slice(ss.Logs, func(i, j int) bool {
		return ss.Logs[i].Timestamp.Before(ss.Logs[j].Timestamp)
	})
}
