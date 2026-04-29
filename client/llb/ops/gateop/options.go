package gateop

import (
	"time"

	"github.com/bons/bons-ci/client/llb"
)

// GateOption configures a GateOp.
type GateOption func(*GateOp)

// WithGateID sets the gate identifier.
func WithGateID(id string) GateOption {
	return func(g *GateOp) { g.info.ID = id }
}

// WithDescription sets the human-readable description.
func WithDescription(desc string) GateOption {
	return func(g *GateOp) { g.info.Description = desc }
}

// WithRequestor sets who requested the gate.
func WithRequestor(r string) GateOption {
	return func(g *GateOp) { g.info.Requestor = r }
}

// WithTimeout sets the maximum wait duration.
func WithTimeout(d time.Duration) GateOption {
	return func(g *GateOp) { g.info.Timeout = d }
}

// WithApprover sets the approval strategy.
func WithApprover(a Approver) GateOption {
	return func(g *GateOp) { g.approver = a }
}

// WithGateMetadata adds key-value metadata to the gate.
func WithGateMetadata(key, value string) GateOption {
	return func(g *GateOp) {
		if g.info.Metadata == nil {
			g.info.Metadata = make(map[string]string)
		}
		g.info.Metadata[key] = value
	}
}

// WithGateConstraints applies constraints to the gate operation.
func WithGateConstraints(co llb.ConstraintsOpt) GateOption {
	return func(g *GateOp) { co.SetConstraintsOption(&g.constraints) }
}
