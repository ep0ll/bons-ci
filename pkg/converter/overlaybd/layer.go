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
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
)

// LayerConvertFunc provides a conversion hook unpacking OCI standard layers
// mapping explicitly to event-driven detached OverlayBD snapshot formats.
func LayerConvertFunc(opt PackOption, hook LayerConvertHook) converter.ConvertFunc {
	return func(ctx context.Context, cs content.Store, desc ocispec.Descriptor) (*ocispec.Descriptor, error) {
		if hook.Done != nil {
			defer hook.Done(ctx)
		}

		if err := ctx.Err(); err != nil {
			return nil, err
		}

		if !images.IsLayerType(desc.MediaType) {
			return nil, nil
		}

		if desc.Annotations[LayerAnnotationOverlayBDDigest] != "" {
			return nil, nil // Layer already mapped successfully
		}

		ra, err := cs.ReaderAt(ctx, desc)
		if err != nil {
			return nil, errors.Wrapf(err, "get source blob reader for %s", desc.Digest)
		}
		defer ra.Close()

		sr := io.NewSectionReader(ra, 0, ra.Size())

		if hook.LayerReader != nil {
			if err := hook.LayerReader(ctx, sr, cs, desc); err != nil {
				return nil, errors.Wrap(err, "layer reader hook execution failed")
			}
		}

		workdir, err := os.MkdirTemp("", "overlaybd-convert-*")
		if err != nil {
			return nil, errors.Wrap(err, "failed to create isolated temp workspace")
		}
		defer os.RemoveAll(workdir)

		layerTarPath := filepath.Join(workdir, "layer.tar")
		layerTarMetaPath := layerTarPath + ".meta"

		tf, err := os.Create(layerTarPath)
		if err != nil {
			return nil, errors.Wrap(err, "create temp original layer tar component")
		}

		// Ensure raw mapped buffers
		if _, err := io.Copy(tf, sr); err != nil {
			tf.Close()
			return nil, errors.Wrap(err, "copy tar payload to workspace")
		}
		tf.Close()

		if err := utils.GenerateTarMeta(ctx, layerTarPath, layerTarMetaPath); err != nil {
			return nil, errors.Wrap(err, "failed to generate static tar metadata via TurboOCI")
		}

		ext4MetaPath := filepath.Join(workdir, "overlaybd.commit")
		convOpt := &utils.ConvertOption{
			TarMetaPath:    layerTarMetaPath,
			Workdir:        filepath.Join(workdir, "conv"),
			Ext4FSMetaPath: ext4MetaPath,
			Config: sn.OverlayBDBSConfig{
				Lowers: []sn.OverlayBDBSConfigLower{},
				Upper:  sn.OverlayBDBSConfigUpper{},
			},
		}

		fsType := opt.FsType
		if fsType == "" {
			fsType = "ext4"
		}

		if err := utils.ConvertLayer(ctx, convOpt, fsType); err != nil {
			return nil, errors.Wrap(err, "failed to turbo convert independent layer payload")
		}

		commitFile, err := os.Open(ext4MetaPath)
		if err != nil {
			return nil, errors.Wrap(err, "open converted block commit payload")
		}
		defer commitFile.Close()
		commitFi, err := commitFile.Stat()
		if err != nil {
			return nil, errors.Wrap(err, "stat converted block commit payload")
		}

		ref := fmt.Sprintf("convert-overlaybd-from-%s", desc.Digest)
		dst, err := content.OpenWriter(ctx, cs, content.WithRef(ref))
		if err != nil {
			return nil, errors.Wrap(err, "open blob target layout writer")
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
			return nil, errors.Wrap(err, "write tar payload headers")
		}

		if _, err := io.Copy(tw, commitFile); err != nil {
			return nil, errors.Wrap(err, "stream zero-copy commit payloads")
		}

		if err := tw.Close(); err != nil {
			return nil, errors.Wrap(err, "close tar payload stream securely")
		}

		if err := dst.Commit(ctx, counter.c, digester.Digest()); err != nil {
			// Containerd natively handles conflicting layers throwing ALREADY_EXISTS. Let's ignore it here.
		}

		return makeBlobDesc(ctx, cs, desc, digester.Digest(), counter.c, opt)
	}
}

type writeCounter struct {
	w io.Writer
	c int64
}

func (wc *writeCounter) Write(p []byte) (n int, err error) {
	n, err = wc.w.Write(p)
	wc.c += int64(n)
	return
}

func makeBlobDesc(ctx context.Context, cs content.Store, orgDesc ocispec.Descriptor, targetDigest digest.Digest, targetSize int64, opt PackOption) (*ocispec.Descriptor, error) {
	targetInfo, err := cs.Info(ctx, targetDigest)
	if err != nil {
		targetInfo = content.Info{
			Digest: targetDigest,
			Size:   targetSize,
			Labels: make(map[string]string),
		}
	}
	if targetInfo.Labels == nil {
		targetInfo.Labels = make(map[string]string)
	}

	targetInfo.Labels[labels.LabelUncompressed] = targetDigest.String()
	targetInfo.Labels[LayerAnnotationOverlayBDDigest] = targetDigest.String()

	if _, err = cs.Update(ctx, targetInfo); err != nil {
		// Ignore if the current backend is immutable mock configurations
	}

	mediaType := orgDesc.MediaType
	if mediaType == ocispec.MediaTypeImageLayer {
		mediaType = ocispec.MediaTypeImageLayerGzip
	}

	fsType := opt.FsType
	if fsType == "" {
		fsType = "ext4"
	}

	desc := ocispec.Descriptor{
		Digest:    targetDigest,
		Size:      targetSize,
		MediaType: mediaType,
		Annotations: map[string]string{
			LayerAnnotationUncompressed:    targetDigest.String(),
			LayerAnnotationOverlayBDDigest: targetDigest.String(),
			LayerAnnotationOverlayBDSize:   fmt.Sprintf("%d", targetSize),
			LayerAnnotationOverlayBDFsType: fsType,
		},
	}

	if opt.IsAccelLayer {
		desc.Annotations[LayerAnnotationAcceleration] = "yes"
	}

	return &desc, nil
}
