# Changelog

All notable changes to graphmaster are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project
uses [Semantic Versioning](https://semver.org/).

## [Unreleased]

### 0.1.0 (not released yet)

First release.

#### Added

- **Indexing** of Go, Python, TypeScript, TSX, JavaScript, Rust, Java, C,
  C++, C# and PHP with tree-sitter into a SQLite graph at
  `<repo>/.graphmaster/graph.db`: definitions, calls, references, imports,
  inheritance, tests, C/C++ includes and declarations.
- **Name resolution** across files with per-language rules and five
  confidence tiers (import-qualified, lexical, receiver-typed, unique global,
  ambiguous).
- **Go implicit interfaces and virtual dispatch:** derived `implements`
  edges from Go method sets (and compile-time assertions); `callers`,
  `callees` and `impact` follow calls through interface and base-class
  methods (`~via`, `~implemented by`) in every language with a type
  hierarchy.
- **Freshness guarantee:** an incremental dirty check (git index stat,
  untracked cache, racy-git rule, content keys) before every MCP call and
  hook; incremental reindex and resolution; a content-addressed parse cache
  (`cache.db`) that survives rebuilds and branch switches; per-file atomic,
  crash-safe writes; single writer with a cross-process lock and
  stale/building banners.
- **13 MCP tools** over stdio (`graphmaster mcp`): `find_symbol`, `search`,
  `repo_map`, `file_outline`, `read_symbol`, `callers`, `callees`, `impact`,
  `imports`, `inheritance`, `tests_for`, `traverse`, `status`, with
  token-budgeted plain-text output. The same tools from the shell with
  `graphmaster query`.
- **Search steering** for agent hooks with `strict`, `balanced` (default) and
  `off` modes: symbol-like greps and broad file scans are answered from the
  graph; retries are allowed in balanced mode; per-session soft budget;
  shell command parsing for `grep`, `rg`, `ag`, `ack`, `git grep`, `find`,
  `fd`, `ls -R`, `tree` and `locate`.
- **Context injection** at session and subagent start, outlines for
  whole-file reads of large code files (Claude Code) and context
  re-injection on compaction (OpenCode).
- **Agent integrations** for Claude Code, Codex CLI, Cursor and OpenCode,
  including Cursor duplicate-hook detection and a generated OpenCode plugin.
- **`graphmaster install` / `uninstall`:** one-time, user-level, reversible
  configuration of every detected agent, with backups, install state,
  `--dry-run` diffs and byte-exact restore.
- **`graphmaster doctor`:** binary, config, per-agent MCP/hooks/instructions,
  Codex hook trust, Cursor dedupe and index health.
- **`graphmaster ui`:** a local graph explorer (search, ego graph, file and
  package views, impact highlighting) on `127.0.0.1` with token
  authentication.
- **Configuration** through `~/.config/graphmaster/config.toml`,
  `<repo>/.graphmaster/config.toml` and `GRAPHMASTER_*` environment
  variables.
- Benchmarks on Kubernetes v1.34.0 and synthetic repositories, nightly
  performance gates.

#### Coming with the release

- Prebuilt binaries for macOS and Linux (amd64, arm64).
- Homebrew tap: `brew install pragmataW/tap/graphmaster`.
- `install.sh` with SHA-256 verification.
