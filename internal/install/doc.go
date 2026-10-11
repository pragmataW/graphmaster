// Package install is the agent-neutral configuration engine behind
// `graphmaster install` / `uninstall`.
//
// Agent adapters describe the edits they want as a Plan: a list of
// FileChange values, each targeting one file in one Format:
//
//   - FormatJSON: JSON or JSONC edited through github.com/tailscale/hujson so
//     comments, ordering and unrelated keys survive. Ops: JSONSet (object
//     member at a JSON pointer) and JSONAppend (array element).
//   - FormatTOML: a marker block (# graphmaster:begin v1 ... # graphmaster:end)
//     appended or replaced in place; the whole file is re-parsed with go-toml
//     before anything is written.
//   - FormatMarkdown: a marker block (<!-- graphmaster:begin v1 --> ...
//     <!-- graphmaster:end -->).
//   - FormatGenerated: a file graphmaster owns entirely (e.g. the OpenCode
//     plugin).
//
// The Engine applies plans, taking a timestamped backup of every file it
// touches the first time and recording pre/post hashes plus the exact
// insertions in install-state.json. Install is idempotent. Uninstall restores
// a file byte-for-byte from its backup when the file still has the
// post-install hash; otherwise it surgically removes only graphmaster's own
// insertions. Every operation can run as a dry run that reports unified diffs.
package install
