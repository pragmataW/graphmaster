# Agent integrations

`graphmaster install` writes user-level configuration for every supported
agent it detects; `--agents claude,codex,cursor,opencode` picks agents
explicitly (also ones that are not detected). Nothing is written per
project.

Every agent gets the same three things:

1. **The MCP server** `graphmaster` (`graphmaster mcp` over stdio), which
   serves the [13 tools](mcp-tools.md).
2. **Hooks** (`graphmaster hook <event> --agent <name>`), which steer
   searches, inject context and keep the graph fresh after edits.
3. **Instructions**: a short block telling the model which tool answers which
   question.

All agent-specific code is a thin translation layer in
`internal/adapters/<agent>`: payload parsing, decision rendering and the
install plan. The decisions themselves are made by the shared core
(`internal/steer`, `internal/hookrun`), so every agent follows the same rules
within what its hook API allows.

## Detection

| Agent | Detected when |
|---|---|
| Claude Code | `~/.claude/` or `~/.claude.json` exists (or `$CLAUDE_CONFIG_DIR`), or `claude` is on `PATH` |
| Codex CLI | `~/.codex/` exists (or `$CODEX_HOME`), or `codex` is on `PATH` |
| Cursor | `~/.cursor/` exists, or `cursor` is on `PATH` |
| OpenCode | `~/.config/opencode/` exists (or under `$XDG_CONFIG_HOME`), `~/.opencode/bin/opencode` exists, or `opencode` is on `PATH` |

## How install edits files

- **JSON / JSONC** files are edited in place with
  [hujson](https://github.com/tailscale/hujson): comments, key order and
  unrelated entries survive. graphmaster sets its own `graphmaster` member or
  appends its own hook entries; it never rewrites yours.
- **TOML** (`~/.codex/config.toml`) and **Markdown** files get a marker block
  (`# graphmaster:begin v1` … `# graphmaster:end`,
  `<!-- graphmaster:begin v1 -->` … `<!-- graphmaster:end -->`). TOML is
  re-parsed before anything is written.
- **Generated files** (the OpenCode plugin, the Cursor rule) are owned by
  graphmaster entirely.
- Before the first edit of each file a timestamped copy goes to
  `~/.config/graphmaster/backups/`, and `install-state.json` records the
  file's hash before and after plus every insertion.
- Install is idempotent: running it again changes nothing, or updates the
  entries in place when the binary path changed.
- `--dry-run` prints unified diffs and writes nothing.

`graphmaster uninstall` restores a file byte for byte from its backup when
the file still has the hash install left it with. If you edited the file
since, only graphmaster's insertions are removed and your edits stay. Files
graphmaster created are deleted, along with directories it created for them.

Hook commands use an absolute path to the binary. `install` resolves a
stable one: Homebrew's `bin` symlink rather than the versioned Cellar path
(so `brew upgrade` keeps working), else the `PATH` entry, else the running
executable. It refuses to register a temporary build such as `go run`.

## Claude Code

| What | Where |
|---|---|
| MCP server | `~/.claude.json` → `mcpServers.graphmaster` (`type: stdio`) |
| Hooks | `~/.claude/settings.json` → `hooks` |
| Instructions | marker block in `~/.claude/CLAUDE.md` |

`$CLAUDE_CONFIG_DIR` (absolute) relocates all three into that directory.

```json
{
  "hooks": {
    "SessionStart":  [{ "hooks": [{ "type": "command", "command": "/opt/homebrew/bin/graphmaster hook session-start --agent claude", "timeout": 600 }] }],
    "SubagentStart": [{ "hooks": [{ "type": "command", "command": "/opt/homebrew/bin/graphmaster hook subagent-start --agent claude", "timeout": 30 }] }],
    "PreToolUse":    [{ "matcher": "Grep|Glob|Read|Bash", "hooks": [{ "type": "command", "command": "/opt/homebrew/bin/graphmaster hook pre-tool-use --agent claude", "timeout": 10 }] }],
    "PostToolUse":   [{ "matcher": "Edit|MultiEdit|Write|NotebookEdit|Bash", "hooks": [{ "type": "command", "command": "/opt/homebrew/bin/graphmaster hook post-tool-use --agent claude", "timeout": 15 }] }],
    "PreCompact":    [{ "hooks": [{ "type": "command", "command": "/opt/homebrew/bin/graphmaster hook pre-compact --agent claude", "timeout": 30 }] }]
  }
}
```

Behaviour:

- **SessionStart** builds the index on first use (waits up to 9m50s) and
  injects the start context (`additionalContext`).
- **SubagentStart** injects the same context into subagents. Claude's Explore
  and Plan agents do not read `CLAUDE.md`, so this is how they learn about the
  graph.
- **PreToolUse** steers `Grep`, `Glob` and `Bash` searches
  (`permissionDecision: "deny"` with the graph results as the reason). For a
  whole-file `Read` of a code file over 400 lines it allows the read and adds
  an outline. graphmaster never answers `allow` explicitly, so your own
  permission rules still apply.
- **PostToolUse** reindexes edited files, or runs a full refresh after `Bash`.
- **PreCompact** keeps the session going; Claude Code accepts no model
  context there.

After installing, restart Claude Code; `claude mcp list` should list
`graphmaster`. Claude asks before the first use of each MCP tool; allow
`mcp__graphmaster` in `/permissions` to skip those prompts.

## Codex CLI

| What | Where |
|---|---|
| MCP server | marker block in `~/.codex/config.toml` |
| Hooks | `~/.codex/hooks.json` |
| Instructions | marker block in `~/.codex/AGENTS.md` |

`$CODEX_HOME` (absolute) replaces `~/.codex`.

```toml
# graphmaster:begin v1
[mcp_servers.graphmaster]
command = "/opt/homebrew/bin/graphmaster"
args = ["mcp"]
# graphmaster:end
```

Hooks: `SessionStart` (600 s), `SubagentStart` (30 s), `PreToolUse` with
matcher `Bash` (10 s), `PostToolUse` with matcher `Bash|apply_patch` (15 s).
Codex has no grep or glob tool and no PreCompact event: it searches and
reads through the shell, so steering works by parsing shell commands
(`rg Foo`, `grep -rn Foo .`, `find . -name '*.go'`, ...; see
[steering.md](steering.md#shell-commands)). After `apply_patch` the patched
paths are parsed from the patch and reindexed.

**Trust the hooks.** Codex runs user hooks only after they are approved:
start `codex`, run `/hooks` and approve the graphmaster entries. Codex stores
the approval in `config.toml` (`[hooks.state."…"] trusted_hash`).
`graphmaster doctor` reads it and reports untrusted hooks:

```text
codex
  ok    hooks installed in ~/.codex/hooks.json -> /opt/homebrew/bin/graphmaster
  WARN  4 of 4 graphmaster hooks not trusted yet: start codex, run /hooks and approve the graphmaster hooks
```

Approve them again whenever a reinstall changes them.

**AGENTS.md size.** Codex reads only the first 32 KiB of `AGENTS.md` by
default (`project_doc_max_bytes`). The graphmaster block is about 1 KiB and
goes at the end; `install` warns when the file is already larger than 32 KiB.

## Cursor

| What | Where |
|---|---|
| MCP server | `~/.cursor/mcp.json` → `mcpServers.graphmaster` |
| Hooks | `~/.cursor/hooks.json` (schema `version: 1`) |
| Instructions | `~/.cursor/rules/graphmaster.mdc` (`alwaysApply: true`) when `~/.cursor/rules/` exists; otherwise the MCP server's own instructions |

Hooks: `sessionStart` (10 s), `subagentStart` (5 s), `preToolUse` with matcher
`Shell|Read|Grep` (5 s), `beforeShellExecution` (5 s), `afterFileEdit` (10 s),
`afterShellExecution` (10 s). Each runs
`graphmaster hook <event> --agent cursor`; the native event name travels in
the payload.

Behaviour:

- **sessionStart** cannot block, so it never waits for a build. It starts a
  detached `graphmaster index --quiet` and returns
  `additional_context`: the full start context when an index exists, or a
  short "being built in the background" note on first use.
- **preToolUse / beforeShellExecution** answer
  `{"permission": "deny", "agent_message": "<graph results>", "user_message": "graphmaster: …"}`
  to redirect a search. Both see shell commands on purpose: when
  `preToolUse` denies, the command never reaches `beforeShellExecution`; when
  it allows, the core allows the identical command again.
- Cursor cannot attach context to an allowed tool call, so there are no read
  outlines and no subagent context.
- **afterFileEdit / afterShellExecution** keep the graph fresh.

**Duplicate hooks.** Cursor also executes Claude Code's hooks from
`~/.claude/settings.json`. When both are installed, graphmaster recognizes
Cursor's payload on the `--agent claude` call (`cursor_version`, or
`conversation_id` with `generation_id`) and exits silently, so the
Cursor-native hook makes the only decision. If only the Claude hooks are
installed, they steer Cursor too. `doctor` shows which case applies:

```text
cursor
  ok    Cursor also runs the hooks in ~/.claude/settings.json; dedupe active: Cursor-native graphmaster hooks answer and the duplicate calls stay silent
```

Cursor keeps its User Rules in a settings database, not a file. If
`~/.cursor/rules/` does not exist, paste the graphmaster block (from
`~/.claude/CLAUDE.md` or `internal/instructions/graphmaster.md`) into
*Cursor Settings → Rules → User Rules* if you want it pinned. Restart Cursor
after installing and check that the server is enabled under *Cursor Settings
→ MCP*.

## OpenCode

| What | Where |
|---|---|
| MCP server | `~/.config/opencode/opencode.json` → `mcp.graphmaster` (`type: local`); `opencode.jsonc` is edited instead when only that file exists |
| Hooks | generated plugin `~/.config/opencode/plugins/graphmaster.js` |
| Instructions | marker block in `~/.config/opencode/AGENTS.md` |

`$XDG_CONFIG_HOME` (absolute) replaces `~/.config`.

OpenCode has no command hooks; it loads JavaScript plugins. graphmaster
generates a small plugin with the absolute binary path embedded. The plugin
runs `graphmaster hook <event> --agent opencode` with `spawnSync` and a
timeout (5 s for gating events, 8 s for post-tool notifications) and speaks
a private JSON protocol with it:

| Plugin hook | graphmaster event | Effect |
|---|---|---|
| `tool.execute.before` for `grep`, `glob`, `list`, `read`, `bash` | `pre-tool-use` | a deny throws an `Error` carrying the graph results |
| `tool.execute.after` for `bash` | `post-tool-use` | full refresh |
| `file.edited` event (edit, write, apply_patch) | `post-tool-use` | reindex the file |
| `session.created` event | `session-start` / `subagent-start` | background build; child sessions are attributed as subagents |
| `experimental.session.compacting` | `pre-compact` | appends the start context to the compaction prompt |

Caveats: OpenCode plugins cannot add context to tool calls or to session
start, so graphmaster's guidance reaches the model through the
`AGENTS.md` block, the MCP server instructions and the error message of a
redirected search. If the binary moves, run `graphmaster install` again to
regenerate the plugin. The plugin fails open: any spawn error, timeout or
unexpected output lets the tool run.

## Hook protocol summary

| Neutral event | Claude Code | Codex | Cursor | OpenCode |
|---|---|---|---|---|
| `session-start` | SessionStart | SessionStart | sessionStart | session.created |
| `subagent-start` | SubagentStart | SubagentStart | subagentStart | session.created (child) |
| `pre-tool-use` | PreToolUse | PreToolUse | preToolUse, beforeShellExecution | tool.execute.before |
| `post-tool-use` | PostToolUse | PostToolUse | afterFileEdit, afterShellExecution | tool.execute.after, file.edited |
| `pre-compact` | PreCompact | (none) | (none) | experimental.session.compacting |

Every hook process exits 0 and fails open: an internal error, a panic or a
missed deadline produces the agent's "no opinion" answer and a line in
`<repo>/.graphmaster/log`. A hook never blocks a tool because graphmaster
broke.
