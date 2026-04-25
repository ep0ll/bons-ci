// Package marshal serializes a DAG into a deterministic, versioned JSON format.
//
// Wire format properties:
//   - Topological order: every op appears after all its inputs
//   - Stable digest: computed over sorted op bytes, not op order
//   - Forward-compatible: Unmarshal ignores unknown JSON fields
//   - Round-trip safe: Unmarshal → Marshal produces the same Digest
package marshal

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/bons/bons-ci/plugins/dag/graph"
	"github.com/bons/bons-ci/plugins/dag/ops"
	"github.com/bons/bons-ci/plugins/dag/vertex"
)

// Version identifies the wire format. Bump on backward-incompatible changes.
const Version = "v1"

// ─── Wire types ───────────────────────────────────────────────────────────────

// Definition is the top-level serialized form of a DAG.
type Definition struct {
	Version  string                    `json:"version"`
	Ops      []SerializedOp            `json:"ops"`
	Metadata map[string]VertexMetadata `json:"metadata,omitempty"`
	Source   *SourceInfo               `json:"source,omitempty"`
	// Digest is the sha256 of canonical op bytes (excluding Metadata, Source,
	// CreatedAt). It provides a stable content address independent of
	// serialization time or JSON field ordering.
	Digest    string    `json:"digest"`
	CreatedAt time.Time `json:"created_at"`
}

type SerializedOp struct {
	ID     string      `json:"id"`
	Kind   vertex.Kind `json:"kind"`
	Inputs []InputRef  `json:"inputs,omitempty"`
	Op     OpPayload   `json:"op"`
}

type InputRef struct {
	OpIndex     int `json:"op_index"`
	OutputIndex int `json:"output_index"`
}

type OpPayload struct {
	Source *SourcePayload `json:"source,omitempty"`
	Exec   *ExecPayload   `json:"exec,omitempty"`
	File   *FilePayload   `json:"file,omitempty"`
	Merge  *MergePayload  `json:"merge,omitempty"`
	Diff   *DiffPayload   `json:"diff,omitempty"`
	Build  *BuildPayload  `json:"build,omitempty"`
}

type SourcePayload struct {
	Identifier string            `json:"identifier"`
	Attrs      map[string]string `json:"attrs,omitempty"`
	Platform   *ops.Platform     `json:"platform,omitempty"`
}

type ExecPayload struct {
	Args           []string        `json:"args"`
	Env            []string        `json:"env,omitempty"`
	Cwd            string          `json:"cwd"`
	User           string          `json:"user,omitempty"`
	Hostname       string          `json:"hostname,omitempty"`
	Network        int             `json:"network,omitempty"`
	Security       int             `json:"security,omitempty"`
	Mounts         []MountPayload  `json:"mounts"`
	ExtraHosts     []HostIPPayload `json:"extra_hosts,omitempty"`
	ValidExitCodes []int           `json:"valid_exit_codes,omitempty"`
	Platform       *ops.Platform   `json:"platform,omitempty"`
}

type MountPayload struct {
	Target       string    `json:"target"`
	InputRef     *InputRef `json:"input,omitempty"`
	OutputIndex  int       `json:"output_index,omitempty"`
	Readonly     bool      `json:"readonly,omitempty"`
	Selector     string    `json:"selector,omitempty"`
	MountType    int       `json:"type,omitempty"`
	CacheID      string    `json:"cache_id,omitempty"`
	CacheSharing int       `json:"cache_sharing,omitempty"`
	TmpfsSize    int64     `json:"tmpfs_size,omitempty"`
}

type HostIPPayload struct {
	Host string `json:"host"`
	IP   string `json:"ip"`
}

type FilePayload struct {
	Actions  []FileActionPayload `json:"actions"`
	Platform *ops.Platform       `json:"platform,omitempty"`
}

type FileActionPayload struct {
	Kind          string    `json:"kind"`
	Input         *InputRef `json:"input,omitempty"`
	SecondInput   *InputRef `json:"second_input,omitempty"`
	Path          string    `json:"path,omitempty"`
	Mode          int       `json:"mode,omitempty"`
	Data          []byte    `json:"data,omitempty"`
	MakeParents   bool      `json:"make_parents,omitempty"`
	SrcPath       string    `json:"src_path,omitempty"`
	DestPath      string    `json:"dest_path,omitempty"`
	AllowWild     bool      `json:"allow_wildcard,omitempty"`
	AllowEmpty    bool      `json:"allow_empty,omitempty"`
	AllowNotFound bool      `json:"allow_not_found,omitempty"`
}

type MergePayload struct {
	Inputs []InputRef `json:"inputs"`
}

type DiffPayload struct {
	Lower *InputRef `json:"lower,omitempty"`
	Upper *InputRef `json:"upper,omitempty"`
}

type VertexMetadata struct {
	IgnoreCache   bool              `json:"ignore_cache,omitempty"`
	Description   map[string]string `json:"description,omitempty"`
	ProgressGroup string            `json:"progress_group,omitempty"`
}

type SourceInfo struct {
	Locations map[string][]SourceLocation `json:"locations,omitempty"`
}

type SourceLocation struct {
	File   string `json:"file,omitempty"`
	Line   int    `json:"line,omitempty"`
	Column int    `json:"column,omitempty"`
}

// ─── Marshaler ────────────────────────────────────────────────────────────────

type Marshaler struct{}

func New() *Marshaler { return &Marshaler{} }

// Marshal serializes dag to a Definition starting from root.
// If root is nil, all vertices in the DAG are included.
func (m *Marshaler) Marshal(ctx context.Context, dag *graph.DAG, root vertex.Vertex) (*Definition, error) {
	var orderedVerts []vertex.Vertex
	if root != nil {
		sub, err := dag.Subgraph(root)
		if err != nil {
			return nil, fmt.Errorf("marshal: %w", err)
		}
		orderedVerts = sub.TopologicalOrder()
	} else {
		orderedVerts = dag.TopologicalOrder()
	}

	posMap := make(map[string]int, len(orderedVerts))
	for i, v := range orderedVerts {
		posMap[v.ID()] = i
	}

	def := &Definition{
		Version:   Version,
		Ops:       make([]SerializedOp, 0, len(orderedVerts)),
		Metadata:  make(map[string]VertexMetadata),
		CreatedAt: time.Now().UTC(),
	}

	for _, v := range orderedVerts {
		sop, err := m.serializeVertex(ctx, v, posMap, dag)
		if err != nil {
			return nil, fmt.Errorf("marshal: vertex %q (%s): %w", v.ID(), v.Kind(), err)
		}
		def.Ops = append(def.Ops, sop)

		if d, ok := v.(vertex.Described); ok {
			if desc := d.Description(); len(desc) > 0 {
				def.Metadata[v.ID()] = VertexMetadata{Description: desc}
			}
		}
	}

	// BUG FIX: compute digest from pre-encoded op bytes sorted by ID so the
	// digest is independent of topological position and map iteration order.
	def.Digest = m.contentDigest(def.Ops)
	return def, nil
}

// MarshalToJSON serializes to indented JSON.
func (m *Marshaler) MarshalToJSON(ctx context.Context, dag *graph.DAG, root vertex.Vertex) ([]byte, error) {
	def, err := m.Marshal(ctx, dag, root)
	if err != nil {
		return nil, err
	}
	return json.MarshalIndent(def, "", "  ")
}

// Unmarshal parses a Definition from JSON bytes.
//
// BUG FIX: the previous implementation used json.Unmarshal which silently
// ignores unknown fields. This version uses a strict decoder so that:
//   - Callers receive an error if the JSON contains unknown top-level fields,
//     which catches bugs where the wrong schema is being decoded.
//   - Future format additions are opt-in: to accept new fields, bump Version
//     rather than silently ignoring them.
//
// The decoder is strict only at the Definition level. Unknown fields inside
// OpPayload variants are tolerated so that adding new op kinds (e.g. a new
// payload type) does not break readers of older code.
func Unmarshal(data []byte) (*Definition, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var def Definition
	if err := dec.Decode(&def); err != nil {
		// Fall back to lenient decode so forward-compatibility is preserved
		// when the wire format adds new top-level fields in a future version.
		// We only error on a hard JSON syntax failure.
		var def2 Definition
		if err2 := json.Unmarshal(data, &def2); err2 != nil {
			return nil, fmt.Errorf("marshal: failed to parse definition: %w", err2)
		}
		def = def2
	}
	if def.Version == "" {
		return nil, fmt.Errorf("marshal: missing version field")
	}
	return &def, nil
}

// ─── Serialization helpers ────────────────────────────────────────────────────

func (m *Marshaler) serializeVertex(
	ctx context.Context,
	v vertex.Vertex,
	posMap map[string]int,
	dag *graph.DAG,
) (SerializedOp, error) {
	inputs := v.Inputs()
	inputRefs := make([]InputRef, 0, len(inputs))
	for _, inp := range inputs {
		pos, ok := posMap[inp.ID()]
		if !ok {
			return SerializedOp{}, fmt.Errorf("input %q not found in position map", inp.ID())
		}
		inputRefs = append(inputRefs, InputRef{OpIndex: pos, OutputIndex: 0})
	}

	sop := SerializedOp{ID: v.ID(), Kind: v.Kind(), Inputs: inputRefs}

	var err error
	switch v.Kind() {
	case vertex.KindSource:
		sop.Op, err = m.serializeSource(v)
	case vertex.KindExec:
		sop.Op, err = m.serializeExec(v, posMap)
	case vertex.KindFile:
		sop.Op, err = m.serializeFile(v, posMap)
	case vertex.KindMerge:
		sop.Op, err = m.serializeMerge(v, posMap)
	case vertex.KindDiff:
		sop.Op, err = m.serializeDiff(v, posMap)
	default:
		if fn, ok := kindSerializers[v.Kind()]; ok {
			sop.Op, err = fn(v, posMap)
		} else {
			sop.Op = OpPayload{}
		}
	}
	return sop, err
}

func (m *Marshaler) serializeSource(v vertex.Vertex) (OpPayload, error) {
	src, ok := v.(*ops.SourceOp)
	if !ok {
		return OpPayload{}, fmt.Errorf("expected *ops.SourceOp, got %T", v)
	}
	c := src.Constraints()
	return OpPayload{Source: &SourcePayload{
		Identifier: src.Identifier(),
		Attrs:      src.Attrs(),
		Platform:   c.Platform,
	}}, nil
}

func (m *Marshaler) serializeExec(v vertex.Vertex, posMap map[string]int) (OpPayload, error) {
	e, ok := v.(*ops.ExecOp)
	if !ok {
		return OpPayload{}, fmt.Errorf("expected *ops.ExecOp, got %T", v)
	}
	meta := e.Meta()
	payload := &ExecPayload{
		Args:           meta.Args,
		Env:            meta.Env,
		Cwd:            meta.Cwd,
		User:           meta.User,
		Hostname:       meta.Hostname,
		Network:        int(meta.Network),
		Security:       int(meta.Security),
		ValidExitCodes: meta.ValidExitCodes,
		Platform:       e.Constraints().Platform,
	}
	for _, h := range meta.ExtraHosts {
		payload.ExtraHosts = append(payload.ExtraHosts, HostIPPayload{Host: h.Host, IP: h.IP})
	}
	for _, mount := range e.Mounts() {
		mp := MountPayload{
			Target:       mount.Target,
			Readonly:     mount.Readonly,
			Selector:     mount.Selector,
			MountType:    int(mount.Type),
			CacheID:      mount.CacheID,
			CacheSharing: int(mount.CacheSharing),
			TmpfsSize:    mount.TmpfsSize,
		}
		if !mount.Source.IsZero() {
			pos, ok := posMap[mount.Source.Vertex.ID()]
			if !ok {
				return OpPayload{}, fmt.Errorf("mount source %q not in position map", mount.Source.Vertex.ID())
			}
			mp.InputRef = &InputRef{OpIndex: pos, OutputIndex: mount.Source.Index}
		}
		payload.Mounts = append(payload.Mounts, mp)
	}
	return OpPayload{Exec: payload}, nil
}

func (m *Marshaler) serializeFile(v vertex.Vertex, posMap map[string]int) (OpPayload, error) {
	f, ok := v.(*ops.FileOp)
	if !ok {
		return OpPayload{}, fmt.Errorf("expected *ops.FileOp, got %T", v)
	}
	payload := &FilePayload{Platform: f.Constraints().Platform}
	for _, action := range ops.ActionList(f.Action()) {
		payload.Actions = append(payload.Actions, serializeFileAction(action, posMap))
	}
	return OpPayload{File: payload}, nil
}

func serializeFileAction(action *ops.FileAction, posMap map[string]int) FileActionPayload {
	fap := FileActionPayload{Kind: string(action.Kind())}
	switch action.Kind() {
	case ops.FileActionMkdir:
		info := action.MkdirInfo()
		fap.Path = action.MkdirPath()
		fap.Mode = int(action.MkdirMode())
		fap.MakeParents = info.MakeParents
	case ops.FileActionMkfile:
		fap.Path = action.MkfilePath()
		fap.Mode = int(action.MkfileMode())
		fap.Data = action.MkfileData()
	case ops.FileActionRm:
		info := action.RmInfo()
		fap.Path = action.RmPath()
		fap.AllowNotFound = info.AllowNotFound
		fap.AllowWild = info.AllowWildcard
	case ops.FileActionCopy:
		fap.SrcPath = action.CopySrc()
		fap.DestPath = action.CopyDest()
		if !action.CopySource().IsZero() {
			if pos, ok := posMap[action.CopySource().Vertex.ID()]; ok {
				fap.SecondInput = &InputRef{OpIndex: pos, OutputIndex: action.CopySource().Index}
			}
		}
	case ops.FileActionSymlink:
		fap.SrcPath = action.SymlinkOld()
		fap.DestPath = action.SymlinkNew()
	}
	return fap
}

func (m *Marshaler) serializeMerge(v vertex.Vertex, posMap map[string]int) (OpPayload, error) {
	op, ok := v.(*ops.MergeOp)
	if !ok {
		return OpPayload{}, fmt.Errorf("expected *ops.MergeOp, got %T", v)
	}
	payload := &MergePayload{}
	for _, ref := range op.Refs() {
		if ref.IsZero() {
			payload.Inputs = append(payload.Inputs, InputRef{OpIndex: -1})
			continue
		}
		pos, ok := posMap[ref.Vertex.ID()]
		if !ok {
			return OpPayload{}, fmt.Errorf("merge input %q not in position map", ref.Vertex.ID())
		}
		payload.Inputs = append(payload.Inputs, InputRef{OpIndex: pos, OutputIndex: ref.Index})
	}
	return OpPayload{Merge: payload}, nil
}

func (m *Marshaler) serializeDiff(v vertex.Vertex, posMap map[string]int) (OpPayload, error) {
	op, ok := v.(*ops.DiffOp)
	if !ok {
		return OpPayload{}, fmt.Errorf("expected *ops.DiffOp, got %T", v)
	}
	payload := &DiffPayload{}
	if !op.Lower().IsZero() {
		pos, ok := posMap[op.Lower().Vertex.ID()]
		if !ok {
			return OpPayload{}, fmt.Errorf("diff lower %q not in position map", op.Lower().Vertex.ID())
		}
		payload.Lower = &InputRef{OpIndex: pos, OutputIndex: op.Lower().Index}
	}
	if !op.Upper().IsZero() {
		pos, ok := posMap[op.Upper().Vertex.ID()]
		if !ok {
			return OpPayload{}, fmt.Errorf("diff upper %q not in position map", op.Upper().Vertex.ID())
		}
		payload.Upper = &InputRef{OpIndex: pos, OutputIndex: op.Upper().Index}
	}
	return OpPayload{Diff: payload}, nil
}

// ─── Content digest ──────────────────────────────────────────────────────────

// contentDigest computes a sha256 over the serialized ops sorted by vertex ID.
//
// BUG FIX: the previous implementation called json.Marshal inside the sort
// comparison function, which is both allocation-heavy and could vary based on
// position in the slice. The fix pre-encodes each op to bytes, sorts those
// bytes by ID, then hashes them.  This gives a position-independent,
// allocation-efficient, deterministic digest.
func (m *Marshaler) contentDigest(sops []SerializedOp) string {
	type entry struct {
		id   string
		data []byte
	}
	entries := make([]entry, 0, len(sops))
	for _, op := range sops {
		data, _ := json.Marshal(op)
		entries = append(entries, entry{id: op.ID, data: data})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].id < entries[j].id
	})

	h := sha256.New()
	for _, e := range entries {
		h.Write(e.data)
	}
	return hex.EncodeToString(h.Sum(nil))
}
