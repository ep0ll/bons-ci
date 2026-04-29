// Package gateop implements the GateOp vertex — an approval gate that blocks
// graph execution until an external signal is received. Gates enable human
// approval, webhook-based authorization, and automated approval policies.
package gateop

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/bons/bons-ci/client/llb"
	"github.com/moby/buildkit/solver/pb"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
)

// ─────────────────────────────────────────────────────────────────────────────
// GateOp
// ─────────────────────────────────────────────────────────────────────────────

// GateOp blocks execution of the downstream graph until an Approver signals
// approval. At marshal time, the gate configuration is embedded in the
// definition; at solve time, the solver calls the Approver to wait for the
// signal.
//
// Gates are idempotent: the same gate ID always produces the same digest,
// enabling cache hits for previously-approved gates.
type GateOp struct {
	cache       llb.MarshalCache
	source      llb.Output
	info        GateInfo
	approver    Approver
	constraints llb.Constraints
	output      llb.Output
}

var _ llb.Vertex = (*GateOp)(nil)

// GateInfo describes the gate for display and tracking purposes.
type GateInfo struct {
	ID          string        // Unique identifier for this gate
	Description string        // Human-readable description
	Requestor   string        // Who requested the gate
	Timeout     time.Duration // Maximum wait time (0 = no timeout)
	Metadata    map[string]string
	CreatedAt   time.Time
}

// Approval represents the result of an approval request.
type Approval struct {
	Approved  bool
	Approver  string    // Identity of who approved/denied
	Reason    string    // Optional reason
	Timestamp time.Time // When the decision was made
}

// NewGateOp creates a GateOp.
func NewGateOp(source llb.Output, opts ...GateOption) *GateOp {
	info := GateInfo{
		ID:        generateGateID(),
		Metadata:  make(map[string]string),
		CreatedAt: time.Now(),
	}

	op := &GateOp{
		source:   source,
		info:     info,
		approver: &AutoApprover{},
	}

	for _, o := range opts {
		o(op)
	}

	op.output = llb.NewOutput(op)
	return op
}

// Validate checks the gate op.
func (g *GateOp) Validate(_ context.Context, _ *llb.Constraints) error {
	if g.source == nil {
		return errors.New("gate op requires a source state to gate")
	}
	if g.approver == nil {
		return errors.New("gate op requires an approver")
	}
	if g.info.ID == "" {
		return errors.New("gate op requires an ID")
	}
	return nil
}

// Marshal serializes the gate configuration. The gate uses a source op with
// "gate://" scheme for wire format.
func (g *GateOp) Marshal(ctx context.Context, constraints *llb.Constraints) (digest.Digest, []byte, *pb.OpMetadata, []*llb.SourceLocation, error) {
	cache := g.cache.Acquire()
	defer cache.Release()

	if dgst, dt, md, srcs, err := cache.Load(constraints); err == nil {
		return dgst, dt, md, srcs, nil
	}

	if err := g.Validate(ctx, constraints); err != nil {
		return "", nil, nil, nil, err
	}

	pop, md := llb.MarshalConstraints(constraints, &g.constraints)

	// Add source as input.
	inp, err := g.source.ToInput(ctx, constraints)
	if err != nil {
		return "", nil, nil, nil, err
	}
	pop.Inputs = append(pop.Inputs, inp)

	// Encode gate as a custom source op with metadata.
	attrs := map[string]string{
		"gate.id":          g.info.ID,
		"gate.description": g.info.Description,
		"gate.requestor":   g.info.Requestor,
		"gate.created_at":  g.info.CreatedAt.Format(time.RFC3339),
		"gate.approver":    g.approver.Name(),
	}

	if g.info.Timeout > 0 {
		attrs["gate.timeout"] = g.info.Timeout.String()
	}

	for k, v := range g.info.Metadata {
		attrs["gate.meta."+k] = v
	}

	pop.Op = &pb.Op_Source{Source: &pb.SourceOp{
		Identifier: "gate://" + g.info.ID,
		Attrs:      attrs,
	}}

	dt, err := llb.DeterministicMarshal(pop)
	if err != nil {
		return "", nil, nil, nil, err
	}

	return cache.Store(dt, md, g.constraints.SourceLocations, constraints)
}

// Output returns the gate output.
func (g *GateOp) Output() llb.Output { return g.output }

// Inputs returns the source.
func (g *GateOp) Inputs() []llb.Output {
	if g.source == nil {
		return nil
	}
	return []llb.Output{g.source}
}

// Info returns the gate info.
func (g *GateOp) Info() GateInfo { return g.info }

// Wait calls the approver to wait for approval. This is called at solve time.
func (g *GateOp) Wait(ctx context.Context) (Approval, error) {
	if g.info.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, g.info.Timeout)
		defer cancel()
	}
	return g.approver.WaitForApproval(ctx, g.info)
}

// ─────────────────────────────────────────────────────────────────────────────
// Convenience constructor
// ─────────────────────────────────────────────────────────────────────────────

// Gate creates a State that blocks until approved. Usage:
//
//	gated := Gate(prevState,
//	    WithDescription("Deploy to production?"),
//	    WithApprover(NewWebhookApprover("https://...")),
//	    WithTimeout(30 * time.Minute),
//	)
func Gate(source llb.State, opts ...GateOption) llb.State {
	if source.Output() == nil {
		return llb.Scratch()
	}
	return llb.NewState(NewGateOp(source.Output(), opts...).Output())
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

// generateGateID creates a unique gate ID.
func generateGateID() string {
	h := sha256.Sum256([]byte(time.Now().String()))
	return "gate-" + hex.EncodeToString(h[:8])
}

// Ensure fmt usage.
var _ = fmt.Sprint
