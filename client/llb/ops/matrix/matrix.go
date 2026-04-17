// Package matrix provides a fan-out vertex that expands a single template
// vertex across a set of parameter maps, producing one Result per
// configuration.
//
// Each configuration is injected as build arguments into a clone of the base
// constraints before the template vertex is marshalled, so every expansion
// produces a distinct content address.
//
// Example
//
//	results, err := matrix.Expand(
//	    ctx,
//	    templateVertex,
//	    baseConstraints,
//	    matrix.NewAxis("GO_VERSION", "1.21", "1.22", "1.23"),
//	    matrix.NewAxis("GOOS",       "linux", "darwin"),
//	)
//	// 6 Results: one per GO_VERSION×GOOS combination
package matrix

import (
	"context"
	"fmt"

	"github.com/bons/bons-ci/client/llb/core"
	"github.com/moby/buildkit/solver/pb"
)

// ─── Axis ─────────────────────────────────────────────────────────────────────

// Axis is one dimension of the parameter matrix.
type Axis struct {
	Key    string   // build-arg name
	Values []string // possible values
}

// NewAxis creates an Axis.
func NewAxis(key string, values ...string) Axis {
	return Axis{Key: key, Values: values}
}

// ─── ParamMap ─────────────────────────────────────────────────────────────────

// ParamMap is one concrete configuration: build-arg key → value.
type ParamMap = map[string]string

// ─── Result ───────────────────────────────────────────────────────────────────

// Result is one row of the matrix expansion.
type Result struct {
	// Params is the build-arg map for this configuration.
	Params ParamMap
	// Digest is the content address of templateVertex under these params.
	Digest core.VertexID
	// Constraints are the per-config constraints used for this expansion.
	Constraints *core.Constraints
	// Output produces this expansion's result when marshalled.
	Output core.Output
}

// ─── Expand ───────────────────────────────────────────────────────────────────

// Expand fans out templateVertex over the cartesian product of the provided
// axes, returning one Result per configuration.
//
// An error is returned if any expansion fails validation or serialisation.
// The remaining expansions are still returned up to the failing one.
func Expand(
	ctx context.Context,
	templateVertex core.Vertex,
	base *core.Constraints,
	axes ...Axis,
) ([]Result, error) {
	if templateVertex == nil {
		return nil, fmt.Errorf("matrix.Expand: templateVertex must not be nil")
	}
	configs := cartesian(axes)
	if len(configs) == 0 {
		configs = []ParamMap{{}} // single expansion with no extra args
	}
	return expandConfigs(ctx, templateVertex, base, configs)
}

// ExplicitExpand fans out using a caller-supplied list of param maps instead of
// an axis cartesian product. Useful when combinations are non-rectangular.
func ExplicitExpand(
	ctx context.Context,
	templateVertex core.Vertex,
	base *core.Constraints,
	configs []ParamMap,
) ([]Result, error) {
	if templateVertex == nil {
		return nil, fmt.Errorf("matrix.ExplicitExpand: templateVertex must not be nil")
	}
	return expandConfigs(ctx, templateVertex, base, configs)
}

func expandConfigs(
	ctx context.Context,
	templateVertex core.Vertex,
	base *core.Constraints,
	configs []ParamMap,
) ([]Result, error) {
	results := make([]Result, 0, len(configs))
	for _, params := range configs {
		c := base.Clone()
		for k, v := range params {
			c.BuildArgs[k] = v
		}
		if err := templateVertex.Validate(ctx, c); err != nil {
			return results, fmt.Errorf("matrix.Expand config %v: validate: %w", params, err)
		}
		mv, err := templateVertex.Marshal(ctx, c)
		if err != nil {
			return results, fmt.Errorf("matrix.Expand config %v: marshal: %w", params, err)
		}
		results = append(results, Result{
			Params:      params,
			Digest:      mv.Digest,
			Constraints: c,
			Output:      &configOutput{vertex: templateVertex, c: c},
		})
	}
	return results, nil
}

// ─── cartesian ────────────────────────────────────────────────────────────────

func cartesian(axes []Axis) []ParamMap {
	if len(axes) == 0 {
		return nil
	}
	result := []ParamMap{{}}
	for _, axis := range axes {
		if len(axis.Values) == 0 {
			continue
		}
		var next []ParamMap
		for _, existing := range result {
			for _, val := range axis.Values {
				m := make(ParamMap, len(existing)+1)
				for k, v := range existing {
					m[k] = v
				}
				m[axis.Key] = val
				next = append(next, m)
			}
		}
		result = next
	}
	return result
}

// ─── configOutput ─────────────────────────────────────────────────────────────

// configOutput is a core.Output that pins a vertex to fixed constraints.
type configOutput struct {
	vertex core.Vertex
	c      *core.Constraints
}

func (o *configOutput) Vertex(_ context.Context, _ *core.Constraints) core.Vertex {
	return o.vertex
}

func (o *configOutput) ToInput(ctx context.Context, _ *core.Constraints) (*pb.Input, error) {
	mv, err := o.vertex.Marshal(ctx, o.c)
	if err != nil {
		return nil, err
	}
	return &pb.Input{Digest: string(mv.Digest), Index: 0}, nil
}
