// Package model holds the language-neutral contracts shared by every
// graphmaster layer: language and kind enums, the per-file Facts produced by
// parsing, and the LanguageSpec / RepoContext interfaces used by parse and
// resolve.
//
// The package is intentionally dependency-free (no cgo, no tree-sitter) so it
// can be imported from anywhere. Enum values are persisted in graph.db and
// must never be renumbered; append new values at the end.
package model
