# Cache
## func (sr *immutableRef) tryComputeOverlayBlob(ctx context.Context, lower, upper []mount.Mount, mediaType string, ref string, compressorFunc compression.Compressor) (_ ocispecs.Descriptor, ok bool, err error)
it uses overlayfs, and errors if not

`overlay.GetUpperdir` gets upperdir after validating mounts count againest lowerdir count( lowerdir count + 1 = len of upperdir)
writes content of upperdir to constent store by archiving or compressing with the correct compressor and digest of uncompressed size
if compressionFunc not specified then empty Uncompressed label is set to contentstore
