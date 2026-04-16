# llbx

**llbx** is an advanced, event-driven Go library for constructing and mutating BuildKit [LLB](https://github.com/moby/buildkit/blob/master/docs/reference/buildctl.md) build graphs. It extends the BuildKit `llb` package with a fully reactive, modular, and strictly typed architecture.

---

## Architecture Overview

```
llbx/
├── core/           foundational interfaces (Vertex, Output, Edge), Constraints, errors
├── reactive/       typed pub/sub EventBus and Observable primitives
├── graph/          immutable DAG + structural mutations + traversal/selection
├── marshal/        cache-keyed serialisation → pb.Definition
├── ops/
│   ├── registry    plugin factory registry for custom vertex types
│   ├── exec/       exec (container run) op with typed mounts, secrets, SSH
│   ├── file/       file op (mkdir / mkfile / rm / symlink / copy) action chain
│   ├── merge/      overlay merge op
│   ├── diff/       filesystem diff op
│   └── source/
│       ├── git/    git repository source (named ref types: branch/tag/commit)
│       ├── http/   HTTP/HTTPS fetch source
│       ├── image/  Docker/OCI image source with resolve modes
│       ├── local/  local client-directory source
│       └── oci/    OCI layout content-store source
├── state/          immutable, composable State builder
└── builder/        top-level reactive orchestrator
```

---

## Key Design Principles

### 1 · Immutability

Every `Vertex` and `State` is immutable after construction. All mutation methods (`WithOption`, `WithInputs`) return **new** instances. Old values can be retained as rollback checkpoints.

### 2 · Reactive change propagation

When a graph mutation changes the content-address (digest) of a vertex, all vertices that transitively depend on it are **automatically re-digested**. A `GraphEvent` is emitted on the shared bus for each affected vertex.

```go
b := builder.New()
b.Subscribe(func(e reactive.GraphEvent) {
    log.Printf("graph changed: kind=%s affected=%s prev=%s",
        e.Kind, e.AffectedID, e.PreviousID)
})
```

### 3 · Strict separation of concerns

| Package | Responsibility |
|---------|----------------|
| `core` | Contracts only – no dependencies on other llbx packages |
| `reactive` | Event infrastructure – no build semantics |
| `graph` | Graph topology – no serialisation |
| `marshal` | Serialisation – no topology mutations |
| `ops/*` | Concrete vertex logic – no graph management |
| `state` | Composition API – no serialisation |
| `builder` | Orchestration – thin glue layer |

### 4 · Named, typed options

Every source op uses a dedicated option type rather than positional parameters:

```go
// Bad (original llb):  Git("github.com/foo/bar.git", "v1.0")
// Good (llbx):
v, err := git.New(
    git.WithRemote("https://github.com/foo/bar.git"),
    git.WithRef(git.TagRef("v1.0")),          // distinguishes tags from branches/commits
    git.WithKeepGitDir(false),
    git.WithAuthTokenSecret("GIT_TOKEN"),
)
```

### 5 · Forward/backward compatibility

Options are additive. New options can be added without breaking existing callers. Option structs carry explicit field names and safe zero values.

---

## Usage

### Basic pipeline

```go
ctx := context.Background()
b   := builder.New()

alpine := b.Image("alpine:3.20")

withHello := alpine.File(file.New(
    file.OnState(alpine.Output()),
    file.Do(
        file.Mkdir("/out", 0755, file.WithMkdirParents(true)),
        file.Mkfile("/out/hello.txt", 0644, []byte("hello from llbx\n")),
    ),
))

def, err := b.Serialize(ctx, withHello)
// def.ToPB() → *pb.Definition, ready for buildkitd
```

### Git source

```go
src, err := git.New(
    git.WithRemote("https://github.com/moby/buildkit.git"),
    git.WithRef(git.TagRef("v0.15.0")),
    git.WithSubDirectory("docs"),
    git.WithShallowClone(true),
)
```

### Exec with mounts

```go
import execop "github.com/bons/bons-ci/client/llb/ops/exec"

alpine, _ := image.New(image.WithRef("alpine:3.20"))
ctx_src, _ := local.New(local.WithName("context"))

build, _ := execop.New(
    execop.WithRootMount(alpine.Output(), true),
    execop.WithCommand("go", "build", "-o", "/out/bin", "./cmd/..."),
    execop.WithWorkingDir("/src"),
    execop.WithMount(execop.Mount{
        Target:   "/src",
        Source:   ctx_src.Output(),
        ReadOnly: true,
    }),
    execop.WithMount(execop.Mount{
        Target: "/out",
        Source: state.Scratch().Output(),
    }),
    execop.WithMount(execop.Mount{
        Target:       "/root/pkg",
        Type:         execop.MountTypeCache,
        CacheID:      "go-pkg-cache",
        CacheSharing: execop.CacheSharingShared,
    }),
)
```

### Merge and Diff

```go
base     := b.Image("alpine:3.20")
modified := base.File(addFiles)

patch    := base.Diff(modified)          // only the changed layers
combined := base.Merge(patchA, patchB)   // independent patches applied in parallel
```

### Graph mutation

```go
g, err := b.BuildGraph(ctx, root)

mut := b.Mutator(g)

// Swap one image for another; all downstream digests update automatically.
ng, err := mut.Replace(ctx, oldDigest, newImageVertex, constraints)

// Rewire inputs.
ng, err = mut.Reparent(ctx, targetDigest, newEdges, constraints)

// Insert a vertex between a producer and its consumers.
ng, err = mut.InsertBefore(ctx, targetDigest, middlewareVertex, constraints)

// Remove a single-input vertex and short-circuit its consumers to its producer.
ng, err = mut.Bypass(ctx, targetDigest, constraints)
```

### Graph traversal and selection

```go
tr := b.Traversal(g)

// Topological order (inputs before consumers).
ordered := tr.TopologicalOrder(g.Roots())

// Find all exec ops.
sel := b.Selector(g)
execIDs := sel.ByType(core.VertexTypeExec)

// Custom predicate.
gitIDs := sel.Where(func(id core.VertexID, v core.Vertex) bool {
    return v.Type() == core.VertexTypeSource
})
```

---

## Reactive observable

```go
obs := reactive.NewObservable("v1.0")
defer obs.Close()

obs.Subscribe(func(e reactive.ChangeEvent[string]) {
    fmt.Printf("version changed: %s → %s\n", e.Old, e.New)
})

obs.Set("v2.0") // fires the subscriber
obs.Set("v2.0") // no-op; value unchanged, no event fired
```

---

## Extending with custom vertex types

```go
// 1. Implement core.Vertex (and optionally core.MutatingVertex).
type MyOp struct { /* … */ }
func (o *MyOp) Type() core.VertexType { return "my-op" }
// … implement Inputs, Outputs, Validate, Marshal

// 2. Register a factory.
ops.DefaultRegistry.Register("my-op", &myOpFactory{})

// 3. Use via the state API.
st := state.From(myOpInstance.Output())
```

---

## Testing

```
go test ./...
```
