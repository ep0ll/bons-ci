# policies/dag.rego
#
# DAG expansion policy evaluated for kind="dag".
# Rules are loaded from data.buildkit.dag_rules (see data.rego).
#
# Input shape:
#   {
#     "op": { "id": "...", "type": "source"|"exec"|..., "identifier": "...",
#             "attrs": {...}, "inputs": [...] },
#     "ancestors": ["parentId", ...],
#     "labels":    { "key": "value" }
#   }
#
# Output shape (data.buildkit.policy.dag.result):
#   {
#     "action":     "ALLOW" | "EXPAND",
#     "messages":   [...],
#     "updates":    {},
#     "expansions": [ { "id": "...", "type": "...", "identifier": "...",
#                       "attrs": {...}, "depends_on": [...] } ]
#   }

package buildkit.policy.dag

import rego.v1

default result := {"action": "ALLOW", "messages": [], "updates": {}, "expansions": []}

# ── Selector matching ──────────────────────────────────────────────────────────

op_matches(selector, op) if {
	selector.op_type != ""
	selector.op_type == op.type
}

op_matches(selector, op) if {
	object.get(selector, "op_type", "") == ""
	object.get(selector, "identifier_prefix", "") == ""
	object.get(selector, "label_key", "") == ""
	# Wildcard: no constraints → matches everything.
}

op_matches(selector, op) if {
	prefix := object.get(selector, "identifier_prefix", "")
	prefix != ""
	startswith(op.identifier, prefix)
}

op_matches(selector, op) if {
	label_key := object.get(selector, "label_key", "")
	label_key != ""
	label_val := object.get(selector, "label_value", "")
	input.labels[label_key] == label_val
}

op_matches(selector, op) if {
	ident_glob := object.get(selector, "identifier_glob", "")
	ident_glob != ""
	glob.match(ident_glob, [], op.identifier)
}

# ── Already-expanded guard ────────────────────────────────────────────────────

# Prevent re-expansion when an ancestor already produced this op's expansion.
already_expanded if {
	some anc in input.ancestors
	suffix := concat("", [input.op.id, "-"])
	startswith(anc, suffix)
}

# ── Matching rules ─────────────────────────────────────────────────────────────

matching_rules[i] if {
	rule := data.buildkit.dag_rules[i]
	op_matches(rule.selector, input.op)
}

# ── Node construction ─────────────────────────────────────────────────────────

# Build an ExpandedNode from a rule template and the current op.
build_node(template, rule_idx) := node if {
	raw_id := object.get(template, "id_suffix", concat("-expanded-", ["", format_int(rule_idx, 10)]))
	node_id := concat("", [input.op.id, raw_id])

	raw_ident := object.get(template, "identifier", "")
	node_ident := interpolate(raw_ident, input.op)

	base_attrs  := object.get(input.op, "attrs", {})
	tmpl_attrs  := object.get(template, "attrs", {})
	merged_attrs := object.union(base_attrs, tmpl_attrs)

	extra_deps := object.get(template, "extra_deps", [])
	all_deps   := array.concat([input.op.id], extra_deps)

	node := {
		"id":         node_id,
		"type":       object.get(template, "type", input.op.type),
		"identifier": node_ident,
		"attrs":      merged_attrs,
		"depends_on": all_deps,
	}
}

# String interpolation for identifier templates.
interpolate(tmpl, op) := result if {
	s1 := replace(tmpl, "${identifier}", object.get(op, "identifier", ""))
	s2 := replace(s1,   "${type}",       object.get(op, "type",       ""))
	result := replace(s2, "${id}",       object.get(op, "id",         ""))
}

# ── All expansion nodes ───────────────────────────────────────────────────────

all_nodes := [node |
	some i
	matching_rules[i]
	rule := data.buildkit.dag_rules[i]
	node := build_node(rule.expand_template, i)
]

# ── Result assembly ───────────────────────────────────────────────────────────

result := {
	"action":   "EXPAND",
	"messages": expand_messages,
	"updates":  {},
	"expansions": all_nodes,
} if {
	count(matching_rules) > 0
	count(all_nodes) > 0
	not already_expanded
}

expand_messages := [m |
	some i
	matching_rules[i]
	m := sprintf("op %q (type=%q) expanded by dag rule %d", [input.op.id, input.op.type, i])
]

# Guard: skip if already expanded.
result := {
	"action":   "ALLOW",
	"messages": ["skipped: already expanded"],
	"updates":  {},
	"expansions": [],
} if { already_expanded }
