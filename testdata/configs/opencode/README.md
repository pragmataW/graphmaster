# OpenCode config fixtures

`home/` is a synthetic but realistic `$HOME` for the OpenCode adapter install
tests (`internal/adapters/opencode`). It mirrors what a user of OpenCode v1.x
typically has under `~/.config/opencode/`: a JSONC `opencode.json` with
comments, other MCP servers, permissions and agents; a global `AGENTS.md`; and
an unrelated user plugin in `plugins/`.

The tests copy the tree into a temp `HOME`, run `graphmaster install` twice
(idempotence) and `uninstall` (byte-exact restore). Never edit these files to
make a test pass; they model user data graphmaster must preserve.
