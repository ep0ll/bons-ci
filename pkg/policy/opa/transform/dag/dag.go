// Package dag implements DAG tree expansion transforms.
// When OPA returns action=EXPAND for kind="dag", the transformer interprets
// the decision's Expansions slice to generate new build-graph nodes.
//
// Architecture:
//   - DAGInput is the JSON-serialisable OPA input (no pb dependency).
//   - ExpanderTransformer parses Expansions into typed ExpandedNodes.
//   - Walker provides depth-first traversal of a raw definition for callers
//     that need to feed each op through the engine one at a time.
package dag

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	polOtel "github.com/bons/bons-ci/pkg/policy/opa/internal/otel"
	"github.com/bons/bons-ci/pkg/policy/opa/transform"
)

// ─── Input / Output shapes ────────────────────────────────────────────────────

// OpType is the type of a build graph operation.
type OpType string

const (
	OpTypeSource OpType = "source"
	OpTypeExec   OpType = "exec"
	OpTypeFile   OpType = "file"
	OpTypeMerge  OpType = "merge"
	OpTypeDiff   OpType = "diff"
)

// OpDescriptor is a JSON-serialisable description of a build graph op.
// It is designed to be constructed without importing solver/pb.
type OpDescriptor struct {
	ID         string            `json:"id"`
	Type       string            `json:"type"`
	Identifier string            `json:"identifier,omitempty"`
	Attrs      map[string]string `json:"attrs,omitempty"`
	Inputs     []string          `json:"inputs,omitempty"`
}

// DAGInput is the typed OPA input for kind="dag".
type DAGInput struct {
	Op        *OpDescriptor     `json:"op"`
	Ancestors []string          `json:"ancestors,omitempty"`
	Labels    map[string]string `json:"labels,omitempty"`
}

// ExpandedNode is a new build-graph node produced by expansion.
type ExpandedNode struct {
	ID         string            `json:"id"`
	Type       string            `json:"type"`
	Identifier string            `json:"identifier,omitempty"`
	Attrs      map[string]string `json:"attrs,omitempty"`
	DependsOn  []string          `json:"depends_on,omitempty"`
}

// ExpansionKey is the key under which ExpanderTransformer stores results
// in Decision.Updates. Callers retrieve the slice via this constant.
const ExpansionKey = "dag_expanded_nodes"

// ─── ExpanderTransformer ──────────────────────────────────────────────────────

// ExpanderTransformer converts OPA EXPAND decisions into typed ExpandedNodes
// and writes them into Decision.Updates[ExpansionKey].
type ExpanderTransformer struct {
	tracer trace.Tracer
}

// NewExpanderTransformer creates an ExpanderTransformer.
func NewExpanderTransformer() *ExpanderTransformer {
	return &ExpanderTransformer{tracer: polOtel.Tracer("dag.expander")}
}

func (t *ExpanderTransformer) Name() string { return "dag.expander" }

func (t *ExpanderTransformer) Apply(ctx context.Context, input any, dec transform.Decision) (transform.Decision, error) {
	if dec.Action != "EXPAND" {
		return dec, nil
	}

	dagInput, ok := input.(DAGInput)
	if !ok {
		return dec, fmt.Errorf("dag.expander: expected DAGInput, got %T", input)
	}

	ctx, span := t.tracer.Start(ctx, polOtel.Namespace+".dag.expand",
		trace.WithAttributes(
			polOtel.AttrOpID.String(safeID(dagInput.Op)),
			polOtel.AttrOpType.String(safeType(dagInput.Op)),
			attribute.Int("expansion_count", len(dec.Expansions)),
		),
	)
	defer span.End()

	if len(dec.Expansions) == 0 {
		return dec, nil
	}

	nodes, err := parseExpansions(dec.Expansions)
	if err != nil {
		polOtel.RecordError(ctx, err)
		return dec, fmt.Errorf("dag.expander: parse expansions: %w", err)
	}

	polOtel.AddEvent(ctx, "dag.nodes_generated",
		attribute.Int("count", len(nodes)),
	)

	if dec.Updates == nil {
		dec.Updates = make(map[string]any)
	}
	dec.Updates[ExpansionKey] = nodes
	dec.Mutated = true
	return dec, nil
}

// ─── Walker ───────────────────────────────────────────────────────────────────

// VisitFunc is called during DAG traversal with each op.
// Return false to prune the subtree rooted at this op.
type VisitFunc func(id string, op *OpDescriptor, ancestors []string) bool

// Walker performs depth-first traversal of a logical DAG represented as a
// slice of OpDescriptors. It is index-based so it never imports solver/pb.
type Walker struct {
	ops     []*OpDescriptor
	byID    map[string]*OpDescriptor
	visited map[string]bool
}

// NewWalker creates a Walker for ops. The last op is treated as the terminal.
func NewWalker(ops []*OpDescriptor) *Walker {
	byID := make(map[string]*OpDescriptor, len(ops))
	for _, op := range ops {
		byID[op.ID] = op
	}
	return &Walker{ops: ops, byID: byID, visited: make(map[string]bool)}
}

// Walk traverses the DAG from the terminal op (last in ops).
func (w *Walker) Walk(visit VisitFunc) {
	if len(w.ops) == 0 {
		return
	}
	terminal := w.ops[len(w.ops)-1]
	w.walk(terminal, nil, visit)
}

func (w *Walker) walk(op *OpDescriptor, ancestors []string, visit VisitFunc) {
	if w.visited[op.ID] {
		return
	}
	w.visited[op.ID] = true

	if !visit(op.ID, op, ancestors) {
		return
	}
	newAncestors := append(ancestors, op.ID) //nolint:gocritic
	for _, inputID := range op.Inputs {
		child, ok := w.byID[inputID]
		if !ok {
			continue
		}
		w.walk(child, newAncestors, visit)
	}
}

// ─── Registration helper ──────────────────────────────────────────────────────

// RegisterAll registers DAG transforms into reg.
func RegisterAll(reg *transform.Registry) {
	reg.Register(transform.Key{Kind: "dag", Action: "EXPAND"},
		NewExpanderTransformer(),
	)
}

// ─── Parsing helpers ──────────────────────────────────────────────────────────

func parseExpansions(raw []map[string]any) ([]ExpandedNode, error) {
	nodes := make([]ExpandedNode, 0, len(raw))
	for i, item := range raw {
		n, err := parseExpandedNode(item)
		if err != nil {
			return nil, fmt.Errorf("expansion[%d]: %w", i, err)
		}
		nodes = append(nodes, n)
	}
	return nodes, nil
}

func parseExpandedNode(m map[string]any) (ExpandedNode, error) {
	n := ExpandedNode{}

	if v, _ := m["id"].(string); v != "" {
		n.ID = v
	} else {
		return n, fmt.Errorf("expansion node missing required field 'id'")
	}
	if v, _ := m["type"].(string); v != "" {
		n.Type = v
	} else {
		return n, fmt.Errorf("expansion node %q missing required field 'type'", n.ID)
	}

	n.Identifier, _ = m["identifier"].(string)

	if rawAttrs, ok := m["attrs"].(map[string]any); ok {
		n.Attrs = make(map[string]string, len(rawAttrs))
		for k, v := range rawAttrs {
			s, ok := v.(string)
			if !ok {
				return n, fmt.Errorf("expansion node %q: attrs[%q] must be string, got %T", n.ID, k, v)
			}
			n.Attrs[k] = s
		}
	}

	if deps, ok := m["depends_on"].([]any); ok {
		for _, d := range deps {
			s, ok := d.(string)
			if !ok {
				return n, fmt.Errorf("expansion node %q: depends_on entry must be string", n.ID)
			}
			n.DependsOn = append(n.DependsOn, s)
		}
	}

	return n, nil
}

func safeID(op *OpDescriptor) string {
	if op == nil {
		return ""
	}
	return op.ID
}

func safeType(op *OpDescriptor) string {
	if op == nil {
		return ""
	}
	return op.Type
}
