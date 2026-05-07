package layermerkle

import "errors"

// Sentinel errors returned by layermerkle APIs.
var (
	// ErrLayerNotFound is returned when a layer ID is not registered.
	ErrLayerNotFound = errors.New("layermerkle: layer not found")

	// ErrPathEscapes is returned when a resolved path escapes the layer root.
	ErrPathEscapes = errors.New("layermerkle: path escapes layer root")

	// ErrHashFailed is returned when file hashing fails unrecoverably.
	ErrHashFailed = errors.New("layermerkle: hash computation failed")

	// ErrCacheFull is returned when the cache is at capacity and eviction fails.
	ErrCacheFull = errors.New("layermerkle: hash cache is full")

	// ErrInvalidLayerStack is returned for an empty or malformed layer stack.
	ErrInvalidLayerStack = errors.New("layermerkle: invalid layer stack")

	// ErrVertexClosed is returned when an operation targets a finalized vertex.
	ErrVertexClosed = errors.New("layermerkle: vertex is already finalized")

	// ErrEventDropped is a non-fatal error indicating an event was silently dropped
	// because the engine is shutting down or the input buffer is full.
	ErrEventDropped = errors.New("layermerkle: event dropped")

	// ErrWhiteout indicates the file was deleted in the resolved layer (whiteout entry).
	ErrWhiteout = errors.New("layermerkle: file is a whiteout (deleted in this layer)")

	// ErrEngineNotRunning is returned when Feed or Submit is called before Start.
	ErrEngineNotRunning = errors.New("layermerkle: engine is not running")
)
