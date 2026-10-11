// Package repo discovers what is in a git working tree without spawning git
// on the hot path: it locates the repository (worktrees and submodules
// included), decodes .git/index for tracked files, walks the tree for
// untracked-but-not-ignored files with git-compatible ignore semantics and a
// directory mtime cache, and classifies file contents (size cap, binary,
// minified, generated, language).
//
// All paths returned by this package are repo-relative and slash-separated.
// Persistence of the stat and directory caches is the store's job; this
// package only produces and consumes plain rows.
package repo
