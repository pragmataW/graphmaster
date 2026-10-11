// Package index keeps graph.db in sync with the working tree.
//
// The entry point is Indexer.Freshen, which implements PLAN "Freshness
// algoritması": it locates the repository, works out which files changed
// since the last scan (git index stat shortcut, untracked walk with the
// persisted directory cache, parallel lstat sweep plus dirty hints, the
// racy-git rule and content keys), returns early when nothing changed, and
// otherwise takes the cross-process write lock and runs the pipeline:
// content filters, parse cache (cache.db), a guarded worker pool, a bounded
// channel and a single batched writer, followed by the name resolution pass.
//
// Freshen is safe to call before every MCP tool call and hook: concurrent
// calls in one process share one run (singleflight), and a caller that
// cannot get the write lock within its budget gets a Result describing the
// stale state instead of blocking.
//
// Facts are mapped onto graph rows in write.go; package rows and import
// targets are maintained by an order-independent post-pass (link.go) so an
// incremental update and a from-scratch build produce the same graph.
//
// Module identities and import targets depend on other files (go.mod,
// tsconfig.json, whether a/b.py exists, ...). RepoCtx records every such
// answer with the file it was consulted for (store table dep_owners); when
// an answer changes, only those files are reprocessed, from cache.
package index
