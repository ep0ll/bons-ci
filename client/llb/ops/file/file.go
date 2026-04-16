// Package file provides the BuildKit file operation (FileOp) for llbx.
//
// A FileOp is a sequence of atomic file actions (Mkdir, Mkfile, Rm, Symlink,
// Copy) executed within a single op node. Chaining actions together avoids
// the overhead of separate exec containers.
//
// Example
//
//	op := file.New(
//	    file.OnState(scratch),
//	    file.Do(
//	        file.Mkdir("/out", 0755, file.WithMkdirParents(true)),
//	        file.Mkfile("/out/hello", 0644, []byte("hello\n")),
//	    ),
//	)
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

// Action is a single file system operation within a FileOp.
type Action interface {
	// toProto converts the action to its protobuf form.
	// parent is the working directory; base is the pb.InputIndex of the
	// filesystem state the action operates on.
	toProto(ctx context.Context, parent string, base pb.InputIndex) (pb.IsFileAction, error)
	// additionalInput returns the extra Output this action reads from, or nil.
	// Used for Copy actions that read from a secondary source.
	additionalInput() core.Output
	// addCaps registers any capability requirements to the Constraints.
	addCaps(c *core.Constraints)
}

// ─── Config ───────────────────────────────────────────────────────────────────

// Config holds all parameters for a FileOp vertex.
type Config struct {
	// BaseState is the filesystem state that actions operate on.
	BaseState core.Output

	// WorkingDir is the directory relative to which relative paths are resolved.
	WorkingDir string

	// Actions is the ordered sequence of file operations.
	Actions []Action

	// Constraints are per-vertex LLB constraints.
	Constraints core.Constraints
}

// ─── Option ───────────────────────────────────────────────────────────────────

// Option is a functional option for Config.
type Option func(*Config)

// OnState sets the base filesystem state.
func OnState(out core.Output) Option { return func(c *Config) { c.BaseState = out } }

// InDir sets the working directory for relative path resolution.
func InDir(dir string) Option { return func(c *Config) { c.WorkingDir = dir } }

// Do appends file actions to the op.
func Do(actions ...Action) Option {
	return func(c *Config) { c.Actions = append(c.Actions, actions...) }
}

// WithConstraintsOption applies a core.ConstraintsOption.
func WithConstraintsOption(opt core.ConstraintsOption) Option {
	return func(c *Config) { opt(&c.Constraints) }
}

// ─── Mkdir action ─────────────────────────────────────────────────────────────

// MkdirInfo holds optional parameters for a Mkdir action.
type MkdirInfo struct {
	MakeParents bool
	ChownOpt    *ChownOpt
	CreatedTime *time.Time
}

// MkdirOption is a functional option for MkdirInfo.
type MkdirOption func(*MkdirInfo)

// WithMkdirParents enables parent directory creation.
func WithMkdirParents(v bool) MkdirOption { return func(m *MkdirInfo) { m.MakeParents = v } }

// WithMkdirChown sets the owner of the created directory.
func WithMkdirChown(opt ChownOpt) MkdirOption { return func(m *MkdirInfo) { m.ChownOpt = &opt } }

// WithMkdirTime sets the creation timestamp.
func WithMkdirTime(t time.Time) MkdirOption { return func(m *MkdirInfo) { m.CreatedTime = &t } }

type mkdirAction struct {
	filePath string
	mode     os.FileMode
	info     MkdirInfo
}

// Mkdir creates a Mkdir action.
func Mkdir(filePath string, mode os.FileMode, opts ...MkdirOption) Action {
	a := &mkdirAction{filePath: filePath, mode: mode}
	for _, o := range opts {
		o(&a.info)
	}
	return a
}

func (a *mkdirAction) additionalInput() core.Output { return nil }
func (a *mkdirAction) addCaps(_ *core.Constraints)  {}
func (a *mkdirAction) toProto(_ context.Context, parent string, base pb.InputIndex) (pb.IsFileAction, error) {
	return &pb.FileAction_Mkdir{
		Mkdir: &pb.FileActionMkDir{
			Path:        normalisePath(parent, a.filePath, false),
			Mode:        int32(a.mode & 0777),
			MakeParents: a.info.MakeParents,
			Owner:       a.info.ChownOpt.marshal(base),
			Timestamp:   marshalTime(a.info.CreatedTime),
		},
	}, nil
}

// ─── Mkfile action ────────────────────────────────────────────────────────────

// MkfileInfo holds optional parameters for a Mkfile action.
type MkfileInfo struct {
	ChownOpt    *ChownOpt
	CreatedTime *time.Time
}

// MkfileOption is a functional option for MkfileInfo.
type MkfileOption func(*MkfileInfo)

// WithMkfileChown sets the owner.
func WithMkfileChown(opt ChownOpt) MkfileOption { return func(m *MkfileInfo) { m.ChownOpt = &opt } }

// WithMkfileTime sets the creation timestamp.
func WithMkfileTime(t time.Time) MkfileOption { return func(m *MkfileInfo) { m.CreatedTime = &t } }

type mkfileAction struct {
	filePath string
	mode     os.FileMode
	data     []byte
	info     MkfileInfo
}

// Mkfile creates a Mkfile action.
func Mkfile(filePath string, mode os.FileMode, data []byte, opts ...MkfileOption) Action {
	a := &mkfileAction{filePath: filePath, mode: mode, data: data}
	for _, o := range opts {
		o(&a.info)
	}
	return a
}

func (a *mkfileAction) additionalInput() core.Output { return nil }
func (a *mkfileAction) addCaps(_ *core.Constraints)  {}
func (a *mkfileAction) toProto(_ context.Context, parent string, base pb.InputIndex) (pb.IsFileAction, error) {
	return &pb.FileAction_Mkfile{
		Mkfile: &pb.FileActionMkFile{
			Path:      normalisePath(parent, a.filePath, false),
			Mode:      int32(a.mode & 0777),
			Data:      a.data,
			Owner:     a.info.ChownOpt.marshal(base),
			Timestamp: marshalTime(a.info.CreatedTime),
		},
	}, nil
}

// ─── Rm action ────────────────────────────────────────────────────────────────

// RmInfo holds optional parameters for a Rm action.
type RmInfo struct {
	AllowNotFound bool
	AllowWildcard bool
}

// RmOption is a functional option for RmInfo.
type RmOption func(*RmInfo)

// WithRmAllowNotFound does not fail if the path does not exist.
func WithRmAllowNotFound(v bool) RmOption { return func(r *RmInfo) { r.AllowNotFound = v } }

// WithRmAllowWildcard enables glob patterns in the path.
func WithRmAllowWildcard(v bool) RmOption { return func(r *RmInfo) { r.AllowWildcard = v } }

type rmAction struct {
	filePath string
	info     RmInfo
}

// Rm creates a Rm action.
func Rm(filePath string, opts ...RmOption) Action {
	a := &rmAction{filePath: filePath}
	for _, o := range opts {
		o(&a.info)
	}
	return a
}

func (a *rmAction) additionalInput() core.Output { return nil }
func (a *rmAction) addCaps(_ *core.Constraints)  {}
func (a *rmAction) toProto(_ context.Context, parent string, _ pb.InputIndex) (pb.IsFileAction, error) {
	return &pb.FileAction_Rm{
		Rm: &pb.FileActionRm{
			Path:          normalisePath(parent, a.filePath, false),
			AllowNotFound: a.info.AllowNotFound,
			AllowWildcard: a.info.AllowWildcard,
		},
	}, nil
}

// ─── Symlink action ───────────────────────────────────────────────────────────

type symlinkAction struct {
	oldpath, newpath string
	chown            *ChownOpt
	createdTime      *time.Time
}

// Symlink creates a Symlink action (newpath → oldpath).
func Symlink(oldpath, newpath string, chown *ChownOpt, createdAt *time.Time) Action {
	return &symlinkAction{oldpath: oldpath, newpath: newpath, chown: chown, createdTime: createdAt}
}

func (a *symlinkAction) additionalInput() core.Output { return nil }
func (a *symlinkAction) addCaps(c *core.Constraints) {
	core.ConstraintsAddCap(c, pb.CapFileSymlinkCreate)
}
func (a *symlinkAction) toProto(_ context.Context, _ string, base pb.InputIndex) (pb.IsFileAction, error) {
	return &pb.FileAction_Symlink{
		Symlink: &pb.FileActionSymlink{
			Oldpath:   a.oldpath,
			Newpath:   a.newpath,
			Owner:     a.chown.marshal(base),
			Timestamp: marshalTime(a.createdTime),
		},
	}, nil
}

// ─── Copy action ──────────────────────────────────────────────────────────────

// CopyInfo holds all options for a Copy action.
type CopyInfo struct {
	FollowSymlinks      bool
	CopyDirContentsOnly bool
	IncludePatterns     []string
	ExcludePatterns     []string
	AllowWildcard       bool
	AllowEmptyWildcard  bool
	CreateDestPath      bool
	AttemptUnpack       bool
	ChownOpt            *ChownOpt
	CreatedTime         *time.Time
}

// CopyOption is a functional option for CopyInfo.
type CopyOption func(*CopyInfo)

// WithCopyFollowSymlinks follows symlinks during copy.
func WithCopyFollowSymlinks(v bool) CopyOption { return func(c *CopyInfo) { c.FollowSymlinks = v } }

// WithCopyDirContentsOnly copies only the contents of the source directory.
func WithCopyDirContentsOnly(v bool) CopyOption {
	return func(c *CopyInfo) { c.CopyDirContentsOnly = v }
}

// WithCopyIncludePatterns adds include glob filters.
func WithCopyIncludePatterns(patterns []string) CopyOption {
	return func(c *CopyInfo) { c.IncludePatterns = patterns }
}

// WithCopyExcludePatterns adds exclude glob filters.
func WithCopyExcludePatterns(patterns []string) CopyOption {
	return func(c *CopyInfo) { c.ExcludePatterns = patterns }
}

// WithCopyCreateDestPath creates the destination directory if absent.
func WithCopyCreateDestPath(v bool) CopyOption { return func(c *CopyInfo) { c.CreateDestPath = v } }

// WithCopyChown sets the owner of copied files.
func WithCopyChown(opt ChownOpt) CopyOption { return func(c *CopyInfo) { c.ChownOpt = &opt } }

// WithCopyTime sets the modification time on copied files.
func WithCopyTime(t time.Time) CopyOption { return func(c *CopyInfo) { c.CreatedTime = &t } }

type copyAction struct {
	src, dst string
	source   core.Output
	info     CopyInfo
}

// Copy creates a Copy action.
func Copy(source core.Output, src, dst string, opts ...CopyOption) Action {
	a := &copyAction{src: src, dst: dst, source: source}
	for _, o := range opts {
		o(&a.info)
	}
	return a
}

func (a *copyAction) additionalInput() core.Output { return a.source }
func (a *copyAction) addCaps(c *core.Constraints) {
	if len(a.info.IncludePatterns) > 0 || len(a.info.ExcludePatterns) > 0 {
		core.ConstraintsAddCap(c, pb.CapFileCopyIncludeExcludePatterns)
	}
}
func (a *copyAction) toProto(ctx context.Context, parent string, base pb.InputIndex) (pb.IsFileAction, error) {
	srcPath := filepath.ToSlash(path.Clean(a.src))
	if !path.IsAbs(srcPath) {
		srcPath = path.Join("/", parent, srcPath)
	}
	return &pb.FileAction_Copy{
		Copy: &pb.FileActionCopy{
			Src:                              srcPath,
			Dest:                             normalisePath(parent, a.dst, true),
			Owner:                            a.info.ChownOpt.marshal(base),
			IncludePatterns:                  a.info.IncludePatterns,
			ExcludePatterns:                  a.info.ExcludePatterns,
			AllowWildcard:                    a.info.AllowWildcard,
			AllowEmptyWildcard:               a.info.AllowEmptyWildcard,
			FollowSymlink:                    a.info.FollowSymlinks,
			DirCopyContents:                  a.info.CopyDirContentsOnly,
			CreateDestPath:                   a.info.CreateDestPath,
			AttemptUnpackDockerCompatibility: a.info.AttemptUnpack,
			Timestamp:                        marshalTime(a.info.CreatedTime),
			Mode:                             -1, // preserve source permissions
		},
	}, nil
}

// ─── ChownOpt ─────────────────────────────────────────────────────────────────

// ChownOpt specifies file ownership using either user names or numeric IDs.
type ChownOpt struct {
	User  *UserOpt
	Group *UserOpt
}

// ChownByID creates a ChownOpt using numeric UID/GID.
func ChownByID(uid, gid int) ChownOpt {
	return ChownOpt{
		User:  &UserOpt{UID: uid},
		Group: &UserOpt{UID: gid},
	}
}

// ChownByName creates a ChownOpt using string names (e.g., "root:root").
func ChownByName(user, group string) ChownOpt {
	return ChownOpt{
		User:  &UserOpt{Name: user},
		Group: &UserOpt{Name: group},
	}
}

func (c *ChownOpt) marshal(base pb.InputIndex) *pb.ChownOpt {
	if c == nil {
		return nil
	}
	return &pb.ChownOpt{User: c.User.marshal(base), Group: c.Group.marshal(base)}
}

// UserOpt identifies a user either by name or numeric ID.
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

// ─── Vertex ───────────────────────────────────────────────────────────────────

// Vertex is the llbx implementation of the file op.
type Vertex struct {
	config Config
	cache  marshal.Cache
}

// New constructs a file op Vertex.
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

// ─── core.Vertex ──────────────────────────────────────────────────────────────

func (v *Vertex) Type() core.VertexType { return core.VertexTypeFile }

func (v *Vertex) Inputs() []core.Edge {
	seen := map[core.Output]struct{}{}
	var edges []core.Edge
	if v.config.BaseState != nil {
		seen[v.config.BaseState] = struct{}{}
		edges = append(edges, core.Edge{Vertex: nil, Index: 0})
	}
	for _, a := range v.config.Actions {
		if inp := a.additionalInput(); inp != nil {
			if _, ok := seen[inp]; !ok {
				seen[inp] = struct{}{}
				edges = append(edges, core.Edge{Vertex: nil, Index: 0})
			}
		}
	}
	return edges
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
	h := marshal.Acquire(&v.cache)
	defer h.Release()
	if dgst, bytes, meta, srcs, err := h.Load(c); err == nil {
		return &core.MarshaledVertex{Digest: dgst, Bytes: bytes, Metadata: meta, SourceLocations: srcs}, nil
	}

	cfg := &v.config
	core.ConstraintsAddCap(&cfg.Constraints, pb.CapFileBase)

	pop, md := marshal.MarshalConstraints(c, &cfg.Constraints)
	pop.Platform = nil // file op is not platform specific

	pfo := &pb.FileOp{}

	// Collect all inputs.
	type inputEntry struct {
		output core.Output
		index  int // pb input index
	}
	var inputs []inputEntry
	addInput := func(out core.Output) (pb.InputIndex, error) {
		inp, err := out.ToInput(ctx, c)
		if err != nil {
			return 0, err
		}
		for i, e := range inputs {
			existing, err := e.output.ToInput(ctx, c)
			if err != nil {
				return 0, err
			}
			if existing.Digest == inp.Digest && existing.Index == inp.Index {
				return pb.InputIndex(i), nil
			}
		}
		idx := pb.InputIndex(len(inputs))
		inputs = append(inputs, inputEntry{output: out, index: int(idx)})
		return idx, nil
	}

	// Base state input.
	var baseInputIdx pb.InputIndex = -1
	if cfg.BaseState != nil {
		idx, err := addInput(cfg.BaseState)
		if err != nil {
			return nil, fmt.Errorf("file.Vertex.Marshal base state: %w", err)
		}
		baseInputIdx = idx
	}

	// Register additional inputs from Copy actions.
	actionInputs := make([]pb.InputIndex, len(cfg.Actions))
	for i, action := range cfg.Actions {
		actionInputs[i] = -1
		if inp := action.additionalInput(); inp != nil {
			idx, err := addInput(inp)
			if err != nil {
				return nil, fmt.Errorf("file.Vertex.Marshal action[%d] input: %w", i, err)
			}
			actionInputs[i] = idx
		}
		action.addCaps(&cfg.Constraints)
	}

	// Fill pb.Inputs.
	for _, e := range inputs {
		inp, err := e.output.ToInput(ctx, c)
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

		var inputIdx int64
		if i == 0 {
			if baseInputIdx >= 0 {
				inputIdx = int64(baseInputIdx)
			} else {
				inputIdx = -1
			}
		} else {
			inputIdx = int64(len(inputs) + i - 1) // relative index
		}

		secondaryInput := int64(-1)
		if actionInputs[i] >= 0 {
			secondaryInput = int64(actionInputs[i])
		}

		proto, err := action.toProto(ctx, cfg.WorkingDir, pb.InputIndex(max(0, int(baseInputIdx))))
		if err != nil {
			return nil, fmt.Errorf("file.Vertex.Marshal action[%d]: %w", i, err)
		}

		pfo.Actions = append(pfo.Actions, &pb.FileAction{
			Input:          inputIdx,
			SecondaryInput: secondaryInput,
			Output:         output,
			Action:         proto,
		})
	}

	pop.Op = &pb.Op_File{File: pfo}

	bytes, err := marshal.DeterministicMarshal(pop)
	if err != nil {
		return nil, fmt.Errorf("file.Vertex.Marshal: %w", err)
	}
	dgst, bytes, meta, srcs, _ := h.Store(bytes, md, c.SourceLocations, c)
	return &core.MarshaledVertex{Digest: dgst, Bytes: bytes, Metadata: meta, SourceLocations: srcs}, nil
}

func (v *Vertex) WithInputs(inputs []core.Edge) (core.Vertex, error) {
	// Reparenting file ops is supported; inputs[0] is the new base state.
	newCfg := v.config
	if len(inputs) > 0 {
		newCfg.BaseState = &edgeOutput{edge: inputs[0]}
	}
	return &Vertex{config: newCfg}, nil
}

// Config returns a copy of the vertex's configuration.
func (v *Vertex) Config() Config { return v.config }

// Output returns a core.Output for the file op result.
func (v *Vertex) Output() core.Output { return &fileOutput{vertex: v} }

type fileOutput struct{ vertex *Vertex }

func (o *fileOutput) Vertex(_ context.Context, _ *core.Constraints) core.Vertex { return o.vertex }
func (o *fileOutput) ToInput(ctx context.Context, c *core.Constraints) (*pb.Input, error) {
	mv, err := o.vertex.Marshal(ctx, c)
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

func normalisePath(parent, p string, keepSlash bool) string {
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

// Compile-time checks.
var (
	_ core.Vertex         = (*Vertex)(nil)
	_ core.MutatingVertex = (*Vertex)(nil)
)
