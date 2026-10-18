<div align="center">

# graphmaster

**Stop your AI coding agent from grepping its way through your codebase.**

A local, always-fresh code graph for Claude Code, Codex, Cursor and OpenCode.
One binary, one install, no daemon, no account, no telemetry.

[![ci](https://github.com/pragmataW/graphmaster/actions/workflows/ci.yml/badge.svg)](https://github.com/pragmataW/graphmaster/actions/workflows/ci.yml)
[![release](https://img.shields.io/github/v/release/pragmataW/graphmaster?sort=semver)](https://github.com/pragmataW/graphmaster/releases)
[![license](https://img.shields.io/badge/license-MIT-blue)](LICENSE)

</div>

```sh
brew install pragmataW/tap/graphmaster          # or: go install github.com/pragmataW/graphmaster@latest
graphmaster install                             # once; then restart your agent
```

---

## The problem

Coding agents find their way around code the way a newcomer with a terminal
would: `grep -rn`, `find`, `ls`, then `cat` the whole file. On a real
codebase that goes wrong in predictable ways:

- **Context rot.** Every `grep -rn handleRequest` pours dozens of loosely
  related lines into the context window. The agent reads a 2,000-line file
  to look at one function. A few rounds later the code that matters is
  buried under noise, answers get worse and every turn costs more tokens.
- **Text is not structure.** grep cannot tell a definition from a comment,
  a call from a string, or `Store.Get` from `Cache.Get`. It does not know
  that a call through an interface lands in three implementations, that a
  class implements a framework interface, or which tests reach the function
  being changed.

Code-graph tools exist to fix this, but in practice they tend to fail in
three places:

1. **The graph goes stale.** It is built once, or refreshed by a watcher
   that is not running, and misses the edit the agent made a second ago, a
   `sed -i`, a `git checkout` or new untracked files. A stale graph is worse
   than grep, because the agent trusts it.
2. **The agent ignores it.** Registering an MCP server is not enough.
   Agents, and their subagents in particular, keep reaching for grep out of
   habit.
3. **Setup is a chore.** A per-project init, a background service to keep
   alive and re-checking things after a reboot, or a paid, closed engine
   above a certain repository size.

## What graphmaster does

graphmaster parses your repository with tree-sitter into a SQLite graph of
definitions, calls, references, imports, inheritance and tests, serves it
over MCP, and hooks into each agent so that it actually gets used.

- **Always fresh.** Before *every* MCP call and *every* hook, an incremental
  dirty check compares the working tree with the index (git index stat,
  untracked-file cache, content hashes) and re-parses only what changed.
  Agent edits, shell edits, your editor, branch switches and new files all
  show up in the next answer. A no-change check on Kubernetes takes about
  50 ms, and the edit of 50 files is indexed in 0.4 s.
- **Steering, with an escape hatch.** When the agent greps for something
  that looks like a code symbol, the hook answers from the graph instead and
  names the MCP tool to use next. In the default `balanced` mode the same
  search run a second time goes through, so the agent is never stuck when it
  really needs a text match. `strict` blocks code greps entirely; `off`
  turns steering off.
- **Install once, forget it.** `graphmaster install` registers the MCP
  server, the hooks and a short instruction block for every agent it finds.
  No per-project step: the first session in a repository builds the index.
  The agent starts graphmaster itself, so there is nothing to launch and
  nothing to redo after a reboot. `graphmaster uninstall` reverts every
  change byte for byte.
- **Free and local.** MIT licensed. The graph stays in your repository under
  `.graphmaster/` (excluded from git automatically). No network access, no
  telemetry.

### What the agent sees

The agent runs `Grep PlaceOrder`. Instead of raw matches it gets:

```text
graphmaster: grep `PlaceOrder` looks up code symbols; the code graph has them:
  method service.OrderService.PlaceOrder  go/internal/service/order.go:24-32  func (s *OrderService) PlaceOrder(id string, qty int) (string, error)
  method shop.services.orders.OrderService.place_order  python/shop/services/orders.py:16-21  def place_order(self, sku: str, qty: int) -> int
  test service.TestPlaceOrder  go/internal/service/order_test.go:9-21  func TestPlaceOrder(t *testing.T)
Next: use the graphmaster MCP tools read_symbol <name> for source, callers/callees for usage, find_symbol for more matches.
Need a literal text match? Run the same search again; it will be allowed.
```

and from there asks precise questions:

```text
> callers Kubelet.SyncPod                                              # Kubernetes (Go)
method kubelet.Kubelet.SyncPod  pkg/kubelet/kubelet.go:1899-2106  func (kl *Kubelet) SyncPod(ctx context.Context, ...)
  method kubelet.podWorkers.podWorkerLoop  pkg/kubelet/pod_workers.go:1237-1363  @1309  ~via kubelet.podSyncer.SyncPod
  test kubelet.TestSyncPodSpans  pkg/kubelet/kubelet_test.go:3274-3408  @3370
  ...

> inheritance Psr\Http\Server\MiddlewareInterface --direction down     # Slim (PHP)
external symbol "MiddlewareInterface": not defined in this repository; extended or implemented by:
  class Slim.Middleware.BodyParsingMiddleware  Slim/Middleware/BodyParsingMiddleware.php:36-201  @36
  class Slim.Middleware.ErrorMiddleware  Slim/Middleware/ErrorMiddleware.php:29-215  @29
  ...
```

A few lines with exact locations, instead of a page of grep output.

## Features

- **11 grammars, 10 languages:** Go, Python, TypeScript, TSX, JavaScript,
  Rust, Java, C, C++, C# and PHP, with cross-file name resolution per
  language (Go modules, Python packages, tsconfig paths and workspaces, Cargo
  workspaces, Java packages, C/C++ includes, C# namespaces, PHP PSR-4) and a
  confidence score on every edge.
- **Calls through abstractions:** Go implicit interfaces, interface and
  base-class dispatch in every language with a type hierarchy (`~via`), and
  library symbols that have no definition in the repository (`callers
  useState`, implementers of a framework interface).
- **13 MCP tools:** `find_symbol`, `search`, `repo_map`, `file_outline`,
  `read_symbol`, `callers`, `callees`, `impact`, `imports`, `inheritance`,
  `tests_for`, `traverse`, `status`. Plain-text answers within a token
  budget.
- **Context where it helps:** a compact repo map and tool cheat-sheet at
  session and subagent start, an outline instead of a whole large file
  (Claude Code), context kept across compaction (OpenCode).
- **Built for big repositories:** Kubernetes (2.4M lines) indexes in about
  30 s; a content-addressed parse cache makes branch switches and rebuilds
  cheap; writes are per-file atomic and crash-safe.
- **Graph explorer:** `graphmaster ui` opens an interactive, lazily loaded
  view of the graph on `127.0.0.1`.
- **Same answers from the shell:** `graphmaster query callers PlaceOrder
  --depth 2`.

## Install

Prebuilt binaries for macOS and Linux (amd64, arm64; Linux builds are fully
static). Windows builds are experimental.

**Homebrew**

```sh
brew install pragmataW/tap/graphmaster
```

**Install script**: downloads the release archive for your platform,
verifies its SHA-256 against the release's `checksums.txt` and installs to
`/usr/local/bin` (as root) or `~/.local/bin`.

```sh
curl -fsSL https://raw.githubusercontent.com/pragmataW/graphmaster/main/install.sh | sh
# options after `sh -s --`: -b DIR, -v 0.1.0, -n (dry run)
```

**go install**: needs Go 1.25+ and a C compiler for the tree-sitter
grammars (Xcode Command Line Tools on macOS, `gcc` or `clang` on Linux).
The binary lands in `$(go env GOPATH)/bin`; make sure that is on your
`PATH`.

```sh
go install github.com/pragmataW/graphmaster@latest
```

A plain `go install` uses a pure-Go SQLite driver. Release builds (Homebrew,
install script) and builds with `-tags "sqlite_fts5 sqlite_omit_load_extension"`
use the cgo SQLite driver, which indexes about 5-10% faster.

Then, once:

```sh
graphmaster install            # every detected agent; --agents claude,codex to pick; --dry-run to preview
graphmaster doctor             # check the setup at any time
```

Restart your agent and open any git repository. That's it.

> **Codex** only runs hooks you have trusted: start `codex`, run `/hooks`
> and approve the graphmaster entries. `graphmaster doctor` tells you when
> this is still missing.

## Supported agents

| | Claude Code | Codex CLI | Cursor | OpenCode |
|---|---|---|---|---|
| MCP server | `~/.claude.json` | `~/.codex/config.toml` | `~/.cursor/mcp.json` | `~/.config/opencode/opencode.json` |
| Hooks | `~/.claude/settings.json` | `~/.codex/hooks.json` | `~/.cursor/hooks.json` | generated plugin |
| Steered tools | Grep, Glob, Bash | shell | Grep, Shell | grep, glob, list, bash |
| Refresh after edits | Edit, Write, Bash, … | apply_patch, Bash | afterFileEdit, afterShellExecution | file.edited, bash |
| Index at session start | builds (blocking) | builds (blocking) | builds in background | builds in background |
| Subagent context | yes | yes | no | no |

Cursor also executes Claude Code's hooks; graphmaster detects this and
answers each tool call once. What `install` writes for each agent and how
each integration behaves: [docs/agents.md](docs/agents.md).

## Steering modes

| Mode | Symbol-like search over code | Other code searches | Non-code files, one named file, Read |
|---|---|---|---|
| `balanced` (default) | first time answered from the graph; the same search again is allowed; after 8 redirects per session everything is allowed | allowed | always allowed |
| `strict` | always answered from the graph | blocked | always allowed |
| `off` | allowed | allowed | allowed |

Pipelines such as `ps aux | grep foo`, searches outside the repository or
outside indexed code, and searches the graph has no answer for are never
blocked. Any internal error lets the tool call through. Set the mode in
`~/.config/graphmaster/config.toml`, per repository in
`<repo>/.graphmaster/config.toml`, or with `GRAPHMASTER_MODE`. Exact rules:
[docs/steering.md](docs/steering.md).

## MCP tools

Every tool refreshes the index first and starts with a banner such as
`graphmaster callers · fresh (+1 files, 6ms)`. Symbol arguments take a
qualified name in any language's spelling (`pkg.Type.Method`,
`ns::Cls::m`, `App::run`), `path:line`, a bare name or a `skey` handle from
earlier output; an ambiguous name returns numbered candidates.

| Tool | Answers |
|---|---|
| `find_symbol` | where a function, type, class, variable is defined |
| `search` | symbol and file names from words or fragments |
| `repo_map` | directory and package tree with sizes and key symbols |
| `file_outline` | definitions of a file with line ranges and signatures |
| `read_symbol` | the source of one definition |
| `callers` / `callees` | call tree up to depth 5, through interfaces; library names too |
| `impact` | what may break if a symbol, a file or the uncommitted changes are modified, with affected tests |
| `imports` | imports of a file or package, or who imports it |
| `inheritance` | supertypes and subtypes, including implementers of library types |
| `tests_for` | tests that exercise a symbol |
| `traverse` | wider neighborhoods over chosen edge kinds |
| `status` | index health, languages, size, steering mode |

Arguments and sample outputs: [docs/mcp-tools.md](docs/mcp-tools.md).

## CLI

| Command | |
|---|---|
| `graphmaster install` / `uninstall` | register or remove graphmaster for your agents (`--agents`, `--dry-run`) |
| `graphmaster doctor` | per-agent install state, Codex hook trust, index health |
| `graphmaster index [--force]` | build or refresh the current repository's graph (agents do this for you) |
| `graphmaster status` | index statistics |
| `graphmaster query <tool> [arg] [--flags]` | any MCP tool from the shell |
| `graphmaster ui` | graph explorer on `127.0.0.1` |
| `graphmaster mcp`, `graphmaster hook` | entry points started by your agent |

## Performance

Kubernetes v1.34.0 on an Apple M4 ([bench/k8s.sh](bench/README.md)):

| Operation | Time |
|---|---|
| Full index, cold | 31 s |
| Refresh, nothing changed | 53 ms |
| Refresh after editing 50 files | 0.4 s |
| A query from the shell, including process start and freshness check | 50–100 ms |

On synthetic repositories: 1M lines index in 17 s, a no-change refresh of
100,000 files takes under 120 ms, and tool latency (p95, 10,000 files) is
under 1 ms for most tools. Nightly performance gates keep these numbers in
check; see [bench/README.md](bench/README.md).

## Privacy

- No telemetry and no network requests. The only listener is `graphmaster
  ui`, bound to `127.0.0.1` with a random per-process token.
- The graph lives in `<repo>/.graphmaster/`, which graphmaster adds to
  `.git/info/exclude`. Your tracked `.gitignore` is never touched.
- Agent configs are changed only by `install` and `uninstall`, with
  timestamped backups under `~/.config/graphmaster/backups/`.

## Configuration

Later wins: defaults < `~/.config/graphmaster/config.toml` <
`<repo>/.graphmaster/config.toml` < `GRAPHMASTER_*` environment variables.
The main knobs are `mode`, `excludes` / `extra_excludes`, `max_file_size`,
`languages`, `soft_budget` and per-event timeouts. Everything:
[docs/configuration.md](docs/configuration.md).

## Troubleshooting

| Symptom | Fix |
|---|---|
| The agent is never redirected | `graphmaster doctor`; restart the agent; in Codex approve the hooks in `/hooks`; check that `mode` is not `off` |
| "hooks ... point to missing binary" | the binary moved; run `graphmaster install` again |
| Results look stale or empty | `graphmaster status`; `⚠ building 37%` means the first build is still running |
| "not a git repository" | `graphmaster index --force` indexes a plain directory |
| A hook misbehaves | hooks never block on errors and log to `<repo>/.graphmaster/log` |
| Claude Code asks before every graphmaster tool | allow `mcp__graphmaster` in `/permissions` |

## Uninstall

```sh
graphmaster uninstall
brew uninstall graphmaster   # or delete the binary
```

Config files you have not edited since the install are restored byte for
byte; files you changed keep your changes and lose only graphmaster's
entries. Remove the per-repository `.graphmaster/` directories if you like.

## Documentation

- [docs/agents.md](docs/agents.md): per-agent integration details
- [docs/steering.md](docs/steering.md): steering rules
- [docs/mcp-tools.md](docs/mcp-tools.md): tool reference with sample output
- [docs/languages.md](docs/languages.md): resolution per language, confidence tiers, adding a language
- [docs/configuration.md](docs/configuration.md): every option and the on-disk layout
- [docs/architecture.md](docs/architecture.md): packages, freshness algorithm, schema
- [CONTRIBUTING.md](CONTRIBUTING.md) · [CHANGELOG.md](CHANGELOG.md) · [SECURITY.md](SECURITY.md)

## License

[MIT](LICENSE)
