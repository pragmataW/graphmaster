# Configuration

graphmaster works without any configuration. Everything below is optional.

## Layers

Settings are merged in this order; a later layer overrides only the keys it
sets:

1. built-in defaults;
2. the user config: `$XDG_CONFIG_HOME/graphmaster/config.toml` when
   `XDG_CONFIG_HOME` is an absolute path, else
   `~/.config/graphmaster/config.toml`;
3. the project config: `<repo>/.graphmaster/config.toml`;
4. environment variables `GRAPHMASTER_*`.

`graphmaster install` creates the user config (only if it does not exist)
with `mode = "balanced"` and every other option commented out at its default
value. `graphmaster uninstall` keeps it.

The project config lives inside `.graphmaster/`, which git ignores, so it is
a per-checkout setting and is never committed.

Unknown keys produce a warning (shown by `graphmaster doctor`) and are
ignored, so a config written for a newer graphmaster still loads. A file that
fails to parse or holds an invalid value makes graphmaster fall back to the
defaults and log the error; `graphmaster doctor` reports it.

## Options

```toml
# How searches over code are steered to the code graph:
#   "balanced"  redirect the first symbol-like grep/glob, allow it when repeated
#   "strict"    always redirect code searches to the graph
#   "off"       never steer; the MCP tools stay available
mode = "balanced"

# Files larger than this are not parsed.
max_file_size = "1MiB"

# Paths never indexed (gitignore syntax); setting this replaces the defaults.
excludes = ["vendor/", "node_modules/", "dist/", "build/", "target/", "*.min.*"]
# Extra paths to skip on top of excludes.
extra_excludes = []

# Languages to index (default: all).
languages = ["go", "python", "typescript", "tsx", "javascript", "rust", "java", "c", "cpp", "csharp", "php"]

# Balanced mode stops redirecting after this many denials per agent session.
soft_budget = 8

# How long each caller waits for another process updating the index before
# answering from the current graph with a staleness banner.
[timeouts]
mcp = "20s"
pre_tool_use = "1.5s"
post_tool_use = "1.5s"
session_start = "9m50s"
subagent_start = "1.5s"
pre_compact = "1.5s"
```

| Key | Type | Default | Notes |
|---|---|---|---|
| `mode` | `"strict"` \| `"balanced"` \| `"off"` | `"balanced"` | case-insensitive; see [steering.md](steering.md) |
| `max_file_size` | size | `"1MiB"` | integer bytes or a string with a unit: `B`, `KB`/`MB`/`GB`/`TB` (powers of 1000), `KiB`/`MiB`/`GiB`/`TiB` (powers of 1024); must be > 0 |
| `excludes` | list of gitignore patterns | see above | **replaces** the default list |
| `extra_excludes` | list of gitignore patterns | `[]` | **accumulates** across layers on top of `excludes` |
| `languages` | list of language names | all | `go`, `python`, `typescript`, `tsx`, `javascript`, `rust`, `java`, `c`, `cpp`, `csharp`, `php`; aliases `golang`, `py`, `ts`, `js`, `jsx`, `rs`, `c++`, `cxx`, `cs`, `c#` |
| `soft_budget` | integer ≥ 0 | `8` | balanced-mode redirects per `(session, agent)`; `0` uses the default |
| `timeouts.mcp` | duration | `"20s"` | MCP tool calls and `graphmaster query` |
| `timeouts.pre_tool_use` | duration | `"1.5s"` | steering hooks |
| `timeouts.post_tool_use` | duration | `"1.5s"` | reindexing after edits and shell commands |
| `timeouts.session_start` | duration | `"9m50s"` | session start; Claude Code and Codex may build the first index here |
| `timeouts.subagent_start` | duration | `"1.5s"` | subagent start context |
| `timeouts.pre_compact` | duration | `"1.5s"` | context re-injected before compaction |

Durations use Go syntax: `"750ms"`, `"1.5s"`, `"2m"`.

Files ignored by git (`.gitignore` at every level, `.git/info/exclude`,
`core.excludesFile`) are never indexed, in addition to `excludes` and
`extra_excludes`. Untracked files that are not ignored *are* indexed, so new
files show up before you `git add` them.

### Timeouts in practice

The timeouts are lock budgets, not deadlines for the work itself. Only one
process writes a repository's graph at a time (`.graphmaster/write.lock`).
If a caller finds changes while another process is writing, it waits up to
its budget; after that it answers from the current graph and marks the
answer `⚠ stale: N files pending`. Hooks add a small grace period on top of
the budget and are installed with agent-side timeouts above that, so a
slow index never makes an agent's hook fail.

### When options take effect

Options are read by every process at start, so a change applies to the next
tool call or hook; there is nothing to restart. Changing `max_file_size`,
`excludes` or `extra_excludes` makes the next refresh rebuild the graph
(parse results are cached in `cache.db`, so this is mostly a re-link).
Changing `languages` reprocesses only the affected languages.

## Environment variables

| Variable | Overrides | Format |
|---|---|---|
| `GRAPHMASTER_MODE` | `mode` | `strict`, `balanced` or `off` |
| `GRAPHMASTER_MAX_FILE_SIZE` | `max_file_size` | `2MiB`, `500000`, ... |
| `GRAPHMASTER_EXCLUDES` | `excludes` (replaces) | comma-separated |
| `GRAPHMASTER_EXTRA_EXCLUDES` | `extra_excludes` (appends) | comma-separated |
| `GRAPHMASTER_LANGUAGES` | `languages` | comma-separated |
| `GRAPHMASTER_SOFT_BUDGET` | `soft_budget` | integer |
| `GRAPHMASTER_TIMEOUT_MCP` | `timeouts.mcp` | duration |
| `GRAPHMASTER_TIMEOUT_PRE_TOOL_USE` | `timeouts.pre_tool_use` | duration |
| `GRAPHMASTER_TIMEOUT_POST_TOOL_USE` | `timeouts.post_tool_use` | duration |
| `GRAPHMASTER_TIMEOUT_SESSION_START` | `timeouts.session_start` | duration |
| `GRAPHMASTER_TIMEOUT_SUBAGENT_START` | `timeouts.subagent_start` | duration |
| `GRAPHMASTER_TIMEOUT_PRE_COMPACT` | `timeouts.pre_compact` | duration |

Empty values are ignored. The variables must be visible to the process the
agent starts (`graphmaster mcp`, `graphmaster hook`), so set them in the
environment you launch the agent from.

Debugging aids (not needed in normal use):

| Variable | Effect |
|---|---|
| `GRAPHMASTER_RECORD=<dir>` | `graphmaster hook` dumps each payload and its environment into `<dir>` (used to capture test fixtures) |
| `GRAPHMASTER_CPUPROFILE=<file>` | `graphmaster index` writes a CPU profile |
| `GRAPHMASTER_TRACE_RESOLVE=1` | prints name-resolution phase timings |

Agent locations honour `CLAUDE_CONFIG_DIR` (Claude Code), `CODEX_HOME`
(Codex) and `XDG_CONFIG_HOME` (OpenCode, graphmaster's own config).

## Examples

Turn steering off for one repository:

```sh
mkdir -p .graphmaster && printf 'mode = "off"\n' > .graphmaster/config.toml
```

Index only the backend of a mixed repository and skip generated protobufs:

```toml
# <repo>/.graphmaster/config.toml
languages = ["go"]
extra_excludes = ["*.pb.go", "third_party/"]
```

Give a slow network file system more time before answering stale:

```toml
[timeouts]
pre_tool_use = "3s"
mcp = "60s"
```

## On-disk layout

Per repository, in the work tree root:

```text
.graphmaster/
  .gitignore     "*", so nothing in here is ever committed
  config.toml    optional project config (you create it)
  graph.db       the code graph (SQLite, WAL); derived data, safe to delete
  cache.db       parse cache keyed by content; survives graph rebuilds
  files.snap     snapshot of the file table for fast hook start-up
  write.lock     cross-process writer lock
  log            hook errors (rotated to log.1 at 1 MiB)
```

graphmaster also appends `/.graphmaster/` to `.git/info/exclude` the first
time it indexes a repository. It never edits tracked files. Deleting
`.graphmaster/` is always safe: the next session rebuilds it.

Per user:

```text
~/.config/graphmaster/          ($XDG_CONFIG_HOME/graphmaster)
  config.toml                   user config
  install-state.json            what install changed (hashes and insertions)
  install.lock                  serializes concurrent install/uninstall runs
  backups/<timestamp>/...       copies of agent config files before the first edit
```
