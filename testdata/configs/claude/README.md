# Claude Code config fixtures

`home/` is a synthetic but realistic user HOME for Claude Code: a
`~/.claude.json` with other MCP servers and per-project state (JSONC comments
included), a `~/.claude/settings.json` that already has the user's own hooks
and permissions, and a `~/.claude/CLAUDE.md` with personal rules.

`internal/adapters/claude` tests copy this tree into a temporary HOME, apply
the adapter's install plan through `install.Engine` twice (must be idempotent)
and uninstall (must restore every file byte for byte).
