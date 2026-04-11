# policies/source.rego
#
# Source policy evaluated for kind="source".
# Rules are evaluated against data.buildkit.source_rules loaded from data.rego
# (or injected at runtime via OPA storage).
#
# Semantics:
#   - CONVERT short-circuits: first matching CONVERT wins.
#   - ALLOW / DENY: last matching rule wins (later index overrides earlier).
#   - Default (no matches): ALLOW.
#
# Input shape:
#   { "identifier": "docker-image://...", "attrs": {...} }
#
# Output shape (data.buildkit.policy.source.result):
#   {
#     "action":   "ALLOW" | "DENY" | "CONVERT",
#     "messages": ["..."],
#     "updates":  { "identifier": "...", "attrs": {...}, "pattern": "...", ... }
#   }

package buildkit.policy.source

import rego.v1

# ── Default ────────────────────────────────────────────────────────────────────
default result := {"action": "ALLOW", "messages": [], "updates": {}}

# ── Matching helpers ───────────────────────────────────────────────────────────

# identifier_matches handles exact, glob, and regex selectors transparently.
identifier_matches(sel, ident) if { sel == ident }
identifier_matches(sel, ident) if {
	contains(sel, "*")
	glob.match(sel, [], ident)
}
identifier_matches(sel, ident) if {
	not contains(sel, "*")
	sel != ident
	regex.match(sel, ident)
}

# attr_matches returns true when all constraints pass.
attr_matches(constraints, attrs) if { count(constraints) == 0 }
attr_matches(constraints, attrs) if {
	count(constraints) > 0
	every c in constraints { attr_ok(c, attrs) }
}

attr_ok(c, attrs) if { c.condition == "EQUAL";    attrs[c.key] == c.value }
attr_ok(c, attrs) if { c.condition == "NOTEQUAL"; attrs[c.key] != c.value }
attr_ok(c, attrs) if { c.condition == "MATCHES";  regex.match(c.value, attrs[c.key]) }
# proto zero-value: EQUAL by default
attr_ok(c, attrs) if { not c.condition; attrs[c.key] == c.value }

# ── Rule indexing ─────────────────────────────────────────────────────────────

# matching_rules: set of indices of rules that match the current input.
matching_rules[i] if {
	rule := data.buildkit.source_rules[i]
	identifier_matches(rule.selector.identifier, input.identifier)
	attr_matches(
		object.get(rule.selector, "constraints", []),
		object.get(input, "attrs", {}),
	)
}

# ── CONVERT: first matching wins ───────────────────────────────────────────────

convert_indices[i] if {
	matching_rules[i]
	data.buildkit.source_rules[i].action == "CONVERT"
}

first_convert_index := min(convert_indices) if count(convert_indices) > 0

convert_rule := data.buildkit.source_rules[first_convert_index] if {
	defined(first_convert_index)
}

result := {
	"action":   "CONVERT",
	"messages": [],
	"updates":  object.get(convert_rule, "updates", {}),
} if { defined(convert_rule) }

# ── ALLOW / DENY: last matching wins ─────────────────────────────────────────

allow_deny_indices[i] if {
	matching_rules[i]
	action := data.buildkit.source_rules[i].action
	action == "ALLOW"
}
allow_deny_indices[i] if {
	matching_rules[i]
	action := data.buildkit.source_rules[i].action
	action == "DENY"
}

last_allow_deny_index := max(allow_deny_indices) if count(allow_deny_indices) > 0

last_allow_deny_action := data.buildkit.source_rules[last_allow_deny_index].action if {
	defined(last_allow_deny_index)
}

# DENY result (no CONVERT matched)
result := {
	"action":   "DENY",
	"messages": deny_messages,
	"updates":  {},
} if {
	not defined(convert_rule)
	last_allow_deny_action == "DENY"
}

deny_messages := [m |
	some i
	matching_rules[i]
	data.buildkit.source_rules[i].action == "DENY"
	m := sprintf("source %q denied by rule index %d", [input.identifier, i])
]

# ALLOW result (explicit ALLOW was last)
result := {
	"action":   "ALLOW",
	"messages": [],
	"updates":  {},
} if {
	not defined(convert_rule)
	last_allow_deny_action == "ALLOW"
}

# ── defined helper ─────────────────────────────────────────────────────────────
defined(x) if { x != null }
