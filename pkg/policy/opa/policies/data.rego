# policies/data.rego
#
# Default rule data. In production, replace these with rules loaded from your
# config store and injected via OPA storage API or bundle data.json.
# The engine calls Build() / Watch() on a bundle.Loader that composes
# StaticSource (these defaults) with a DirSource (operator overrides),
# so operator policies transparently win via ComposedSource ordering.

package buildkit

# ── Source rules ──────────────────────────────────────────────────────────────
# Evaluated by policies/source.rego.
# - CONVERT short-circuits on first match.
# - ALLOW / DENY: last matching rule wins.
#
# Selector fields:
#   identifier    — exact string, glob (* = any chars except /), or regex
#   constraints   — [ { key, value, condition: EQUAL|NOTEQUAL|MATCHES } ]
#
# Update fields for CONVERT:
#   identifier          — new exact identifier
#   glob_pattern        — glob source pattern for wildcard rewrite
#   glob_replacement    — replacement template using ${1}, ${2}, …
#   pattern             — regex pattern for regex rewrite
#   replacement         — replacement template (Go regexp syntax)
#   attrs               — map of attribute overrides

source_rules := [
	# ── 1. Pin alpine:3.18 to a specific digest (exact CONVERT).
	{
		"selector": {"identifier": "docker-image://docker.io/library/alpine:3.18"},
		"action":   "CONVERT",
		"updates": {
			"identifier": "docker-image://docker.io/library/alpine:3.18@sha256:c0d488a800e4127c334ad20d61d7bc21b4097540327217dfab52262adc02380c",
		},
	},

	# ── 2. Redirect all golang images to an internal mirror (wildcard CONVERT).
	{
		"selector": {"identifier": "docker-image://docker.io/library/golang:*"},
		"action":   "CONVERT",
		"updates": {
			"glob_pattern":     "docker-image://docker.io/library/golang:*",
			"glob_replacement": "docker-image://mirror.internal.example.com/library/golang:${1}",
		},
	},

	# ── 3. Redirect any docker.io image to a registry mirror (glob, ** = with /).
	{
		"selector": {"identifier": "docker-image://docker.io/**"},
		"action":   "CONVERT",
		"updates": {
			"glob_pattern":     "docker-image://docker.io/**",
			"glob_replacement": "docker-image://mirror.internal.example.com/${1}",
		},
	},

	# ── 4. Enforce HTTP checksum on any HTTPS source that lacks one.
	#       (condition EQUAL on empty string = attr is absent / empty)
	{
		"selector": {
			"identifier": "https://*",
			"constraints": [
				{"key": "http.checksum", "condition": "EQUAL", "value": ""},
			],
		},
		"action":  "DENY",
		"updates": {},
	},

	# ── 5. Allow our own HTTPS source (ALLOW overrides the DENY above for this host).
	{
		"selector": {"identifier": "https://artifacts.internal.example.com/*"},
		"action":   "ALLOW",
		"updates":  {},
	},

	# ── 6. Deny all remaining docker-image sources not already CONVERTed.
	{
		"selector": {"identifier": "docker-image://*"},
		"action":   "DENY",
		"updates":  {},
	},

	# ── 7. Allow images from the internal mirror (overrides rule 6, last wins).
	{
		"selector": {"identifier": "docker-image://mirror.internal.example.com/*"},
		"action":   "ALLOW",
		"updates":  {},
	},

	# ── 8. Regex-based digest pinning: busybox any tag → pinned digest (regex CONVERT).
	{
		"selector": {
			"identifier": `^docker-image://docker\.io/library/busybox:(.+)$`,
		},
		"action": "CONVERT",
		"updates": {
			"pattern":     `^docker-image://docker\.io/library/busybox:(.+)$`,
			"replacement": "docker-image://mirror.internal.example.com/library/busybox:${1}@sha256:3614ca5eacf0a3a1bcc361c939202a974b4902b9334ff36eb29ffe9011aaad83",
		},
	},
]

# ── DAG expansion rules ───────────────────────────────────────────────────────
# Evaluated by policies/dag.rego.
#
# Selector fields (any combination; all must match):
#   op_type            — "source", "exec", "file", "merge", "diff"
#   identifier_prefix  — op.identifier must start with this
#   identifier_glob    — glob match against op.identifier
#   label_key          — input.labels[label_key] must equal label_value
#   label_value        — paired with label_key
#
# expand_template fields:
#   type         — op type for the generated node
#   id_suffix    — appended to op.id to form the new node id
#                  (default: "-expanded-<rule_idx>")
#   identifier   — identifier for the new node; supports ${identifier}, ${type}, ${id}
#   attrs        — merged with op.attrs (template wins on conflicts)
#   extra_deps   — additional depends_on entries beyond the source op

dag_rules := [
	# ── 1. Add a security-scan node after every docker-image source pull.
	{
		"selector": {
			"op_type":           "source",
			"identifier_prefix": "docker-image://",
		},
		"expand_template": {
			"type":      "exec",
			"id_suffix": "-security-scan",
			"identifier": "security-scanner:latest",
			"attrs": {
				"scan.target":  "${identifier}",
				"scan.format":  "sarif",
				"scan.fail_on": "HIGH",
			},
		},
	},

	# ── 2. Add a provenance-attestation node after every exec that writes artifacts.
	{
		"selector": {
			"op_type":   "exec",
			"label_key": "buildkit.attestation",
			"label_value": "true",
		},
		"expand_template": {
			"type":      "exec",
			"id_suffix": "-provenance",
			"identifier": "slsa-provenance:latest",
			"attrs": {
				"attestation.builder": "buildkit",
				"attestation.source":  "${id}",
			},
		},
	},

	# ── 3. Add cache-warm node for exec ops labelled for caching.
	{
		"selector": {
			"op_type":   "exec",
			"label_key": "buildkit.cache.warm",
			"label_value": "true",
		},
		"expand_template": {
			"type":      "exec",
			"id_suffix": "-cache-warm",
			"identifier": "cache-warmer:latest",
			"attrs": {
				"cache.strategy": "warm-ahead",
			},
		},
	},
]
