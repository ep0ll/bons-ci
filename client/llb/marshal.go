package llb

import (
	"io"
	"sync"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/buildkit/solver/pb"
	digest "github.com/opencontainers/go-digest"
	"google.golang.org/protobuf/proto"
)

// ─────────────────────────────────────────────────────────────────────────────
// Definition
// ─────────────────────────────────────────────────────────────────────────────

// Definition is the LLB definition structure with per-vertex metadata entries.
// It corresponds to the Definition structure defined in solver/pb.Definition.
type Definition struct {
	Def         [][]byte
	Metadata    map[digest.Digest]OpMetadata
	Source      *pb.Source
	Constraints *Constraints
}

// ToPB converts the Definition to its protobuf wire representation.
func (def *Definition) ToPB() *pb.Definition {
	metas := make(map[string]*pb.OpMetadata, len(def.Metadata))
	for dgst, meta := range def.Metadata {
		metas[string(dgst)] = meta.ToPB()
	}
	return &pb.Definition{
		Def:      def.Def,
		Source:   def.Source,
		Metadata: metas,
	}
}

// FromPB populates the Definition from a protobuf wire representation.
func (def *Definition) FromPB(x *pb.Definition) {
	def.Def = x.Def
	def.Source = x.Source
	def.Metadata = make(map[digest.Digest]OpMetadata, len(x.Metadata))
	for dgst, meta := range x.Metadata {
		def.Metadata[digest.Digest(dgst)] = NewOpMetadata(meta)
	}
}

// Head returns the digest of the terminal (head) vertex in the definition.
func (def *Definition) Head() (digest.Digest, error) {
	if len(def.Def) == 0 {
		return "", nil
	}

	last := def.Def[len(def.Def)-1]

	var pop pb.Op
	if err := pop.UnmarshalVT(last); err != nil {
		return "", err
	}
	if len(pop.Inputs) == 0 {
		return "", nil
	}
	return digest.Digest(pop.Inputs[0].Digest), nil
}

// WriteTo serializes the Definition to a writer.
func WriteTo(def *Definition, w io.Writer) error {
	b, err := def.ToPB().Marshal()
	if err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}

// ReadFrom deserializes a Definition from a reader.
func ReadFrom(r io.Reader) (*Definition, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	var pbDef pb.Definition
	if err := pbDef.UnmarshalVT(b); err != nil {
		return nil, err
	}
	var def Definition
	def.FromPB(&pbDef)
	return &def, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// MarshalCache
// ─────────────────────────────────────────────────────────────────────────────

// MarshalCache provides a per-Constraints memoization layer for vertex
// marshalling. Each vertex embeds a MarshalCache to avoid redundant protobuf
// serialization when the same vertex is marshalled with the same constraints.
//
// Thread safety: the caller acquires the cache (locking the mutex), performs
// Load/Store, then releases. This RAII pattern ensures the cache is never
// accessed concurrently for the same vertex.
type MarshalCache struct {
	mu    sync.Mutex
	cache map[*Constraints]*marshalCacheResult
}

// MarshalCacheInstance is an acquired MarshalCache handle. It must be released
// after use.
type MarshalCacheInstance struct {
	*MarshalCache
}

// marshalCacheResult holds the cached marshal output.
type marshalCacheResult struct {
	digest digest.Digest
	dt     []byte
	md     *pb.OpMetadata
	srcs   []*SourceLocation
}

// Acquire locks the cache and returns a handle for Load/Store operations.
// The caller MUST call Release when done.
func (mc *MarshalCache) Acquire() *MarshalCacheInstance {
	mc.mu.Lock()
	return &MarshalCacheInstance{mc}
}

// Load checks whether a cached result exists for the given constraints.
func (mc *MarshalCacheInstance) Load(c *Constraints) (digest.Digest, []byte, *pb.OpMetadata, []*SourceLocation, error) {
	res, ok := mc.cache[c]
	if !ok {
		return "", nil, nil, nil, cerrdefs.ErrNotFound
	}
	return res.digest, res.dt, res.md, res.srcs, nil
}

// Store saves a marshal result keyed by the constraints and returns the
// computed digest alongside the stored values.
func (mc *MarshalCacheInstance) Store(dt []byte, md *pb.OpMetadata, srcs []*SourceLocation, c *Constraints) (digest.Digest, []byte, *pb.OpMetadata, []*SourceLocation, error) {
	res := &marshalCacheResult{
		digest: digest.FromBytes(dt),
		dt:     dt,
		md:     md,
		srcs:   srcs,
	}
	if mc.cache == nil {
		mc.cache = make(map[*Constraints]*marshalCacheResult)
	}
	mc.cache[c] = res
	return res.digest, res.dt, res.md, res.srcs, nil
}

// Release unlocks the cache mutex.
func (mc *MarshalCacheInstance) Release() {
	mc.mu.Unlock()
}

// ─────────────────────────────────────────────────────────────────────────────
// Deterministic marshal
// ─────────────────────────────────────────────────────────────────────────────

// DeterministicMarshal serializes a protobuf message with deterministic output,
// ensuring the same logical message always produces the same byte sequence.
func DeterministicMarshal[M proto.Message](m M) ([]byte, error) {
	return proto.MarshalOptions{Deterministic: true}.Marshal(m)
}
