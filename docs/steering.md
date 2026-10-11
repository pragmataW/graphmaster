# Steering

Agents reach for `grep`, `rg`, `find` and `ls -R` out of habit, even when a
code graph is one tool call away. graphmaster's hooks look at every search the
agent is about to run and, when the code graph can answer it better, block the
search and hand the graph's answer back in the denial message. This page
describes exactly when that happens.

Implementation: `internal/steer` (decision engine, pattern/glob/shell
classifiers) and `internal/hookrun` (hook dispatch, dedupe, timeouts).

## Modes

| Mode | Behaviour |
|---|---|
| `balanced` (default) | The first symbol-like search and the first broad file scan are redirected to the graph. Running the same search again is allowed. Plain-text searches are always allowed. After `soft_budget` (default 8) redirects in one agent session nothing is blocked any more. |
| `strict` | Every content search or broad file scan over indexed code is redirected, symbol-like or not, and retries are redirected too. Use it when you want the agent to rely on the graph exclusively. |
| `off` | Hooks never block anything. The MCP tools, context injection and post-edit reindexing stay active. |

Set the mode in any configuration layer:

```toml
# ~/.config/graphmaster/config.toml (all repositories)
# or <repo>/.graphmaster/config.toml (one repository)
mode = "strict"
```

```sh
GRAPHMASTER_MODE=off codex     # one process
```

The main session and every subagent follow the same rules. The soft budget
is counted per `(session, agent)` pair, so each subagent has its own.

## What is never blocked

graphmaster fails open. A search is allowed without further checks when:

- the mode is `off`;
- the working directory is not inside a git repository (or a directory
  indexed with `graphmaster index --force`);
- the repository has no index yet (a background build is started instead);
- the tool is not a search: edits, writes, web tools, MCP tools, `Task`, ...;
- it is a **Read** (reads are never denied; see [Read outlines](#read-outlines));
- every target is outside indexed code: Markdown, JSON, YAML, logs, files in
  excluded directories, paths outside the repository;
- it targets **one named file** (`grep -n Foo main.go`): that is a targeted
  read, not a search;
- the glob names a specific file (`**/user_service.go`) or only non-code
  files (`**/*.md`);
- the shell command line is not purely read-only (it writes files, redirects
  output to a file, runs a build, ...), reads only stdin (`ps aux | grep
  java`), or changes into a directory that cannot be resolved statically
  (`cd $DIR && rg Foo`);
- the shell command cannot be parsed;
- the graph has no result for the pattern (balanced mode);
- anything inside graphmaster fails or exceeds its deadline. The error goes
  to `<repo>/.graphmaster/log` (rotated at 1 MiB) and the agent proceeds as if
  graphmaster were not installed.

## Balanced mode, step by step

For a `PreToolUse` event on a grep, glob or shell tool:

1. **Freshen.** The index is brought up to date with the `pre_tool_use` lock
   budget (1.5 s by default). If another process is writing, the decision is
   made on the current graph.
2. **Scope.** Paths and `--include`/`--glob` filters are resolved against the
   repository. If no target can contain indexed code, allow.
3. **Classify the request.**
   - *Symbol-like content search*: the pattern looks like a lookup of a code
     symbol (see below).
   - *Text search*: any other content search over code. **Allowed** in
     balanced mode.
   - *Exploration*: a broad file-name scan over code, such as Glob `**/*.ts`,
     `src/**`, `find . -name '*.go'`, `fd -e py`, `ls -R`, `tree`.
4. **Retry check.** The normalized pattern (`pnorm`: the sorted identifier
   tokens of the pattern, ignoring paths and flags) is looked up in the
   session's history. Seen before: allow. That is the escape hatch: if the
   agent really needs the text match, it repeats the search.
5. **Graph lookup** (25 ms budget): `find_symbol` for up to four symbol
   expressions of the pattern, falling back to `search` with the longest
   identifier; for an exploration, `repo_map` of the scanned directory (at
   most 40 lines). No results: allow.
6. **Budget.** If the session already had `soft_budget` redirects, allow and
   remind the agent once to prefer the MCP tools.
7. **Deny** with a reason of at most 1200 bytes: up to 12 graph results, the
   MCP tool to use next and how to get past the block.

Example (Claude Code, `Grep` for `PlaceOrder`):

```text
graphmaster: grep `PlaceOrder` looks up code symbols; the code graph has them:
  method service.OrderService.PlaceOrder  go/internal/service/order.go:24-32  func (s *OrderService) PlaceOrder(id string, qty int) (string, error)
  method shop.services.orders.OrderService.place_order  python/shop/services/orders.py:16-21  def place_order(self, sku: str, qty: int) -> int
  test service.TestPlaceOrder  go/internal/service/order_test.go:9-21  func TestPlaceOrder(t *testing.T)
  test tests.test_orders.test_place_order  python/tests/test_orders.py:14-16  def test_place_order()
  test service.TestPlaceOrderOutOfStock  go/internal/service/order_test.go:23-28  func TestPlaceOrderOutOfStock(t *testing.T)
  test tests.test_orders.test_place_order_out_of_stock  python/tests/test_orders.py:19-21  def test_place_order_out_of_stock()
Next: use the graphmaster MCP tools read_symbol <name> for source, callers/callees for usage, find_symbol for more matches.
Need a literal text match? Run the same search again; it will be allowed.
```

An exploration is answered with the directory layout:

```text
graphmaster: glob `**/*.go` scans the code tree; the code graph already has the layout (repo_map):
  ./  65 files, 1122 loc
    c/  6 files, 89 loc  · Vec2, clamp_int, log_msg
    go/  7 files, 177 loc  · Item, Memory, Store
    ...
Next: use the graphmaster MCP tools repo_map (path_prefix) and file_outline <path>; search finds files by name.
Need the raw file list? Run the same search again; it will be allowed.
```

## Strict mode

Every grep, glob or shell search whose scope contains indexed code is denied,
including text searches and retries. The reason still quotes graph results
when there are any:

```text
graphmaster (strict mode): rg `TODO` is blocked; code search goes through the code graph.
Use the graphmaster MCP tools find_symbol, search, read_symbol, callers and impact.
To allow text searches again, set mode = "balanced" in .graphmaster/config.toml (or GRAPHMASTER_MODE=balanced).
```

Non-code files, single named files, reads, stdin pipelines and everything in
[What is never blocked](#what-is-never-blocked) except the balanced-only
rules stay allowed in strict mode.

## Which patterns are symbol-like

A pattern is symbol-like when, after stripping regex anchors (`^`, `$`, `\b`)
and simple escapes, it is:

- an identifier or a qualified name: `PlaceOrder`, `OrderService.PlaceOrder`,
  `geo::Shape`, `Foo#bar`, `$this->save`, optionally followed by `(`;
- such a name after definition keywords: `func PlaceOrder`, `class Animal`,
  `def place_order`, `type Store interface`, `pub fn area`, `export const x`
  (keywords: `func def class fn type struct interface trait impl enum const
  let var function public private protected static async export pub abstract
  final virtual override typedef union mod package module record val object
  namespace`);
- an alternation of such names (`Get|Put`) or names joined by `.*`
  (`OrderService.*PlaceOrder`).

Not symbol-like (treated as text): several words separated by spaces,
quotes or other punctuation, `TODO`/`FIXME`/`XXX`/`HACK` markers, URLs,
paths containing `/`, kebab-case, fewer than three characters, digits only,
character classes, quantifiers, groups and lookarounds, non-ASCII text, and a
definition keyword on its own.

## Shell commands

Codex, Cursor (`Shell`, `beforeShellExecution`), Claude Code (`Bash`) and
OpenCode (`bash`) run searches through the shell, so graphmaster parses the
whole command line with `mvdan.cc/sh`:

- every simple command in pipelines, lists, subshells, command and process
  substitutions is inspected;
- `bash -c`, `sh -c`, `zsh -c`, `env -S` arguments are parsed again (nested
  up to a fixed depth); wrappers `xargs`, `env`, `time`, `command`, `builtin`,
  `nice` are unwrapped; GNU aliases (`ggrep`, `gfind`, `fdfind`, ...) are
  recognized;
- search tools: `grep`, `egrep`, `fgrep`, `rgrep`, `rg`, `ag`, `ack`,
  `git grep`, `find`, `fd`, `ls -R`, `tree`, `locate`, each with its own table
  of value-taking flags so patterns, paths and `--include` globs are told
  apart;
- `cd`, `pushd`, `popd` and `git -C` move the scope of later commands.

A command line is steered only when **every** command in it is read-only
(search tools, `cat`, `head`, `sort`, `sed -n`, `awk` without output files,
...), nothing is redirected to a file, and at least one search walks a tree
reading files. Within such a line, a symbol-like content search wins over a
text search, which wins over a file-name scan.

## Read outlines

Reads are never denied. When Claude Code reads a code file of more than 400
lines in full (no `offset`/`limit`), graphmaster allows the read and adds an
outline of up to 25 definitions as context, pointing at `read_symbol` and
ranged reads:

```text
graphmaster: go/internal/util/shell.go has 1382 lines. Outline (read one definition with the read_symbol MCP tool, or Read with offset/limit):
struct SearchCall  13-33  type SearchCall struct
  field Tool  16-16  Tool string
  ...
function AnalyzeShell  68-77  func AnalyzeShell(cmd string) (ShellAnalysis, error)
...
… +87 more (file_outline)
```

Codex reads files through the shell and Cursor and OpenCode cannot attach
context to an allowed call, so they do not get outlines.

## Session, subagent and compaction context

At session start (and for every subagent where the agent supports it)
graphmaster injects at most about 600 tokens of context: index statistics and
freshness, an MCP tool cheat-sheet, the rules of the active mode and the
top-level `repo_map`. Claude Code's Explore and Plan subagents do not read
`CLAUDE.md`, which is why the `SubagentStart` hook exists.

```text
graphmaster: a code graph of this repository is available; prefer it over grep/find/ls for code navigation.
index: 64 files (python 7, rust 7, java 7, ...), 236 symbols, 604 edges · fresh
MCP tools (server "graphmaster"):
  find_symbol <name>      definitions by name (exact > prefix > fuzzy)
  search <words>          symbol and file names
  ...
Steering mode balanced: the first grep/glob for a code symbol is blocked once with graph results; run the same search again to get text matches. Text, docs and config searches and Read are never blocked. After 8 redirects per agent nothing is blocked.
repo map:
./  65 files, 1122 loc
  ...
```

Claude Code and Codex wait at session start until the first index is built
(`session_start` budget, 9m50s). Cursor and OpenCode hooks cannot wait that
long, so graphmaster starts a detached `graphmaster index --quiet` and
returns a short "being built in the background" note; steering starts once
the index exists. On OpenCode the same context is appended when a session is
compacted.

## Keeping the graph fresh after edits

`PostToolUse` hooks never block. After an edit tool (Edit, Write,
apply_patch, `afterFileEdit`, `file.edited`) the edited paths are recorded as
dirty hints and reindexed (typically a few milliseconds). After a shell
command, which may have changed anything (`sed -i`, `git checkout`, code
generators), a full freshness check runs. Both use the `post_tool_use` lock
budget. Even without these hooks, the next MCP call or hook would pick up the
changes; the hooks only move the work off the critical path.

## Duplicate hook calls (Cursor)

Cursor runs its own hooks *and* Claude Code's hooks from
`~/.claude/settings.json`. graphmaster detects Cursor's payload shape
(`cursor_version`, or `conversation_id` with `generation_id`) on calls made
with `--agent claude`. When Cursor-native graphmaster hooks are installed,
that duplicate call exits silently. As a second line of defence, identical
`PreToolUse` calls (same session, tool-use id, event and normalized input)
within 10 minutes replay the first decision without spending budget.

## Tuning

| Want | Do |
|---|---|
| Fewer redirects per session | `soft_budget = 3` |
| No steering in one repository | `mode = "off"` in `<repo>/.graphmaster/config.toml` |
| Graph-only navigation | `mode = "strict"` |
| Faster hooks on a busy machine | lower `[timeouts] pre_tool_use` (answers come from the current graph with a staleness banner) |

All options: [configuration.md](configuration.md).
