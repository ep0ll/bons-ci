/*
Package dirsync compares two directory trees ("lower" vs "upper") and streams
typed results over channels.

# Output streams

[Diff] produces two concurrent output streams:

  - [ExclusivePath] — paths that exist only in lower, pruned at the highest
    directory boundary so a single os.RemoveAll per entry deletes the whole
    sub-tree (see "Deletion DSA" below).
  - [CommonPath] — paths present in both trees, annotated with a two-tier
    equality verdict: fast stat-based metadata check, then incremental SHA-256
    content hashing only when metadata differs.

# Filtering

[Options] exposes three composable filtering axes:

	IncludePatterns: restrict output to matching entries (empty = include all)
	ExcludePatterns: suppress matching entries; prune matching directories
	Filter:          caller-supplied [PathFilter] with veto power over patterns

AllowWildcards toggles filepath.Match glob syntax for the pattern lists.
Exclude takes precedence over include.  Directories that fail an include check
are still traversed so their children can be evaluated individually
(FilterSkip, not FilterPrune).

RequiredPaths asserts that listed paths appear in output after filtering; absent
paths trigger a [*MissingRequiredPathsError] on [Result.Err].

# PathFilter interface

Implement [PathFilter] to add custom filtering logic:

	type myFilter struct{}

	func (myFilter) Decide(relPath string, isDir bool) dirsync.FilterDecision {
	    if strings.HasPrefix(relPath, "generated/") {
	        if isDir { return dirsync.FilterPrune } // stop recursion
	        return dirsync.FilterSkip
	    }
	    return dirsync.FilterAllow
	}

Pass it via [Options.Filter]:

	res, err := dirsync.Diff(ctx, lower, upper, dirsync.Options{
	    Filter: myFilter{},
	})

Compose it with the built-in pattern filter using [NewCompositeFilter]:

	builtin, _ := dirsync.BuildFilter(opts)
	combined   := dirsync.NewCompositeFilter(myFilter{}, builtin)

# Walk algorithm

The core algorithm is an O(N) merge-sort scan over pre-sorted [os.ReadDir]
output.  It reads each directory exactly once (one getdents64 syscall) and
advances two pointers — one into lower, one into upper — resolving each entry
into exactly one of: exclusive-lower, exclusive-upper (ignored), or common.

# Deletion DSA

Exclusive lower paths form a minimal cover of the lower tree's exclusive
sub-forest.  When [ExclusivePath.Pruned] is true the entire sub-tree rooted at
[ExclusivePath.AbsPath] is exclusive; no descendants are emitted separately.  A
caller performing deletions needs at most k os.RemoveAll calls, where k is the
number of pruned roots — not one call per file.

# Concurrency model

[Diff] starts one background goroutine that owns the walker (single-threaded
merge-sort) and a fixed-size pool of hash-worker goroutines.  The walker writes
exclusive paths synchronously to [Result.Exclusive] and enqueues hash jobs
asynchronously; workers write completed [CommonPath] values to [Result.Common].

All blocking channel sends select on ctx.Done() so context cancellation drains
cleanly.  Cancellation is not reported as a walk error.

Both output channels MUST be drained before reading [Result.Err].
*/
package dirsync
