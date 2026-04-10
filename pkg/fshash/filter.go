package fshash

import (
	"io/fs"
	"path"
	"strings"
)

// FilterDecision is the action a [Filter] returns for an entry.
type FilterDecision uint8

const (
	// Include signals that the entry (and, for directories, its subtree)
	// should be hashed.
	Include FilterDecision = iota
	// Exclude signals that the entry should be skipped entirely.
	Exclude
	// ExcludeDir signals that a directory itself is excluded but its children
	// should still be evaluated individually (useful for "ignore this dir but
	// not its subdirs" patterns). For non-directory entries it is equivalent
	// to Exclude.
	ExcludeDir
)

// Filter decides whether to include or skip a filesystem entry.
//
// Implementations MUST be safe for concurrent use.
type Filter interface {
	// Decide is called for every entry before it is hashed.
	//
	// relPath is the slash-separated path relative to the root passed to
	// [Checksummer.Sum]; fi carries the entry's metadata.
	Decide(relPath string, fi fs.FileInfo) FilterDecision
}

// FilterFunc adapts a plain function to the Filter interface.
type FilterFunc func(relPath string, fi fs.FileInfo) FilterDecision

func (f FilterFunc) Decide(relPath string, fi fs.FileInfo) FilterDecision {
	return f(relPath, fi)
}

// AllowAll is a [Filter] that includes every entry.
var AllowAll Filter = FilterFunc(func(_ string, _ fs.FileInfo) FilterDecision {
	return Include
})

// ExcludeNames returns a [Filter] that skips entries whose base name is in
// the provided set.  Comparison is case-sensitive.
func ExcludeNames(names ...string) Filter {
	set := make(map[string]struct{}, len(names))
	for _, n := range names {
		set[n] = struct{}{}
	}
	return FilterFunc(func(relPath string, _ fs.FileInfo) FilterDecision {
		if _, ok := set[path.Base(relPath)]; ok {
			return Exclude
		}
		return Include
	})
}

// ExcludePatterns returns a [Filter] that skips entries whose relative path
// matches any of the given glob patterns (using [path.Match] semantics).
// A pattern ending in "/" is treated as a directory-only pattern.
func ExcludePatterns(patterns ...string) Filter {
	return FilterFunc(func(relPath string, fi fs.FileInfo) FilterDecision {
		for _, p := range patterns {
			dirOnly := strings.HasSuffix(p, "/")
			if dirOnly && !fi.IsDir() {
				continue
			}
			trimmed := strings.TrimSuffix(p, "/")
			matched, _ := path.Match(trimmed, relPath)
			if !matched {
				// Also try matching just the base name.
				matched, _ = path.Match(trimmed, path.Base(relPath))
			}
			if matched {
				return Exclude
			}
		}
		return Include
	})
}

// ChainFilters returns a [Filter] that applies each provided filter in order.
// The first non-Include decision wins.
func ChainFilters(filters ...Filter) Filter {
	return FilterFunc(func(relPath string, fi fs.FileInfo) FilterDecision {
		for _, f := range filters {
			if d := f.Decide(relPath, fi); d != Include {
				return d
			}
		}
		return Include
	})
}

// noopFilter is the zero-value filter used when Options.Filter is nil.
type noopFilter struct{}

func (noopFilter) Decide(_ string, _ fs.FileInfo) FilterDecision { return Include }
