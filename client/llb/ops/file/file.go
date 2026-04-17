// Package file provides the BuildKit file operation (mkdir/mkfile/rm/symlink/copy).
package file

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/bons/bons-ci/client/llb/core"
	"github.com/bons/bons-ci/client/llb/marshal"
	"github.com/moby/buildkit/solver/pb"
)

// ─── Action interface ─────────────────────────────────────────────────────────

// Action is one atomic file operation within a FileOp vertex.
type Action interface {
	toProto(ctx context.Context, workingDir string, base pb.InputIndex) (pb.IsFileAction, error)
	additionalInput() core.Output
	addCaps(c *core.Constraints)
}

// ─── ChownOpt / UserOpt ───────────────────────────────────────────────────────

// ChownOpt specifies ownership using names or numeric IDs.
type ChownOpt struct {
	User  *UserOpt
	Group *UserOpt
}

// ChownByID creates a ChownOpt from numeric UID/GID.
func ChownByID(uid, gid int) *ChownOpt {
	return &ChownOpt{User: &UserOpt{UID: uid}, Group: &UserOpt{UID: gid}}
}

// ChownByName creates a ChownOpt from string names.
func ChownByName(user, group string) *ChownOpt {
	return &ChownOpt{User: &UserOpt{Name: user}, Group: &UserOpt{Name: group}}
}

func (c *ChownOpt) marshal(base pb.InputIndex) *pb.ChownOpt {
	if c == nil {
		return nil
	}
	return &pb.ChownOpt{User: c.User.marshal(base), Group: c.Group.marshal(base)}
}

// UserOpt identifies a user by UID or name.
type UserOpt struct {
	UID  int
	Name string
}

func (u *UserOpt) marshal(base pb.InputIndex) *pb.UserOpt {
	if u == nil {
		return nil
	}
	if u.Name != "" {
		return &pb.UserOpt{User: &pb.UserOpt_ByName{ByName: &pb.NamedUserOpt{
			Name: u.Name, Input: int64(base),
		}}}
	}
	return &pb.UserOpt{User: &pb.UserOpt_ByID{ByID: uint32(u.UID)}}
}

// ─── Mkdir ────────────────────────────────────────────────────────────────────

type MkdirOption func(*mkdirAction)

func WithMkdirParents(v bool) MkdirOption { return func(a *mkdirAction) { a.makeParents = v } }
func WithMkdirChown(c *ChownOpt) MkdirOption { return func(a *mkdirAction) { a.chown = c } }
func WithMkdirTime(t time.Time) MkdirOption { return func(a *mkdirAction) { a.createdTime = &t } }

type mkdirAction struct {
	path        string
	mode        os.FileMode
	makeParents bool
	chown       *ChownOpt
	createdTime *time.Time
}

// Mkdir creates a directory-creation action.
func Mkdir(p string, mode os.FileMode, opts ...MkdirOption) Action {
	a := &mkdirAction{path: p, mode: mode}
	for _, o := range opts {
		o(a)
	}
	return a
}

func (a *mkdirAction) additionalInput() core.Output { return nil }
func (a *mkdirAction) addCaps(_ *core.Constraints)  {}
func (a *mkdirAction) toProto(_ context.Context, wd string, base pb.InputIndex) (pb.IsFileAction, error) {
	return &pb.FileAction_Mkdir{Mkdir: &pb.FileActionMkDir{
		Path:        normPath(wd, a.path, false),
		Mode:        int32(a.mode & 0777),
		MakeParents: a.makeParents,
		Owner:       a.chown.marshal(base),
		Timestamp:   marshalTime(a.createdTime),
	}}, nil
}

// ─── Mkfile ───────────────────────────────────────────────────────────────────

type MkfileOption func(*mkfileAction)

func WithMkfileChown(c *ChownOpt) MkfileOption { return func(a *mkfileAction) { a.chown = c } }
func WithMkfileTime(t time.Time) MkfileOption   { return func(a *mkfileAction) { a.createdTime = &t } }

type mkfileAction struct {
	path        string
	mode        os.FileMode
	data        []byte
	chown       *ChownOpt
	createdTime *time.Time
}

// Mkfile creates a file-creation action.
func Mkfile(p string, mode os.FileMode, data []byte, opts ...MkfileOption) Action {
	a := &mkfileAction{path: p, mode: mode, data: data}
	for _, o := range opts {
		o(a)
	}
	return a
}

func (a *mkfileAction) additionalInput() core.Output { return nil }
func (a *mkfileAction) addCaps(_ *core.Constraints)  {}
func (a *mkfileAction) toProto(_ context.Context, wd string, base pb.InputIndex) (pb.IsFileAction, error) {
	return &pb.FileAction_Mkfile{Mkfile: &pb.FileActionMkFile{
		Path:      normPath(wd, a.path, false),
		Mode:      int32(a.mode & 0777),
		Data:      a.data,
		Owner:     a.chown.marshal(base),
		Timestamp: marshalTime(a.createdTime),
	}}, nil
}

// ─── Rm ───────────────────────────────────────────────────────────────────────

type RmOption func(*rmAction)

func WithRmAllowNotFound(v bool) RmOption { return func(a *rmAction) { a.allowNotFound = v } }
func WithRmAllowWildcard(v bool) RmOption { return func(a *rmAction) { a.allowWildcard = v } }

type rmAction struct {
	path          string
	allowNotFound bool
	allowWildcard bool
}

// Rm creates a removal action.
func Rm(p string, opts ...RmOption) Action {
	a := &rmAction{path: p}
	for _, o := range opts {
		o(a)
	}
	return a
}

func (a *rmAction) additionalInput() core.Output { return nil }
func (a *rmAction) addCaps(_ *core.Constraints)  {}
func (a *rmAction) toProto(_ context.Context, wd string, _ pb.InputIndex) (pb.IsFileAction, error) {
	return &pb.FileAction_Rm{Rm: &pb.FileActionRm{
		Path:          normPath(wd, a.path, false),
		AllowNotFound: a.allowNotFound,
		AllowWildcard: a.allowWildcard,
	}}, nil
}

// ─── Symlink ──────────────────────────────────────────────────────────────────

type symlinkAction struct {
	oldpath, newpath string
	chown            *ChownOpt
	createdTime      *time.Time
}

// Symlink creates a symlink action (newpath → oldpath).
func Symlink(oldpath, newpath string, chown *ChownOpt, createdAt *time.Time) Action {
	return &symlinkAction{oldpath: oldpath, newpath: newpath, chown: chown, createdTime: createdAt}
}

func (a *symlinkAction) additionalInput() core.Output { return nil }
func (a *symlinkAction) addCaps(c *core.Constraints) {
	core.ConstraintsAddCap(c, pb.CapFileSymlinkCreate)
}
func (a *symlinkAction) toProto(_ context.Context, _ string, base pb.InputIndex) (pb.IsFileAction, error) {
	return &pb.FileAction_Symlink{Symlink: &pb.FileActionSymlink{
		Oldpath:   a.oldpath,
		Newpath:   a.newpath,
		Owner:     a.chown.marshal(base),
		Timestamp: marshalTime(a.createdTime),
	}}, nil
}

// ─── Copy ─────────────────────────────────────────────────────────────────────

// CopyOption is a functional option for the copy action.
type CopyOption func(*copyAction)

func WithCopyFollowSymlinks(v bool) CopyOption { return func(a *copyAction) { a.followSymlinks = v } }
func WithCopyDirContentsOnly(v bool) CopyOption { return func(a *copyAction) { a.dirContentsOnly = v } }
func WithCopyInclude(patterns []string) CopyOption { return func(a *copyAction) { a.include = patterns } }
func WithCopyExclude(patterns []string) CopyOption { return func(a *copyAction) { a.exclude = patterns } }
func WithCopyCreateDest(v bool) CopyOption         { return func(a *copyAction) { a.createDest = v } }
func WithCopyAllowWildcard(v bool) CopyOption      { return func(a *copyAction) { a.allowWildcard = v } }
func WithCopyAttemptUnpack(v bool) CopyOption      { return func(a *copyAction) { a.attemptUnpack = v } }
func WithCopyChown(c *ChownOpt) CopyOption         { return func(a *copyAction) { a.chown = c } }
func WithCopyTime(t time.Time) CopyOption          { return func(a *copyAction) { a.createdTime = &t } }

type copyAction struct {
	src, dst        string
	source          core.Output
	followSymlinks  bool
	dirContentsOnly bool
	include         []string
	exclude         []string
	allowWildcard   bool
	allowEmptyWild  bool
	createDest      bool
	attemptUnpack   bool
	chown           *ChownOpt
	createdTime     *time.Time
}

// Copy creates a copy action.
func Copy(source core.Output, src, dst string, opts ...CopyOption) Action {
	a := &copyAction{src: src, dst: dst, source: source}
	for _, o := range opts {
		o(a)
	}
	return a
}

func (a *copyAction) additionalInput() core.Output { return a.source }
func (a *copyAction) addCaps(c *core.Constraints) {
	if len(a.include) > 0 || len(a.exclude) > 0 {
		core.ConstraintsAddCap(c, pb.CapFileCopyIncludeExcludePatterns)
	}
}
func (a *copyAction) toProto(_ context.Context, wd string, base pb.InputIndex) (pb.IsFileAction, error) {
	srcPath := filepath.ToSlash(path.Clean(a.src))
	if !path.IsAbs(srcPath) {
		srcPath = path.Join("/", wd, srcPath)
	}
	return &pb.FileAction_Copy{Copy: &pb.FileActionCopy{
		Src:                 srcPath,
		Dest:                normPath(wd, a.dst, true),
		Owner:               a.chown.marshal(base),
		IncludePatterns:     a.include,
		ExcludePatterns:     a.exclude,
		AllowWildcard:       a.allowWildcard,
		AllowEmptyWildcard:  a.allowEmptyWild,
		FollowSymlink:       a.followSymlinks,
		DirCopyContents:     a.dirContentsOnly,
		CreateDestPath:      a.createDest,
		AttemptUnpackDockerCompatibility: a.attemptUnpack,
		Timestamp:           marshalTime(a.createdTime),
		Mode:                -1,
	}}, nil
}

// ─── Config ───────────────────────────────────────────────────────────────────

// Config holds all parameters for a FileOp vertex.
type Config struct {
	BaseState   core.Output   // filesystem to operate on (nil = scratch)
	WorkingDir  string        // for relative path resolution; defaults to "/"
	Actions     []Action      // ordered operations
	Constraints core.Constraints
}

// Option is a functional option for Config.
type Option func(*Config)

func OnState(out core.Output) Option   { return func(c *Config) { c.BaseState = out } }
func InDir(dir string) Option          { return func(c *Config) { c.WorkingDir = dir } }
func Do(actions ...Action) Option      { return func(c *Config) { c.Actions = append(c.Actions, actions...) } }
func WithConstraintsOption(opt core.ConstraintsOption) Option {
	return func(c *Config) { opt(&c.Constraints) }
}

// ─── Vertex ───────────────────────────────────────────────────────────────────

// Vertex is the llbx file op implementation.
type Vertex struct {
	config Config
	cache  marshal.Cache
}

// New constructs a file op vertex.
func New(opts ...Option) (*Vertex, error) {
	cfg := Config{WorkingDir: "/"}
	for _, o := range opts {
		o(&cfg)
	}
	if len(cfg.Actions) == 0 {
		return nil, fmt.Errorf("file.New: at least one Action is required")
	}
	return &Vertex{config: cfg}, nil
}

// ─── core.Vertex ─────────────────────────────────────────────────────────────

func (v *Vertex) Type() core.VertexType { return core.VertexTypeFile }

func (v *Vertex) Inputs() []core.Edge {
	seen := map[core.Output]struct{}{}
	var out []core.Edge
	if v.config.BaseState != nil {
		seen[v.config.BaseState] = struct{}{}
		out = append(out, core.Edge{Vertex: v.config.BaseState.Vertex(context.Background(), nil), Index: 0})
	}
	for _, a := range v.config.Actions {
		if inp := a.additionalInput(); inp != nil {
			if _, ok := seen[inp]; !ok {
				seen[inp] = struct{}{}
				out = append(out, core.Edge{Vertex: inp.Vertex(context.Background(), nil), Index: 0})
			}
		}
	}
	return out
}

func (v *Vertex) Outputs() []core.OutputSlot {
	return []core.OutputSlot{{Index: 0, Description: "file op result"}}
}

func (v *Vertex) Validate(_ context.Context, _ *core.Constraints) error {
	if len(v.config.Actions) == 0 {
		return &core.ValidationError{Field: "Actions", Cause: fmt.Errorf("must not be empty")}
	}
	return nil
}

func (v *Vertex) Marshal(ctx context.Context, c *core.Constraints) (*core.MarshaledVertex, error) {
	h := v.cache.Acquire()
	defer h.Release()
	if dgst, bytes, meta, srcs, err := h.Load(c); err == nil {
		return &core.MarshaledVertex{Digest: dgst, Bytes: bytes, Metadata: meta, SourceLocations: srcs}, nil
	}

	cfg := &v.config
	core.ConstraintsAddCap(&cfg.Constraints, pb.CapFileBase)
	for _, a := range cfg.Actions {
		a.addCaps(&cfg.Constraints)
	}

	pop, md := marshal.MarshalConstraints(c, &cfg.Constraints)
	pop.Platform = nil

	pfo := &pb.FileOp{}

	// Collect and deduplicate inputs.
	type inputEntry struct{ out core.Output }
	var inputList []inputEntry
	addInput := func(out core.Output) (pb.InputIndex, error) {
		inp, err := out.ToInput(ctx, c)
		if err != nil {
			return 0, err
		}
		for i, e := range inputList {
			existing, _ := e.out.ToInput(ctx, c)
			if existing != nil && existing.Digest == inp.Digest && existing.Index == inp.Index {
				return pb.InputIndex(i), nil
			}
		}
		idx := pb.InputIndex(len(inputList))
		inputList = append(inputList, inputEntry{out: out})
		return idx, nil
	}

	var baseIdx pb.InputIndex = -1
	if cfg.BaseState != nil {
		idx, err := addInput(cfg.BaseState)
		if err != nil {
			return nil, fmt.Errorf("file.Marshal base: %w", err)
		}
		baseIdx = idx
	}

	// Secondary inputs for Copy actions.
	secondaryIdxs := make([]pb.InputIndex, len(cfg.Actions))
	for i, action := range cfg.Actions {
		secondaryIdxs[i] = -1
		if inp := action.additionalInput(); inp != nil {
			idx, err := addInput(inp)
			if err != nil {
				return nil, fmt.Errorf("file.Marshal action[%d]: %w", i, err)
			}
			secondaryIdxs[i] = idx
		}
	}

	// Populate pb inputs.
	for _, e := range inputList {
		inp, err := e.out.ToInput(ctx, c)
		if err != nil {
			return nil, err
		}
		pop.Inputs = append(pop.Inputs, inp)
	}

	// Build actions.
	for i, action := range cfg.Actions {
		output := int64(pb.SkipOutput)
		if i+1 == len(cfg.Actions) {
			output = 0
		}

		// Compute input index: first action uses base state or -1 (scratch).
		// Subsequent actions use relative indices (len(inputs) + action-1).
		var inputIdx int64
		if i == 0 {
			inputIdx = int64(baseIdx)
		} else {
			inputIdx = int64(len(inputList) + i - 1)
		}

		secondaryIdx := int64(-1)
		if secondaryIdxs[i] >= 0 {
			secondaryIdx = int64(secondaryIdxs[i])
		}

		proto, err := action.toProto(ctx, cfg.WorkingDir, pb.InputIndex(max(0, int(baseIdx))))
		if err != nil {
			return nil, fmt.Errorf("file.Marshal action[%d]: %w", i, err)
		}
		pfo.Actions = append(pfo.Actions, &pb.FileAction{
			Input:          inputIdx,
			SecondaryInput: secondaryIdx,
			Output:         output,
			Action:         proto,
		})
	}

	pop.Op = &pb.Op_File{File: pfo}
	bytes, err := marshal.DeterministicMarshal(pop)
	if err != nil {
		return nil, fmt.Errorf("file.Marshal: %w", err)
	}
	dgst, bytes, meta, srcs, _ := h.Store(bytes, md, c.SourceLocations, c)
	return &core.MarshaledVertex{Digest: dgst, Bytes: bytes, Metadata: meta, SourceLocations: srcs}, nil
}

func (v *Vertex) WithInputs(inputs []core.Edge) (core.Vertex, error) {
	newCfg := v.config
	if len(inputs) > 0 && inputs[0].Vertex != nil {
		newCfg.BaseState = &edgeOutput{edge: inputs[0]}
	}
	return &Vertex{config: newCfg}, nil
}

func (v *Vertex) Config() Config      { return v.config }
func (v *Vertex) Output() core.Output { return &fileOutput{v: v} }

type fileOutput struct{ v *Vertex }

func (o *fileOutput) Vertex(_ context.Context, _ *core.Constraints) core.Vertex { return o.v }
func (o *fileOutput) ToInput(ctx context.Context, c *core.Constraints) (*pb.Input, error) {
	mv, err := o.v.Marshal(ctx, c)
	if err != nil {
		return nil, err
	}
	return &pb.Input{Digest: string(mv.Digest), Index: 0}, nil
}

type edgeOutput struct{ edge core.Edge }

func (e *edgeOutput) Vertex(_ context.Context, _ *core.Constraints) core.Vertex { return e.edge.Vertex }
func (e *edgeOutput) ToInput(ctx context.Context, c *core.Constraints) (*pb.Input, error) {
	mv, err := e.edge.Vertex.Marshal(ctx, c)
	if err != nil {
		return nil, err
	}
	return &pb.Input{Digest: string(mv.Digest), Index: int64(e.edge.Index)}, nil
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func normPath(parent, p string, keepSlash bool) string {
	orig := p
	p = path.Clean(p)
	if !path.IsAbs(p) {
		p = path.Join("/", parent, p)
	}
	if keepSlash {
		if strings.HasSuffix(orig, "/") && !strings.HasSuffix(p, "/") {
			p += "/"
		} else if strings.HasSuffix(orig, "/.") {
			if p != "/" {
				p += "/"
			}
			p += "."
		}
	}
	return p
}

func marshalTime(t *time.Time) int64 {
	if t == nil {
		return -1
	}
	return t.UnixNano()
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

var (
	_ core.Vertex         = (*Vertex)(nil)
	_ core.MutatingVertex = (*Vertex)(nil)
)
