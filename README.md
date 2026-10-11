# graphmaster

A local code graph for AI coding agents. graphmaster indexes a repository with
tree-sitter into SQLite and serves it to Claude Code, Codex, Cursor and
OpenCode over MCP and agent hooks, steering agents away from blind grep toward
precise symbol, reference and call-graph queries.

> Status: early development. Subcommands are stubs.

## Features (planned for v1)

- Incremental indexing for Go, Python, JavaScript, TypeScript/TSX, Rust, Java,
  C, C++, C# and PHP.
- No daemon: the agent spawns `graphmaster mcp`; hooks are short-lived.
- One-time `graphmaster install` for all supported agents; `uninstall` reverts it.
- `graphmaster ui` local graph explorer.

## Build

Requires Go 1.25+ and a C toolchain (cgo).

```sh
make build      # ./graphmaster
make test       # go test -race with the required build tags
./graphmaster --version
```

All builds must use `-tags "sqlite_fts5 sqlite_omit_load_extension"`.

## Usage

```sh
graphmaster install   # register with detected agents
graphmaster index     # build / refresh the graph for the current repo
graphmaster status
graphmaster doctor
```

## License

MIT, see [LICENSE](LICENSE).
