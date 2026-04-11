# policies/matrix.rego
#
# Matrix build expansion policy evaluated for kind="matrix".
# Computes the cartesian product, applies exclusions and inclusions,
# and returns fully typed BuildConfig objects.
#
# Input shape:
#   {
#     "strategy": {
#       "matrix":       { "os": ["linux","windows"], "arch": ["amd64","arm64"] },
#       "include":      [ {"os":"linux","arch":"amd64","runner":"ubuntu-22.04"} ],
#       "exclude":      [ {"os":"windows","arch":"arm64"} ],
#       "max_parallel": 4,
#       "fail_fast":    true
#     },
#     "base_op": { ... },
#     "labels":  { ... }
#   }
#
# Output shape (data.buildkit.policy.matrix.result):
#   {
#     "action":   "ALLOW" | "DENY" | "EXPAND",
#     "messages": [...],
#     "updates":  { "max_parallel": N, "fail_fast": true|false },
#     "expansions": [ { "id": "...", "vars": {...} } ]
#   }
#
# When the input matrix is empty, ALLOW is returned (Go-side Expand() handles it).
# When all combinations are excluded, DENY is returned with an explanatory message.

package buildkit.policy.matrix

import rego.v1

default result := {"action": "ALLOW", "messages": [], "updates": {}, "expansions": []}

# ── Validation ────────────────────────────────────────────────────────────────

has_axes if { count(input.strategy.matrix) > 0 }

# ── Helpers ───────────────────────────────────────────────────────────────────

# subset_of(sub, super) is true when every k-v in sub is present in super.
subset_of(sub, super) if {
	every k, v in sub { super[k] == v }
}

is_excluded(combo) if {
	some exc in input.strategy.exclude
	subset_of(exc, combo)
}

# ── Cartesian product ─────────────────────────────────────────────────────────
# OPA set comprehension over simultaneous enumeration.

# Collect one value per axis to form a single combination.
# The full product is the set of all such combinations that cover every axis.
raw_combinations contains combo if {
	# For each axis, pick exactly one value.
	combo := {axis: val |
		some axis
		axis_vals := input.strategy.matrix[axis]
		some val in axis_vals
	}
	# Require the combo covers ALL axes (complete cartesian product row).
	count(combo) == count(input.strategy.matrix)
}

# Apply exclusions.
filtered_combinations contains combo if {
	combo := raw_combinations[_]
	not is_excluded(combo)
}

# ── Include merging ───────────────────────────────────────────────────────────

# Extra keys injected into an existing combo from matching includes.
extra_for_combo(combo) := extras if {
	extras := {k: v |
		some inc in input.strategy.include
		subset_of(combo, inc)   # include overlaps this combo
		some k, v in inc
		not combo[k]            # only keys not already in combo
	}
}

# ── Standalone includes ───────────────────────────────────────────────────────

standalone_includes contains inc if {
	some inc in input.strategy.include
	# No existing filtered combo is subsumed by this include.
	not any_filtered_subset_of(inc)
}

any_filtered_subset_of(inc) if {
	some combo in filtered_combinations
	subset_of(combo, inc)
}

# ── ID generation ─────────────────────────────────────────────────────────────

combo_id(combo) := id if {
	sorted_vals := [v |
		some k in sort(object.keys(combo))
		v := combo[k]
	]
	id := concat("-", sorted_vals)
}

# ── Build config construction ─────────────────────────────────────────────────

matrix_configs := configs if {
	from_combos := [cfg |
		some combo in filtered_combinations
		extras := extra_for_combo(combo)
		cfg := {
			"id":    combo_id(combo),
			"vars":  combo,
			"extra": extras,
		}
	]
	from_includes := [cfg |
		some inc in standalone_includes
		cfg := {
			"id":   combo_id(inc),
			"vars": inc,
		}
	]
	configs := array.concat(from_combos, from_includes)
}

# ── Result assembly ───────────────────────────────────────────────────────────

# Valid expansion.
result := {
	"action":   "EXPAND",
	"messages": [sprintf("matrix expanded to %d configurations", [count(matrix_configs)])],
	"updates": {
		"max_parallel": input.strategy.max_parallel,
		"fail_fast":    input.strategy.fail_fast,
	},
	"expansions": matrix_configs,
} if {
	has_axes
	count(matrix_configs) > 0
}

# All combinations excluded.
result := {
	"action":   "DENY",
	"messages": ["matrix expansion produced no configurations: all combinations excluded"],
	"updates":  {},
	"expansions": [],
} if {
	has_axes
	count(matrix_configs) == 0
}
