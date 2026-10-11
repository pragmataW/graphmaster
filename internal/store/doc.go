// Package store is graphmaster's SQLite layer.
//
// It owns two database files inside a repository's .graphmaster directory:
//
//   - graph.db: the derived code graph (files, nodes, edges, imports, FTS5
//     symbol search) plus small steering/hook state tables. It is versioned
//     with PRAGMA user_version; a mismatch, unreadable file or failed
//     quick_check moves it aside and recreates an empty schema (no
//     migrations; the indexer rebuilds it from source).
//   - cache.db: content-addressed parse cache that survives graph.db
//     rebuilds (see Cache).
//
// Writes go through a single Writer (one connection, batched transactions,
// per-file atomicity) while the process holds the cross-process write lock
// (see Lock). Readers use DB() and the typed accessors concurrently thanks
// to WAL mode.
package store
