package registry

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/bons/bons-ci/content/registry/ingestion"
	"github.com/bons/bons-ci/content/registry/reader"
	ocirepo "github.com/bons/bons-ci/content/registry/registry_repo"
	"github.com/bons/bons-ci/content/registry/writer"
	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/transfer/registry"
	"github.com/distribution/reference"
	digest "github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
)

const (
	CreatedAtLabel = "bonsci/createdat"
	UpdatedAtLabel = "bonsci/updatedat"
)

type registryStore struct {
	ref   string
	store content.Store
	opts  []registry.Opt

	// Cache for Info lookups to reduce remote calls
	infoCache    sync.Map // digest.Digest -> cacheEntry
	infoCacheTTL time.Duration

	registryCache ocirepo.RegistryRepo
	ingester      ingestion.IngestManager
}

type cacheEntry struct {
	info      content.Info
	timestamp time.Time
}

func cacheInfo(r *registryStore, dgst digest.Digest, info content.Info) {
	r.infoCache.Store(dgst, cacheEntry{
		info:      info,
		timestamp: time.Now(),
	})
}

// Abort implements ContentStore.
func (r *registryStore) Abort(ctx context.Context, ref string) error {
	ingestion, err := r.ingester.Get(ctx, ref)
	if err != nil {
		return err
	}

	return ingestion.Abort(ctx)
}

// Delete implements ContentStore.
func (r *registryStore) Delete(ctx context.Context, dgst digest.Digest) error {
	return r.store.Delete(ctx, dgst)
}

func fetchLocalInfo(ctx context.Context, dgst digest.Digest, r *registryStore) (content.Info, error) {
	return r.store.Info(ctx, dgst)
}

func fetchRegistryInfo(ctx context.Context, dgst digest.Digest, r *registryStore) (content.Info, error) {
	named, err := reference.ParseNamed(r.ref)
	if err != nil {
		return content.Info{}, err
	}

	canonical, err := reference.WithDigest(named, dgst)
	if err != nil {
		return content.Info{}, err
	}

	reg, err := GetOrCreateRegistry(ctx, canonical.String(), r)
	if err != nil {
		return content.Info{}, err
	}

	_, desc, err := reg.Resolve(ctx)
	if err != nil {
		return content.Info{}, err
	}

	var (
		createdAt time.Time
		updatedAt time.Time
	)

	var labels = make(map[string]string, len(desc.Annotations))
	for k, v := range desc.Annotations {
		labels["annos."+k] = v

		if k == CreatedAtLabel {
			createdAt, err = time.Parse(time.Layout, v)
			if err != nil {
				return content.Info{}, err
			}
		}

		if k == UpdatedAtLabel {
			updatedAt, err = time.Parse(time.Layout, v)
			if err != nil {
				return content.Info{}, err
			}
		}
	}

	info := content.Info{
		Digest: dgst,
		Size:   desc.Size,
		Labels: labels,
	}

	if !createdAt.IsZero() {
		info.CreatedAt = createdAt
	}

	if !updatedAt.IsZero() {
		info.UpdatedAt = updatedAt
	}

	return info, err
}

func loadInfoCache(r *registryStore, dgst digest.Digest) (content.Info, error) {
	// Check cache first
	if entry, ok := r.infoCache.Load(dgst); ok {
		cached := entry.(cacheEntry)
		if time.Since(cached.timestamp) < r.infoCacheTTL {
			return cached.info, nil
		}
		r.infoCache.Delete(dgst)
	}

	return content.Info{}, errors.Wrap(ErrNotFound, "no cache info")
}

// Info implements ContentStore.
func (r *registryStore) Info(ctx context.Context, dgst digest.Digest) (content.Info, error) {
	if info, err := loadInfoCache(r, dgst); err == nil {
		return info, nil
	}

	if info, err := fetchLocalInfo(ctx, dgst, r); err == nil {
		cacheInfo(r, dgst, info)
		return info, err
	}

	info, err := fetchRegistryInfo(ctx, dgst, r)
	if err != nil {
		return content.Info{}, err
	}

	cacheInfo(r, dgst, info)
	return info, nil
}

// ListStatuses implements ContentStore.
func (r *registryStore) ListStatuses(ctx context.Context, filters ...string) ([]content.Status, error) {
	return r.store.ListStatuses(ctx, filters...)
}

// ReaderAt implements ContentStore.
func (r *registryStore) ReaderAt(ctx context.Context, desc v1.Descriptor) (content.ReaderAt, error) {
	// if the content is already fetched from registry or exists locally,
	// use it instead of fetching it again from registry
	if readerAt, err := r.store.ReaderAt(ctx, desc); err == nil {
		return readerAt, nil
	}

	fetcher, err := Fetcher(ctx, r.ref, r.registryCache)
	if err != nil {
		return nil, err
	}

	rc, err := fetcher.Fetch(ctx, desc)
	if err != nil {
		return nil, err
	}

	// Cache the content locally for future reads
	// This creates a NEW ingestion in the local store (separate from registry ingestions)
	writer, err := r.store.Writer(ctx, content.WithDescriptor(desc))
	if err != nil {
		return nil, err
	}

	return reader.RegistryReader(rc, writer, desc.Size)
}

// Status implements ContentStore.
func (r *registryStore) Status(ctx context.Context, ref string) (content.Status, error) {
	return r.store.Status(ctx, ref)
}

// Update implements ContentStore.
func (r *registryStore) Update(ctx context.Context, info content.Info, fieldpaths ...string) (content.Info, error) {
	return r.store.Update(ctx, info, fieldpaths...)
}

// Walk implements ContentStore.
func (r *registryStore) Walk(ctx context.Context, fn content.WalkFunc, filters ...string) error {
	return r.store.Walk(ctx, fn, filters...)
}

// Writer implements ContentStore.
func (r *registryStore) Writer(ctx context.Context, opts ...content.WriterOpt) (_ content.Writer, err error) {
	var opt = &content.WriterOpts{}
	for _, op := range opts {
		if err := op(opt); err != nil {
			return nil, errors.Wrap(err, "failed to apply writer option")
		}
	}

	// Extract and validate digest
	dgst := opt.Desc.Digest
	ref := opt.Ref

	if dgst == "" {
		dgst, err = retriveDigestFromRef(ref)
		if err != nil {
			return nil, err
		}
	}

	// Use ref or digest as ingestion key
	ingestionRef := ref
	if ingestionRef == "" {
		ingestionRef = dgst.String()
	}

	pusher, err := GetOrCreatePusher(ctx, r, r.ref, opt.Desc)
	if err != nil {
		return nil, err
	}

	remoteWriter, err := pusher.Push(ctx, opt.Desc)
	if err != nil {
		return nil, err
	}

	rw, err := writer.NewRegistryWriter(ctx,
		remoteWriter,
		writer.WithDescriptor(opt.Desc),
		writer.WithReference(ingestionRef),
		writer.WithIngestManager(r.ingester),
	)
	if err != nil {
		return nil, err
	}

	if _, err = r.ingester.Put(ctx, rw); err != nil {
		return nil, err
	}

	return rw, nil
}

func retriveDigestFromRef(ref string) (digest.Digest, error) {
	if ref == "" {
		return "", errors.Wrap(ErrMissingDescriptor, "either descriptor or ref with digest is required")
	}

	// Try to extract digest from ref (format: name@sha256:...)
	if !strings.Contains(ref, "@") {
		return "", errors.Wrap(ErrMissingDescriptor, "ref must contain digest (@sha256:...)")
	}

	parts := strings.Split(ref, "@")
	if len(parts) != 2 {
		return "", errors.Wrap(ErrInvalidReference, "invalid ref format")
	}

	var err error
	dgst, err := digest.Parse(parts[1])
	if err != nil {
		return "", errors.Wrap(err, "failed to parse digest from ref")
	}

	return dgst, dgst.Validate()
}
