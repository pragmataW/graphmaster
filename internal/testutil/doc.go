// Package testutil provides shared test fixtures and harness helpers:
//
//   - Materialize copies testdata/fixture-repo into a temp dir and turns it
//     into a git repository with tracked, untracked, ignored, vendored and
//     oversized files.
//   - Synth generates deterministic synthetic repositories for benchmarks.
//   - CanonicalDump renders graph.db as sorted text keyed by stable
//     identities (path, qname, kind) instead of row ids, so an incremental
//     build can be compared with a from-scratch build.
package testutil
