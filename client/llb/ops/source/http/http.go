// Package http provides the HTTP/HTTPS fetch source operation.
package http

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"github.com/bons/bons-ci/client/llb/core"
	"github.com/bons/bons-ci/client/llb/marshal"
	"github.com/moby/buildkit/solver/pb"
	digest "github.com/opencontainers/go-digest"
)

type Config struct {
	URL              string // required
	Checksum         digest.Digest
	Filename         string
	Permissions      os.FileMode
	UID, GID         int
	AuthHeaderSecret string
	AcceptHeader     string
	UserAgent        string
	Constraints      core.Constraints
}

type Option func(*Config)

func WithURL(url string) Option            { return func(c *Config) { c.URL = url } }
func WithChecksum(d digest.Digest) Option  { return func(c *Config) { c.Checksum = d } }
func WithFilename(name string) Option      { return func(c *Config) { c.Filename = name } }
func WithPermissions(m os.FileMode) Option { return func(c *Config) { c.Permissions = m } }
func WithOwner(uid, gid int) Option        { return func(c *Config) { c.UID = uid; c.GID = gid } }
func WithAuthHeaderSecret(s string) Option { return func(c *Config) { c.AuthHeaderSecret = s } }
func WithAcceptHeader(v string) Option     { return func(c *Config) { c.AcceptHeader = v } }
func WithUserAgent(ua string) Option       { return func(c *Config) { c.UserAgent = ua } }
func WithConstraintsOption(opt core.ConstraintsOption) Option {
	return func(c *Config) { opt(&c.Constraints) }
}

type Vertex struct {
	config Config
	cache  marshal.Cache
	attrs  map[string]string
}

func New(opts ...Option) (*Vertex, error) {
	cfg := Config{}
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.URL == "" {
		return nil, fmt.Errorf("http.New: URL is required")
	}
	v := &Vertex{config: cfg}
	v.buildAttrs()
	return v, nil
}

func (v *Vertex) buildAttrs() {
	cfg := &v.config
	attrs := make(map[string]string)
	if cfg.Checksum != "" {
		attrs[pb.AttrHTTPChecksum] = cfg.Checksum.String()
		core.ConstraintsAddCap(&cfg.Constraints, pb.CapSourceHTTPChecksum)
	}
	if cfg.Filename != "" {
		attrs[pb.AttrHTTPFilename] = cfg.Filename
	}
	if cfg.Permissions != 0 {
		attrs[pb.AttrHTTPPerm] = "0" + strconv.FormatInt(int64(cfg.Permissions), 8)
		core.ConstraintsAddCap(&cfg.Constraints, pb.CapSourceHTTPPerm)
	}
	if cfg.UID != 0 {
		attrs[pb.AttrHTTPUID] = strconv.Itoa(cfg.UID)
		core.ConstraintsAddCap(&cfg.Constraints, pb.CapSourceHTTPUIDGID)
	}
	if cfg.GID != 0 {
		attrs[pb.AttrHTTPGID] = strconv.Itoa(cfg.GID)
		core.ConstraintsAddCap(&cfg.Constraints, pb.CapSourceHTTPUIDGID)
	}
	if cfg.AuthHeaderSecret != "" {
		attrs[pb.AttrHTTPAuthHeaderSecret] = cfg.AuthHeaderSecret
		core.ConstraintsAddCap(&cfg.Constraints, pb.CapSourceHTTPAuth)
	}
	if cfg.AcceptHeader != "" || cfg.UserAgent != "" {
		if cfg.AcceptHeader != "" {
			attrs[pb.AttrHTTPHeaderPrefix+"accept"] = cfg.AcceptHeader
		}
		if cfg.UserAgent != "" {
			attrs[pb.AttrHTTPHeaderPrefix+"user-agent"] = cfg.UserAgent
		}
		core.ConstraintsAddCap(&cfg.Constraints, pb.CapSourceHTTPHeader)
	}
	core.ConstraintsAddCap(&cfg.Constraints, pb.CapSourceHTTP)
	v.attrs = attrs
}

func (v *Vertex) Type() core.VertexType { return core.VertexTypeSource }
func (v *Vertex) Inputs() []core.Edge   { return nil }
func (v *Vertex) Outputs() []core.OutputSlot {
	return []core.OutputSlot{{Index: 0, Description: "downloaded file"}}
}

func (v *Vertex) Validate(_ context.Context, _ *core.Constraints) error {
	if v.config.URL == "" {
		return &core.ValidationError{Field: "URL", Cause: fmt.Errorf("must not be empty")}
	}
	return nil
}

func (v *Vertex) Marshal(ctx context.Context, c *core.Constraints) (*core.MarshaledVertex, error) {
	h := v.cache.Acquire()
	defer h.Release()
	if dgst, bytes, meta, srcs, err := h.Load(c); err == nil {
		return &core.MarshaledVertex{Digest: dgst, Bytes: bytes, Metadata: meta, SourceLocations: srcs}, nil
	}
	pop, md := marshal.MarshalConstraints(c, &v.config.Constraints)
	pop.Platform = nil
	pop.Op = &pb.Op_Source{Source: &pb.SourceOp{
		Identifier: v.config.URL,
		Attrs:      v.attrs,
	}}
	bytes, err := marshal.DeterministicMarshal(pop)
	if err != nil {
		return nil, fmt.Errorf("http.Marshal: %w", err)
	}
	dgst, bytes, meta, srcs, _ := h.Store(bytes, md, c.SourceLocations, c)
	return &core.MarshaledVertex{Digest: dgst, Bytes: bytes, Metadata: meta, SourceLocations: srcs}, nil
}

func (v *Vertex) WithInputs(inputs []core.Edge) (core.Vertex, error) {
	if len(inputs) != 0 {
		return nil, &core.IncompatibleInputsError{VertexType: v.Type(), Want: "exactly 0", Got: len(inputs)}
	}
	return v, nil
}

func (v *Vertex) Config() Config      { return v.config }
func (v *Vertex) Output() core.Output { return &httpOutput{v: v} }

type httpOutput struct{ v *Vertex }

func (o *httpOutput) Vertex(_ context.Context, _ *core.Constraints) core.Vertex { return o.v }
func (o *httpOutput) ToInput(ctx context.Context, c *core.Constraints) (*pb.Input, error) {
	mv, err := o.v.Marshal(ctx, c)
	if err != nil {
		return nil, err
	}
	return &pb.Input{Digest: string(mv.Digest), Index: 0}, nil
}

var (
	_ core.Vertex         = (*Vertex)(nil)
	_ core.MutatingVertex = (*Vertex)(nil)
)
