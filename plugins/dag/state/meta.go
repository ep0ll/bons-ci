package state

import (
	"sync"

	"github.com/bons/bons-ci/plugins/dag/ops"
)

// Meta holds the execution context metadata associated with a State.
type Meta struct {
	dir      string
	user     string
	hostname string
	network  ops.NetMode
	platform *ops.Platform
	env      *EnvList
	labels   map[string]string // attached via WithLabel; NOT part of content digest
}

func defaultMeta() *Meta {
	return &Meta{dir: "/", env: &EnvList{}}
}

func (m *Meta) clone() *Meta {
	if m == nil {
		return defaultMeta()
	}
	p := *m
	if m.platform != nil {
		plat := *m.platform
		p.platform = &plat
	}
	// Deep-copy labels so mutations to the clone don't affect the original.
	if m.labels != nil {
		p.labels = make(map[string]string, len(m.labels))
		for k, v := range m.labels {
			p.labels[k] = v
		}
	}
	return &p
}

// EnvList is a persistent, immutable key-value store for environment variables.
// Nodes form a singly-linked list; newer nodes override older ones.
//
// Correctness invariant: Del(k) followed by Set(k,v) on the same chain always
// produces a list where Get(k) returns (v, true) AND ToSlice() includes k=v.
type EnvList struct {
	parent *EnvList
	key    string
	value  string
	del    bool

	once   sync.Once
	values map[string]string
	keys   []string // live keys in first-insertion order
}

func (e *EnvList) Set(key, value string) *EnvList {
	return &EnvList{parent: e, key: key, value: value}
}

func (e *EnvList) Del(key string) *EnvList {
	return &EnvList{parent: e, key: key, del: true}
}

func (e *EnvList) SetDefault(key, value string) *EnvList {
	if _, ok := e.Get(key); ok {
		return e
	}
	return e.Set(key, value)
}

func (e *EnvList) Get(key string) (string, bool) {
	e.materialise()
	v, ok := e.values[key]
	return v, ok
}

func (e *EnvList) ToSlice() []string {
	e.materialise()
	out := make([]string, 0, len(e.keys))
	for _, k := range e.keys {
		out = append(out, k+"="+e.values[k])
	}
	return out
}

func (e *EnvList) Len() int {
	e.materialise()
	return len(e.values)
}

// materialise flattens the linked list into maps exactly once.
//
// BUG FIX: the previous implementation used a single `seen` map that
// conflated two concerns: (a) whether a key already has a slot in `keys`
// and (b) whether a key was deleted. This caused Del(k)→Set(k,v) to leave
// k absent from ToSlice() even though Get(k) returned the correct value.
//
// Fix: use `inKeys` only for orderedKeys tracking; handle deletions
// purely through the `keys` slice and value map.
func (e *EnvList) materialise() {
	e.once.Do(func() {
		m := make(map[string]string)
		inKeys := make(map[string]bool) // tracks slot presence in keys slice
		var keys []string

		var walk func(n *EnvList)
		walk = func(n *EnvList) {
			if n == nil {
				return
			}
			walk(n.parent) // process oldest → newest
			if n.key == "" {
				return
			}
			if n.del {
				delete(m, n.key)
				if inKeys[n.key] {
					for i, k := range keys {
						if k == n.key {
							keys = append(keys[:i], keys[i+1:]...)
							break
						}
					}
					inKeys[n.key] = false
				}
			} else {
				m[n.key] = n.value
				if !inKeys[n.key] {
					keys = append(keys, n.key)
					inKeys[n.key] = true
				}
				// key already in slice: just update value in map (position preserved)
			}
		}
		walk(e)

		e.values = m
		e.keys = keys
	})
}
