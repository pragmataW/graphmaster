# Cursor config fixtures

`home/` is a **synthetic** but realistic user HOME for the Cursor adapter
install tests (`internal/adapters/cursor`). The tests copy it into a temporary
directory, apply the adapter's install plan through `install.Engine` twice
(must be idempotent) and uninstall (must restore every byte).

- `.cursor/mcp.json`: JSONC with comments, a trailing comma and two unrelated
  MCP servers that must survive.
- `.cursor/hooks.json`: existing user hooks (including on events graphmaster
  also uses) and a comment.
- `.cursor/rules/`: present, so the adapter places its instruction block in
  `~/.cursor/rules/graphmaster.mdc`.

These files were written by hand from Cursor's documentation; they are not
copies of a real user's configuration.
