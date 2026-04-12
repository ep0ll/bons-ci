package fshash

// BLAKE3-256 — self-contained pure-Go implementation.
// Reference: https://github.com/BLAKE3-team/BLAKE3-specs/blob/master/blake3.pdf
//
// BLAKE3 is a cryptographic hash function with ~3–15 GB/s throughput
// (scalar Go; SIMD can reach >20 GB/s). It uses a 7-round Merkle tree
// compression function seeded from the SHA-256 IV.
//
// This implementation:
//   - Is correct for all input sizes (empty through multi-terabyte).
//   - Produces 32 bytes of output (Blake3-256).
//   - Implements hash.Hash for plug-in use.
//   - Is NOT parallelised (scalar only). For maximum throughput on large files,
//     the caller should use sharded hashing (SKILL §3) to parallelise at the
//     file level.
//
// Official test vector (empty input):
//   af1349b9f5f9a1a6a0404dea36dcc9499bcb25c9adc112b7cc9a87ea5b84c7ec (32 B)

import (
	"encoding/binary"
	"math/bits"
)

// BLAKE3 IV — first 8 words of the SHA-256 IV (same as BLAKE2s).
var blake3IV = [8]uint32{
	0x6A09E667, 0xBB67AE85, 0x3C6EF372, 0xA54FF53A,
	0x510E527F, 0x9B05688C, 0x1F83D9AB, 0x5BE0CD19,
}

// Message schedule (sigma) for all 7 rounds per the BLAKE3 spec §5.
// Round 0 is the identity; subsequent rounds use the fixed permutation.
var blake3Sigma = [7][16]byte{
	{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15},
	{2, 6, 3, 10, 7, 0, 4, 13, 1, 11, 12, 5, 9, 14, 15, 8},
	{3, 4, 10, 12, 13, 2, 7, 14, 6, 5, 9, 0, 11, 15, 8, 1},
	{10, 7, 12, 9, 14, 3, 13, 15, 4, 0, 11, 2, 5, 8, 1, 6},
	{12, 13, 9, 11, 15, 10, 14, 8, 7, 2, 5, 3, 0, 1, 6, 4},
	{9, 14, 11, 5, 8, 12, 15, 1, 13, 3, 0, 10, 2, 6, 4, 7},
	{11, 15, 5, 0, 1, 9, 8, 6, 14, 10, 2, 12, 3, 4, 7, 13},
}

// Domain separation flags (spec §2.6).
const (
	b3FlagChunkStart uint32 = 1 << 0
	b3FlagChunkEnd   uint32 = 1 << 1
	b3FlagParent     uint32 = 1 << 2
	b3FlagRoot       uint32 = 1 << 3
)

const (
	b3BlockSize = 64   // bytes per block
	b3ChunkSize = 1024 // bytes per chunk = 16 blocks
	b3OutLen    = 32   // 256-bit output
)

// ── Compression ───────────────────────────────────────────────────────────────

// blake3G is the BLAKE3 quarter-round mixing function (spec §5.2).
// Rotation constants: 16, 12, 8, 7.
func blake3G(s *[16]uint32, a, b, c, d int, x, y uint32) {
	s[a] += s[b] + x
	s[d] = bits.RotateLeft32(s[d]^s[a], -16)
	s[c] += s[d]
	s[b] = bits.RotateLeft32(s[b]^s[c], -12)
	s[a] += s[b] + y
	s[d] = bits.RotateLeft32(s[d]^s[a], -8)
	s[c] += s[d]
	s[b] = bits.RotateLeft32(s[b]^s[c], -7)
}

// blake3Round applies one full round: 4 column Gs then 4 diagonal Gs.
func blake3Round(state *[16]uint32, m *[16]uint32, r int) {
	s := blake3Sigma[r]
	// Column mixing.
	blake3G(state, 0, 4, 8, 12, m[s[0]], m[s[1]])
	blake3G(state, 1, 5, 9, 13, m[s[2]], m[s[3]])
	blake3G(state, 2, 6, 10, 14, m[s[4]], m[s[5]])
	blake3G(state, 3, 7, 11, 15, m[s[6]], m[s[7]])
	// Diagonal mixing.
	blake3G(state, 0, 5, 10, 15, m[s[8]], m[s[9]])
	blake3G(state, 1, 6, 11, 12, m[s[10]], m[s[11]])
	blake3G(state, 2, 7, 8, 13, m[s[12]], m[s[13]])
	blake3G(state, 3, 4, 9, 14, m[s[14]], m[s[15]])
}

// blake3Compress runs the 7-round BLAKE3 compression function (spec §5.2).
//
// State layout before mixing:
//
//	[cv[0..7] | IV[0..3] | counter_lo | counter_hi | block_len | flags]
//
// After 7 rounds: state[i] ^= state[i+8], state[i+8] ^= cv[i] for i in 0..7.
func blake3Compress(cv [8]uint32, block *[16]uint32, counter uint64, blockLen, flags uint32) [16]uint32 {
	state := [16]uint32{
		cv[0], cv[1], cv[2], cv[3],
		cv[4], cv[5], cv[6], cv[7],
		blake3IV[0], blake3IV[1], blake3IV[2], blake3IV[3],
		uint32(counter), uint32(counter >> 32), blockLen, flags,
	}
	for r := 0; r < 7; r++ {
		blake3Round(&state, block, r)
	}
	for i := 0; i < 8; i++ {
		state[i] ^= state[i+8]
		state[i+8] ^= cv[i]
	}
	return state
}

// blake3CV extracts the 8-word chaining value from a compression output.
func blake3CV(state [16]uint32) [8]uint32 {
	return [8]uint32{state[0], state[1], state[2], state[3],
		state[4], state[5], state[6], state[7]}
}

// blake3Parent computes a parent node that combines two 256-bit chaining values.
// The 512-bit input block is left[0..7] || right[0..7] packed as little-endian words.
func blake3Parent(left, right [8]uint32) [8]uint32 {
	var block [16]uint32
	for i := 0; i < 8; i++ {
		block[i] = left[i]
		block[i+8] = right[i]
	}
	out := blake3Compress(blake3IV, &block, 0, b3BlockSize, b3FlagParent)
	return blake3CV(out)
}

// ── Chunk accumulator ─────────────────────────────────────────────────────────

// blake3Chunk accumulates up to b3ChunkSize (1024) bytes across 16 blocks.
// Its chaining value (cv) evolves as each complete block is compressed.
type blake3Chunk struct {
	cv             [8]uint32  // running chaining value (starts at blake3IV)
	blockWords     [16]uint32 // current in-progress block as little-endian uint32s
	blockLen       int        // bytes written to blockWords so far (0..64)
	blocksConsumed int        // complete blocks compressed so far (0..15)
	chunkCounter   uint64     // sequential position of this chunk in the input
}

func newBlake3Chunk(counter uint64) blake3Chunk {
	return blake3Chunk{cv: blake3IV, chunkCounter: counter}
}

// update feeds p into the chunk, compressing complete 64-byte blocks eagerly.
// The last block (full or partial) is left pending for finalise() to mark CHUNK_END.
func (c *blake3Chunk) update(p []byte) {
	for len(p) > 0 {
		space := b3BlockSize - c.blockLen
		if space > len(p) {
			space = len(p)
		}
		// Accumulate bytes into blockWords as little-endian uint32s.
		// blockWords is zero-initialised at chunk creation and after each compressBlock.
		for i := 0; i < space; i++ {
			byteOff := c.blockLen + i
			wordIdx := byteOff >> 2
			shift := uint(byteOff&3) * 8
			c.blockWords[wordIdx] |= uint32(p[i]) << shift
		}
		c.blockLen += space
		p = p[space:]

		// Compress a full block only when more data follows — we must keep the
		// final block (full or partial) for finalise() to set CHUNK_END.
		if c.blockLen == b3BlockSize && len(p) > 0 {
			c.compressBlock(false)
		}
	}
}

// compressBlock runs compression on the current block and advances cv.
func (c *blake3Chunk) compressBlock(isLast bool) {
	flags := uint32(0)
	if c.blocksConsumed == 0 {
		flags |= b3FlagChunkStart // first block in chunk
	}
	if isLast {
		flags |= b3FlagChunkEnd
	}
	out := blake3Compress(c.cv, &c.blockWords, c.chunkCounter, uint32(c.blockLen), flags)
	c.cv = blake3CV(out)
	c.blocksConsumed++
	c.blockWords = [16]uint32{} // reset for the next block
	c.blockLen = 0
}

// finalise compresses the last pending block with CHUNK_END and returns the CV.
func (c *blake3Chunk) finalise() [8]uint32 {
	c.compressBlock(true)
	return c.cv
}

// ── hash.Hash implementation ──────────────────────────────────────────────────

// blake3State implements hash.Hash for BLAKE3-256.
//
// Chunk counter discipline:
//
//	numChunksCompleted is the count of fully committed chunks pushed to the stack.
//	It is used directly as the chunkCounter for new chunks, because BLAKE3 requires
//	chunk n to use n as its compression counter — this cannot be reconstructed
//	from the Merkle stack depth after parent merges reduce stackLen.
type blake3State struct {
	chunk              blake3Chunk   // in-progress chunk
	stack              [54][8]uint32 // Merkle subtree CVs (max depth = 53 for 2^53 chunks)
	stackLen           int
	numChunksCompleted uint64 // monotone counter; drives new chunk counters
}

func newBlake3() *blake3State {
	h := &blake3State{}
	h.Reset()
	return h
}

func (h *blake3State) Reset() {
	h.chunk = newBlake3Chunk(0)
	h.stackLen = 0
	h.numChunksCompleted = 0
}

func (h *blake3State) BlockSize() int { return b3BlockSize }
func (h *blake3State) Size() int      { return b3OutLen }

func (h *blake3State) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		// Remaining capacity in the current chunk.
		chunkConsumed := h.chunk.blocksConsumed*b3BlockSize + h.chunk.blockLen
		remaining := b3ChunkSize - chunkConsumed

		take := len(p)
		if take > remaining {
			take = remaining
		}
		h.chunk.update(p[:take])
		p = p[take:]

		// If the chunk is now full AND more data follows, commit it.
		chunkConsumed = h.chunk.blocksConsumed*b3BlockSize + h.chunk.blockLen
		if chunkConsumed == b3ChunkSize && len(p) > 0 {
			cv := h.chunk.finalise()
			h.pushChunkCV(cv)
			// New chunk counter = number of chunks completed so far (post-increment
			// happens inside pushChunkCV, so numChunksCompleted is already n+1).
			h.chunk = newBlake3Chunk(h.numChunksCompleted)
		}
	}
	return n, nil
}

// pushChunkCV adds a completed chunk CV to the Merkle stack and merges
// consecutive equal-height subtrees (binary carry chain).
//
// KEY: We use numChunksCompleted (before incrementing) to drive the merge
// condition, NOT the stackLen, because stackLen collapses after merges.
func (h *blake3State) pushChunkCV(cv [8]uint32) {
	h.stack[h.stackLen] = cv
	h.stackLen++
	h.numChunksCompleted++

	// Merge while numChunksCompleted is a multiple of 2^k (trailing zero bits
	// indicate that two subtrees of equal height are waiting on the stack).
	count := h.numChunksCompleted
	for h.stackLen >= 2 && count&1 == 0 {
		right := h.stack[h.stackLen-1]
		left := h.stack[h.stackLen-2]
		h.stack[h.stackLen-2] = blake3Parent(left, right)
		h.stackLen--
		count >>= 1
	}
}

// Sum appends the BLAKE3-256 digest to b without modifying h's state.
func (h *blake3State) Sum(b []byte) []byte {
	// Copy the state so finalisation is non-destructive.
	c := *h
	cv := c.computeRoot()
	var out [b3OutLen]byte
	for i, w := range cv {
		binary.LittleEndian.PutUint32(out[i*4:], w)
	}
	return append(b, out[:]...)
}

// computeRoot finalises the current chunk and folds the Merkle stack to
// produce the root chaining value.  Must be called on a COPY of the state.
func (h *blake3State) computeRoot() [8]uint32 {
	// Flags for the last block of the current (final) chunk.
	lastBlockFlags := b3FlagChunkEnd
	if h.chunk.blocksConsumed == 0 {
		lastBlockFlags |= b3FlagChunkStart
	}

	if h.stackLen == 0 {
		// Single-chunk (or empty) input — the last block is also the root.
		out := blake3Compress(
			h.chunk.cv,
			&h.chunk.blockWords,
			h.chunk.chunkCounter,
			uint32(h.chunk.blockLen),
			lastBlockFlags|b3FlagRoot,
		)
		return blake3CV(out)
	}

	// Multi-chunk input: compress the last chunk's final block without ROOT flag.
	out := blake3Compress(
		h.chunk.cv,
		&h.chunk.blockWords,
		h.chunk.chunkCounter,
		uint32(h.chunk.blockLen),
		lastBlockFlags,
	)
	cv := blake3CV(out)

	// Fold the stack from right (most recent) to left (oldest), producing parent
	// nodes.  The bottom-most fold receives b3FlagRoot.
	for i := h.stackLen - 1; i >= 0; i-- {
		var block [16]uint32
		left := h.stack[i]
		for j := 0; j < 8; j++ {
			block[j] = left[j]
			block[j+8] = cv[j]
		}
		parentFlags := b3FlagParent
		if i == 0 {
			parentFlags |= b3FlagRoot
		}
		out = blake3Compress(blake3IV, &block, 0, b3BlockSize, parentFlags)
		cv = blake3CV(out)
	}
	return cv
}
