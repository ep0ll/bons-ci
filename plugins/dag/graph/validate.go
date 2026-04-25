package graph

import (
	"fmt"
	"strings"

	"github.com/bons/bons-ci/plugins/dag/vertex"
)

type color int

const (
	white color = iota
	gray
	black
)

// detectCycles runs a three-color DFS over the upstream adjacency and returns
// an error if a back-edge (cycle) is found.
func detectCycles(
	vertices map[string]vertex.Vertex,
	upstream map[string][]string,
) error {
	colors := make(map[string]color, len(vertices))
	for id := range vertices {
		colors[id] = white
	}
	var path []string

	var dfs func(id string) error
	dfs = func(id string) error {
		colors[id] = gray
		path = append(path, id)

		for _, inputID := range upstream[id] {
			switch colors[inputID] {
			case gray:
				cycle := buildCyclePath(path, inputID, vertices)
				return fmt.Errorf("graph: cycle detected → %s", cycle)
			case white:
				if err := dfs(inputID); err != nil {
					return err
				}
			}
		}

		path = path[:len(path)-1]
		colors[id] = black
		return nil
	}

	for id := range vertices {
		if colors[id] == white {
			if err := dfs(id); err != nil {
				return err
			}
		}
	}
	return nil
}

func buildCyclePath(path []string, cycleStart string, vertices map[string]vertex.Vertex) string {
	startIdx := -1
	for i, id := range path {
		if id == cycleStart {
			startIdx = i
			break
		}
	}
	if startIdx == -1 {
		return cycleStart + " → (cycle)"
	}
	cycle := path[startIdx:]
	names := make([]string, len(cycle))
	for i, id := range cycle {
		v, ok := vertices[id]
		if !ok {
			n := len(id)
			if n > 12 {
				n = 12
			}
			names[i] = id[:n]
			continue
		}
		if n, ok := v.(vertex.Named); ok {
			names[i] = n.Name()
		} else {
			n := len(id)
			if n > 12 {
				n = 12
			}
			names[i] = string(v.Kind()) + ":" + id[:n]
		}
	}
	names = append(names, names[0])
	return strings.Join(names, " → ")
}
