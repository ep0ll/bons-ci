package nydus

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/containerd/fifo"
	nydusConv "github.com/containerd/nydus-snapshotter/pkg/converter"
	"github.com/containerd/nydus-snapshotter/pkg/converter/tool"
	gzip "github.com/klauspost/pgzip"
	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"
)

// Pack converts an OCI tar stream to nydus formatted stream with a tar-like
// structure that arranges the data as follows:
//
// `data | tar_header | data | tar_header | [toc_entry | ... | toc_entry | tar_header]`
//
// The caller should write OCI tar stream into the returned `io.WriteCloser`,
// then the Pack method will write the nydus formatted stream to `dest`
// provided by the caller.
//
// Important: the caller must check `io.WriteCloser.Close() == nil` to ensure
// the conversion workflow is finished.
func Pack(ctx context.Context, dest io.Writer, opt PackOption) (io.WriteCloser, error) {
	if opt.FsVersion == "" {
		opt.FsVersion = "6"
	}

	builderPath := getBuilder(opt.BuilderPath)

	requiredFeatures := tool.NewFeatures(tool.FeatureTar2Rafs)
	if opt.BatchSize != "" && opt.BatchSize != "0" {
		requiredFeatures.Add(tool.FeatureBatchSize)
	}
	if opt.Encrypt {
		requiredFeatures.Add(tool.FeatureEncrypt)
	}

	detectedFeatures, err := tool.DetectFeatures(builderPath, requiredFeatures, tool.GetHelp)
	if err != nil {
		return nil, err
	}
	opt.features = detectedFeatures

	if opt.OCIRef {
		if opt.FsVersion == "6" {
			return packFromTar(ctx, dest, opt)
		}
		return nil, fmt.Errorf("oci ref can only be supported by fs version 6")
	}

	if opt.features.Contains(tool.FeatureBatchSize) && opt.FsVersion != "6" {
		return nil, fmt.Errorf("'--batch-size' can only be supported by fs version 6")
	}

	if opt.features.Contains(tool.FeatureTar2Rafs) {
		return packFromTar(ctx, dest, opt)
	}

	return packFromDirectory(ctx, dest, opt, builderPath)
}

func packFromDirectory(ctx context.Context, dest io.Writer, opt PackOption, builderPath string) (io.WriteCloser, error) {
	workDir, err := ensureWorkDir(opt.WorkDir)
	if err != nil {
		return nil, errors.Wrap(err, "ensure work directory")
	}
	defer func() {
		if err != nil {
			os.RemoveAll(workDir)
		}
	}()

	sourceDir := filepath.Join(workDir, "source")
	if err := os.MkdirAll(sourceDir, 0755); err != nil {
		return nil, errors.Wrap(err, "create source directory")
	}

	pr, pw := io.Pipe()

	unpackDone := make(chan bool, 1)
	go func() {
		if err := unpackOciTar(ctx, sourceDir, pr); err != nil {
			pr.CloseWithError(errors.Wrapf(err, "unpack to %s", sourceDir))
			close(unpackDone)
			return
		}
		unpackDone <- true
	}()

	wc := newWriteCloser(pw, func() error {
		defer os.RemoveAll(workDir)

		// Because PipeWriter#Close is called does not mean that the PipeReader
		// has finished reading all the data, and unpack may not be complete yet,
		// so we need to wait for that here.
		<-unpackDone

		blobPath := filepath.Join(workDir, "blob")
		blobFifo, err := fifo.OpenFifo(ctx, blobPath, syscall.O_CREAT|syscall.O_RDONLY|syscall.O_NONBLOCK, 0640)
		if err != nil {
			return errors.Wrapf(err, "create fifo file")
		}
		defer blobFifo.Close()

		go func() {
			err := tool.Pack(tool.PackOption{
				BuilderPath: builderPath,

				BlobPath:         blobPath,
				FsVersion:        opt.FsVersion,
				SourcePath:       sourceDir,
				ChunkDictPath:    opt.ChunkDictPath,
				PrefetchPatterns: opt.PrefetchPatterns,
				AlignedChunk:     opt.AlignedChunk,
				ChunkSize:        opt.ChunkSize,
				BatchSize:        opt.BatchSize,
				Compressor:       opt.Compressor,
				Timeout:          opt.Timeout,
				Encrypt:          opt.Encrypt,

				Features: opt.features,
			})
			if err != nil {
				pw.CloseWithError(errors.Wrapf(err, "convert blob for %s", sourceDir))
				blobFifo.Close()
			}
		}()

		buffer := bufPool.Get().(*[]byte)
		defer bufPool.Put(buffer)
		if _, err := io.CopyBuffer(dest, blobFifo, *buffer); err != nil {
			return errors.Wrap(err, "pack nydus tar")
		}

		return nil
	})

	return wc, nil
}

func packFromTar(ctx context.Context, dest io.Writer, opt PackOption) (io.WriteCloser, error) {
	workDir, err := ensureWorkDir(opt.WorkDir)
	if err != nil {
		return nil, errors.Wrap(err, "ensure work directory")
	}
	defer func() {
		if err != nil {
			os.RemoveAll(workDir)
		}
	}()

	rafsBlobPath := filepath.Join(workDir, "blob.rafs")
	rafsBlobFifo, err := fifo.OpenFifo(ctx, rafsBlobPath, syscall.O_CREAT|syscall.O_RDONLY|syscall.O_NONBLOCK, 0640)
	if err != nil {
		return nil, errors.Wrapf(err, "create fifo file")
	}

	tarBlobPath := filepath.Join(workDir, "blob.targz")
	tarBlobFifo, err := fifo.OpenFifo(ctx, tarBlobPath, syscall.O_CREAT|syscall.O_WRONLY|syscall.O_NONBLOCK, 0640)
	if err != nil {
		defer rafsBlobFifo.Close()
		return nil, errors.Wrapf(err, "create fifo file")
	}

	pr, pw := io.Pipe()
	eg := errgroup.Group{}

	wc := newWriteCloser(pw, func() error {
		defer os.RemoveAll(workDir)
		if err := eg.Wait(); err != nil {
			return errors.Wrapf(err, "convert nydus ref")
		}
		return nil
	})

	eg.Go(func() error {
		defer tarBlobFifo.Close()
		buffer := bufPool.Get().(*[]byte)
		defer bufPool.Put(buffer)
		if _, err := io.CopyBuffer(tarBlobFifo, pr, *buffer); err != nil {
			return errors.Wrapf(err, "copy targz to fifo")
		}
		return nil
	})

	eg.Go(func() error {
		defer rafsBlobFifo.Close()
		buffer := bufPool.Get().(*[]byte)
		defer bufPool.Put(buffer)
		if _, err := io.CopyBuffer(dest, rafsBlobFifo, *buffer); err != nil {
			return errors.Wrapf(err, "copy blob meta fifo to nydus blob")
		}
		return nil
	})

	eg.Go(func() error {
		var err error
		if opt.OCIRef {
			err = tool.Pack(tool.PackOption{
				BuilderPath: getBuilder(opt.BuilderPath),

				OCIRef:     opt.OCIRef,
				BlobPath:   rafsBlobPath,
				SourcePath: tarBlobPath,
				Timeout:    opt.Timeout,

				Features: opt.features,
			})
		} else {
			err = tool.Pack(tool.PackOption{
				BuilderPath: getBuilder(opt.BuilderPath),

				BlobPath:         rafsBlobPath,
				FsVersion:        opt.FsVersion,
				SourcePath:       tarBlobPath,
				ChunkDictPath:    opt.ChunkDictPath,
				PrefetchPatterns: opt.PrefetchPatterns,
				AlignedChunk:     opt.AlignedChunk,
				ChunkSize:        opt.ChunkSize,
				BatchSize:        opt.BatchSize,
				Compressor:       opt.Compressor,
				Timeout:          opt.Timeout,
				Encrypt:          opt.Encrypt,

				Features: opt.features,
			})
		}
		if err != nil {
			// Without handling the returned error because we just only
			// focus on the command exit status in `tool.Pack`.
			_ = wc.Close()
		}
		return errors.Wrapf(err, "call builder")
	})

	return wc, nil
}

// packToTar packs files to .tar(.gz) stream then return reader.
func packToTar(files []nydusConv.File, compress bool) io.ReadCloser {
	dirHdr := &tar.Header{
		Name:     "image",
		Mode:     0755,
		Typeflag: tar.TypeDir,
	}

	pr, pw := io.Pipe()

	go func() {
		// Prepare targz writer
		var tw *tar.Writer
		var gw *gzip.Writer
		var err error

		if compress {
			gw = gzip.NewWriter(pw)
			tw = tar.NewWriter(gw)
		} else {
			tw = tar.NewWriter(pw)
		}

		defer func() {
			err1 := tw.Close()
			var err2 error
			if gw != nil {
				err2 = gw.Close()
			}

			var finalErr error

			// Return the first error encountered to the other end and ignore others.
			switch {
			case err != nil:
				finalErr = err
			case err1 != nil:
				finalErr = err1
			case err2 != nil:
				finalErr = err2
			}

			pw.CloseWithError(finalErr)
		}()

		// Write targz stream
		if err = tw.WriteHeader(dirHdr); err != nil {
			return
		}

		for _, file := range files {
			hdr := tar.Header{
				Name: filepath.Join("image", file.Name),
				Mode: 0444,
				Size: file.Size,
			}
			if err = tw.WriteHeader(&hdr); err != nil {
				return
			}
			if _, err = io.Copy(tw, file.Reader); err != nil {
				return
			}
		}
	}()

	return pr
}
