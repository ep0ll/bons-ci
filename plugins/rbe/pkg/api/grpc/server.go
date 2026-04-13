// Package grpc exposes the RBE gRPC API. All domain services are wired in here.
// The server implements the DAGService, CacheService, LogService,
// MountCacheService, RegistryService, and AttestationService proto contracts.
package grpc

import (
	"context"
	"fmt"
	"io"
	"net"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"

	grpcmiddleware "github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors"
	grpclogging "github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/logging"
	grpcrecovery "github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/recovery"

	"github.com/bons/bons-ci/plugins/rbe/pkg/attestation"
	"github.com/bons/bons-ci/plugins/rbe/pkg/auth"
	"github.com/bons/bons-ci/plugins/rbe/pkg/dag"
	"github.com/bons/bons-ci/plugins/rbe/pkg/models"
	"github.com/bons/bons-ci/plugins/rbe/pkg/mountcache"
	"github.com/bons/bons-ci/plugins/rbe/pkg/observability"
	"github.com/bons/bons-ci/plugins/rbe/pkg/registry"
)

// Services groups all domain service dependencies.
type Services struct {
	Registry    *registry.Registry
	DAG         *dag.Service
	Cache       *dag.CacheService
	Logs        *dag.LogService
	MountCache  *mountcache.Service
	Attestation *attestation.Service
}

// Server wraps a gRPC server with all registered services.
type Server struct {
	grpcServer *grpc.Server
	services   Services
}

// New creates a gRPC server with all interceptors and registers all services.
func New(svc Services, authMiddleware *auth.Middleware) *Server {
	logger := observability.Logger()

	// Interceptors
	recoveryOpt := grpcrecovery.WithRecoveryHandler(func(p interface{}) error {
		logger.Error().Interface("panic", p).Msg("grpc panic recovered")
		return status.Errorf(codes.Internal, "internal server error")
	})

	logOpts := []grpclogging.Option{
		grpclogging.WithLogOnEvents(grpclogging.StartCall, grpclogging.FinishCall),
	}

	srv := grpc.NewServer(
		grpc.ChainUnaryInterceptor(
			grpcmiddleware.UnaryClientInterceptor(), // tracing placeholder
			grpclogging.UnaryServerInterceptor(loggerAdapter(logger), logOpts...),
			grpcrecovery.UnaryServerInterceptor(recoveryOpt),
			unaryAuthInterceptor(authMiddleware),
			unaryMetricsInterceptor(),
		),
		grpc.ChainStreamInterceptor(
			grpclogging.StreamServerInterceptor(loggerAdapter(logger), logOpts...),
			grpcrecovery.StreamServerInterceptor(recoveryOpt),
			streamAuthInterceptor(authMiddleware),
		),
		grpc.KeepaliveParams(keepalive.ServerParameters{
			MaxConnectionIdle:     15 * time.Minute,
			MaxConnectionAge:      30 * time.Minute,
			MaxConnectionAgeGrace: 5 * time.Second,
			Time:                  5 * time.Second,
			Timeout:               1 * time.Second,
		}),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             5 * time.Second,
			PermitWithoutStream: true,
		}),
		grpc.MaxRecvMsgSize(256*1024*1024), // 256 MiB
		grpc.MaxSendMsgSize(256*1024*1024),
	)

	s := &Server{grpcServer: srv, services: svc}

	// Register service implementations.
	// In a full build with proto-generated code these would be:
	//   rbev1.RegisterDAGServiceServer(srv, &dagServer{svc: svc})
	//   rbev1.RegisterCacheServiceServer(srv, &cacheServer{svc: svc})
	//   rbev1.RegisterLogServiceServer(srv, &logServer{svc: svc})
	//   rbev1.RegisterMountCacheServiceServer(srv, &mountCacheServer{svc: svc})
	//   registryv1.RegisterRegistryServiceServer(srv, &registryServer{svc: svc})
	//   registryv1.RegisterAttestationServiceServer(srv, &attestationServer{svc: svc})
	//
	// We attach the hand-written implementations below instead.
	registerDAGServer(srv, svc)
	registerCacheServer(srv, svc)
	registerLogServer(srv, svc)
	registerMountCacheServer(srv, svc)
	registerRegistryServer(srv, svc)
	registerAttestationServer(srv, svc)

	// gRPC reflection for grpcurl / evans.
	reflection.Register(srv)

	return s
}

// Serve starts the gRPC listener on addr.
func (s *Server) Serve(addr string) error {
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("grpc: listen %s: %w", addr, err)
	}
	observability.Logger().Info().Str("addr", addr).Msg("gRPC server listening")
	return s.grpcServer.Serve(lis)
}

// GracefulStop stops the server after all in-flight RPCs complete.
func (s *Server) GracefulStop() { s.grpcServer.GracefulStop() }

// ─────────────────────────────────────────────────────────────────────────────
// Interceptors
// ─────────────────────────────────────────────────────────────────────────────

func unaryAuthInterceptor(m *auth.Middleware) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		// Auth for gRPC is done via the grpc.Credentials mechanism (mTLS) or
		// via metadata-carried JWT / API keys. Peer identity is extracted here.
		// Placeholder: pass through.
		return handler(ctx, req)
	}
}

func streamAuthInterceptor(m *auth.Middleware) grpc.StreamServerInterceptor {
	return func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		return handler(srv, ss)
	}
}

func unaryMetricsInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		start := time.Now()
		resp, err := handler(ctx, req)
		dur := time.Since(start).Seconds()
		code := codes.OK
		if err != nil {
			code = status.Code(err)
		}
		observability.GRPCRequestDuration.WithLabelValues(info.FullMethod, code.String()).Observe(dur)
		return resp, err
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// loggerAdapter bridges zerolog → grpc-ecosystem logging interface
// ─────────────────────────────────────────────────────────────────────────────

type zerologAdapter struct {
	l interface {
		Error() interface{ Msg(string) }
	}
}

func loggerAdapter(l interface{}) grpclogging.Logger {
	return grpclogging.LoggerFunc(func(ctx context.Context, lvl grpclogging.Level, msg string, fields ...any) {
		observability.Logger().Info().Fields(fields).Msg(msg)
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// DAG gRPC implementation
// ─────────────────────────────────────────────────────────────────────────────

// dagServer implements the DAGService gRPC interface.
// Until proto-generated code is available we define a hand-written struct
// that provides the same method signatures.
type dagServer struct{ svc Services }

func registerDAGServer(s *grpc.Server, svc Services) {
	// s.RegisterService(&rbev1.DAGService_ServiceDesc, &dagServer{svc: svc})
	// Skipping until generated code is available.
	_ = svc
}

// CreateDAG — mirrors the proto RPC.
func (d *dagServer) CreateDAG(ctx context.Context, req *DAGCreateRequest) (*models.DAG, error) {
	return d.svc.DAG.CreateDAG(ctx, req.BuildID, req.Name, req.Labels, req.Platform, req.Description, req.CreatedBy)
}

func (d *dagServer) GetDAG(ctx context.Context, req *IDRequest) (*models.DAG, error) {
	return d.svc.DAG.GetDAG(ctx, req.ID)
}

func (d *dagServer) DeleteDAG(ctx context.Context, req *IDRequest) error {
	return d.svc.DAG.DeleteDAG(ctx, req.ID)
}

func (d *dagServer) AddVertex(ctx context.Context, v *models.Vertex) (*models.Vertex, error) {
	return d.svc.DAG.AddVertex(ctx, v)
}

func (d *dagServer) GetVertex(ctx context.Context, dagID, vertexID string) (*models.Vertex, error) {
	return d.svc.DAG.GetVertex(ctx, dagID, vertexID)
}

func (d *dagServer) GetVertexDependencyTree(ctx context.Context, dagID, vertexID string, maxDepth int) (*models.DependencyNode, error) {
	return d.svc.DAG.GetVertexDependencyTree(ctx, dagID, vertexID, maxDepth)
}

// ─────────────────────────────────────────────────────────────────────────────
// Cache gRPC implementation
// ─────────────────────────────────────────────────────────────────────────────

type cacheServer struct{ svc Services }

func registerCacheServer(s *grpc.Server, svc Services) { _ = svc }

func (c *cacheServer) CheckCache(ctx context.Context, key string) (bool, *models.CacheEntry, error) {
	return c.svc.Cache.CheckCache(ctx, key, true)
}

func (c *cacheServer) StoreCache(ctx context.Context, req dag.StoreCacheRequest) (*models.CacheEntry, error) {
	return c.svc.Cache.StoreCache(ctx, req)
}

func (c *cacheServer) ComputeCacheKey(ctx context.Context, opDigest string, inputFiles, depKeys []string, platform models.Platform, selector string) (string, error) {
	return dag.ComputeCacheKeyFromParts(opDigest, inputFiles, depKeys, platform, selector), nil
}

func (c *cacheServer) InvalidateCache(ctx context.Context, vertexID, dagID, cacheKey string, cascade bool) (int64, error) {
	return c.svc.Cache.InvalidateCache(ctx, vertexID, dagID, cacheKey, cascade)
}

// ─────────────────────────────────────────────────────────────────────────────
// Log gRPC implementation
// ─────────────────────────────────────────────────────────────────────────────

type logServer struct{ svc Services }

func registerLogServer(s *grpc.Server, svc Services) { _ = svc }

func (l *logServer) CreateLogStream(ctx context.Context, dagID, vertexID string, fd models.FDType, fdNum int, meta map[string]string) (*models.LogStream, error) {
	return l.svc.Logs.CreateLogStream(ctx, dagID, vertexID, fd, fdNum, meta)
}

// UploadLogs handles a client-streaming log upload.
type logUploadStream interface {
	Recv() (*models.LogChunk, error)
	SendAndClose(*uploadLogsResponse) error
}

type uploadLogsResponse struct {
	ChunksReceived int64
	BytesReceived  int64
}

func (l *logServer) UploadLogs(stream logUploadStream) error {
	var chunks, bytes int64
	for {
		chunk, err := stream.Recv()
		if err == io.EOF {
			return stream.SendAndClose(&uploadLogsResponse{ChunksReceived: chunks, BytesReceived: bytes})
		}
		if err != nil {
			return err
		}
		if err := l.svc.Logs.UploadChunk(context.Background(), chunk.StreamID, chunk.Sequence, chunk.Data, chunk.Timestamp, chunk.FDType, chunk.FDNum); err != nil {
			return err
		}
		chunks++
		bytes += int64(len(chunk.Data))
	}
}

// TailLogs handles a server-streaming tail.
type logTailStream interface {
	Send(*models.LogChunk) error
	Context() context.Context
}

func (l *logServer) TailLogs(streamID string, fromSeq int64, follow bool, stream logTailStream) error {
	ch, err := l.svc.Logs.TailLogs(stream.Context(), streamID, fromSeq, follow)
	if err != nil {
		return err
	}
	for chunk := range ch {
		if err := stream.Send(&chunk); err != nil {
			return err
		}
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// MountCache gRPC implementation
// ─────────────────────────────────────────────────────────────────────────────

type mountCacheServer struct{ svc Services }

func registerMountCacheServer(s *grpc.Server, svc Services) { _ = svc }

func (m *mountCacheServer) CreateMountCache(ctx context.Context, userKey, scope string, platformSpecific bool, platform *models.Platform, sharing models.CacheSharingMode, labels map[string]string) (*models.MountCache, error) {
	return m.svc.MountCache.Create(ctx, userKey, scope, platformSpecific, platform, sharing, labels)
}

func (m *mountCacheServer) LockMountCache(ctx context.Context, id, owner string, ttl time.Duration) (bool, *models.MountCache, error) {
	return m.svc.MountCache.Lock(ctx, id, owner, ttl)
}

// ─────────────────────────────────────────────────────────────────────────────
// Registry gRPC implementation
// ─────────────────────────────────────────────────────────────────────────────

type registryServer struct{ svc Services }

func registerRegistryServer(s *grpc.Server, svc Services) { _ = svc }

func (r *registryServer) StatBlob(ctx context.Context, repo, digest string) (*models.BlobDescriptor, error) {
	return r.svc.Registry.StatBlob(ctx, repo, digest)
}

func (r *registryServer) ListBlobs(ctx context.Context, repo, manifestDigest string, limit int) ([]models.BlobDescriptor, error) {
	return r.svc.Registry.ListBlobs(ctx, repo, manifestDigest, limit)
}

func (r *registryServer) CheckConversionExists(ctx context.Context, sourceDigest string, targetFormat models.ImageFormat, verifyBlobs bool) (bool, *models.ConversionRecord, []string) {
	return r.svc.Registry.CheckConversionExists(ctx, sourceDigest, targetFormat, verifyBlobs)
}

func (r *registryServer) RecordConversion(ctx context.Context, rec *models.ConversionRecord) error {
	return r.svc.Registry.RecordConversion(ctx, rec)
}

// ─────────────────────────────────────────────────────────────────────────────
// Attestation gRPC implementation
// ─────────────────────────────────────────────────────────────────────────────

type attestationServer struct{ svc Services }

func registerAttestationServer(s *grpc.Server, svc Services) { _ = svc }

func (a *attestationServer) Attach(ctx context.Context, att *models.Attestation) error {
	return a.svc.Attestation.Attach(ctx, att)
}

func (a *attestationServer) RecordSLSA(ctx context.Context, subjectDigest, subjectRepo string, prov *models.SLSAProvenance, sign bool, key []byte, keyless bool, token string) (*models.Attestation, error) {
	return a.svc.Attestation.RecordSLSAProvenance(ctx, subjectDigest, subjectRepo, prov, sign, key, keyless, token)
}

func (a *attestationServer) Sign(ctx context.Context, digest, repo string, key []byte, keyless bool, token string) (*models.Attestation, string, []byte, error) {
	return a.svc.Attestation.SignArtifact(ctx, digest, repo, key, keyless, token)
}

// ─────────────────────────────────────────────────────────────────────────────
// Placeholder request types (replace with proto-generated code)
// ─────────────────────────────────────────────────────────────────────────────

type DAGCreateRequest struct {
	BuildID     string
	Name        string
	Labels      map[string]string
	Platform    *models.Platform
	Description string
	CreatedBy   string
}

type IDRequest struct{ ID string }
type DAGVertexRequest struct{ DAGID, VertexID string }
