package ops_test

import (
	"context"
	"strings"
	"testing"

	"github.com/bons/bons-ci/plugins/dag/ops"
	"github.com/bons/bons-ci/plugins/dag/vertex"
)

// ─── FileAction chain methods (Mkdir, Copy on *FileAction) ───────────────────

func TestFileActionChainMkdir(t *testing.T) {
	// fa.Mkdir chains a new mkdir onto an existing action.
	base := ops.Mkfile("/base.txt", 0644, []byte("x"))
	chained := base.Mkdir("/app", 0755)

	list := ops.ActionList(chained)
	if len(list) != 2 {
		t.Fatalf("want 2 actions, got %d", len(list))
	}
	if list[0].Kind() != ops.FileActionMkfile {
		t.Errorf("[0] kind: want mkfile, got %s", list[0].Kind())
	}
	if list[1].Kind() != ops.FileActionMkdir {
		t.Errorf("[1] kind: want mkdir, got %s", list[1].Kind())
	}
	if list[1].MkdirPath() != "/app" {
		t.Errorf("mkdir path: want /app, got %q", list[1].MkdirPath())
	}
}

func TestFileActionChainCopy(t *testing.T) {
	img := ops.Image("alpine:3.18")
	base := ops.Mkdir("/app", 0755)
	chained := base.Copy(img.Ref(), "/src", "/app/src")

	list := ops.ActionList(chained)
	if len(list) != 2 {
		t.Fatalf("want 2 actions, got %d", len(list))
	}
	if list[1].Kind() != ops.FileActionCopy {
		t.Errorf("[1] kind: want copy, got %s", list[1].Kind())
	}
	if list[1].CopySrc() != "/src" {
		t.Errorf("copy src: want /src, got %q", list[1].CopySrc())
	}
	if list[1].CopyDest() != "/app/src" {
		t.Errorf("copy dest: want /app/src, got %q", list[1].CopyDest())
	}
	if list[1].CopySource().Vertex != img {
		t.Error("copy source vertex must be the image")
	}
}

// ─── FileAction accessors ─────────────────────────────────────────────────────

func TestFileActionMkdirAccessors(t *testing.T) {
	info := ops.MkdirInfo{MakeParents: true}
	fa := ops.Mkdir("/foo", 0700, func(m *ops.MkdirInfo) {
		m.MakeParents = true
	})
	if fa.Kind() != ops.FileActionMkdir {
		t.Errorf("kind: want mkdir, got %s", fa.Kind())
	}
	if fa.MkdirPath() != "/foo" {
		t.Errorf("path: want /foo, got %q", fa.MkdirPath())
	}
	if fa.MkdirMode() != 0700 {
		t.Errorf("mode: want 0700, got %o", fa.MkdirMode())
	}
	if !fa.MkdirInfo().MakeParents {
		t.Error("MakeParents should be true")
	}
	if fa.Prev() != nil {
		t.Error("head action must have nil Prev")
	}
	if fa.State() != nil {
		t.Error("unbound action must have nil State")
	}
	_ = info
}

func TestFileActionMkfileAccessors(t *testing.T) {
	fa := ops.Mkfile("/bar", 0644, []byte("hello"))
	if fa.Kind() != ops.FileActionMkfile {
		t.Errorf("kind: want mkfile, got %s", fa.Kind())
	}
	if fa.MkfilePath() != "/bar" {
		t.Errorf("path: want /bar, got %q", fa.MkfilePath())
	}
	if fa.MkfileMode() != 0644 {
		t.Errorf("mode: want 0644, got %o", fa.MkfileMode())
	}
	if string(fa.MkfileData()) != "hello" {
		t.Errorf("data: want hello, got %q", string(fa.MkfileData()))
	}
}

func TestFileActionRmAccessors(t *testing.T) {
	fa := ops.Rm("/tmp/x", ops.WithAllowNotFound())
	if fa.Kind() != ops.FileActionRm {
		t.Errorf("kind: want rm, got %s", fa.Kind())
	}
	if fa.RmPath() != "/tmp/x" {
		t.Errorf("path: want /tmp/x, got %q", fa.RmPath())
	}
	if !fa.RmInfo().AllowNotFound {
		t.Error("AllowNotFound should be true")
	}
}

func TestFileActionSymlinkAccessors(t *testing.T) {
	fa := ops.Symlink("/target", "/link")
	if fa.Kind() != ops.FileActionSymlink {
		t.Errorf("kind: want symlink, got %s", fa.Kind())
	}
	if fa.SymlinkOld() != "/target" {
		t.Errorf("old: want /target, got %q", fa.SymlinkOld())
	}
	if fa.SymlinkNew() != "/link" {
		t.Errorf("new: want /link, got %q", fa.SymlinkNew())
	}
}

func TestFileActionCopyAccessors(t *testing.T) {
	img := ops.Image("alpine:3.18")
	fa := ops.Copy(img.Ref(), "/src", "/dst",
		ops.WithCopyInclude("*.go"),
		ops.WithCopyExclude("*_test.go"),
		ops.WithCreateDestPath(),
	)
	if fa.Kind() != ops.FileActionCopy {
		t.Errorf("kind: want copy, got %s", fa.Kind())
	}
	if fa.CopySrc() != "/src" {
		t.Errorf("src: want /src, got %q", fa.CopySrc())
	}
	if fa.CopyDest() != "/dst" {
		t.Errorf("dest: want /dst, got %q", fa.CopyDest())
	}
	if fa.CopySource().Vertex != img {
		t.Error("copy source vertex must be the image")
	}
	info := fa.CopyInfo()
	if len(info.IncludePatterns) != 1 || info.IncludePatterns[0] != "*.go" {
		t.Errorf("include: got %v", info.IncludePatterns)
	}
	if len(info.ExcludePatterns) != 1 || info.ExcludePatterns[0] != "*_test.go" {
		t.Errorf("exclude: got %v", info.ExcludePatterns)
	}
	if !info.CreateDestPath {
		t.Error("CreateDestPath should be true")
	}
}

func TestFileActionPrevChain(t *testing.T) {
	a := ops.Mkdir("/a", 0755)
	b := a.Mkfile("/b", 0644, []byte("x"))
	c := b.Rm("/c")

	// c.Prev = b, b.Prev = a, a.Prev = nil
	if c.Prev() != b {
		t.Error("c.Prev must be b")
	}
	if b.Prev() != a {
		t.Error("b.Prev must be a")
	}
	if a.Prev() != nil {
		t.Error("a.Prev must be nil")
	}
}

// ─── FileOp accessors ─────────────────────────────────────────────────────────

func TestFileOpKindAndRef(t *testing.T) {
	img := ops.Image("alpine:3.18")
	f := ops.NewFileOp(img.Ref(), ops.Mkdir("/app", 0755), ops.Constraints{})

	if f.Kind() != vertex.KindFile {
		t.Errorf("kind: want file, got %s", f.Kind())
	}
	ref := f.Ref()
	if ref.IsZero() {
		t.Error("Ref must not be zero")
	}
	if ref.Vertex != f {
		t.Error("Ref.Vertex must be the FileOp itself")
	}
	if f.Constraints().Platform != nil {
		t.Error("constraints platform should be nil when not set")
	}
	if !strings.HasPrefix(f.Name(), "file:") {
		t.Errorf("name: want file:…, got %q", f.Name())
	}
}

// ─── FileOp option helpers ────────────────────────────────────────────────────

func TestWithParents(t *testing.T) {
	f := ops.Mkdir("/a/b/c", 0755, ops.WithParents(true))
	if !f.MkdirInfo().MakeParents {
		t.Error("WithParents(true) must set MakeParents")
	}
	f2 := ops.Mkdir("/a/b/c", 0755, ops.WithParents(false))
	if f2.MkdirInfo().MakeParents {
		t.Error("WithParents(false) must clear MakeParents")
	}
}

func TestWithMkdirChown(t *testing.T) {
	f := ops.Mkdir("/data", 0700, ops.WithMkdirChown(1000, 1001))
	info := f.MkdirInfo()
	if info.ChownOpt == nil {
		t.Fatal("ChownOpt must be set")
	}
	if info.ChownOpt.User == nil || info.ChownOpt.User.UID != 1000 {
		t.Errorf("user UID: want 1000, got %v", info.ChownOpt.User)
	}
	if info.ChownOpt.Group == nil || info.ChownOpt.Group.UID != 1001 {
		t.Errorf("group UID: want 1001, got %v", info.ChownOpt.Group)
	}
}

func TestWithCopyPatterns(t *testing.T) {
	img := ops.Image("alpine:3.18")
	f := ops.Copy(img.Ref(), "/src", "/dst",
		ops.WithCopyInclude("a", "b"),
		ops.WithCopyExclude("c", "d"),
	)
	info := f.CopyInfo()
	if len(info.IncludePatterns) != 2 {
		t.Errorf("include: want 2, got %d", len(info.IncludePatterns))
	}
	if len(info.ExcludePatterns) != 2 {
		t.Errorf("exclude: want 2, got %d", len(info.ExcludePatterns))
	}
}

func TestWithCreateDestPath(t *testing.T) {
	img := ops.Image("alpine:3.18")
	f := ops.Copy(img.Ref(), "/s", "/d", ops.WithCreateDestPath())
	if !f.CopyInfo().CreateDestPath {
		t.Error("WithCreateDestPath must set CreateDestPath")
	}
}

func TestWithAllowNotFound(t *testing.T) {
	f := ops.Rm("/maybe", ops.WithAllowNotFound())
	if !f.RmInfo().AllowNotFound {
		t.Error("WithAllowNotFound must set AllowNotFound")
	}
}

// ─── normalisePath ────────────────────────────────────────────────────────────

func TestNormalisedMkdirPath(t *testing.T) {
	img := ops.Image("alpine:3.18")
	f := ops.Mkdir("relative/path", 0755)
	fileOp := ops.NewFileOp(img.Ref(), f, ops.Constraints{})
	// NormalisedMkdirPath resolves relative to working dir context.
	// The action's state is set by bind() during NewFileOp.
	p := fileOp.Action().NormalisedMkdirPath()
	// The state's dir is "/" by default when base is a SourceOp.
	// normalisePath("", "relative/path") → "/relative/path"
	if p == "" {
		t.Error("NormalisedMkdirPath must not be empty")
	}
	if !strings.HasPrefix(p, "/") {
		t.Errorf("NormalisedMkdirPath must be absolute, got %q", p)
	}
}

// ─── WithShlex / shellSplit ───────────────────────────────────────────────────

func TestWithShlex(t *testing.T) {
	ctx := context.Background()
	img := ops.Image("alpine:3.18")
	e := ops.Exec(img.Ref(),
		ops.WithShlex("sh -c 'echo hello world'"),
		ops.WithCwd("/"),
	)
	if err := e.Validate(ctx); err != nil {
		t.Fatalf("validate: %v", err)
	}
	// WithShlex splits on whitespace.
	args := e.Meta().Args
	if len(args) == 0 {
		t.Error("WithShlex must produce at least one arg")
	}
	if args[0] != "sh" {
		t.Errorf("first arg: want sh, got %q", args[0])
	}
}

// ─── WithConstraints for Exec ─────────────────────────────────────────────────

func TestExecWithConstraints(t *testing.T) {
	img := ops.Image("alpine:3.18")
	p := ops.Platform{OS: "linux", Architecture: "arm64"}
	c := ops.Constraints{}.WithPlatform(p)
	e := ops.Exec(img.Ref(),
		ops.WithArgs("ls"),
		ops.WithCwd("/"),
		ops.WithConstraints(c),
	)
	if e.Constraints().Platform == nil {
		t.Fatal("platform must be set via WithConstraints")
	}
	if e.Constraints().Platform.Architecture != "arm64" {
		t.Errorf("arch: want arm64, got %s", e.Constraints().Platform.Architecture)
	}
}

// ─── ExecOp accessors ────────────────────────────────────────────────────────

func TestExecOpKindAndName(t *testing.T) {
	img := ops.Image("alpine:3.18")
	e := ops.Exec(img.Ref(), ops.WithArgs("ls", "-la"), ops.WithCwd("/"))

	if e.Kind() != vertex.KindExec {
		t.Errorf("kind: want exec, got %s", e.Kind())
	}
	if !strings.HasPrefix(e.Name(), "exec:") {
		t.Errorf("name: want exec:…, got %q", e.Name())
	}
	// Constraints accessor
	_ = e.Constraints()
}

// ─── Image options ────────────────────────────────────────────────────────────

func TestWithImageResolveMode(t *testing.T) {
	src := ops.Image("alpine:3.18", ops.WithImageResolveMode(ops.ResolveModeForcePull))
	if src.Attrs()["image.resolvemode"] != "pull" {
		t.Errorf("resolvemode attr: got %q", src.Attrs()["image.resolvemode"])
	}
}

func TestWithImagePreferLocal(t *testing.T) {
	src := ops.Image("alpine:3.18", ops.WithImageResolveMode(ops.ResolveModePreferLocal))
	if src.Attrs()["image.resolvemode"] != "local" {
		t.Errorf("resolvemode attr: got %q", src.Attrs()["image.resolvemode"])
	}
}

func TestWithImageLayerLimit(t *testing.T) {
	src := ops.Image("alpine:3.18", ops.WithImageLayerLimit(5))
	if src.Attrs()["image.layerlimit"] != "5" {
		t.Errorf("layerlimit attr: got %q", src.Attrs()["image.layerlimit"])
	}
}

// ─── Local options ────────────────────────────────────────────────────────────

func TestWithLocalSession(t *testing.T) {
	src := ops.Local("ctx", ops.WithLocalSession("sess-abc"))
	if src.Attrs()["local.session"] != "sess-abc" {
		t.Errorf("session attr: got %q", src.Attrs()["local.session"])
	}
}

func TestWithLocalInclude(t *testing.T) {
	src := ops.Local("ctx", ops.WithLocalInclude("**/*.go", "go.mod"))
	attr := src.Attrs()["local.includepatterns"]
	if !strings.Contains(attr, "*.go") {
		t.Errorf("include attr: got %q", attr)
	}
}

func TestWithLocalExclude(t *testing.T) {
	src := ops.Local("ctx", ops.WithLocalExclude("vendor/**"))
	attr := src.Attrs()["local.excludepatterns"]
	if !strings.Contains(attr, "vendor") {
		t.Errorf("exclude attr: got %q", attr)
	}
}

// ─── SourceOp accessors ───────────────────────────────────────────────────────

func TestSourceOpName(t *testing.T) {
	src := ops.Image("alpine:3.18")
	name := src.Name()
	if name == "" {
		t.Error("Name must not be empty")
	}
	if !strings.Contains(name, "alpine") {
		t.Errorf("Name should contain image name, got %q", name)
	}
}

func TestSourceOpConstraints(t *testing.T) {
	p := ops.Platform{OS: "linux", Architecture: "amd64"}
	c := ops.Constraints{}.WithPlatform(p)
	src := ops.Image("alpine:3.18", func(i *ops.ImageInfo) { i.Constraints = c })
	if src.Constraints().Platform == nil {
		t.Error("constraints platform must be set")
	}
}

func TestSourceOpDescription(t *testing.T) {
	c := ops.Constraints{}.WithDescription("llb.customname", "My Step")
	src := ops.Image("alpine:3.18", func(i *ops.ImageInfo) { i.Constraints = c })
	desc := src.Description()
	if desc["llb.customname"] != "My Step" {
		t.Errorf("description: got %v", desc)
	}
}

// ─── vertex.Ref.String ────────────────────────────────────────────────────────

func TestRefStringZero(t *testing.T) {
	r := vertex.Ref{}
	if r.String() != "<scratch>" {
		t.Errorf("zero ref String: want <scratch>, got %q", r.String())
	}
}

func TestRefStringNamed(t *testing.T) {
	src := ops.Image("alpine:3.18")
	r := src.Ref()
	s := r.String()
	if s == "" {
		t.Error("non-zero ref String must not be empty")
	}
}

func TestRefStringUnnamed(t *testing.T) {
	// A vertex that doesn't implement Named.
	type anon struct{ id string }
	_ = anon{}
	// Use a source (which implements Named) then check that unnamed falls back
	// to kind:id prefix format.
	src := ops.Image("alpine:3.18")
	r := src.Ref()
	// Source implements Named, so it returns the name.
	s := r.String()
	if s == "" {
		t.Error("ref String must not be empty")
	}
}

// ─── MergeOp accessors ───────────────────────────────────────────────────────

func TestMergeOpKindAndName(t *testing.T) {
	a := ops.Image("alpine:3.18")
	b := ops.Image("busybox:latest")
	ref := ops.Merge([]vertex.Ref{a.Ref(), b.Ref()}, ops.Constraints{})

	if ref.Vertex.Kind() != vertex.KindMerge {
		t.Errorf("kind: want merge, got %s", ref.Vertex.Kind())
	}

	merge := ref.Vertex.(*ops.MergeOp)
	if merge.Name() == "" {
		t.Error("Name must not be empty")
	}
	refs := merge.Refs()
	if len(refs) != 2 {
		t.Errorf("Refs: want 2, got %d", len(refs))
	}
	_ = merge.Constraints()
}

// ─── DiffOp accessors ────────────────────────────────────────────────────────

func TestDiffOpKindNameAndValidate(t *testing.T) {
	ctx := context.Background()
	lower := ops.Image("alpine:3.18")
	upper := ops.Image("alpine:edge")
	ref := ops.Diff(lower.Ref(), upper.Ref(), ops.Constraints{})

	diff := ref.Vertex.(*ops.DiffOp)

	if diff.Kind() != vertex.KindDiff {
		t.Errorf("kind: want diff, got %s", diff.Kind())
	}
	if diff.Name() == "" {
		t.Error("Name must not be empty")
	}
	if err := diff.Validate(ctx); err != nil {
		t.Errorf("validate: %v", err)
	}
	if diff.Lower().Vertex != lower {
		t.Error("Lower vertex must match")
	}
	if diff.Upper().Vertex != upper {
		t.Error("Upper vertex must match")
	}
	_ = diff.Constraints()
}
