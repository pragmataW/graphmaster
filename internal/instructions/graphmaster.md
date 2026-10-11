## graphmaster: code graph navigation

The `graphmaster` MCP server serves a code graph of the current git repository. It refreshes itself before every call; never run `graphmaster index` by hand.

Prefer it over grep/rg/find/ls when navigating code:
- Where is X defined: `find_symbol` (name or qualified name).
- Partial names or file names: `search`.
- Getting oriented: `repo_map`; before reading a big file, `file_outline`, then `read_symbol` for the part you need.
- Relationships: `callers`, `callees`, `imports`, `inheritance`, `traverse`.
- Before changing code: `impact` (symbol, path or changed=true) and `tests_for`.

Search hooks may block a grep/rg/glob for a symbol-like pattern and reply with graph results instead. In the default balanced mode, if you really need a text match (strings, comments, config values, logs), run the same search again and it will be allowed. Non-code files and greps on one named file are never blocked.

If results look stale or empty, call `status`. The steering mode (strict, balanced, off) is set in ~/.config/graphmaster/config.toml or with GRAPHMASTER_MODE.
