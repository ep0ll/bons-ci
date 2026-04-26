package core

import "github.com/moby/buildkit/solver/pb"

// NewSourceMap creates a new SourceMap.
func NewSourceMap(filename, language string, data []byte) *SourceMap {
	return &SourceMap{
		Filename: filename,
		Language: language,
		Data:     data,
	}
}

// Location returns a ConstraintsOption that attaches source location ranges to
// the constraints. This links a vertex to a specific position in the source file.
func (s *SourceMap) Location(r []*pb.Range) ConstraintsOption {
	return func(c *Constraints) {
		if s == nil {
			return
		}
		c.SourceLocations = append(c.SourceLocations, &SourceLocation{
			SourceMap: s,
			Ranges:    r,
		})
	}
}
