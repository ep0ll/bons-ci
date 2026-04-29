// Package fileop implements the FileOp vertex for LLB graph construction.
// FileOp chains multiple file-system actions (mkdir, mkfile, copy, rm, symlink)
// into a single atomic vertex.
package fileop

import (
	"context"
	"os"
	"path"
	"strconv"

	"github.com/bons/bons-ci/client/llb"
	"github.com/moby/buildkit/solver/pb"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
)

// ─────────────────────────────────────────────────────────────────────────────
// FileAction
// ─────────────────────────────────────────────────────────────────────────────

// FileAction is an ordered chain of file-system sub-actions that can be
// applied to a State via State.File(). Actions are appended fluently:
//
//	a := Mkdir("/foo", 0755).Mkfile("/foo/bar", 0644, data).Rm("/baz")
type FileAction struct {
	state *llb.State
	prev  *FileAction
	act   subAction
}

// subAction is the interface for individual file operations.
type subAction interface {
	toProtoAction(ctx context.Context, parent string, base pb.InputIndex) (pb.IsFileAction, error)
}

// Mkdir returns a FileAction that creates a directory.
func Mkdir(p string, mode os.FileMode, opts ...MkdirOption) *FileAction {
	info := &MkdirInfo{}
	for _, o := range opts {
		o.SetMkdirOption(info)
	}
	return &FileAction{act: &fileActionMkdir{file: p, mode: mode, info: *info}}
}

// Mkfile returns a FileAction that creates a file with content.
func Mkfile(p string, mode os.FileMode, data []byte, opts ...MkfileOption) *FileAction {
	info := &MkfileInfo{}
	for _, o := range opts {
		o.SetMkfileOption(info)
	}
	return &FileAction{act: &fileActionMkfile{file: p, mode: mode, data: data, info: *info}}
}

// Rm returns a FileAction that removes a path.
func Rm(p string, opts ...RmOption) *FileAction {
	info := &RmInfo{}
	for _, o := range opts {
		o.SetRmOption(info)
	}
	return &FileAction{act: &fileActionRm{file: p, info: *info}}
}

// Copy returns a FileAction that copies content from another state.
func Copy(src llb.State, srcPath, destPath string, opts ...CopyOption) *FileAction {
	info := &CopyInfo{}
	for _, o := range opts {
		o.SetCopyOption(info)
	}
	return &FileAction{
		state: &src,
		act:   &fileActionCopy{src: srcPath, dest: destPath, info: *info},
	}
}

// Symlink returns a FileAction that creates a symbolic link.
func Symlink(oldpath, newpath string) *FileAction {
	return &FileAction{act: &fileActionSymlink{oldpath: oldpath, newpath: newpath}}
}

// Mkdir chains a directory creation onto this action.
func (fa *FileAction) Mkdir(p string, mode os.FileMode, opts ...MkdirOption) *FileAction {
	a := Mkdir(p, mode, opts...)
	a.prev = fa
	return a
}

// Mkfile chains a file creation onto this action.
func (fa *FileAction) Mkfile(p string, mode os.FileMode, data []byte, opts ...MkfileOption) *FileAction {
	a := Mkfile(p, mode, data, opts...)
	a.prev = fa
	return a
}

// Rm chains a removal onto this action.
func (fa *FileAction) Rm(p string, opts ...RmOption) *FileAction {
	a := Rm(p, opts...)
	a.prev = fa
	return a
}

// Copy chains a copy onto this action.
func (fa *FileAction) Copy(src llb.State, srcPath, destPath string, opts ...CopyOption) *FileAction {
	a := Copy(src, srcPath, destPath, opts...)
	a.prev = fa
	return a
}

// Symlink chains a symlink creation onto this action.
func (fa *FileAction) Symlink(oldpath, newpath string) *FileAction {
	a := Symlink(oldpath, newpath)
	a.prev = fa
	return a
}

// ─────────────────────────────────────────────────────────────────────────────
// FileOp
// ─────────────────────────────────────────────────────────────────────────────

// FileOp is a Vertex that executes a chain of file actions atomically.
type FileOp struct {
	cache       llb.MarshalCache
	action      *FileAction
	output      llb.Output
	base        llb.State
	constraints llb.Constraints
}

var _ llb.Vertex = (*FileOp)(nil)

// NewFileOp creates a FileOp for the given state and action chain.
func NewFileOp(s llb.State, action *FileAction, c llb.Constraints) *FileOp {
	op := &FileOp{
		action:      action,
		base:        s,
		constraints: c,
	}
	op.output = llb.NewOutput(op)
	return op
}

// Validate checks the file op.
func (f *FileOp) Validate(_ context.Context, _ *llb.Constraints) error {
	if f.action == nil {
		return errors.New("file op requires at least one action")
	}
	return nil
}

// Marshal serializes the FileOp into a pb.Op with a pb.FileOp payload.
func (f *FileOp) Marshal(ctx context.Context, constraints *llb.Constraints) (digest.Digest, []byte, *pb.OpMetadata, []*llb.SourceLocation, error) {
	cache := f.cache.Acquire()
	defer cache.Release()

	if dgst, dt, md, srcs, err := cache.Load(constraints); err == nil {
		return dgst, dt, md, srcs, nil
	}

	if err := f.Validate(ctx, constraints); err != nil {
		return "", nil, nil, nil, err
	}

	pop, md := llb.MarshalConstraints(constraints, &f.constraints)

	// Collect all actions in order.
	var actions []subAction
	var states []*llb.State
	for a := f.action; a != nil; a = a.prev {
		actions = append([]subAction{a.act}, actions...)
		states = append([]*llb.State{a.state}, states...)
	}

	// Add base state as input 0.
	if f.base.Output() != nil {
		baseInput, err := f.base.Output().ToInput(ctx, constraints)
		if err != nil {
			return "", nil, nil, nil, err
		}
		pop.Inputs = append(pop.Inputs, baseInput)
	}

	// Add copy sources as additional inputs.
	inputMap := make(map[*llb.State]pb.InputIndex)
	for _, st := range states {
		if st == nil {
			continue
		}
		if _, ok := inputMap[st]; ok {
			continue
		}
		if st.Output() != nil {
			inp, err := st.Output().ToInput(ctx, constraints)
			if err != nil {
				return "", nil, nil, nil, err
			}
			inputMap[st] = pb.InputIndex(len(pop.Inputs))
			pop.Inputs = append(pop.Inputs, inp)
		}
	}

	pfo := &pb.FileOp{}
	for i, act := range actions {
		base := pb.InputIndex(0) // default to base state
		if states[i] != nil {
			if idx, ok := inputMap[states[i]]; ok {
				base = idx
			}
		}

		protoAct, err := act.toProtoAction(ctx, "", base)
		if err != nil {
			return "", nil, nil, nil, err
		}
		pfo.Actions = append(pfo.Actions, &pb.FileAction{
			Input:          int64(base),
			SecondaryInput: int64(pb.Empty),
			Output:         int64(0),
			Action:         protoAct,
		})
	}

	pop.Op = &pb.Op_File{File: pfo}

	dt, err := llb.DeterministicMarshal(pop)
	if err != nil {
		return "", nil, nil, nil, err
	}

	return cache.Store(dt, md, f.constraints.SourceLocations, constraints)
}

// Output returns the single output.
func (f *FileOp) Output() llb.Output { return f.output }

// Inputs collects all outputs referenced by the action chain.
func (f *FileOp) Inputs() []llb.Output {
	seen := make(map[llb.Output]struct{})
	var outs []llb.Output
	if f.base.Output() != nil {
		seen[f.base.Output()] = struct{}{}
		outs = append(outs, f.base.Output())
	}
	for a := f.action; a != nil; a = a.prev {
		if a.state != nil && a.state.Output() != nil {
			if _, ok := seen[a.state.Output()]; !ok {
				seen[a.state.Output()] = struct{}{}
				outs = append(outs, a.state.Output())
			}
		}
	}
	return outs
}

// ─────────────────────────────────────────────────────────────────────────────
// Sub-action implementations
// ─────────────────────────────────────────────────────────────────────────────

type fileActionMkdir struct {
	file string
	mode os.FileMode
	info MkdirInfo
}

func (a *fileActionMkdir) toProtoAction(_ context.Context, _ string, _ pb.InputIndex) (pb.IsFileAction, error) {
	return &pb.FileAction_Mkdir{
		Mkdir: &pb.FileActionMkDir{
			Path:        a.file,
			Mode:        int32(a.mode & 0777),
			MakeParents: a.info.MakeParents,
		},
	}, nil
}

type fileActionMkfile struct {
	file string
	mode os.FileMode
	data []byte
	info MkfileInfo
}

func (a *fileActionMkfile) toProtoAction(_ context.Context, _ string, _ pb.InputIndex) (pb.IsFileAction, error) {
	return &pb.FileAction_Mkfile{
		Mkfile: &pb.FileActionMkFile{
			Path: a.file,
			Mode: int32(a.mode & 0777),
			Data: a.data,
		},
	}, nil
}

type fileActionRm struct {
	file string
	info RmInfo
}

func (a *fileActionRm) toProtoAction(_ context.Context, _ string, _ pb.InputIndex) (pb.IsFileAction, error) {
	return &pb.FileAction_Rm{
		Rm: &pb.FileActionRm{
			Path:          a.file,
			AllowNotFound: a.info.AllowNotFound,
			AllowWildcard: a.info.AllowWildcard,
		},
	}, nil
}

type fileActionCopy struct {
	src  string
	dest string
	info CopyInfo
}

func (a *fileActionCopy) toProtoAction(_ context.Context, _ string, base pb.InputIndex) (pb.IsFileAction, error) {
	cp := &pb.FileActionCopy{
		Src:                              a.src,
		Dest:                             a.dest,
		AllowWildcard:                    a.info.AllowWildcard,
		AllowEmptyWildcard:               a.info.AllowEmptyWildcard,
		FollowSymlink:                    a.info.FollowSymlinks,
		DirCopyContents:                  a.info.DirCopyContents,
		AttemptUnpackDockerCompatibility: a.info.AttemptUnpack,
		CreateDestPath:                   a.info.CreateDestPath,
	}
	if a.info.Mode != nil {
		cp.Mode = int32(*a.info.Mode)
	}
	return &pb.FileAction_Copy{Copy: cp}, nil
}

type fileActionSymlink struct {
	oldpath string
	newpath string
}

func (a *fileActionSymlink) toProtoAction(_ context.Context, _ string, _ pb.InputIndex) (pb.IsFileAction, error) {
	return &pb.FileAction_Symlink{
		Symlink: &pb.FileActionSymlink{
			Oldpath: a.oldpath,
			Newpath: a.newpath,
		},
	}, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Option types
// ─────────────────────────────────────────────────────────────────────────────

// MkdirInfo holds options for mkdir.
type MkdirInfo struct {
	MakeParents bool
}

// MkdirOption configures a mkdir action.
type MkdirOption interface {
	SetMkdirOption(*MkdirInfo)
}

type mkdirOptionFunc func(*MkdirInfo)

func (f mkdirOptionFunc) SetMkdirOption(i *MkdirInfo) { f(i) }

// WithParents enables creation of parent directories.
func WithParents(b bool) MkdirOption {
	return mkdirOptionFunc(func(i *MkdirInfo) { i.MakeParents = b })
}

// MkfileInfo holds options for mkfile.
type MkfileInfo struct{}

// MkfileOption configures a mkfile action.
type MkfileOption interface {
	SetMkfileOption(*MkfileInfo)
}

// RmInfo holds options for rm.
type RmInfo struct {
	AllowNotFound bool
	AllowWildcard bool
}

// RmOption configures a rm action.
type RmOption interface {
	SetRmOption(*RmInfo)
}

type rmOptionFunc func(*RmInfo)

func (f rmOptionFunc) SetRmOption(i *RmInfo) { f(i) }

// WithAllowNotFound makes rm not error on missing paths.
func WithAllowNotFound(b bool) RmOption {
	return rmOptionFunc(func(i *RmInfo) { i.AllowNotFound = b })
}

// WithAllowWildcard enables glob patterns in rm.
func WithAllowWildcard(b bool) RmOption {
	return rmOptionFunc(func(i *RmInfo) { i.AllowWildcard = b })
}

// CopyInfo holds options for copy.
type CopyInfo struct {
	Mode              *int
	FollowSymlinks    bool
	DirCopyContents   bool
	AttemptUnpack     bool
	CreateDestPath    bool
	AllowWildcard     bool
	AllowEmptyWildcard bool
	IncludePatterns   []string
	ExcludePatterns   []string
}

// CopyOption configures a copy action.
type CopyOption interface {
	SetCopyOption(*CopyInfo)
}

type copyOptionFunc func(*CopyInfo)

func (f copyOptionFunc) SetCopyOption(i *CopyInfo) { f(i) }

// WithCopyFollowSymlinks follows symlinks during copy.
func WithCopyFollowSymlinks(b bool) CopyOption {
	return copyOptionFunc(func(i *CopyInfo) { i.FollowSymlinks = b })
}

// WithCopyDirContents copies directory contents instead of the directory itself.
func WithCopyDirContents(b bool) CopyOption {
	return copyOptionFunc(func(i *CopyInfo) { i.DirCopyContents = b })
}

// WithCreateDestPath creates destination directories as needed.
func WithCreateDestPath(b bool) CopyOption {
	return copyOptionFunc(func(i *CopyInfo) { i.CreateDestPath = b })
}

// WithCopyIncludePatterns restricts which paths are copied.
func WithCopyIncludePatterns(patterns []string) CopyOption {
	return copyOptionFunc(func(i *CopyInfo) { i.IncludePatterns = patterns })
}

// WithCopyExcludePatterns excludes paths from copy.
func WithCopyExcludePatterns(patterns []string) CopyOption {
	return copyOptionFunc(func(i *CopyInfo) { i.ExcludePatterns = patterns })
}

// File applies a FileAction to a State and returns the result.
func File(base llb.State, action *FileAction, opts ...llb.ConstraintsOpt) llb.State {
	var c llb.Constraints
	for _, o := range opts {
		o.SetConstraintsOption(&c)
	}
	llb.AddCap(&c, pb.CapFileBase)
	op := NewFileOp(base, action, c)
	return base.WithOutput(op.Output())
}

// Ensure path and strconv usages.
var (
	_ = path.Join
	_ = strconv.Itoa
)
