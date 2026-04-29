// Package solveop implements the SolveOp vertex — a nested LLB solver that
// restricts access to secrets, SSH agents, and environment variables. Only
// explicitly bound resources are available inside the nested solve scope.
package solveop

import (
	"context"
	"sort"
	"strings"

	"github.com/bons/bons-ci/client/llb"
	"github.com/moby/buildkit/solver/pb"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
)

// ─────────────────────────────────────────────────────────────────────────────
// SolveOp
// ─────────────────────────────────────────────────────────────────────────────

// SolveOp is a vertex that solves a nested LLB definition within a scoped
// execution environment. Unlike a plain BuildOp, SolveOp restricts resource
// access: only explicitly passed secrets, SSH agents, environment variables,
// and local sources are visible inside the inner solve.
//
// This enables multi-tenant and least-privilege build pipelines where
// different pipeline stages see only their authorized resources.
type SolveOp struct {
	cache       llb.MarshalCache
	source      llb.Output
	info        *SolveInfo
	constraints llb.Constraints
	output      llb.Output
}

var _ llb.Vertex = (*SolveOp)(nil)

// SolveInfo holds the configuration for a nested solve.
type SolveInfo struct {
	Secrets       []SecretBinding
	SSHBindings   []SSHBinding
	Env           map[string]string
	Frontend      string
	FrontendAttrs map[string]string
	CacheImports  []CacheConfig
	CacheExports  []CacheConfig
	LocalSources  map[string]string
	OCISources    map[string]string
}

// SecretBinding maps a secret ID to its value source for the inner solve.
type SecretBinding struct {
	ID    string
	Value string // literal value or file path
	IsEnv bool   // mount as environment variable instead of file
}

// SSHBinding configures SSH agent forwarding for the inner solve.
type SSHBinding struct {
	ID     string
	Socket string // Unix socket path or empty for default agent
}

// CacheConfig describes a cache import or export configuration.
type CacheConfig struct {
	Type  string            // "registry", "local", "s3", "gha", etc.
	Attrs map[string]string // type-specific attributes
}

// NewSolveOp creates a SolveOp from a source output (the inner LLB definition).
func NewSolveOp(source llb.Output, opts ...SolveOption) *SolveOp {
	info := &SolveInfo{
		Env:           make(map[string]string),
		FrontendAttrs: make(map[string]string),
		LocalSources:  make(map[string]string),
		OCISources:    make(map[string]string),
	}
	for _, o := range opts {
		o(info)
	}

	op := &SolveOp{
		source: source,
		info:   info,
	}
	op.output = llb.NewOutput(op)
	return op
}

// Validate checks the solve op for correctness.
func (s *SolveOp) Validate(_ context.Context, _ *llb.Constraints) error {
	if s.source == nil {
		return errors.New("solve op requires a source (inner LLB definition)")
	}
	return nil
}

// Marshal serializes the SolveOp. It uses pb.BuildOp as the wire format,
// extended with custom attributes to carry the scoped bindings.
func (s *SolveOp) Marshal(ctx context.Context, constraints *llb.Constraints) (digest.Digest, []byte, *pb.OpMetadata, []*llb.SourceLocation, error) {
	cache := s.cache.Acquire()
	defer cache.Release()

	if dgst, dt, md, srcs, err := cache.Load(constraints); err == nil {
		return dgst, dt, md, srcs, nil
	}

	if err := s.Validate(ctx, constraints); err != nil {
		return "", nil, nil, nil, err
	}

	pop, md := llb.MarshalConstraints(constraints, &s.constraints)

	pbo := &pb.BuildOp{
		Builder: int64(pb.LLBBuilder),
		Inputs: map[string]*pb.BuildInput{
			pb.LLBDefinitionInput: {Input: 0},
		},
		Attrs: s.buildAttrs(),
	}

	pop.Op = &pb.Op_Build{Build: pbo}

	inp, err := s.source.ToInput(ctx, constraints)
	if err != nil {
		return "", nil, nil, nil, err
	}
	pop.Inputs = append(pop.Inputs, inp)

	dt, err := llb.DeterministicMarshal(pop)
	if err != nil {
		return "", nil, nil, nil, err
	}

	return cache.Store(dt, md, s.constraints.SourceLocations, constraints)
}

// buildAttrs encodes the solve scope configuration into deterministic string
// attributes for wire transmission.
func (s *SolveOp) buildAttrs() map[string]string {
	attrs := make(map[string]string)

	// Secrets.
	if len(s.info.Secrets) > 0 {
		var parts []string
		for _, sec := range s.info.Secrets {
			mode := "file"
			if sec.IsEnv {
				mode = "env"
			}
			parts = append(parts, sec.ID+"="+sec.Value+":"+mode)
		}
		sort.Strings(parts)
		attrs["solve.secrets"] = strings.Join(parts, ";")
	}

	// SSH bindings.
	if len(s.info.SSHBindings) > 0 {
		var parts []string
		for _, ssh := range s.info.SSHBindings {
			parts = append(parts, ssh.ID+"="+ssh.Socket)
		}
		sort.Strings(parts)
		attrs["solve.ssh"] = strings.Join(parts, ";")
	}

	// Environment.
	if len(s.info.Env) > 0 {
		var parts []string
		for k, v := range s.info.Env {
			parts = append(parts, k+"="+v)
		}
		sort.Strings(parts)
		attrs["solve.env"] = strings.Join(parts, ";")
	}

	// Frontend.
	if s.info.Frontend != "" {
		attrs["solve.frontend"] = s.info.Frontend
	}
	for k, v := range s.info.FrontendAttrs {
		attrs["solve.frontend."+k] = v
	}

	// Cache.
	for i, ci := range s.info.CacheImports {
		prefix := "solve.cache.import." + strings.Repeat("0", 3-len(string(rune(i+'0')))) + string(rune(i+'0'))
		attrs[prefix+".type"] = ci.Type
		for k, v := range ci.Attrs {
			attrs[prefix+"."+k] = v
		}
	}
	for i, ce := range s.info.CacheExports {
		prefix := "solve.cache.export." + strings.Repeat("0", 3-len(string(rune(i+'0')))) + string(rune(i+'0'))
		attrs[prefix+".type"] = ce.Type
		for k, v := range ce.Attrs {
			attrs[prefix+"."+k] = v
		}
	}

	// Local sources.
	for k, v := range s.info.LocalSources {
		attrs["solve.local."+k] = v
	}
	for k, v := range s.info.OCISources {
		attrs["solve.oci."+k] = v
	}

	return attrs
}

// Output returns the single output.
func (s *SolveOp) Output() llb.Output { return s.output }

// Inputs returns the inner definition source.
func (s *SolveOp) Inputs() []llb.Output {
	if s.source == nil {
		return nil
	}
	return []llb.Output{s.source}
}

// ─────────────────────────────────────────────────────────────────────────────
// Convenience constructor
// ─────────────────────────────────────────────────────────────────────────────

// Solve creates a State that solves the given inner state within a scoped
// environment.
func Solve(inner llb.State, opts ...SolveOption) llb.State {
	if inner.Output() == nil {
		return llb.Scratch()
	}
	return llb.NewState(NewSolveOp(inner.Output(), opts...).Output())
}
