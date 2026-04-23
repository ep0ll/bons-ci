// Package selector provides a label-based dynamic selection vertex that picks
// the best-matching candidate output from a labelled set, based on build
// constraints.
//
// Unlike conditional.Vertex (which evaluates a predicate) the selector ranks
// all candidates by label match score and returns the highest-scoring one. This
// makes it suitable for "platform dispatch" scenarios where multiple similar
// base images are available and the best one should be chosen automatically.
//
// Example – choose the best pre-built cache image
//
//	sel, _ := selector.New(
//	    selector.WithCandidate(linuxAmd64Cache.Output(),  core.Labels{"os":"linux","arch":"amd64"}),
//	    selector.WithCandidate(linuxArm64Cache.Output(),  core.Labels{"os":"linux","arch":"arm64"}),
//	    selector.WithCandidate(windowsAmd64Cache.Output(),core.Labels{"os":"windows","arch":"amd64"}),
//	    selector.WithRequired(core.Labels{"os":"linux"}),  // must match "os"
//	    selector.WithPrefer(core.Labels{"arch":"amd64"}),  // prefer amd64 when possible
//	)
package selector

import (
	"context"
	"fmt"

	"github.com/bons/bons-ci/client/llb/core"
	"github.com/bons/bons-ci/client/llb/marshal"
	"github.com/moby/buildkit/solver/pb"
)

// ─── Candidate ────────────────────────────────────────────────────────────────

// Candidate is one option that the selector may choose from.
type Candidate struct {
	Output core.Output
	Labels core.Labels
}

// ─── ScoreFunc ────────────────────────────────────────────────────────────────

// ScoreFunc computes a numeric affinity score for a candidate given the build
// constraints. Higher scores win. Returning a score < 0 disqualifies the
// candidate entirely.
type ScoreFunc func(candidate Candidate, c *core.Constraints) int

// DefaultScoreFunc scores candidates by how many of their labels match the
// platform fields in c. Unmatched labels are neutral (score unchanged).
func DefaultScoreFunc(candidate Candidate, c *core.Constraints) int {
	score := 0
	for k, v := range candidate.Labels {
		if c.Platform == nil {
			continue
		}
		switch k {
		case "os":
			if c.Platform.OS == v {
				score += 10
			}
		case "arch", "architecture":
			if c.Platform.Architecture == v {
				score += 10
			}
		case "variant":
			if c.Platform.Variant == v {
				score += 5
			}
		}
	}
	return score
}

// BuildArgScoreFunc scores candidates by matching their labels against build
// arguments in the constraints. Use alongside DefaultScoreFunc.
func BuildArgScoreFunc(candidate Candidate, c *core.Constraints) int {
	score := 0
	for k, v := range candidate.Labels {
		if bv, ok := c.BuildArg(k); ok && bv == v {
			score += 10
		}
	}
	return score
}

// CombineScoreFuncs sums multiple ScoreFuncs.
func CombineScoreFuncs(fns ...ScoreFunc) ScoreFunc {
	return func(candidate Candidate, c *core.Constraints) int {
		total := 0
		for _, fn := range fns {
			s := fn(candidate, c)
			if s < 0 {
				return -1 // disqualified
			}
			total += s
		}
		return total
	}
}

// ─── Config ──────────────────────────────────────────────────────────────────

// Config holds all parameters for the selector vertex.
type Config struct {
	// Candidates is the labelled set of possible outputs.
	Candidates []Candidate
	// ScoreFunc ranks candidates. Defaults to DefaultScoreFunc.
	ScoreFunc ScoreFunc
	// Required are labels that every candidate must match to be considered.
	// Candidates not matching all required labels receive score -1.
	Required core.Labels
	// Prefer are labels that boost a candidate's score when matched.
	// Unlike Required, not matching Prefer does not disqualify a candidate.
	Prefer core.Labels
	// Fallback is the output used when no candidate qualifies.
	// Nil = scratch.
	Fallback    core.Output
	Constraints core.Constraints
}

// Option is a functional option for Config.
type Option func(*Config)

func WithCandidate(out core.Output, labels core.Labels) Option {
	return func(c *Config) {
		c.Candidates = append(c.Candidates, Candidate{Output: out, Labels: labels})
	}
}
func WithScoreFunc(fn ScoreFunc) Option      { return func(c *Config) { c.ScoreFunc = fn } }
func WithRequired(labels core.Labels) Option { return func(c *Config) { c.Required = labels } }
func WithPrefer(labels core.Labels) Option   { return func(c *Config) { c.Prefer = labels } }
func WithFallback(out core.Output) Option    { return func(c *Config) { c.Fallback = out } }
func WithConstraintsOption(opt core.ConstraintsOption) Option {
	return func(c *Config) { opt(&c.Constraints) }
}

// ─── Vertex ──────────────────────────────────────────────────────────────────

// Vertex is the selector op. At marshal time it scores each candidate against
// the build constraints, picks the highest-scoring qualified candidate, and
// returns that candidate's MarshaledVertex transparently.
type Vertex struct {
	config Config
	cache  marshal.Cache
}

// New constructs a selector vertex.
func New(opts ...Option) (*Vertex, error) {
	cfg := Config{ScoreFunc: DefaultScoreFunc}
	for _, o := range opts {
		o(&cfg)
	}
	if len(cfg.Candidates) == 0 {
		return nil, fmt.Errorf("selector.New: at least one Candidate is required")
	}
	return &Vertex{config: cfg}, nil
}

// ─── core.Vertex ─────────────────────────────────────────────────────────────

func (v *Vertex) Type() core.VertexType { return core.VertexTypeSelector }

func (v *Vertex) Inputs() []core.Edge {
	edges := make([]core.Edge, 0, len(v.config.Candidates)+1)
	for _, c := range v.config.Candidates {
		if c.Output != nil {
			edges = append(edges, core.Edge{
				Vertex: c.Output.Vertex(context.Background(), nil), Index: 0,
			})
		}
	}
	if v.config.Fallback != nil {
		edges = append(edges, core.Edge{
			Vertex: v.config.Fallback.Vertex(context.Background(), nil), Index: 0,
		})
	}
	return edges
}

func (v *Vertex) Outputs() []core.OutputSlot {
	return []core.OutputSlot{{Index: 0, Description: "best-matching candidate"}}
}

func (v *Vertex) Validate(_ context.Context, _ *core.Constraints) error {
	if len(v.config.Candidates) == 0 {
		return &core.ValidationError{Field: "Candidates", Cause: fmt.Errorf("must not be empty")}
	}
	return nil
}

// Marshal scores all candidates and returns the winner's MarshaledVertex.
func (v *Vertex) Marshal(ctx context.Context, c *core.Constraints) (*core.MarshaledVertex, error) {
	h := v.cache.Acquire()
	defer h.Release()
	if dgst, bytes, meta, srcs, err := h.Load(c); err == nil {
		return &core.MarshaledVertex{Digest: dgst, Bytes: bytes, Metadata: meta, SourceLocations: srcs}, nil
	}

	winner, err := v.selectCandidate(c)
	if err != nil {
		if v.config.Fallback == nil {
			return nil, err
		}
		// Use fallback.
		fbVtx := v.config.Fallback.Vertex(ctx, c)
		if fbVtx == nil {
			return nil, err
		}
		mv, merr := fbVtx.Marshal(ctx, c)
		if merr != nil {
			return nil, fmt.Errorf("selector.Marshal fallback: %w", merr)
		}
		dgst, bytes, meta, srcs, _ := h.Store(mv.Bytes, mv.Metadata, mv.SourceLocations, c)
		return &core.MarshaledVertex{Digest: dgst, Bytes: bytes, Metadata: meta, SourceLocations: srcs}, nil
	}

	winnerVtx := winner.Output.Vertex(ctx, c)
	if winnerVtx == nil {
		return nil, fmt.Errorf("selector.Marshal: winning candidate has nil vertex")
	}
	mv, err := winnerVtx.Marshal(ctx, c)
	if err != nil {
		return nil, fmt.Errorf("selector.Marshal winner: %w", err)
	}
	dgst, bytes, meta, srcs, _ := h.Store(mv.Bytes, mv.Metadata, mv.SourceLocations, c)
	return &core.MarshaledVertex{Digest: dgst, Bytes: bytes, Metadata: meta, SourceLocations: srcs}, nil
}

// selectCandidate returns the highest-scoring qualified candidate.
func (v *Vertex) selectCandidate(c *core.Constraints) (Candidate, error) {
	scoreFn := v.config.ScoreFunc
	if scoreFn == nil {
		scoreFn = DefaultScoreFunc
	}

	bestScore := -1
	var best *Candidate

	for i := range v.config.Candidates {
		cand := v.config.Candidates[i]

		// Required label filter.
		if len(v.config.Required) > 0 && !cand.Labels.Match(v.config.Required) {
			continue
		}

		score := scoreFn(cand, c)
		if score < 0 {
			continue // disqualified by scoreFunc
		}

		// Preference boost.
		for k, val := range v.config.Prefer {
			if cand.Labels[k] == val {
				score += 5
			}
		}

		if best == nil || score > bestScore {
			bestScore = score
			cp := cand
			best = &cp
		}
	}

	if best == nil {
		req := fmt.Sprintf("required=%v", v.config.Required)
		return Candidate{}, &core.NoMatchError{Criteria: req}
	}
	return *best, nil
}

// SelectNow returns the winning candidate for the given constraints without
// serialising. Useful for inspection and testing.
func (v *Vertex) SelectNow(c *core.Constraints) (Candidate, error) {
	return v.selectCandidate(c)
}

func (v *Vertex) WithInputs(inputs []core.Edge) (core.Vertex, error) {
	// Map edges back onto candidates by position.
	n := len(v.config.Candidates)
	if len(inputs) > n+1 {
		return nil, &core.IncompatibleInputsError{
			VertexType: v.Type(),
			Got:        len(inputs),
			Want:       fmt.Sprintf("at most %d", n+1),
		}
	}
	newCfg := v.config
	newCands := make([]Candidate, len(v.config.Candidates))
	copy(newCands, v.config.Candidates)
	for i, edge := range inputs {
		if i < n {
			newCands[i].Output = &edgeOutput{edge: edge}
		} else {
			newCfg.Fallback = &edgeOutput{edge: edge}
		}
	}
	newCfg.Candidates = newCands
	return &Vertex{config: newCfg}, nil
}

func (v *Vertex) Output() core.Output { return &selectorOutput{v: v} }

type selectorOutput struct{ v *Vertex }

func (o *selectorOutput) Vertex(_ context.Context, _ *core.Constraints) core.Vertex { return o.v }
func (o *selectorOutput) ToInput(ctx context.Context, c *core.Constraints) (*pb.Input, error) {
	mv, err := o.v.Marshal(ctx, c)
	if err != nil {
		return nil, err
	}
	return &pb.Input{Digest: string(mv.Digest), Index: 0}, nil
}

type edgeOutput struct{ edge core.Edge }

func (e *edgeOutput) Vertex(_ context.Context, _ *core.Constraints) core.Vertex { return e.edge.Vertex }
func (e *edgeOutput) ToInput(ctx context.Context, c *core.Constraints) (*pb.Input, error) {
	mv, err := e.edge.Vertex.Marshal(ctx, c)
	if err != nil {
		return nil, err
	}
	return &pb.Input{Digest: string(mv.Digest), Index: int64(e.edge.Index)}, nil
}

var (
	_ core.Vertex         = (*Vertex)(nil)
	_ core.MutatingVertex = (*Vertex)(nil)
)
