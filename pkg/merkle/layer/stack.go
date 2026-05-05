// Package layer defines the LayerStack — an ordered sequence of layer digests
// representing the ancestry chain of an overlay filesystem mount.
//
// Convention: index 0 is the lowest (oldest) layer; the final index is the
// topmost (output) layer being produced by the current ExecOp.
package layer

import (
	"errors"
	"fmt"
	"strings"
)

// Digest is an opaque identifier for a single layer.
// Callers typically pass OCI digest strings ("sha256:abc…") or BuildKit
// vertex digests, but any stable string identifier is valid.
type Digest string

// Stack is an ordered list of layer Digests from lowest (base) to highest
// (output). It is immutable once constructed.
type Stack []Digest

// ErrEmptyStack is returned when an operation requires at least one layer.
var ErrEmptyStack = errors.New("layer: empty stack")

// New constructs a Stack from ordered digests. At least one digest is required.
func New(digests ...Digest) (Stack, error) {
	if len(digests) == 0 {
		return nil, ErrEmptyStack
	}
	s := make(Stack, len(digests))
	copy(s, digests)
	return s, nil
}

// MustNew is like New but panics on error. Use only in tests or init paths.
func MustNew(digests ...Digest) Stack {
	s, err := New(digests...)
	if err != nil {
		panic(err)
	}
	return s
}

// Top returns the topmost (output) layer digest.
// It returns an error if the stack is empty.
func (s Stack) Top() (Digest, error) {
	if len(s) == 0 {
		return "", ErrEmptyStack
	}
	return s[len(s)-1], nil
}

// MustTop is like Top but panics on error.
func (s Stack) MustTop() Digest {
	d, err := s.Top()
	if err != nil {
		panic(err)
	}
	return d
}

// Base returns the bottommost (oldest) layer digest.
func (s Stack) Base() (Digest, error) {
	if len(s) == 0 {
		return "", ErrEmptyStack
	}
	return s[0], nil
}

// Len returns the number of layers in the stack.
func (s Stack) Len() int { return len(s) }

// Contains reports whether d is in the stack.
func (s Stack) Contains(d Digest) bool {
	for _, v := range s {
		if v == d {
			return true
		}
	}
	return false
}

// Below returns the ordered list of layers below (older than) d, from lowest
// to the layer immediately below d. The returned slice shares no memory with s.
//
// If d is not in the stack, ErrNotInStack is returned.
func (s Stack) Below(d Digest) (Stack, error) {
	for i, v := range s {
		if v == d {
			if i == 0 {
				return Stack{}, nil
			}
			out := make(Stack, i)
			copy(out, s[:i])
			return out, nil
		}
	}
	return nil, fmt.Errorf("%w: %q", ErrNotInStack, d)
}

// AncestorsOf returns all layers in the stack that are older than (below) the
// output layer, in order from lowest to highest. These are the layers whose
// cached hashes can be reused without recomputation.
//
// Returns an error if the stack is empty.
func (s Stack) AncestorsOf(output Digest) (Stack, error) {
	return s.Below(output)
}

// ErrNotInStack is returned when a digest is not present in the stack.
var ErrNotInStack = errors.New("layer: digest not in stack")

// String returns a compact human-readable representation, e.g.
// "[sha256:aaa → sha256:bbb → sha256:ccc]".
func (s Stack) String() string {
	parts := make([]string, len(s))
	for i, d := range s {
		parts[i] = string(d)
	}
	return "[" + strings.Join(parts, " → ") + "]"
}

// Equal reports whether two stacks have identical contents.
func (s Stack) Equal(other Stack) bool {
	if len(s) != len(other) {
		return false
	}
	for i := range s {
		if s[i] != other[i] {
			return false
		}
	}
	return true
}

// Clone returns a deep copy of the stack.
func (s Stack) Clone() Stack {
	if len(s) == 0 {
		return Stack{}
	}
	out := make(Stack, len(s))
	copy(out, s)
	return out
}

// Push returns a new stack with d appended as the new top layer.
// The receiver is not modified.
func (s Stack) Push(d Digest) Stack {
	out := make(Stack, len(s)+1)
	copy(out, s)
	out[len(s)] = d
	return out
}
