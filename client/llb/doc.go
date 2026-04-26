// Package llb is an advanced, content-addressable, event-driven BuildKit LLB
// graph construction library for github.com/bons/bons-ci.
//
// Every vertex in the build graph is identified by the SHA256 of its
// deterministically-serialised protobuf form. When any vertex changes, all
// vertices that transitively depend on it are automatically re-digested and a
// GraphEvent is emitted on the reactive event bus.
//
// # Package layout
//
//	client/llb/
//	├── core/          – foundational interfaces, Constraints, typed errors
//	├── reactive/      – typed EventBus[T] and Observable[T]
//	├── graph/         – content-addressable DAG, mutations, traversal, merge/prune
//	├── marshal/       – per-constraints cache and wire-format serialiser
//	├── ops/
//	│   ├── source/{git,http,image,local,oci}  – source operations
//	│   ├── exec/        – container exec op
//	│   ├── file/        – file op (mkdir/mkfile/rm/symlink/copy)
//	│   ├── merge/       – overlay merge op
//	│   ├── diff/        – filesystem diff op
//	│   ├── conditional/ – compile-time if/else/switch branching
//	│   ├── matrix/      – fan-out across parameter configurations (Vertex + helpers)
//	│   ├── gate/        – policy / OPA-style validation gate
//	│   ├── selector/    – label-based dynamic sub-graph selection
//	│   ├── solve/       – wraps a sub-DAG as a nested BuildOp
//	│   ├── dyn/         – exec with policy output (OPA/Rego)
//	│   └── export/      – vertex-level export declaration (OCI, tar, registry)
//	├── state/         – immutable, composable fluent State API
//	└── builder/       – top-level reactive orchestrator
//
// # Content addressibility
//
// Mutating any field on a vertex invalidates its cached digest. Call
// vertex.Marshal(ctx, constraints) to obtain the new digest. The DAG's
// Mutator methods automatically cascade digest changes up to every consumer
// and emit DigestChanged events on the bus, keeping the entire graph
// consistent without any manual invalidation.
//
// # DAG merge and prune
//
// Use graph.MergeDAGs to combine two independent graphs into one, deduplicating
// shared sub-graphs by content address. Use graph.Prune to remove vertices
// not reachable from a given root set.
//
// # Extended op types
//
//   - conditional.Vertex      – evaluates a predicate at definition time and
//     resolves to one of two output branches.
//   - conditional.SwitchVertex – evaluates multiple cases in order, resolving
//     to the first matching branch. More expressive than if/else.
//   - matrix.Vertex           – stores axes and a template, expanding at marshal
//     time into a merge of all configurations.
//   - matrix.Expand           – fans out eagerly over parameter maps, producing
//     one Result per configuration.
//   - gate.Vertex             – runs a policy function; fails the build if the
//     policy rejects the vertex.
//   - selector.Vertex         – picks the best-matching candidate from a labelled
//     set based on runtime constraints (platform, build-args, etc.).
//   - solve.Vertex            – wraps a sub-DAG as a nested BuildOp definition
//     for composing independently-constructed graphs.
//   - dyn.Vertex              – exec op that produces an OPA/Rego policy file,
//     composable with gate/conditional for dynamic decisions.
//   - export.Vertex           – declares an export target (OCI image, Docker tar,
//     local directory, or registry push).
package llb
