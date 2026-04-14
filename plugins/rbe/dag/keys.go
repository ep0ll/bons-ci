// Package dagstore — keys.go
//
// Key schema
// ══════════
//
// Vertices (content-addressed; shared/deduped across DAGs)
//   vertices/{vertex_hash}/meta               — compressed VertexMeta JSON
//   vertices/{vertex_hash}/stdin              — stdin blob
//   vertices/{vertex_hash}/stdout             — stdout blob
//   vertices/{vertex_hash}/stderr             — stderr blob
//
// DAGs
//   dags/{dag_id}/meta                        — compressed DAGMeta JSON
//   dags/{dag_id}/vertices/{vertex_hash}      — zero-byte presence record
//
// Indices
//   idx/id/{id}                               — content: "dag_id\x00vertex_hash"
//   idx/tree/{dag_id}/{tree_hash}             — content: vertex_hash
//   idx/daghash/{dag_hash}                    — content: dag_id
//
// All keys are ASCII-safe and can be used as S3 object keys unchanged.

package dagstore

import (
	"fmt"
	"strings"
)

const (
	segVertices = "vertices"
	segDags     = "dags"
	segIdx      = "idx"

	suffixMeta = "meta"

	streamSuffixStdin  = "stdin"
	streamSuffixStdout = "stdout"
	streamSuffixStderr = "stderr"
)

// KeySchema generates object store keys.  Callers instantiate it with an
// optional prefix so multiple logical stores can share one bucket.
type KeySchema struct {
	prefix string // e.g. "prod/" — always ends with "/" when non-empty
}

// NewKeySchema returns a KeySchema with an optional namespace prefix.
// An empty prefix means keys start from the bucket root.
func NewKeySchema(prefix string) KeySchema {
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	return KeySchema{prefix: prefix}
}

// ——— vertex keys ————————————————————————————————————————————————————————————

// VertexMeta returns the key for a vertex's compressed metadata object.
func (k KeySchema) VertexMeta(vertexHash string) string {
	return k.join(segVertices, vertexHash, suffixMeta)
}

// VertexStream returns the key for one of a vertex's stream blobs.
func (k KeySchema) VertexStream(vertexHash string, st StreamType) string {
	return k.join(segVertices, vertexHash, streamSuffix(st))
}

// VertexPrefix returns the key prefix covering all objects for a vertex.
// Useful for listing or deleting everything related to a vertex.
func (k KeySchema) VertexPrefix(vertexHash string) string {
	return k.join(segVertices, vertexHash) + "/"
}

// ——— DAG keys ————————————————————————————————————————————————————————————————

// DAGMeta returns the key for a DAG's compressed metadata object.
func (k KeySchema) DAGMeta(dagID string) string {
	return k.join(segDags, dagID, suffixMeta)
}

// DAGVertexMembership returns the zero-byte presence key recording that
// vertexHash belongs to dagID.
func (k KeySchema) DAGVertexMembership(dagID, vertexHash string) string {
	return k.join(segDags, dagID, segVertices, vertexHash)
}

// DAGVerticesPrefix returns the prefix for all vertex membership keys in a DAG.
func (k KeySchema) DAGVerticesPrefix(dagID string) string {
	return k.join(segDags, dagID, segVertices) + "/"
}

// DAGPrefix returns the prefix covering all objects for a DAG.
func (k KeySchema) DAGPrefix(dagID string) string {
	return k.join(segDags, dagID) + "/"
}

// DAGsPrefix returns the root prefix for all DAG objects.
func (k KeySchema) DAGsPrefix() string {
	return k.prefix + segDags + "/"
}

// ——— index keys ——————————————————————————————————————————————————————————————

// IDIndex returns the key for an ID → (dagID, vertexHash) mapping.
func (k KeySchema) IDIndex(id string) string {
	return k.join(segIdx, "id", id)
}

// TreeHashIndex returns the key for a (dagID, treeHash) → vertexHash mapping.
func (k KeySchema) TreeHashIndex(dagID, treeHash string) string {
	return k.join(segIdx, "tree", dagID, treeHash)
}

// DAGHashIndex returns the key for a dagHash → dagID mapping.
func (k KeySchema) DAGHashIndex(dagHash string) string {
	return k.join(segIdx, "daghash", dagHash)
}

// ——— index value encoding ————————————————————————————————————————————————————

const idxSep = "\x00"

// EncodeIDIndexValue encodes (dagID, vertexHash) as a compact index value.
func EncodeIDIndexValue(dagID, vertexHash string) []byte {
	return []byte(dagID + idxSep + vertexHash)
}

// DecodeIDIndexValue decodes the value stored in an ID-index object.
func DecodeIDIndexValue(data []byte) (dagID, vertexHash string, err error) {
	parts := strings.SplitN(string(data), idxSep, 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("malformed id-index value: %q", data)
	}
	return parts[0], parts[1], nil
}

// ——— internal helpers ————————————————————————————————————————————————————————

func (k KeySchema) join(parts ...string) string {
	return k.prefix + strings.Join(parts, "/")
}

func streamSuffix(st StreamType) string {
	switch st {
	case StreamStdin:
		return streamSuffixStdin
	case StreamStdout:
		return streamSuffixStdout
	case StreamStderr:
		return streamSuffixStderr
	default:
		return fmt.Sprintf("stream%d", st)
	}
}
