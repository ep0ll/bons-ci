package overlaybd

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/bons/bons-ci/core/images/converter"
	sn "github.com/containerd/accelerated-container-image/pkg/types"
	"github.com/containerd/accelerated-container-image/pkg/utils"
	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/pkg/labels"
	"github.com/containerd/errdefs"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
)

// LayerConvertFunc returns a ConvertFunc that rewrites one OCI layer blob as an
// OverlayBD blob using the upstream overlaybd CLI tools.
//
// # Conversion pipeline
//
//  1. Fast-path: non-layer blobs and already-converted blobs are skipped.
//  2. The hook.LayerReader is called *synchronously* over the raw source bytes
//     before any CLI tool runs.
//  3. A per-call temp directory is created for all intermediate files; it is
//     unconditionally removed when the function returns.
//  4. The raw layer bytes are copied to a temp tar file, then
//     utils.GenerateTarMeta builds the tar index required by TurboOCI-apply.
//  5. utils.ConvertLayer calls overlaybd-create + turboOCI-apply + overlaybd-commit
//     to produce an ext4/erofs block image.
//  6. The commit file is wrapped in a single-entry tar and written to the
//     content store via a pooled buffer.
//
// # Concurrency
//
// Each invocation is fully self-contained: it owns its own temp directory,
// reader, writer, and pooled buffer.  Multiple goroutines may call the returned
// function simultaneously without sharing any mutable state.
func LayerConvertFunc(opt PackOption, hook LayerConvertHook) converter.ConvertFunc {
	return func(ctx context.Context, cs content.Store, desc ocispec.Descriptor) (*ocispec.Descriptor, error) {
		// Always fire the completion hook.
		if hook.Done != nil {
			defer hook.Done(ctx)
		}

		// Bail on pre-cancelled context before opening any writers.
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		// ── Fast-path 1: non-layer blob ───────────────────────────────────────
		if !images.IsLayerType(desc.MediaType) {
			return nil, nil
		}

		// ── Fast-path 2: already-converted overlaybd blob ─────────────────────
		// If a previous run stamped LayerAnnotationOverlayBDDigest on the blob's
		// Info labels, there is nothing to do.
		info, err := cs.Info(ctx, desc.Digest)
		if err != nil {
			return nil, errors.Wrapf(err, "get blob info %s", desc.Digest)
		}
		if info.Labels[LayerAnnotationOverlayBDDigest] != "" {
			return nil, nil
		}

		// ── Open source reader ────────────────────────────────────────────────
		ra, err := cs.ReaderAt(ctx, desc)
		if err != nil {
			return nil, errors.Wrapf(err, "get source blob reader for %s", desc.Digest)
		}
		defer ra.Close()

		sr := io.NewSectionReader(ra, 0, ra.Size())

		// ── Synchronous pipeline hook ─────────────────────────────────────────
		if hook.LayerReader != nil {
			if err := hook.LayerReader(ctx, sr, cs, desc); err != nil {
				return nil, errors.Wrap(err, "layer reader hook failed")
			}
			// Reset the section reader after the hook may have consumed bytes.
			sr = io.NewSectionReader(ra, 0, ra.Size())
		}

		// ── Isolated workspace per call ───────────────────────────────────────
		// Each call gets its own temp directory so concurrent conversions cannot
		// interfere with each other's intermediate files.
		workdir, err := os.MkdirTemp("", "overlaybd-convert-*")
		if err != nil {
			return nil, errors.Wrap(err, "create conversion workspace")
		}
		defer os.RemoveAll(workdir)

		// ── Step 1: copy raw layer to a temp tar file ─────────────────────────
		layerTarPath := filepath.Join(workdir, "layer.tar")
		layerTarMetaPath := layerTarPath + ".meta"

		if err := writeToFile(layerTarPath, sr); err != nil {
			return nil, errors.Wrap(err, "copy layer bytes to workspace tar")
		}

		// ── Step 2: generate TurboOCI tar metadata index ──────────────────────
		if err := utils.GenerateTarMeta(ctx, layerTarPath, layerTarMetaPath); err != nil {
			return nil, errors.Wrap(err, "generate TurboOCI tar metadata")
		}

		// ── Step 3: convert tar → overlaybd block image ───────────────────────
		ext4MetaPath := filepath.Join(workdir, "overlaybd.commit")
		fsType := opt.FsType
		if fsType == "" {
			fsType = "ext4"
		}
		convOpt := &utils.ConvertOption{
			TarMetaPath:    layerTarMetaPath,
			Workdir:        filepath.Join(workdir, "conv"),
			Ext4FSMetaPath: ext4MetaPath,
			Config: sn.OverlayBDBSConfig{
				Lowers: []sn.OverlayBDBSConfigLower{},
				Upper:  sn.OverlayBDBSConfigUpper{},
			},
		}
		if err := utils.ConvertLayer(ctx, convOpt, fsType); err != nil {
			return nil, errors.Wrap(err, "overlaybd-convert layer")
		}

		// ── Step 4: pack commit file into a tar and write to content store ─────
		commitFile, err := os.Open(ext4MetaPath)
		if err != nil {
			return nil, errors.Wrap(err, "open overlaybd commit file")
		}
		defer commitFile.Close()

		commitFi, err := commitFile.Stat()
		if err != nil {
			return nil, errors.Wrap(err, "stat overlaybd commit file")
		}

		ref := fmt.Sprintf("convert-overlaybd-from-%s", desc.Digest)
		dst, err := content.OpenWriter(ctx, cs, content.WithRef(ref))
		if err != nil {
			return nil, errors.Wrap(err, "open overlaybd blob writer")
		}
		defer func() {
			dst.Close()
			_ = cs.Abort(context.Background(), ref)
		}()

		digester := digest.Canonical.Digester()
		counter := &writeCounter{w: io.MultiWriter(dst, digester.Hash())}
		tw := tar.NewWriter(counter)

		if err := tw.WriteHeader(&tar.Header{
			Name:     "overlaybd.commit",
			Size:     commitFi.Size(),
			Mode:     0444,
			Typeflag: tar.TypeReg,
		}); err != nil {
			return nil, errors.Wrap(err, "write overlaybd commit tar header")
		}

		// Pool a scratch buffer for the tar-body copy.
		buf := bufPool.Get().(*[]byte)
		defer bufPool.Put(buf)
		if _, err := io.CopyBuffer(counter, commitFile, *buf); err != nil {
			return nil, errors.Wrap(err, "copy overlaybd commit body")
		}
		if err := tw.Close(); err != nil {
			return nil, errors.Wrap(err, "close tar writer")
		}

		if err := dst.Commit(ctx, counter.n, digester.Digest()); err != nil {
			if !errdefs.IsAlreadyExists(err) {
				return nil, errors.Wrap(err, "commit overlaybd blob")
			}
		}

		return makeBlobDesc(ctx, cs, desc, digester.Digest(), counter.n, opt, fsType)
	}
}

// writeToFile copies the contents of r into a newly created file at path.
func writeToFile(path string, r io.Reader) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, r)
	return err
}

// writeCounter counts bytes flowing through a Writer so we know the exact
// blob size at commit time without a separate Stat call.
type writeCounter struct {
	w io.Writer
	n int64
}

func (wc *writeCounter) Write(p []byte) (int, error) {
	n, err := wc.w.Write(p)
	wc.n += int64(n)
	return n, err
}

// makeBlobDesc constructs and persists the descriptor for an OverlayBD blob.
//
// Labels persisted on the stored Info:
//   - containerd.io/uncompressed        → fast-path DiffID retrieval
//   - LayerAnnotationOverlayBDDigest    → idempotency flag for future calls
func makeBlobDesc(
	ctx context.Context,
	cs content.Store,
	orgDesc ocispec.Descriptor,
	targetDigest digest.Digest,
	targetSize int64,
	opt PackOption,
	fsType string,
) (*ocispec.Descriptor, error) {
	targetInfo, err := cs.Info(ctx, targetDigest)
	if err != nil {
		// If the blob was committed with AlreadyExists, Info may succeed on a
		// re-read.  If not, synthesise a minimal Info so we can still return a
		// descriptor.
		targetInfo = content.Info{
			Digest: targetDigest,
			Size:   targetSize,
			Labels: make(map[string]string),
		}
	}
	if targetInfo.Labels == nil {
		targetInfo.Labels = make(map[string]string, 3)
	}

	// DiffID fast-path label.
	targetInfo.Labels[labels.LabelUncompressed] = targetDigest.String()
	// Idempotency flag.
	targetInfo.Labels[LayerAnnotationOverlayBDDigest] = targetDigest.String()

	if _, err = cs.Update(ctx, targetInfo,
		"labels."+labels.LabelUncompressed,
		"labels."+LayerAnnotationOverlayBDDigest,
	); err != nil {
		// Non-fatal: labels are cosmetic fast-paths; conversion remains correct.
		_ = err
	}

	// Preserve the source media type; upgrade uncompressed → gzip for
	// downstream compatibility.
	mediaType := orgDesc.MediaType
	if mediaType == ocispec.MediaTypeImageLayer {
		mediaType = ocispec.MediaTypeImageLayerGzip
	}

	annotations := map[string]string{
		LayerAnnotationUncompressed:    targetDigest.String(),
		LayerAnnotationOverlayBDDigest: targetDigest.String(),
		LayerAnnotationOverlayBDSize:   fmt.Sprintf("%d", targetSize),
		LayerAnnotationOverlayBDFsType: fsType,
	}
	if opt.IsAccelLayer {
		annotations[LayerAnnotationAcceleration] = "yes"
	}

	return &ocispec.Descriptor{
		Digest:      targetDigest,
		Size:        targetSize,
		MediaType:   mediaType,
		Annotations: annotations,
	}, nil
}
