//go:build linux
package nydus

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/plugins/content/local"
	nydusConv "github.com/containerd/nydus-snapshotter/pkg/converter"
	"github.com/containerd/nydus-snapshotter/pkg/converter/tool"
	"github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"golang.org/x/sync/errgroup"
)

// Merge multiple nydus bootstraps (from each layer of image) to a final
// bootstrap. And due to the possibility of enabling the `ChunkDictPath`
// option causes the data deduplication, it will return the actual blob
// digests referenced by the bootstrap.
func Merge(ctx context.Context, layers []nydusConv.Layer, dest io.Writer, opt nydusConv.MergeOption) ([]digest.Digest, error) {
	workDir, err := ensureWorkDir(opt.WorkDir)
	if err != nil {
		return nil, errors.Wrap(err, "ensure work directory")
	}
	defer os.RemoveAll(workDir)

	getLayerPath := func(layerIdx int, suffix string) string {
		if layerIdx < 0 || layerIdx >= len(layers) {
			return ""
		}

		digestHex := layers[layerIdx].Digest.Hex()
		if suffix == "" && layers[layerIdx].OriginalDigest != nil {
			digestHex = layers[layerIdx].OriginalDigest.Hex()
		}
		return filepath.Join(workDir, digestHex+suffix)
	}

	unpackLayerEntry := func(layerIdx int, entryName string, filePath string) error {
		if layerIdx < 0 || layerIdx >= len(layers) {
			return errors.Errorf("layer index %d out of bounds", layerIdx)
		}

		file, err := os.Create(filePath)
		if err != nil {
			return errors.Wrapf(err, "create %s file", entryName)
		}
		defer file.Close()

		if _, err := nydusConv.UnpackEntry(layers[layerIdx].ReaderAt, entryName, file); err != nil {
			return errors.Wrapf(err, "unpack %s", entryName)
		}
		return nil
	}

	eg, _ := errgroup.WithContext(ctx)
	sourceBootstrapPaths := []string{}
	rafsBlobDigests := []string{}
	rafsBlobSizes := []int64{}
	rafsBlobTOCDigests := []string{}
	for idx := range layers {
		sourceBootstrapPaths = append(sourceBootstrapPaths, getLayerPath(idx, ""))
		if layers[idx].OriginalDigest != nil {
			rafsBlobTOCDigest, err := calcBlobTOCDigest(layers[idx].ReaderAt)
			if err != nil {
				return nil, errors.Wrapf(err, "calc blob toc digest for layer %s", layers[idx].Digest)
			}
			rafsBlobTOCDigests = append(rafsBlobTOCDigests, rafsBlobTOCDigest.Hex())
			rafsBlobDigests = append(rafsBlobDigests, layers[idx].Digest.Hex())
			rafsBlobSizes = append(rafsBlobSizes, layers[idx].ReaderAt.Size())
		}

		eg.Go(func(idx int) func() error {
			return func() error {
				// Use the hex hash string of whole tar blob as the bootstrap name.
				if err := unpackLayerEntry(idx, nydusConv.EntryBootstrap, getLayerPath(idx, "")); err != nil {
					return err
				}

				if opt.FsVersion == "6" {
					if err := unpackLayerEntry(idx, nydusConv.EntryBlobMeta, getLayerPath(idx, ".blob.meta")); err != nil {
						logrus.Warnf("Failed to extract blob.meta.header for layer %d: %v\n", idx, err)
					}

					if err := unpackLayerEntry(idx, nydusConv.EntryBlobMetaHeader, getLayerPath(idx, ".blob.meta.header")); err != nil {
						logrus.Warnf("Failed to extract blob.meta.header for layer %d: %v\n", idx, err)
					}
				}

				return nil
			}
		}(idx))
	}

	if err := eg.Wait(); err != nil {
		return nil, errors.Wrap(err, "unpack all bootstraps")
	}

	targetBootstrapPath := filepath.Join(workDir, "bootstrap")

	blobDigests, err := tool.Merge(tool.MergeOption{
		BuilderPath: getBuilder(opt.BuilderPath),

		SourceBootstrapPaths: sourceBootstrapPaths,
		RafsBlobDigests:      rafsBlobDigests,
		RafsBlobSizes:        rafsBlobSizes,
		RafsBlobTOCDigests:   rafsBlobTOCDigests,

		TargetBootstrapPath: targetBootstrapPath,
		ChunkDictPath:       opt.ChunkDictPath,
		ParentBootstrapPath: opt.ParentBootstrapPath,
		PrefetchPatterns:    opt.PrefetchPatterns,
		OutputJSONPath:      filepath.Join(workDir, "merge-output.json"),
		Timeout:             opt.Timeout,
	})
	if err != nil {
		return nil, errors.Wrap(err, "merge bootstrap")
	}

	bootstrapRa, err := local.OpenReader(targetBootstrapPath)
	if err != nil {
		return nil, errors.Wrap(err, "open bootstrap reader")
	}
	defer bootstrapRa.Close()

	files := []nydusConv.File{
		{
			Name:   nydusConv.EntryBootstrap,
			Reader: content.NewReader(bootstrapRa),
			Size:   bootstrapRa.Size(),
		},
	}

	if opt.FsVersion == "6" {
		metaRas := make([]io.Closer, 0, len(layers)*2)
		defer func() {
			for _, closer := range metaRas {
				closer.Close()
			}
		}()

		for idx := range layers {
			digestHex := layers[idx].Digest.Hex()
			blobMetaPath := getLayerPath(idx, ".blob.meta")
			blobMetaHeaderPath := getLayerPath(idx, ".blob.meta.header")

			metaContent, err := os.ReadFile(blobMetaPath)
			if err != nil {
				return nil, errors.Wrap(err, "read blob.meta")
			}

			headerContent, err := os.ReadFile(blobMetaHeaderPath)
			if err != nil {
				return nil, errors.Wrap(err, "read blob.meta.header")
			}
			uncompressedSize := len(metaContent)
			alignedUncompressedSize := (uncompressedSize + 4095) &^ 4095
			totalSize := alignedUncompressedSize + len(headerContent)

			if totalSize == 0 {
				logrus.Warnf("blob data for layer %s is empty, skipped\n", digestHex)
				continue
			}

			assembledFileName := fmt.Sprintf("%s.blob.meta", digestHex)
			assembledFilePath := filepath.Join(workDir, assembledFileName)

			writeMetaFile := func() error {
				f, err := os.Create(assembledFilePath)
				if err != nil {
					return err
				}
				defer f.Close()

				if _, err := f.Write(metaContent); err != nil {
					return err
				}

				if padding := alignedUncompressedSize - uncompressedSize; padding > 0 {
					if _, err := f.Write(make([]byte, padding)); err != nil {
						return err
					}
				}

				if _, err := f.Write(headerContent); err != nil {
					return err
				}

				return f.Sync()
			}

			if err := writeMetaFile(); err != nil {
				return nil, errors.Wrap(err, "write blob meta file")
			}

			assembledRa, err := local.OpenReader(assembledFilePath)
			if err != nil {
				return nil, errors.Wrap(err, "open blob meta file")
			}

			metaRas = append(metaRas, assembledRa)

			files = append(files, nydusConv.File{
				Name:   assembledFileName,
				Reader: content.NewReader(assembledRa),
				Size:   int64(totalSize),
			})
		}
	}

	files = append(files, opt.AppendFiles...)
	var rc io.ReadCloser

	if opt.WithTar {
		rc = packToTar(files, false)
	} else {
		rc, err = os.Open(targetBootstrapPath)
		if err != nil {
			return nil, errors.Wrap(err, "open targe bootstrap")
		}
	}
	defer rc.Close()

	buffer := bufPool.Get().(*[]byte)
	defer bufPool.Put(buffer)
	if _, err = io.CopyBuffer(dest, rc, *buffer); err != nil {
		return nil, errors.Wrap(err, "copy merged bootstrap")
	}
	return blobDigests, nil
}
