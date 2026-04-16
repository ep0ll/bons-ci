// Package http provides the HTTP/HTTPS source operation for llbx.
//
// Example
//
//	src := http.New(
//	    http.WithURL("https://example.com/archive.tar.gz"),
//	    http.WithChecksum("sha256:abcdef..."),
//	    http.WithFilename("archive.tar.gz"),
//	    http.WithPermissions(0644),
//	)
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

// ─── Config ───────────────────────────────────────────────────────────────────

// Config holds all parameters for an HTTP source op.
type Config struct {
	// URL is the target HTTP/HTTPS URL. Required.
	URL string

	// Checksum is the expected SHA256 digest of the downloaded content.
	// Providing a checksum enables content verification and stable caching.
	Checksum digest.Digest

	// Filename overrides the filename used when the download is stored.
	// Defaults to the last path segment of the URL.
	Filename string

	// Permissions sets the file mode of the downloaded file.
	Permissions os.FileMode

	// UID is the owner user ID.
	UID int
	// GID is the owner group ID.
	GID int

	// AuthHeaderSecret is the name of a BuildKit secret whose value is used
	// as the "Authorization" HTTP request header.
	AuthHeaderSecret string

	// Headers carries additional HTTP request headers.
	Headers *RequestHeaders

	// Constraints are per-vertex LLB constraints.
	Constraints core.Constraints
}

// RequestHeaders are optional extra headers sent with the HTTP request.
type RequestHeaders struct {
	// Accept sets the "Accept" header value.
	Accept string
	// UserAgent overrides the User-Agent header.
	UserAgent string
}

// ─── Option ───────────────────────────────────────────────────────────────────

// Option is a functional option for Config.
type Option func(*Config)

// WithURL sets the target URL.
func WithURL(url string) Option { return func(c *Config) { c.URL = url } }

// WithChecksum sets the expected content checksum.
func WithChecksum(dgst digest.Digest) Option { return func(c *Config) { c.Checksum = dgst } }

// WithFilename overrides the stored filename.
func WithFilename(name string) Option { return func(c *Config) { c.Filename = name } }

// WithPermissions sets the downloaded file's mode.
func WithPermissions(m os.FileMode) Option { return func(c *Config) { c.Permissions = m } }

// WithOwner sets the UID and GID for the downloaded file.
func WithOwner(uid, gid int) Option {
	return func(c *Config) { c.UID = uid; c.GID = gid }
}

// WithAuthHeaderSecret sets the secret name for HTTP auth.
func WithAuthHeaderSecret(secretName string) Option {
	return func(c *Config) { c.AuthHeaderSecret = secretName }
}

// WithRequestHeaders sets additional HTTP request headers.
func WithRequestHeaders(h RequestHeaders) Option {
	return func(c *Config) { c.Headers = &h }
}

// WithConstraintsOption applies a core.ConstraintsOption.
func WithConstraintsOption(opt core.ConstraintsOption) Option {
	return func(c *Config) { opt(&c.Constraints) }
}

// ─── Vertex ───────────────────────────────────────────────────────────────────

// Vertex is the llbx implementation of the HTTP source op.
type Vertex struct {
	config Config
	cache  marshal.Cache
	attrs  map[string]string
}

// New constructs an HTTP source Vertex.
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
	if cfg.Headers != nil {
		const prefix = pb.AttrHTTPHeaderPrefix
		if cfg.Headers.Accept != "" {
			attrs[prefix+"accept"] = cfg.Headers.Accept
		}
		if cfg.Headers.UserAgent != "" {
			attrs[prefix+"user-agent"] = cfg.Headers.UserAgent
		}
		core.ConstraintsAddCap(&cfg.Constraints, pb.CapSourceHTTPHeader)
	}
	core.ConstraintsAddCap(&cfg.Constraints, pb.CapSourceHTTP)
	v.attrs = attrs
}

// ─── core.Vertex ──────────────────────────────────────────────────────────────

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
	h := marshal.Acquire(&v.cache)
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
		return nil, fmt.Errorf("http.Vertex.Marshal: %w", err)
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

// Config returns a copy of the vertex's configuration.
func (v *Vertex) Config() Config { return v.config }

// WithOption returns a new Vertex with the given option applied.
func (v *Vertex) WithOption(opt Option) *Vertex {
	newCfg := v.config
	opt(&newCfg)
	nv := &Vertex{config: newCfg}
	nv.buildAttrs()
	return nv
}

// Output returns a core.Output referencing output slot 0.
func (v *Vertex) Output() core.Output { return &vertexOutput{vertex: v} }

type vertexOutput struct{ vertex *Vertex }

func (o *vertexOutput) Vertex(_ context.Context, _ *core.Constraints) core.Vertex { return o.vertex }
func (o *vertexOutput) ToInput(ctx context.Context, c *core.Constraints) (*pb.Input, error) {
	mv, err := o.vertex.Marshal(ctx, c)
	if err != nil {
		return nil, err
	}
	return &pb.Input{Digest: string(mv.Digest), Index: 0}, nil
}

var (
	_ core.Vertex         = (*Vertex)(nil)
	_ core.MutatingVertex = (*Vertex)(nil)
)
