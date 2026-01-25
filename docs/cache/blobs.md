# Blobs

## sr.Finalize
commits snapshot of equal mutable and removes any metadata assosiated with equaMutable

## func (sr *immutableRef) layerSet() map[string]struct{}
returns bucket key's set of the correspondng immuntibleRefs from lowest -> highest

## func computeBlobChain(ctx context.Context, sr *immutableRef, createIfNeeded bool, comp compression.Config, s session.Group, filter map[string]struct{}) error