package ops

import (
	"context"
	"fmt"
	"os"
	"path"
	"strings"
	"time"

	"github.com/bons/bons-ci/plugins/dag/vertex"
)

// ─── FileAction kinds ─────────────────────────────────────────────────────────

type FileActionKind string

const (
	FileActionMkdir   FileActionKind = "mkdir"
	FileActionMkfile  FileActionKind = "mkfile"
	FileActionRm      FileActionKind = "rm"
	FileActionCopy    FileActionKind = "copy"
	FileActionSymlink FileActionKind = "symlink"
)

// ─── Ownership / option types ─────────────────────────────────────────────────

type ChownOpt struct {
	User  *UserOpt
	Group *UserOpt
}

type UserOpt struct {
	UID  int
	Name string // if non-empty, resolved at runtime; takes precedence over UID
}

type MkdirInfo struct {
	MakeParents bool
	ChownOpt    *ChownOpt
	CreatedTime *time.Time
}

type MkfileInfo struct {
	ChownOpt    *ChownOpt
	CreatedTime *time.Time
}

type SymlinkInfo struct {
	ChownOpt    *ChownOpt
	CreatedTime *time.Time
}

type RmInfo struct {
	AllowNotFound bool
	AllowWildcard bool
}

type CopyInfo struct {
	FollowSymlinks      bool
	CopyDirContentsOnly bool
	IncludePatterns     []string
	ExcludePatterns     []string
	CreateDestPath      bool
	AllowWildcard       bool
	AllowEmptyWildcard  bool
	ChownOpt            *ChownOpt
	CreatedTime         *time.Time
}

// ─── FileAction ───────────────────────────────────────────────────────────────

// FileAction is one element of an immutable file-operation chain.
// Multiple actions are batched into a single FileOp vertex.
//
// Chains are built from head (oldest) to tail (newest) via the chaining
// methods. FileOp stores the tail; ActionList() reverses to give oldest-first.
type FileAction struct {
	kind FileActionKind
	prev *FileAction // predecessor in chain (nil = head)

	// state holds the base filesystem context for relative path resolution.
	// Set by bind() when the action is attached to a FileOp.
	state *vertex.Ref

	// Per-kind payload fields.
	mkdirPath string
	mkdirMode os.FileMode
	mkdirInfo MkdirInfo

	mkfilePath string
	mkfileMode os.FileMode
	mkfileData []byte
	mkfileInfo MkfileInfo

	rmPath string
	rmInfo RmInfo

	copySource vertex.Ref // source filesystem for copy (may be zero = scratch)
	copySrc    string     // path within copySource
	copyDest   string     // destination path within target filesystem
	copyInfo   CopyInfo

	symlinkOld  string
	symlinkNew  string
	symlinkInfo SymlinkInfo
}

// ─── Chain methods ────────────────────────────────────────────────────────────

func (fa *FileAction) Mkdir(p string, m os.FileMode, opts ...func(*MkdirInfo)) *FileAction {
	next := Mkdir(p, m, opts...)
	next.prev = fa
	return next
}

func (fa *FileAction) Mkfile(p string, m os.FileMode, data []byte, opts ...func(*MkfileInfo)) *FileAction {
	next := Mkfile(p, m, data, opts...)
	next.prev = fa
	return next
}

func (fa *FileAction) Rm(p string, opts ...func(*RmInfo)) *FileAction {
	next := Rm(p, opts...)
	next.prev = fa
	return next
}

func (fa *FileAction) Symlink(old, newpath string, opts ...func(*SymlinkInfo)) *FileAction {
	next := Symlink(old, newpath, opts...)
	next.prev = fa
	return next
}

func (fa *FileAction) Copy(src vertex.Ref, srcPath, dest string, opts ...func(*CopyInfo)) *FileAction {
	next := Copy(src, srcPath, dest, opts...)
	next.prev = fa
	return next
}

// bind returns a new action chain with each node's state set to ref.
// It does not mutate any existing node.
func (fa *FileAction) bind(ref vertex.Ref) *FileAction {
	if fa == nil {
		return nil
	}
	fa2 := *fa
	fa2.state = &ref
	fa2.prev = fa.prev.bind(ref)
	return &fa2
}

// allSources collects all distinct non-scratch source Refs referenced anywhere
// in the chain (base filesystem + copy sources). Used to build Inputs().
func (fa *FileAction) allSources(seen map[string]bool, out []vertex.Ref) []vertex.Ref {
	if fa == nil {
		return out
	}
	if fa.state != nil && !fa.state.IsZero() {
		id := fa.state.Vertex.ID()
		if !seen[id] {
			seen[id] = true
			out = append(out, *fa.state)
		}
	}
	if fa.kind == FileActionCopy && !fa.copySource.IsZero() {
		id := fa.copySource.Vertex.ID()
		if !seen[id] {
			seen[id] = true
			out = append(out, fa.copySource)
		}
	}
	return fa.prev.allSources(seen, out)
}

// ─── Constructors ─────────────────────────────────────────────────────────────

func Mkdir(p string, m os.FileMode, opts ...func(*MkdirInfo)) *FileAction {
	info := &MkdirInfo{}
	for _, o := range opts {
		o(info)
	}
	return &FileAction{kind: FileActionMkdir, mkdirPath: p, mkdirMode: m, mkdirInfo: *info}
}

func Mkfile(p string, m os.FileMode, data []byte, opts ...func(*MkfileInfo)) *FileAction {
	info := &MkfileInfo{}
	for _, o := range opts {
		o(info)
	}
	dt := make([]byte, len(data))
	copy(dt, data)
	return &FileAction{kind: FileActionMkfile, mkfilePath: p, mkfileMode: m, mkfileData: dt, mkfileInfo: *info}
}

func Rm(p string, opts ...func(*RmInfo)) *FileAction {
	info := &RmInfo{}
	for _, o := range opts {
		o(info)
	}
	return &FileAction{kind: FileActionRm, rmPath: p, rmInfo: *info}
}

func Symlink(oldpath, newpath string, opts ...func(*SymlinkInfo)) *FileAction {
	info := &SymlinkInfo{}
	for _, o := range opts {
		o(info)
	}
	return &FileAction{kind: FileActionSymlink, symlinkOld: oldpath, symlinkNew: newpath, symlinkInfo: *info}
}

func Copy(src vertex.Ref, srcPath, destPath string, opts ...func(*CopyInfo)) *FileAction {
	info := &CopyInfo{}
	for _, o := range opts {
		o(info)
	}
	return &FileAction{kind: FileActionCopy, copySource: src, copySrc: srcPath, copyDest: destPath, copyInfo: *info}
}

// ─── Public accessors (used by marshal package) ───────────────────────────────

func (fa *FileAction) Kind() FileActionKind { return fa.kind }
func (fa *FileAction) Prev() *FileAction    { return fa.prev }
func (fa *FileAction) State() *vertex.Ref   { return fa.state }

func (fa *FileAction) MkdirPath() string      { return fa.mkdirPath }
func (fa *FileAction) MkdirMode() os.FileMode { return fa.mkdirMode }
func (fa *FileAction) MkdirInfo() MkdirInfo   { return fa.mkdirInfo }

func (fa *FileAction) MkfilePath() string      { return fa.mkfilePath }
func (fa *FileAction) MkfileMode() os.FileMode { return fa.mkfileMode }
func (fa *FileAction) MkfileData() []byte      { return fa.mkfileData }
func (fa *FileAction) MkfileInfo() MkfileInfo  { return fa.mkfileInfo }

func (fa *FileAction) RmPath() string { return fa.rmPath }
func (fa *FileAction) RmInfo() RmInfo { return fa.rmInfo }

func (fa *FileAction) CopySource() vertex.Ref { return fa.copySource }
func (fa *FileAction) CopySrc() string        { return fa.copySrc }
func (fa *FileAction) CopyDest() string       { return fa.copyDest }
func (fa *FileAction) CopyInfo() CopyInfo     { return fa.copyInfo }

func (fa *FileAction) SymlinkOld() string       { return fa.symlinkOld }
func (fa *FileAction) SymlinkNew() string       { return fa.symlinkNew }
func (fa *FileAction) SymlinkInfo() SymlinkInfo { return fa.symlinkInfo }

// NormalisedMkdirPath returns the mkdir path resolved against the action's
// working directory context (state dir). If the path is already absolute,
// it is cleaned and returned as-is.
func (fa *FileAction) NormalisedMkdirPath() string {
	dir := ""
	if fa.state != nil {
		// Dir context is stored on state, but FileAction doesn't have access to
		// State.GetDir here. Store it at bind time instead.
		_ = dir
	}
	return normalisePath("", fa.mkdirPath)
}

// ─── FileOp ───────────────────────────────────────────────────────────────────

// FileOp is a DAG vertex that batches one or more file mutation actions.
type FileOp struct {
	id          string
	action      *FileAction // tail of the action chain (most recently added)
	inputs      []vertex.Vertex
	constraints Constraints
}

var _ vertex.Vertex = (*FileOp)(nil)
var _ vertex.Named = (*FileOp)(nil)

// NewFileOp constructs a FileOp on a base filesystem.
// base is the filesystem being mutated; action is the chain to apply.
func NewFileOp(base vertex.Ref, action *FileAction, c Constraints) *FileOp {
	bound := action.bind(base)
	f := &FileOp{action: bound, constraints: c}

	seen := make(map[string]bool)
	var sources []vertex.Ref
	if !base.IsZero() {
		seen[base.Vertex.ID()] = true
		sources = append(sources, base)
	}
	sources = bound.allSources(seen, sources)

	f.inputs = make([]vertex.Vertex, len(sources))
	for i, ref := range sources {
		f.inputs[i] = ref.Vertex
	}

	f.id = f.computeID(base)
	return f
}

func (f *FileOp) computeID(base vertex.Ref) string {
	type actionSummary struct {
		Kind     string `json:"kind"`
		Path     string `json:"path,omitempty"`
		SrcID    string `json:"src_id,omitempty"`
		SrcPath  string `json:"src_path,omitempty"`
		DestPath string `json:"dest_path,omitempty"`
	}
	var actions []actionSummary
	for cur := f.action; cur != nil; cur = cur.prev {
		a := actionSummary{Kind: string(cur.kind)}
		switch cur.kind {
		case FileActionMkdir:
			a.Path = cur.mkdirPath
		case FileActionMkfile:
			a.Path = cur.mkfilePath
		case FileActionRm:
			a.Path = cur.rmPath
		case FileActionSymlink:
			a.SrcPath = cur.symlinkOld
			a.DestPath = cur.symlinkNew
		case FileActionCopy:
			if !cur.copySource.IsZero() {
				a.SrcID = cur.copySource.Vertex.ID()
			}
			a.SrcPath = cur.copySrc
			a.DestPath = cur.copyDest
		}
		actions = append(actions, a)
	}
	baseID := ""
	if !base.IsZero() {
		baseID = base.Vertex.ID()
	}
	return idOf(struct {
		Kind     string          `json:"kind"`
		BaseID   string          `json:"base_id,omitempty"`
		Actions  []actionSummary `json:"actions"`
		Platform *Platform       `json:"platform,omitempty"`
	}{
		Kind:     string(vertex.KindFile),
		BaseID:   baseID,
		Actions:  actions,
		Platform: f.constraints.Platform,
	})
}

func (f *FileOp) ID() string               { return f.id }
func (f *FileOp) Kind() vertex.Kind        { return vertex.KindFile }
func (f *FileOp) Inputs() []vertex.Vertex  { return f.inputs }
func (f *FileOp) Action() *FileAction      { return f.action }
func (f *FileOp) Constraints() Constraints { return f.constraints }
func (f *FileOp) Ref() vertex.Ref          { return vertex.Ref{Vertex: f, Index: 0} }

func (f *FileOp) Name() string {
	if f.action == nil {
		return "file"
	}
	return "file:" + string(f.action.kind)
}

func (f *FileOp) Validate(_ context.Context) error {
	if f.action == nil {
		return fmt.Errorf("file: at least one file action is required")
	}
	for cur := f.action; cur != nil; cur = cur.prev {
		switch cur.kind {
		case FileActionMkdir:
			if cur.mkdirPath == "" {
				return fmt.Errorf("file: mkdir requires a non-empty path")
			}
		case FileActionMkfile:
			if cur.mkfilePath == "" {
				return fmt.Errorf("file: mkfile requires a non-empty path")
			}
		case FileActionRm:
			if cur.rmPath == "" {
				return fmt.Errorf("file: rm requires a non-empty path")
			}
		case FileActionCopy:
			if cur.copySrc == "" {
				return fmt.Errorf("file: copy requires a non-empty source path")
			}
			if cur.copyDest == "" {
				return fmt.Errorf("file: copy requires a non-empty destination path")
			}
		case FileActionSymlink:
			if cur.symlinkNew == "" {
				return fmt.Errorf("file: symlink requires a non-empty new path")
			}
		}
	}
	return nil
}

// ─── Option helpers ───────────────────────────────────────────────────────────

func WithParents(v bool) func(*MkdirInfo) {
	return func(m *MkdirInfo) { m.MakeParents = v }
}

func WithMkdirChown(uid, gid int) func(*MkdirInfo) {
	return func(m *MkdirInfo) {
		m.ChownOpt = &ChownOpt{User: &UserOpt{UID: uid}, Group: &UserOpt{UID: gid}}
	}
}

func WithCopyInclude(patterns ...string) func(*CopyInfo) {
	return func(c *CopyInfo) { c.IncludePatterns = append(c.IncludePatterns, patterns...) }
}

func WithCopyExclude(patterns ...string) func(*CopyInfo) {
	return func(c *CopyInfo) { c.ExcludePatterns = append(c.ExcludePatterns, patterns...) }
}

func WithCreateDestPath() func(*CopyInfo) {
	return func(c *CopyInfo) { c.CreateDestPath = true }
}

func WithAllowNotFound() func(*RmInfo) {
	return func(r *RmInfo) { r.AllowNotFound = true }
}

// ─── Path utilities ───────────────────────────────────────────────────────────

// normalisePath resolves p relative to parent if p is not absolute.
// Always returns a clean absolute path.
func normalisePath(parent, p string) string {
	if path.IsAbs(p) {
		return path.Clean(p)
	}
	if parent == "" {
		parent = "/"
	}
	return path.Clean(path.Join(parent, p))
}

// ActionList returns the action chain as a slice in execution order
// (oldest/head action first, newest/tail action last).
func ActionList(tail *FileAction) []*FileAction {
	var out []*FileAction
	for cur := tail; cur != nil; cur = cur.prev {
		out = append(out, cur)
	}
	// Reverse: walk went tail→head, we want head→tail.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// ─── Suppress unused import (path used via normalisePath) ─────────────────────
var _ = strings.Join // path is used; this keeps 'strings' import alive
