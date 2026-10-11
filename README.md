# graphmaster

A local code graph for AI coding agents. graphmaster parses your repository
with tree-sitter into a SQLite graph, serves it to Claude Code, Codex, Cursor
and OpenCode over MCP, and uses each agent's hooks to steer blind
`grep`/`find`/`ls` searches toward precise symbol, reference and call-graph
queries.

One static binary. No daemon, no telemetry, no account, no per-project setup.

> **Status:** v0.1.0 is not released yet. Everything described here is
> implemented on `main` and covered by tests; the Homebrew tap, the
> `install.sh` script and prebuilt release binaries are **coming with the
> v0.1.0 release**. Until then, build from source with `go install` (see
> [Install](#install)).

## Why

Coding agents navigate code by grepping. Every `grep -rn Foo` dumps dozens of
loosely related lines into the context window, the agent reads whole files to
find one function, and after a few rounds the useful context is buried under
noise (context rot). A code graph answers "where is `Foo` defined, who calls
it, what breaks if I change it" in a few precise lines.

Existing code-graph tools for agents tend to fall short in one of three ways,
and graphmaster is built around fixing them:

1. **Freshness guarantee.** A graph that lags behind the working tree is worse
   than grep. graphmaster runs an incremental dirty check before *every* MCP
   tool call and every hook, so answers always reflect the files on disk,
   including edits made a second ago by the agent itself, by `sed -i`, by a
   branch switch or by you in your editor. A no-change check on Kubernetes
   takes about 50 ms.
2. **Steering, not just availability.** Agents keep grepping even when a
   better tool is registered. graphmaster's hooks intercept symbol-like
   searches, answer them from the graph and tell the agent which MCP tool to
   use next. In the default `balanced` mode the agent can always get the text
   search by simply running it again, so it is never stuck.
3. **Zero-friction install.** `graphmaster install` once, for every agent you
   use. There is no per-project `init`, no background service to keep alive and
   nothing to redo after a reboot. `graphmaster uninstall` reverts every edit.

## Features

- **10 languages, 11 grammars:** Go, Python, TypeScript, TSX, JavaScript,
  Rust, Java, C, C++, C# and PHP, with cross-file name resolution and
  confidence scores per edge ([docs/languages.md](docs/languages.md)).
- **13 MCP tools:** `find_symbol`, `search`, `repo_map`, `file_outline`,
  `read_symbol`, `callers`, `callees`, `impact`, `imports`, `inheritance`,
  `tests_for`, `traverse`, `status`. Plain-text answers with a token budget
  ([docs/mcp-tools.md](docs/mcp-tools.md)).
- **Search steering** with three modes, `strict`, `balanced` (default) and
  `off` ([docs/steering.md](docs/steering.md)).
- **Context injection:** session and subagent start get a compact repo
  summary and tool cheat-sheet; whole-file reads of large code files get an
  outline (where the agent supports it).
- **Incremental and crash-safe:** git index and untracked-cache tricks for the
  dirty check, a content-addressed parse cache that survives rebuilds and
  branch switches, per-file atomic writes.
- **Graph explorer:** `graphmaster ui` serves an interactive, lazily loaded
  graph view on `127.0.0.1`.
- **Reversible install** for Claude Code, Codex, Cursor and OpenCode, with
  backups, dry-run diffs and a `doctor` command.

## Supported agents

`graphmaster install` configures every agent it detects at user level.
Details per agent: [docs/agents.md](docs/agents.md).

| | Claude Code | Codex CLI | Cursor | OpenCode |
|---|---|---|---|---|
| MCP server | `~/.claude.json` | `~/.codex/config.toml` | `~/.cursor/mcp.json` | `~/.config/opencode/opencode.json` |
| Hooks | `~/.claude/settings.json` | `~/.codex/hooks.json` | `~/.cursor/hooks.json` | generated plugin `~/.config/opencode/plugins/graphmaster.js` |
| Instructions | `~/.claude/CLAUDE.md` block | `~/.codex/AGENTS.md` block | `~/.cursor/rules/graphmaster.mdc` if `~/.cursor/rules` exists, else MCP server instructions | `~/.config/opencode/AGENTS.md` block |
| Steered tools | Grep, Glob, Bash | shell commands | Grep, Shell | grep, glob, list, bash |
| Index refresh after edits | Edit, MultiEdit, Write, NotebookEdit, Bash | apply_patch, Bash | afterFileEdit, afterShellExecution | file.edited, bash |
| Session start | builds the index (blocking), injects context | builds the index (blocking), injects context | background build, injects context | background build, no context |
| Subagent start context | yes | yes | no | no |
| Large-file read outline | yes | no (reads go through the shell) | no | no |
| Context kept across compaction | n/a (Claude takes no context on PreCompact) | no PreCompact event | no | yes (`experimental.session.compacting`) |

Caveats:

- **Codex** runs user hooks only after you trust them. Start `codex`, run
  `/hooks` and approve the graphmaster entries. `graphmaster doctor` reports
  hooks that are not trusted yet. Approve them again after a reinstall.
- **Cursor** also executes Claude Code's hooks from
  `~/.claude/settings.json`. graphmaster recognizes Cursor's payload on those
  calls and stays silent when Cursor's own graphmaster hooks are installed, so
  each tool call gets exactly one decision (`doctor` shows whether the dedupe is
  active). Cursor cannot attach context to an allowed tool call; it only gets
  context at session start.
- **OpenCode** plugins cannot inject context into tool calls. graphmaster's
  hints reach the model only as the error of a redirected search. The plugin
  embeds the absolute binary path: re-run `graphmaster install` if the binary
  moves.

## Supported languages

| Language | Extensions |
|---|---|
| Go | `.go` |
| Python | `.py` `.pyi` |
| TypeScript | `.ts` `.mts` `.cts` |
| TSX | `.tsx` |
| JavaScript | `.js` `.mjs` `.cjs` `.jsx` |
| Rust | `.rs` |
| Java | `.java` |
| C | `.c` `.h` |
| C++ | `.cpp` `.cc` `.cxx` `.c++` `.hpp` `.hh` `.hxx` `.h++` `.ipp` `.tpp` `.C` `.H` |
| C# | `.cs` |
| PHP | `.php` |

How module identity, imports and the confidence tiers work, and how to add a
language: [docs/languages.md](docs/languages.md).

## Install

graphmaster needs macOS or Linux (amd64 or arm64). Windows builds are
experimental.

### Homebrew (coming with the v0.1.0 release)

```sh
brew install pragmataW/tap/graphmaster
```

The tap lives at [pragmataW/homebrew-tap](https://github.com/pragmataW/homebrew-tap).
Agent configs point at Homebrew's stable `bin` symlink, so `brew upgrade` does
not break them.

### Install script (coming with the v0.1.0 release)

```sh
curl -fsSL https://raw.githubusercontent.com/pragmataW/graphmaster/main/install.sh | sh
```

The script downloads the release archive for your platform from GitHub
releases, verifies its SHA-256 against the release's `checksums.txt` and
installs the binary into `/usr/local/bin` (as root) or `~/.local/bin`. Pass
options after `sh -s --`: `-b DIR` for another directory, `-v 0.1.0` for a
specific release, `-n` for a dry run.

### go install (works today)

graphmaster uses cgo (tree-sitter grammars and SQLite), so you need Go 1.25+
and a C compiler (Xcode Command Line Tools on macOS, `gcc` or `clang` on
Linux). The two build tags are required: `sqlite_fts5` enables full-text
search and `sqlite_omit_load_extension` drops SQLite's extension loader.

```sh
CGO_ENABLED=1 go install -tags "sqlite_fts5 sqlite_omit_load_extension" \
  github.com/pragmataW/graphmaster/cmd/graphmaster@latest
```

Make sure `$(go env GOPATH)/bin` is on your `PATH` before running
`graphmaster install`: agent configs record the path of the installed binary,
and `install` refuses to register a temporary `go run` build.

## Quickstart

```sh
graphmaster install    # once; registers MCP + hooks + instructions for every detected agent
```

Then restart your agent and open any git repository. That's it.

- There is no per-project step. The first session in a repository builds
  `.graphmaster/graph.db` (Claude Code and Codex wait for it at session
  start; Cursor and OpenCode build it in the background).
- From then on every tool call and hook refreshes the graph incrementally.
- Ask the agent to "find who calls X" or just let it grep: the first
  symbol-like search is answered from the graph.

Check the setup at any time:

```sh
graphmaster doctor     # per-agent install state, Codex hook trust, index health
```

Want to see what `install` would change first? `graphmaster install
--dry-run` prints unified diffs and writes nothing. Limit it to some agents
with `--agents claude,codex`.

## CLI reference

| Command | What it does |
|---|---|
| `graphmaster install [--agents a,b] [--dry-run]` | Register the MCP server, hooks and instructions for detected (or named) agents; create `~/.config/graphmaster/config.toml` if missing. |
| `graphmaster uninstall [--agents a,b] [--dry-run]` | Revert what `install` changed. Untouched files are restored byte for byte from backup; edited files lose only graphmaster's entries. |
| `graphmaster doctor` | Check binary, config, every agent's MCP/hooks/instructions, Codex hook trust, Cursor dedupe and the current repository's index. Exits non-zero on problems. |
| `graphmaster index [--force] [-q]` | Build or refresh the graph of the enclosing repository. Agents do this automatically; `--force` rebuilds from scratch (keeping the parse cache) and allows a non-git directory. |
| `graphmaster status` | File counts by status, languages, symbols/edges, DB size, last scan, steering mode. Never builds. |
| `graphmaster query <tool> [arg] [--flags]` | Run any MCP tool from the shell, e.g. `graphmaster query callers PlaceOrder --depth 2`. Prints exactly what the MCP server returns. Flags mirror the tool's arguments (`max_tokens` → `--max-tokens`). |
| `graphmaster ui [--port N] [--no-open]` | Serve the graph explorer on `127.0.0.1` with a random access token. |
| `graphmaster mcp` | The MCP server (stdio). Started by your agent, not by you. |
| `graphmaster hook <event> --agent <name>` | Hook entry point. Started by your agent; always exits 0 and fails open. |
| `graphmaster --version` | Version, commit and build date. |

## MCP tools

All tools refresh the index first and start with a banner such as
`graphmaster callers · fresh (+1 files, 6ms)`. Every tool takes `max_tokens`
(default 1500, cap 6000). Symbol arguments accept a qualified name, a dotted
suffix of one, `path:line`, a bare name or a 16-hex-digit `skey`; an ambiguous
name returns a numbered candidate list. Full reference with sample outputs:
[docs/mcp-tools.md](docs/mcp-tools.md).

| Tool | Main arguments | Answers |
|---|---|---|
| `find_symbol` | `query`, `kind`, `lang`, `path_prefix`, `exact`, `limit`=20 | Definitions by name: exact > prefix > fuzzy, exported first |
| `search` | `query`, `kinds`, `path_prefix`, `limit`=30 | Full-text and substring search over symbol names and file paths |
| `repo_map` | `path_prefix`, `depth`=2 | Directory/package tree with file counts, LOC, most-referenced symbols |
| `file_outline` | `path`, `depth`=2, `include_private`=true | Nested definitions with line ranges and signatures |
| `read_symbol` | `symbol`, `context_lines`=0, `max_lines`=200 | Source of one definition with line numbers |
| `callers` / `callees` | `symbol`, `depth`=1 (≤5), `min_confidence`=50, `limit`=50 | Call tree; `~` marks edges below confidence 80 |
| `impact` | `symbol` \| `path` \| `changed`=true, `depth`=3 (≤6), `min_confidence`=30 | Reverse transitive closure grouped by file, with affected tests |
| `imports` | `path` \| `module`, `direction`=out\|in | Imports of a file/package, or its importers |
| `inheritance` | `symbol`, `direction`=both\|up\|down, `depth`=3 | Type hierarchy |
| `tests_for` | `symbol` | Tests reaching the symbol within two hops, plus tests named after it |
| `traverse` | `start`, `edge_kinds`, `direction`, `max_depth`=4 (≤8), `min_confidence`=50 | Ranked BFS over chosen edge kinds, hubs summarized |
| `status` | | Index state, file statuses, languages, DB size, last scan, mode |

## Steering modes

| Mode | Symbol-like grep/glob/shell search over code | Other code searches | Non-code files, single named files, Read |
|---|---|---|---|
| `balanced` (default) | First time: blocked, answered with graph results. Same search again: allowed. After 8 redirects per agent session: allowed with a reminder. | allowed | always allowed |
| `strict` | always blocked with graph results | always blocked | always allowed |
| `off` | allowed | allowed | allowed |

Searches outside a git repository, outside indexed code, with no graph
results, and pipelines such as `ps aux | grep foo` are never blocked. Every
internal error fails open. Example of a redirected `Grep PlaceOrder` in
Claude Code:

```text
graphmaster: grep `PlaceOrder` looks up code symbols; the code graph has them:
  method service.OrderService.PlaceOrder  go/internal/service/order.go:24-32  func (s *OrderService) PlaceOrder(id string, qty int) (string, error)
  method shop.services.orders.OrderService.place_order  python/shop/services/orders.py:16-21  def place_order(self, sku: str, qty: int) -> int
  test service.TestPlaceOrder  go/internal/service/order_test.go:9-21  func TestPlaceOrder(t *testing.T)
  ...
Next: use the graphmaster MCP tools read_symbol <name> for source, callers/callees for usage, find_symbol for more matches.
Need a literal text match? Run the same search again; it will be allowed.
```

Set the mode globally in `~/.config/graphmaster/config.toml`, per repository
in `<repo>/.graphmaster/config.toml`, or per process with
`GRAPHMASTER_MODE`. Full rules: [docs/steering.md](docs/steering.md).

## Configuration

Layers, later wins: built-in defaults < `~/.config/graphmaster/config.toml`
(honours `$XDG_CONFIG_HOME`) < `<repo>/.graphmaster/config.toml` <
`GRAPHMASTER_*` environment variables. `graphmaster install` writes a
commented user config with every default.

| Key | Default | Env override |
|---|---|---|
| `mode` | `"balanced"` | `GRAPHMASTER_MODE` |
| `max_file_size` | `"1MiB"` | `GRAPHMASTER_MAX_FILE_SIZE` |
| `excludes` | `["vendor/", "node_modules/", "dist/", "build/", "target/", "*.min.*"]` | `GRAPHMASTER_EXCLUDES` (replaces) |
| `extra_excludes` | `[]` | `GRAPHMASTER_EXTRA_EXCLUDES` (appends) |
| `languages` | all | `GRAPHMASTER_LANGUAGES` |
| `soft_budget` | `8` | `GRAPHMASTER_SOFT_BUDGET` |
| `[timeouts] mcp` | `"20s"` | `GRAPHMASTER_TIMEOUT_MCP` |
| `[timeouts] pre_tool_use` | `"1.5s"` | `GRAPHMASTER_TIMEOUT_PRE_TOOL_USE` |
| `[timeouts] post_tool_use` | `"1.5s"` | `GRAPHMASTER_TIMEOUT_POST_TOOL_USE` |
| `[timeouts] session_start` | `"9m50s"` | `GRAPHMASTER_TIMEOUT_SESSION_START` |
| `[timeouts] subagent_start` | `"1.5s"` | `GRAPHMASTER_TIMEOUT_SUBAGENT_START` |
| `[timeouts] pre_compact` | `"1.5s"` | `GRAPHMASTER_TIMEOUT_PRE_COMPACT` |

Everything in detail: [docs/configuration.md](docs/configuration.md).

## Privacy

- **No telemetry.** graphmaster never phones home; it makes no network
  requests at all. The only listener is `graphmaster ui`, bound to
  `127.0.0.1` and protected by a random per-process token.
- **All local.** The graph lives in your repository at `.graphmaster/`
  (`graph.db`, `cache.db`, a lock file and a hook error log).
- **Never committed.** graphmaster adds `/.graphmaster/` to
  `.git/info/exclude` and writes a `.graphmaster/.gitignore` containing `*`.
  Your tracked `.gitignore` is never touched.
- **Agent configs** are edited only by `install`/`uninstall`, with
  timestamped backups under `~/.config/graphmaster/backups/` and a record of
  every insertion in `~/.config/graphmaster/install-state.json`.

## Performance

Kubernetes v1.34.0 on an Apple M4, measured with
[`bench/k8s.sh`](bench/README.md):

| Operation | Time |
|---|---|
| Full index (cold, parse + resolve) | 31 s |
| No-change refresh | 53 ms |
| Refresh after editing 50 files | 0.4 s |
| One query from the shell, including process start and freshness check | 50–100 ms |

Synthetic repositories (`internal/testutil.Synth`, same machine):

| Benchmark | Result |
|---|---|
| Full index, 1M LOC across all languages (10,000 files × 100 LOC) | 17.2 s (≈58k LOC/s) |
| No-change refresh, 100,000 files, warm (long-lived MCP server) | 88 ms |
| No-change refresh, 100,000 files, cold (new hook process) | 117 ms |
| Tool latency p95, 10,000 files, in process | under 1 ms for `find_symbol`, `search`, `read_symbol`, `file_outline`, `callers`, `callees`, `impact`, `tests_for`, `traverse`; 4.6 ms `imports`; 17 ms `repo_map` |

Reproduce with `go test -run '^$' -bench 'FullBuildSynth|FreshenNoChange'
-benchtime=1x ./internal/index/` and `GRAPHMASTER_QUERY_PERF_FILES=10000 go
test -run TestQueryLatency -v ./internal/query/` (plus the build tags); see
[bench/README.md](bench/README.md) for the benchmark scripts and the nightly
performance gates.

The MCP server is a long-lived process, so in-agent queries skip the process
start and reuse warm caches.

## Comparison

Several projects give coding agents a code graph. The table compares what
their public READMEs and package listings describe as of October 2026; `?`
means we could not verify it from public sources. These projects move
quickly: if a cell is wrong or out of date, please open an issue or a pull
request.

| | graphmaster | [code-review-graph](https://github.com/tirth8205/code-review-graph) | [GrapeRoot](https://pypi.org/project/graperoot/) | [codebase-memory-mcp](https://github.com/DeusData/codebase-memory-mcp) | [codegraph](https://github.com/colbymchenry/codegraph) |
|---|---|---|---|---|---|
| Distribution | single Go binary | Python package (pip) | Python package (PyPI) | single binary, install script | ? |
| Parsing | tree-sitter, 10 languages with per-language name resolution | tree-sitter | AST + dependency graph | tree-sitter (many languages), optional LSP-based resolution | ? |
| Storage | SQLite inside the repo (`.graphmaster/`) | SQLite | ? | persistent local graph | local knowledge graph |
| How the agent gets context | MCP tools + hooks that redirect searches | MCP tools and prompts | injects relevant files into the prompt | MCP tools, instruction files, skills, pre-tool hooks | MCP tools |
| Agent setup | `graphmaster install` for Claude Code, Codex, Cursor, OpenCode | `code-review-graph install` for several platforms | ? | install script configures detected agents | `claude mcp add` |
| Freshness | incremental dirty check before every MCP call and every hook | incremental updates, watch mode | ? | ? | ? |
| Search steering | deny-with-results, `strict` / `balanced` / `off` | ? | ? | pre-tool hooks | ? |
| License | MIT | ? | ? | MIT | ? |
| Telemetry | none | ? | ? | ? | ? |

graphmaster's focus is the combination in the first column: a freshness check
on every call, steering that an agent can always get past, and an install
that needs no per-project step.

## Uninstall

```sh
graphmaster uninstall              # all agents; --agents claude to pick some, --dry-run to preview
```

Agent config files that you have not edited since the install are restored
byte for byte; files you changed afterwards keep your changes and lose only
graphmaster's entries. Your `~/.config/graphmaster/config.toml` is kept. Then
remove the binary (`brew uninstall graphmaster` or delete it from
`$(go env GOPATH)/bin`) and, if you like, the per-repository `.graphmaster/`
directories.

## Troubleshooting

Start with `graphmaster doctor`. It prints one line per check and exits
non-zero when something is broken:

```text
graphmaster v0.1.0 (commit abbd383, built 2026-10-12T09:00:00Z)
  ok    binary /opt/homebrew/bin/graphmaster (Homebrew bin symlink)
  ok    config ~/.config/graphmaster/config.toml (mode balanced)

codex
  ok    detected
  ok    MCP server registered in ~/.codex/config.toml -> /opt/homebrew/bin/graphmaster
  ok    hooks installed in ~/.codex/hooks.json -> /opt/homebrew/bin/graphmaster
  ok    instructions in ~/.codex/AGENTS.md
  WARN  4 of 4 graphmaster hooks not trusted yet: start codex, run /hooks and approve the graphmaster hooks
...
```

| Symptom | Fix |
|---|---|
| The agent never gets redirected | `doctor`; restart the agent; in Codex approve the hooks in `/hooks`; check `mode` is not `off`. |
| "hooks ... point to missing binary" | The binary moved (e.g. a `go install` into a new GOPATH). Re-run `graphmaster install`. |
| Results look stale or empty | Run `graphmaster status` (or the `status` tool). A banner `⚠ stale: N files pending` means another process holds the write lock; `⚠ building 37%` means the first build is still running. |
| "not a git repository" | graphmaster indexes git work trees. Run `graphmaster index --force` to index a plain directory anyway. |
| Corrupt or very old index | `graphmaster index --force` rebuilds `graph.db`; the parse cache keeps it fast. |
| Hook misbehaves | Hooks fail open and log to `<repo>/.graphmaster/log`. |
| Claude Code asks before every graphmaster tool | Allow `mcp__graphmaster` in `/permissions`. |
| Too many redirects | Use `balanced` (retry passes), lower `soft_budget`, or set `mode = "off"` for one repo in `<repo>/.graphmaster/config.toml`. |

## Documentation

- [docs/agents.md](docs/agents.md): what install writes for each agent and how each integration behaves
- [docs/steering.md](docs/steering.md): steering modes and the exact decision rules
- [docs/configuration.md](docs/configuration.md): every option, layering, environment variables, on-disk layout
- [docs/mcp-tools.md](docs/mcp-tools.md): every tool with inputs and sample output
- [docs/languages.md](docs/languages.md): resolution per language, confidence tiers, adding a language
- [docs/architecture.md](docs/architecture.md): packages, freshness algorithm, schema
- [CONTRIBUTING.md](CONTRIBUTING.md) · [CHANGELOG.md](CHANGELOG.md) · [SECURITY.md](SECURITY.md)

## License

MIT, see [LICENSE](LICENSE).
