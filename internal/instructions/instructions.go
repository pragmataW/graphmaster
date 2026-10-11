// Package instructions holds the agent-neutral steering text that
// `graphmaster install` places in each agent's instruction file
// (~/.claude/CLAUDE.md, ~/.codex/AGENTS.md, OpenCode's AGENTS.md and a
// Cursor rule). It is kept short so it fits comfortably inside Codex's
// 32 KiB AGENTS.md budget next to the user's own rules.
package instructions

import _ "embed"

// MaxBytes caps the embedded text.
const MaxBytes = 2048

//go:embed graphmaster.md
var text string

// Text returns the Markdown instruction block body.
func Text() string { return text }
