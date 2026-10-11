# Hook payload fixtures

This directory holds **real hook payloads** captured from each supported agent.
The hook integration tests (U51) replay them through `graphmaster hook` with a
temporary `HOME`, substitute `{{REPO}}` with a materialized fixture repo, and
compare stdout and exit code against goldens.

Agents change their hook schemas without notice. Recording payloads from real
agents keeps the adapters honest; hand-written JSON tends to encode our own
assumptions instead.

```
testdata/hooks/
  claude/    Claude Code
  codex/     Codex CLI
  cursor/    Cursor (native hooks, plus Cursor running Claude hooks)
  opencode/  OpenCode (generated plugin shim)
```

## How recording works

When the `GRAPHMASTER_RECORD` environment variable points to a directory, every
`graphmaster hook <event> --agent <agent>` invocation also writes

```
$GRAPHMASTER_RECORD/<agent>/<event>-<n>.json
```

before it does anything else. `<n>` counts up from 1. The implementation is in
`internal/hookrun/record.go`. Each file contains:

| field        | content                                                                 |
|--------------|-------------------------------------------------------------------------|
| `agent`      | the `--agent` value                                                     |
| `event`      | the event argument as the agent passed it                               |
| `cwd`        | the hook process working directory                                      |
| `env`        | only `CLAUDE_*`, `CODEX_*`, `CURSOR_*`, `OPENCODE_*`, `TERM_PROGRAM`     |
| `stdin`      | the raw stdin payload, embedded as JSON                                 |
| `stdin_text` | the payload as a string, used only when it is not valid JSON            |

Scrubbing happens automatically:

- The repository root becomes `{{REPO}}`, and your home directory becomes
  `{{HOME}}`. Symlink-resolved forms are scrubbed too (for example macOS
  `/private/var/...`), and so are JSON-escaped Windows paths.
- Env variables whose names contain `TOKEN`, `KEY`, `SECRET`, `PASSWORD`,
  `AUTH`, `CREDENTIAL` or `COOKIE` become `{{REDACTED}}`.

Recording never changes the hook's decision. If a recording fails, the hook
still runs (fail-open).

## Capturing (one-time, by a human)

Prerequisites: `graphmaster install` exists and has been run, and `graphmaster
doctor` shows the agent green.

1. Pick a throwaway clone. The `testdata/fixture-repo` checkout works well
   because U51 replays against the same tree:

   ```sh
   export GRAPHMASTER_RECORD="$PWD/.rec"   # outside testdata/, review before copying
   cd /path/to/fixture-repo
   ```

2. Start the agent **from that shell** so it inherits `GRAPHMASTER_RECORD`, then
   drive the scenarios listed below.

3. Review every file in `.rec/` (see "Before committing"), then copy the good
   ones into `testdata/hooks/<agent>/` and give each a descriptive name:

   ```
   testdata/hooks/claude/pre-tool-use-grep-first.json
   testdata/hooks/claude/pre-tool-use-grep-retry.json
   ```

### Claude Code

```sh
GRAPHMASTER_RECORD=$PWD/.rec claude
```

- Open a new session. This records `session-start`.
- Ask it to grep for a symbol such as `Foo`. You should get a deny first, and
  then an allow when the same pattern is retried.
- Ask for a glob, a read of a `.go` file and a read of a `.md` file.
- Ask it to run `rg Foo` and `ps aux | grep foo` through Bash.
- Make an edit (`post-tool-use` for Edit/Write) and a Bash edit such as
  `sed -i`.
- Spawn an Explore or Plan subagent. This records `subagent-start` and the
  subagent's tool calls, which carry `agent_id`.
- Run `/compact`. This records `pre-compact`.

### Codex

```sh
GRAPHMASTER_RECORD=$PWD/.rec codex
```

- Trust the hooks first: run `/hooks` inside Codex and approve graphmaster.
- Start a session (`session-start`).
- Ask it to run `rg Foo` and `grep -rn Foo .` (`pre-tool-use` Bash).
- Ask for a multi-file change so it uses `apply_patch` (`pre-tool-use` and
  `post-tool-use` with patch text).

### Cursor

Cursor reads the environment of the process that launched it. Start it from
the shell so it inherits the variable:

```sh
GRAPHMASTER_RECORD=$PWD/.rec cursor /path/to/fixture-repo
```

On macOS, quit Cursor completely first, or the running instance ignores the
new environment.

- Agent chat: grep, read and run a shell command (`preToolUse` Shell, Read and
  Grep, plus `beforeShellExecution`), edit a file (`afterFileEdit`), run a
  shell command that edits (`afterShellExecution`), and spawn a subagent
  (`subagentStart`).
- **Double execution:** if Claude Code hooks are also installed in
  `~/.claude/settings.json`, Cursor runs them too. The same tool call then
  produces files under both `.rec/cursor/` and `.rec/claude/`. Keep **both**
  files of at least one pair; U51 uses them to test dedupe. The `claude/` file
  will contain `cursor_version` and/or `conversation_id`/`generation_id`. Name
  it with a `cursor-` prefix, for example
  `claude/cursor-dup-pre-tool-use.json`.

### OpenCode

The generated plugin (`~/.config/opencode/plugins/graphmaster.js`) uses
`spawnSync` to call the binary. The child inherits OpenCode's environment:

```sh
GRAPHMASTER_RECORD=$PWD/.rec opencode
```

- Start a session, then grep, glob, read, run a bash command and edit a file.
- Trigger a deny and confirm that the thrown error message reaches the model.
  Note this in the commit message, because recordings cannot show it.

## Before committing

Review every file by hand, even though scrubbing is automatic:

- [ ] `grep -rn "$HOME\|$(whoami)" .rec/` finds nothing. Usernames can also
      appear outside paths: in git author fields, hostnames and prompts.
- [ ] No API keys, tokens, emails or org names appear in `stdin` (prompts and
      transcripts paths can include them).
- [ ] `transcript_path`-style fields point under `{{HOME}}`. Their contents are
      **not** captured, and they do not need to be.
- [ ] Every repo path uses `{{REPO}}`. If you recorded outside the fixture
      repo, rewrite paths so they exist in `testdata/fixture-repo`.
- [ ] Drop near-duplicates. Keep one file per distinct shape or scenario.

Commit:

- the reviewed JSON files under `testdata/hooks/<agent>/`;
- a short note in the commit message with the agent **name and version**
  (`claude --version`, `codex --version`, Cursor "About", `opencode --version`)
  and the capture date, so we can tell when a schema change happened.

Do not commit:

- the `.rec/` directory itself (unreviewed);
- transcript files, session databases or anything copied from `~/.claude`,
  `~/.codex`, `~/.cursor` or `~/.config/opencode`.

## Re-capturing

When an agent changes its hook schema, add the new payloads **next to** the
old ones with a version suffix, for example `pre-tool-use-grep.v2.json`. Old
agent versions are still in use. Replace the old files only when an adapter
intentionally drops support for that version.
