package split

import (
	"context"
	"fmt"

	"github.com/containerd/containerd/v2/core/content"
	digest "github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

func SplitContentStore(read content.Store, writes ...content.Store) (content.Store, error) {
	if len(writes) == 0 {
		return nil, fmt.Errorf("no write content stores")
	}

	return &splitContentStore{
		read: read,
		wrts: writes,
	}, nil
}

type splitContentStore struct {
	read content.Store
	wrts []content.Store
}

// Abort write operations
func (s *splitContentStore) Abort(ctx context.Context, ref string) (err error) {
	for _, scs := range s.wrts {
		e := scs.Abort(ctx, ref)
		if e != nil && err == nil {
			err = e
		}
	}

	return err
}

// Delete from read and write content store
func (s *splitContentStore) Delete(ctx context.Context, dgst digest.Digest) (err error) {
	e := s.read.Delete(ctx, dgst)
	if e != nil && err == nil {
		err = e
	}

	for i, scs := range s.wrts {
		e := scs.Delete(ctx, dgst)
		if e != nil && err == nil && i > 0 {
			err = e
		}
	}

	return err
}

// returns writer first Info
func (s *splitContentStore) Info(ctx context.Context, dgst digest.Digest) (info content.Info, err error) {
	for _, scs := range s.wrts {
		if info, err = scs.Info(ctx, dgst); err == nil {
			return info, nil
		}
	}

	return s.read.Info(ctx, dgst)
}

// ListStatuses returns the list of statuses of the very first writer content store
func (s *splitContentStore) ListStatuses(ctx context.Context, filters ...string) (statuses []content.Status, err error) {
	for _, scs := range s.wrts {
		if statuses, err = scs.ListStatuses(ctx, filters...); err == nil {
			return statuses, nil
		}
	}

	return nil, err
}

// ReaderAt reads the Read first content.Store
func (s *splitContentStore) ReaderAt(ctx context.Context, desc v1.Descriptor) (readerAt content.ReaderAt, err error) {
	readerAt, err = s.read.ReaderAt(ctx, desc)
	if err == nil {
		return readerAt, nil
	}

	for _, scs := range s.wrts {
		readerAt, err = scs.ReaderAt(ctx, desc)
		if err == nil {
			return readerAt, nil
		}
	}

	return nil, err
}

// Status returns status of the very first Write content.Store
func (s *splitContentStore) Status(ctx context.Context, ref string) (status content.Status, err error) {
	for _, scs := range s.wrts {
		if status, err = scs.Status(ctx, ref); err == nil {
			return status, nil
		}
	}

	return content.Status{}, err
}

// Update Updates read and write content.Store
func (s *splitContentStore) Update(ctx context.Context, info content.Info, fieldpaths ...string) (cinfo content.Info, err error) {
	for _, scs := range s.wrts {
		i, e := scs.Update(ctx, info, fieldpaths...)
		if e != nil && err == nil {
			err = e
			cinfo = i
		}
	}

	return cinfo, err
}

// Walk walks through read content.Store
func (s *splitContentStore) Walk(ctx context.Context, fn content.WalkFunc, filters ...string) error {
	return s.read.Walk(ctx, fn, filters...)
}

// Writer implements content.Store.
func (s *splitContentStore) Writer(ctx context.Context, opts ...content.WriterOpt) (wrt content.Writer, err error) {
	var multiWriter = &multiWriter{writers: make([]content.Writer, len(s.wrts))}

	var e error
	for i, scs := range s.wrts {
		wrt, e = scs.Writer(ctx, opts...)
		if e != nil && err == nil {
			err = e
		}

		multiWriter.writers[i] = wrt
	}

	return multiWriter, err
}
