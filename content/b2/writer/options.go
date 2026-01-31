package writer

import (
	"github.com/minio/minio-go/v7"
	"github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

type opts struct {
	Size int64
	Offset int64
	desc v1.Descriptor
	checksum digest.Digester
	putOpts *minio.PutObjectOptions
	ref string
}

type Opts func(*opts) error

func WithChecksum(dgst digest.Digest) Opts {
	return func(opt *opts) error {
		opt.checksum = digest.Canonical.Digester()
		return nil
	}
}

func WithOffset(offset int64) Opts {
	return func(o *opts) error {
		o.Offset = offset
		return nil
	}
}

func WithSize(size int64) Opts {
	return func(o *opts) error {
		o.Size = size
		return nil
	}
}

func WithDesc(desc v1.Descriptor) Opts {
	return func(o *opts) error {
		o.desc = desc
		return nil
	}
}

func WithRef(ref string) Opts {
	return func(o *opts) error {
		o.ref = ref
		return nil
	}
}

func WithPutObjectOptions(po minio.PutObjectOptions) Opts {
	return func(o *opts) error {
		o.putOpts = &po
		return nil
	}
}

func (o opts) Digest() digest.Digest {
	return o.desc.Digest
}
