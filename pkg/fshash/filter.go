package fshash

import (
	"io/fs"
	"path"
	"strings"
)

// FilterDecision is the action a Filter returns for an entry.
type FilterDecision uint8

const (
	Include    FilterDecision = iota // include entry and its subtree
	Exclude                          // skip entry entirely
	ExcludeDir                       // skip dir digest but recurse children
)

// Filter decides whether to include or skip a filesystem entry.
// Implementations MUST be safe for concurrent use.
type Filter interface {
	Decide(relPath string, fi fs.FileInfo) FilterDecision
}

// FilterFunc adapts a plain function to the Filter interface.
type FilterFunc func(relPath string, fi fs.FileInfo) FilterDecision

func (f FilterFunc) Decide(r string, fi fs.FileInfo) FilterDecision { return f(r, fi) }

// AllowAll includes every entry.
var AllowAll Filter = FilterFunc(func(_ string, _ fs.FileInfo) FilterDecision { return Include })

// noopFilter is the zero-value filter used when Options.Filter is nil.
type noopFilter struct{}

func (noopFilter) Decide(_ string, _ fs.FileInfo) FilterDecision { return Include }

// ExcludeNames skips entries whose base name is in the set (case-sensitive).
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

// ExcludePatterns skips entries matching any glob pattern (path.Match semantics).
// A pattern ending in "/" is directory-only.
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
				matched, _ = path.Match(trimmed, path.Base(relPath))
			}
			if matched {
				return Exclude
			}
		}
		return Include
	})
}

// ChainFilters applies filters in order; the first non-Include decision wins.
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
