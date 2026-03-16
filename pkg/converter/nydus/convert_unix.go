//go:build !windows
// +build !windows

package nydus

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"os"

	"github.com/bons/bons-ci/pkg/archive/compression"
	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/pkg/archive"
	nydusConv "github.com/containerd/nydus-snapshotter/pkg/converter"
	"github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
)

const envNydusBuilder = "NYDUS_BUILDER"
const envNydusWorkDir = "NYDUS_WORKDIR"

const configGCLabelKey = "containerd.io/gc.ref.content.config"

func getBuilder(specifiedPath string) string {
	if specifiedPath != "" {
		return specifiedPath
	}

	builderPath := os.Getenv(envNydusBuilder)
	if builderPath != "" {
		return builderPath
	}

	return "nydus-image"
}

func ensureWorkDir(specifiedBasePath string) (string, error) {
	var baseWorkDir string

	if specifiedBasePath != "" {
		baseWorkDir = specifiedBasePath
	} else {
		baseWorkDir = os.Getenv(envNydusWorkDir)
	}
	if baseWorkDir == "" {
		baseWorkDir = os.TempDir()
	}

	if err := os.MkdirAll(baseWorkDir, 0750); err != nil {
		return "", errors.Wrapf(err, "create base directory %s", baseWorkDir)
	}

	workDirPath, err := os.MkdirTemp(baseWorkDir, "nydus-converter-")
	if err != nil {
		return "", errors.Wrap(err, "create work directory")
	}

	return workDirPath, nil
}

// Unpack a OCI formatted tar stream into a directory.
func unpackOciTar(ctx context.Context, dst string, reader io.Reader) error {
	ds, err := compression.DecompressStream(reader)
	if err != nil {
		return errors.Wrap(err, "unpack stream")
	}
	defer ds.Close()

	if _, err := archive.Apply(
		ctx,
		dst,
		ds,
		archive.WithConvertWhiteout(func(_ *tar.Header, _ string) (bool, error) {
			// Keep to extract all whiteout files.
			return true, nil
		}),
	); err != nil {
		return errors.Wrap(err, "apply with convert whiteout")
	}

	// Read any trailing data for some tar formats, in case the
	// PipeWriter of opposite side gets stuck.
	if _, err := io.Copy(io.Discard, ds); err != nil {
		return errors.Wrap(err, "trailing data after applying archive")
	}

	return nil
}

func calcBlobTOCDigest(ra content.ReaderAt) (*digest.Digest, error) {
	maxSize := int64(1 << 20)
	digester := digest.Canonical.Digester()
	if err := seekFileByTarHeader(ra, nydusConv.EntryTOC, &maxSize, func(tocData io.Reader, _ *tar.Header) error {
		if _, err := io.Copy(digester.Hash(), tocData); err != nil {
			return errors.Wrap(err, "calc toc data and header digest")
		}
		return nil
	}); err != nil {
		return nil, err
	}
	tocDigest := digester.Digest()
	return &tocDigest, nil
}

func seekFileByTarHeader(ra content.ReaderAt, targetName string, maxSize *int64, handle func(io.Reader, *tar.Header) error) error {
	const headerSize = 512

	if headerSize > ra.Size() {
		return fmt.Errorf("invalid nydus tar size %d", ra.Size())
	}

	cur := ra.Size() - headerSize
	reader := newSeekReader(ra)

	// Seek from tail to head of nydus formatted tar stream to find
	// target data.
	for {
		// Try to seek the part of tar header.
		_, err := reader.Seek(cur, io.SeekStart)
		if err != nil {
			return errors.Wrapf(err, "seek %d for nydus tar header", cur)
		}

		// Parse tar header.
		tr := tar.NewReader(reader)
		hdr, err := tr.Next()
		if err != nil {
			return errors.Wrap(err, "parse nydus tar header")
		}

		if cur < hdr.Size {
			return fmt.Errorf("invalid nydus tar data, name %s, size %d", hdr.Name, hdr.Size)
		}

		if hdr.Name == targetName {
			if maxSize != nil && hdr.Size > *maxSize {
				return fmt.Errorf("invalid nydus tar size %d", ra.Size())
			}

			// Try to seek the part of tar data.
			_, err = reader.Seek(cur-hdr.Size, io.SeekStart)
			if err != nil {
				return errors.Wrap(err, "seek target data offset")
			}
			dataReader := io.NewSectionReader(reader, cur-hdr.Size, hdr.Size)

			if err := handle(dataReader, hdr); err != nil {
				return errors.Wrap(err, "handle target data")
			}

			return nil
		}

		cur = cur - hdr.Size - headerSize
		if cur < 0 {
			break
		}
	}

	return errors.Wrapf(nydusConv.ErrNotFound, "can't find target %s by seeking tar", targetName)
}
