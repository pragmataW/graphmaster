# Changelog

All notable changes to graphmaster are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project
uses [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- `go install github.com/pragmataW/graphmaster@latest` works without build
  tags: the module root is a main package, and untagged builds use the
  pure-Go `modernc.org/sqlite` driver (FTS5 built in). Release builds keep
  the cgo driver.

### Fixed

- `--version` of a `go install` build reports the module version and
  commit instead of `dev (commit none)`.

## [0.1.0] - 2026-10-18

First release.

### Added

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
- **Library symbols:** `callers useState` or `inheritance
  Psr\Http\Server\MiddlewareInterface` list the definitions that use or
  implement a name defined outside the repository.
- `callees` of a class or struct lists the callees of its methods.
- Symbol arguments accept each language's own spelling (`App::run`,
  `Slim\App::run`, `ns::Cls->m`); argument aliases (`name`, `qname`,
  `file_path`) are mapped to the documented ones.
- `search` with several words falls back to matching any word.
- Prebuilt binaries for macOS and Linux (amd64, arm64; Linux fully static),
  experimental Windows build, `install.sh` with SHA-256 verification and the
  Homebrew tap `pragmataW/tap/graphmaster`.
- Benchmarks on Kubernetes v1.34.0 and synthetic repositories, nightly
  performance gates.

### Fixed during field trials

Agents were run against flask, express, ripgrep, excalidraw, Next.js
commerce, angular-realworld, spring-petclinic and Slim before the release.

- Rust: imports between workspace members (`path` and `workspace = true`
  dependencies) now resolve; targets with a custom `[[bin]] path` get the
  right module names.
- JavaScript/TypeScript: `res.send = function send() {}` style methods on
  module-level objects are indexed; no doubled dots in nested test names.
- Steering: member call-site searches (`\.save\(`, `->save\(`) are
  answered from the graph.
- A same-named `describe()` block no longer makes a symbol ambiguous; test
  methods (JUnit, xUnit, PHPUnit) are labelled `test`.
- `read_symbol path:line` on a line without a definition lists the nearest
  definitions.
- Re-resolution after a whole-repository change skips files already
  resolved in the same run (4.6 s to 1.1 s on ripgrep).

[Unreleased]: https://github.com/pragmataW/graphmaster/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/pragmataW/graphmaster/releases/tag/v0.1.0
