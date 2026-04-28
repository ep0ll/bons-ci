package merkle

// Proof is a Merkle inclusion proof that demonstrates a specific leaf
// is part of a Merkle tree with a known root hash. Proofs are compact:
// they contain only the sibling hashes along the path from leaf to root.
type Proof struct {
	// Path is the file path of the leaf being proven.
	Path string

	// LeafHash is the content hash of the leaf node.
	LeafHash []byte

	// Siblings contains the sibling hashes at each level, from leaf
	// to root. The length equals the tree depth.
	Siblings [][]byte

	// Directions indicates the position of the sibling at each level.
	// true = sibling is on the right, false = sibling is on the left.
	Directions []bool
}

// Verify checks that this proof is consistent with the given root hash.
// It recomputes the root by walking from the leaf upward, combining
// with sibling hashes according to their directions.
func (p *Proof) Verify(rootHash []byte, hasher func(data []byte) []byte) bool {
	if len(p.Siblings) != len(p.Directions) {
		return false
	}

	current := make([]byte, len(p.LeafHash))
	copy(current, p.LeafHash)

	for i, sibling := range p.Siblings {
		var combined []byte
		if p.Directions[i] {
			// Sibling on right: hash(current || sibling)
			combined = append(current, sibling...)
		} else {
			// Sibling on left: hash(sibling || current)
			combined = append(sibling, current...)
		}
		current = hasher(combined)
	}

	return bytesEqual(current, rootHash)
}

// GenerateProof creates a Merkle inclusion proof for the leaf at the
// given path. The tree must have been built via [Tree.Build] first.
// Returns nil if the path is not found in the tree.
func (t *Tree) GenerateProof(path string) *Proof {
	if t.root == nil {
		return nil
	}

	// Find the leaf index.
	leafIdx := -1
	for i, leaf := range t.leaves {
		if leaf.Path == path {
			leafIdx = i
			break
		}
	}
	if leafIdx < 0 {
		return nil
	}

	proof := &Proof{
		Path:     path,
		LeafHash: copyBytes(t.leaves[leafIdx].Hash),
	}

	// Walk up the tree, collecting sibling hashes.
	currentLevel := t.leaves
	idx := leafIdx

	for len(currentLevel) > 1 {
		var nextLevel []*Node

		for i := 0; i < len(currentLevel); i += t.cfg.branchingFactor {
			end := i + t.cfg.branchingFactor
			if end > len(currentLevel) {
				end = len(currentLevel)
			}

			children := currentLevel[i:end]

			// Is our target in this group?
			if idx >= i && idx < end {
				// Collect all siblings (other children in this group).
				localIdx := idx - i
				for j, child := range children {
					if j != localIdx {
						proof.Siblings = append(proof.Siblings, copyBytes(child.Hash))
						proof.Directions = append(proof.Directions, j > localIdx)
					}
				}
			}

			branch := t.combineNodes(children, 0)
			nextLevel = append(nextLevel, branch)
		}

		// Update index for next level.
		idx = idx / t.cfg.branchingFactor
		currentLevel = nextLevel
	}

	return proof
}
