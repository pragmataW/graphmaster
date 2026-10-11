# Architecture

graphmaster is one Go binary (cgo for tree-sitter and SQLite). There is no
daemon and no file watcher. Two kinds of short- or medium-lived processes do
all the work:

- `graphmaster mcp`, started by the agent over stdio and alive for the agent
  session;
- `graphmaster hook <event> --agent <name>`, started by the agent for each
  hook event and gone a few milliseconds later.

Both start every request with the same freshness check against the working
tree, so the graph is never older than the request that reads it. The only
shared state is the SQLite database in `<repo>/.graphmaster/`: one writer at a
time (a file lock), any number of readers (WAL mode).

```text
 agent ──stdio──▶ graphmaster mcp ──▶ internal/mcp ──▶ internal/query ─┐
 agent ──exec───▶ graphmaster hook ─▶ internal/hookrun ─▶ internal/steer ┤
 you ───────────▶ graphmaster query / ui / index / status ──────────────┤
                                                                         ▼
                               internal/index (Freshen: dirty check + pipeline)
                                 │            │             │            │
                           internal/repo  internal/parse  internal/store  internal/resolve
                           (git, ignore,  (tree-sitter,   (graph.db,      (name resolution,
                            filters)       facts)          cache.db)       confidence tiers)
```

Dependencies point one way: adapters, `mcp` and `ui` depend on `query` and
`steer`, which depend on `index`, which depends on `store`, `parse`, `repo`
and `resolve`. All logic lives in the core; adapters only translate formats.

## Packages

| Package | Responsibility |
|---|---|
| `cmd/graphmaster` | cobra CLI: `index`, `status`, `query`, `mcp`, `hook`, `ui`, `install`, `uninstall`, `doctor` |
| `internal/model` | language and kind enums, per-file `Facts`, the `LanguageSpec` interface |
| `internal/config` | TOML layers (defaults < user < project < env) and validation |
| `internal/repo` | locate the repository (worktrees, submodules), decode `.git/index`, walk untracked files with git ignore semantics and a directory cache, content filters |
| `internal/parse` | tree-sitter engine and parser pool, per-language specs (`lang_<x>.go`) and queries (`queries/<x>.scm`), facts codec |
| `internal/store` | SQLite schema, open/pragmas, batched writer, cross-process lock, FTS, parse cache |
| `internal/index` | `Freshen`: dirty detection, pipeline, rebuilds, status |
| `internal/resolve` | resolution framework, generic resolver, `resolve/<lang>.go` rules |
| `internal/query` | the 13 tools as a plain Go API with a token-budgeted text format |
| `internal/steer` | pattern, glob and shell classifiers; the steering decision engine; session start context |
| `internal/hookapi` | agent-neutral hook event and decision types |
| `internal/hookrun` | hook dispatcher: agent detection, Cursor dedupe, per-event deadlines, fail-open, background builds, hook log |
| `internal/adapters/{claude,codex,cursor,opencode}` | payload parsing, decision rendering, install plans |
| `internal/install` | config-file edit engine (JSON/JSONC, TOML and Markdown blocks, generated files), backups, install state, binary path resolution, plan checks for `doctor` |
| `internal/instructions` | the instruction block placed in `CLAUDE.md` / `AGENTS.md` |
| `internal/mcp` | MCP server wiring (`modelcontextprotocol/go-sdk`), per-repository engines |
| `internal/ui` | `graphmaster ui`: bounded JSON API and an embedded, build-free frontend (vendored cytoscape) |
| `internal/version` | build metadata set with `-ldflags` |
| `internal/testutil`, `internal/replay`, `internal/perfgate`, `internal/e2e`, `internal/deps` | test fixtures and synthetic repositories, hook payload replay, benchmark thresholds, end-to-end tests, dependency pins and ABI smoke tests |

## Freshness: `Indexer.Freshen(ctx, budget)`

Every MCP tool call, every hook and every CLI command that reads the graph
calls `Freshen` first. Concurrent calls in one process share one run
(singleflight).

1. **Locate** the repository from the working directory: git root, worktree
   `.git` files and common dirs; submodules are opaque. On first use create
   `.graphmaster/` with a `*` `.gitignore` and add `/.graphmaster/` to
   `.git/info/exclude`.
2. **Tracked files.** If the stat of `.git/index` is unchanged, reuse the
   tracked file list; otherwise decode the index with go-git's format
   package, falling back to `git ls-files -z --stage` for index features it
   cannot read (split index, sparse checkout).
3. **Untracked files.** Walk the tree with the full ignore stack (per
   directory `.gitignore`, `info/exclude`, `core.excludesFile`, plus the
   configured excludes) and git's untracked-cache trick: a directory whose
   mtime and `.gitignore` stat are unchanged has the same entries, so it is
   not read again. The directory cache is persisted in the `dirs` table.
4. **Stat sweep.** Parallel `lstat` of candidate files, plus dirty hints
   recorded by post-edit hooks. A file is clean when `(mtime, size, inode,
   mode)` are unchanged *and* its mtime is older than the previous scan start
   minus 2 seconds (git's racy-git rule).
5. **Classify.** New and changed files get a content key (the git blob
   SHA-1, read from `.git/index` for clean tracked files). An equal key only
   updates the stored stat (a `touch` never reparses). Missing files are
   deletions.
6. **Nothing changed:** return. This is the common path: about 50 ms on
   Kubernetes, 136 ms on 100,000 synthetic files in a warm process.
7. **Changes found:** take the write lock (`.graphmaster/write.lock`, flock)
   within the caller's budget (MCP 20 s, PreToolUse 1.5 s, SessionStart
   9m50s, ...), then repeat steps 2–5 under the lock. If the budget runs out,
   answer from the current graph with `⚠ stale: N files pending`.
8. **Pipeline.** Content filters (size cap, binary, minified, generated) →
   parse cache lookup in `cache.db` → worker pool (one parser and query
   cursor per worker and language; compiled queries are shared read-only) →
   bounded channel → a single writer goroutine with prepared statements that
   commits about every 500 files or 200 ms → name resolution pass →
   `meta.state = ready`.
9. **Crash safety.** Each file's facts are written atomically in one
   transaction, together with the names they affect in `pending_names`. After
   a crash the old stat rows simply make the file dirty again and the
   resolver continues from the queue. A `graph.db` that fails to open, fails
   `quick_check` or has a different schema version is moved aside and rebuilt
   (`cache.db` survives, so rebuilds are mostly cache hits). Bumping one
   language's extractor version re-extracts only that language's files.
10. **First build.** While it runs, `meta.state = building` and `progress`
    are visible to readers, which answer from the partial graph with
    `⚠ building 37% (partial graph)`. Agents that cannot wait at session
    start get a detached `graphmaster index --quiet`.

Module identities and import targets depend on other files (`go.mod`,
`tsconfig.json`, whether `a/b.py` exists). Every such lookup is recorded with
the file it was made for (`dep_owners`); when the answer changes, only those
files are reprocessed.

An incremental update and a from-scratch build must produce the same graph.
The index tests check this with an oracle: random edits, creations,
deletions, renames, branch switches and `.gitignore` changes, comparing a
canonical dump of the incremental graph with a fresh build after every step,
plus a kill test that crashes after N batches and checks the recovery.

## Name resolution

Extraction stores references by name with their qualifier or receiver and
no target (`edges.dst IS NULL`). The resolver processes the
`pending_names` queue in batches: for each pending name it re-resolves every
edge carrying that name and replaces the rows, using five confidence tiers
(import-qualified 100, lexical 90, receiver-typed 80, unique global 50,
ambiguous 30/n). Processed names leave the queue in the same transaction as
their edge updates. Details: [languages.md](languages.md#how-resolution-works).

## Schema overview

`graph.db` holds derived data only. There are no migrations: a schema version
mismatch (`PRAGMA user_version`) triggers a rebuild. Pragmas: WAL journal,
`synchronous=NORMAL`, in-memory temp store, 1 GiB mmap, 5 s busy timeout.

| Table | Contents |
|---|---|
| `meta` | key/value state: schema and extractor versions, `state`, `progress`, last scan time, git index stat, config signature |
| `files` | one row per file: path, language, stat (size, mtime, inode, mode), git blob SHA-1, tracked flag, status (ok, too big, generated, minified, binary, parse error, unsupported), LOC, package |
| `dirs` | untracked-cache: directory mtime, `.gitignore` stat, entry listing |
| `names` | interned identifiers |
| `nodes` | definitions: file, parent, kind, name, qualified name, line/byte span, signature (≤ 200 chars), flags, 8-byte stable key (`skey`) |
| `edges` | `contains`, `calls`, `references`, `imports`, `extends`, `implements`, `embeds`, `tests`, `includes`, `declares`; `dst` NULL while unresolved; qualifier, position and confidence 0–100 |
| `imports` | import spec, alias, imported names, resolved target file or package |
| `packages` | language packages/modules and their directories |
| `pending_names` | resolution queue |
| `dep_owners` | which files depend on which repository lookups |
| `dirty_hints` | paths reported by post-edit hooks |
| `sessions`, `session_patterns` | steering state per (session, agent): denies, allows, normalized patterns already redirected |
| `hook_dedupe` | replayable decisions for duplicate hook calls (10 minute TTL) |
| `sym_fts` | FTS5 over identifier tokens (camelCase/snake_case split) and paths, prefix indexes 2 and 3 |
| `sym_tri` | FTS5 trigram index over raw names for substring search |

Both FTS tables are contentless with `contentless_delete=1`, so they add
little to the database size.

`cache.db` is a separate file so it survives `graph.db` rebuilds:
`parse_cache(blob, lang, xver, facts, last_used)` keyed by content, language
and extractor version, with facts in a compact varint encoding. Garbage
collection drops entries unused for 30 days, then trims the cache to twice
the size the current graph references, least recently used first. A branch switch back and forth is
therefore almost entirely cache hits.

## Query engine

The tools in `internal/query` read SQLite directly. Call trees and impact
sets use recursive CTEs with `UNION` deduplication, a depth column and a
`LIMIT` just above the token budget; `traverse` runs its BFS in Go so it can
rank by confidence and node degree and summarize hubs. Output is plain text,
measured as bytes / 4 against `max_tokens`. The MCP server and the
`graphmaster query` CLI call the same `query.Tools()` registry, so they print
identical text.

## Hooks

`internal/hookrun` handles one invocation: parse the `--agent` flag, detect
Cursor payloads on Claude hooks, decode the payload through the adapter,
locate the repository, load the config, route the event (session start,
subagent start, pre-tool-use, post-tool-use, pre-compact), and render the
decision through the adapter. Each event has a deadline (its lock budget plus
a grace period, capped below the agent-side timeout `install` writes). Any
error, panic or missed deadline produces a "no opinion" answer and a line in
`.graphmaster/log`; the process always exits 0.

## UI

`graphmaster ui` binds `127.0.0.1` (a random port by default) and prints a URL
with a random token. Every request must carry the token and a `Host` header
naming `localhost` or `127.0.0.1`, which blocks other local users and DNS
rebinding. The API never loads the whole graph: `/api/search`, `/api/node`,
`/api/ego`, `/api/file`, `/api/packages` and `/api/impact` each answer one
bounded question (at most 1500 elements per response). The frontend is plain
JavaScript with a vendored cytoscape, embedded with `go:embed`; there is no
build step.
