package writer

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/opencontainers/go-digest"
)

func B2Writer(ctx context.Context, client *minio.Client, bucket string, opt ...Opts) (_ Writer, err error) {
	if client == nil {
		return nil, fmt.Errorf("client is nil")
	}

	pr, pw := io.Pipe()
	var config = &opts{}

	for _, op := range opt {
		if err := op(config); err != nil {
			return nil, err
		}
	}

	if config.Size == 0 {
		config.Size = -1
	}

	var object string
	if config.ref != "" {
		dgst, err := digest.Parse(config.ref)
		if err == nil {
			object, err = DigestToPath(dgst)
			if err != nil {
				return nil, err
			}
		}
	} else if config.desc.Digest != "" {
		object, err = DigestToPath(config.desc.Digest)
		if err != nil {
			return nil, err
		}
	}

	if object == "" {
		return nil, fmt.Errorf("empty s3 object key")
	}

	gctx, cancel := context.WithCancelCause(ctx)
	return &writer{
		client: client,
		bucket: bucket,
		object: object,
		ctx: gctx,
		cancel: cancel,
		once: sync.Once{},
		reader: pr,
		writer: pw,
		offset: config.Offset,
		size: config.Size,
		checksum: config.checksum,
		StartedAt: time.Now(),
		ref: config.ref,
	}, nil
}
