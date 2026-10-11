// Package steer decides whether an agent's search (grep, glob, shell) should
// be redirected to the code graph. This file holds pure path-scope helpers.
package steer

import (
	"path/filepath"
	"strings"

	"github.com/pragmataW/graphmaster/internal/model"
)

// ExtSet is a set of enabled code file extensions (with leading dot). A nil
// ExtSet means "every supported language".
type ExtSet map[string]struct{}

// NewExtSet builds the extension set for the given languages.
func NewExtSet(langs []model.Lang) ExtSet {
	s := ExtSet{}
	for _, l := range langs {
		for _, e := range l.Extensions() {
			s[e] = struct{}{}
		}
	}
	return s
}

// DefaultExtSet returns the extensions of every supported language.
func DefaultExtSet() ExtSet { return NewExtSet(model.AllLangs()) }

// Has reports whether ext (with leading dot) is a code extension. Exact case
// is tried first so ".C" can differ from ".c"; then a lowercase match.
func (s ExtSet) Has(ext string) bool {
	if ext == "" {
		return false
	}
	if s == nil {
		return model.LangFromExt(ext) != model.LangUnknown
	}
	if _, ok := s[ext]; ok {
		return true
	}
	_, ok := s[strings.ToLower(ext)]
	return ok
}

// IsCodePath reports whether path names a file with an enabled code
// extension. Directories and extensionless names are not code paths.
func IsCodePath(path string, exts ExtSet) bool {
	ext, ok := extOf(baseName(path))
	return ok && exts.Has(ext)
}

// TargetKind classifies a search target (file, directory or path glob).
type TargetKind int

const (
	// TargetTree is a directory or anything that may contain code files.
	TargetTree TargetKind = iota
	// TargetCode is a single code file (or a glob limited to code files).
	TargetCode
	// TargetNonCode only covers non-code files (docs, configs, stdin...).
	TargetNonCode
)

func (k TargetKind) String() string {
	switch k {
	case TargetCode:
		return "code"
	case TargetNonCode:
		return "noncode"
	default:
		return "tree"
	}
}

const globMeta = "*?["

// ClassifyTarget classifies a single target path without touching the
// filesystem. Extensionless names are assumed to be directories.
func ClassifyTarget(path string, exts ExtSet) TargetKind {
	switch path {
	case "-", "/dev/null", "/dev/stdin":
		return TargetNonCode
	}
	if path == "" || strings.HasSuffix(path, "/") || strings.HasSuffix(path, `\`) {
		return TargetTree
	}
	base := baseName(path)
	if base == "." || base == ".." {
		return TargetTree
	}
	anyCode := false
	for _, alt := range expandBraces(base) {
		ext, ok := extOf(alt)
		if !ok || strings.ContainsAny(ext, globMeta) {
			// "*", "*.*", "src", "Makefile": cannot rule out code.
			return TargetTree
		}
		if exts.Has(ext) {
			anyCode = true
		}
	}
	if anyCode {
		return TargetCode
	}
	return TargetNonCode
}

// AllNonCode reports whether every target is non-code. An empty target list
// means the current directory and therefore returns false.
func AllNonCode(paths []string, exts ExtSet) bool {
	if len(paths) == 0 {
		return false
	}
	for _, p := range paths {
		if ClassifyTarget(p, exts) != TargetNonCode {
			return false
		}
	}
	return true
}

// OutsideRoot reports whether target (relative to cwd unless absolute) lies
// outside root. It is pure path arithmetic; symlinks are not resolved.
func OutsideRoot(root, cwd, target string) bool {
	abs := target
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(cwd, target)
	}
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(abs))
	if err != nil {
		return true
	}
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// AllOutsideRoot reports whether every target lies outside root. An empty
// list means cwd, which is checked instead.
func AllOutsideRoot(root, cwd string, targets []string) bool {
	if len(targets) == 0 {
		return OutsideRoot(root, cwd, ".")
	}
	for _, t := range targets {
		if !OutsideRoot(root, cwd, t) {
			return false
		}
	}
	return true
}

// baseName returns the last slash- or backslash-separated segment.
func baseName(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}

// extOf returns the extension (with dot) of a base name. Dotfiles such as
// ".gitignore" and names without a dot have no extension.
func extOf(base string) (string, bool) {
	dot := strings.LastIndexByte(base, '.')
	if dot <= 0 || dot == len(base)-1 {
		return "", false
	}
	return base[dot:], true
}

// looksLikeFile reports whether p plausibly names a single file rather than a
// directory: its last segment has an extension.
func looksLikeFile(p string) bool {
	if p == "" || strings.HasSuffix(p, "/") {
		return false
	}
	base := baseName(p)
	if base == "." || base == ".." {
		return false
	}
	_, ok := extOf(base)
	return ok
}

// maxBraceExpansions bounds brace expansion to keep classification cheap.
const maxBraceExpansions = 64

// expandBraces performs shell-style {a,b} expansion. Braces without a
// top-level comma or without a match are kept literally.
func expandBraces(s string) []string {
	out := make([]string, 0, 1)
	expandInto(s, &out)
	return out
}

func expandInto(s string, out *[]string) {
	if len(*out) >= maxBraceExpansions {
		return
	}
	open, close, alts := findBraceGroup(s)
	if open < 0 {
		*out = append(*out, s)
		return
	}
	prefix, suffix := s[:open], s[close+1:]
	for _, a := range alts {
		expandInto(prefix+a+suffix, out)
	}
}

// findBraceGroup locates the first {...} group with a top-level comma and
// returns its bounds and comma-separated alternatives, or -1 if none.
func findBraceGroup(s string) (int, int, []string) {
	for i := 0; i < len(s); i++ {
		if s[i] != '{' {
			continue
		}
		end, alts := matchBraceGroup(s, i)
		if end < 0 {
			return -1, -1, nil // unbalanced: nothing further can match
		}
		if alts != nil {
			return i, end, alts
		}
	}
	return -1, -1, nil
}

// matchBraceGroup scans the group opening at s[open]. It returns the index of
// the matching '}' (or -1) and the alternatives (nil when there is no
// top-level comma, i.e. literal braces).
func matchBraceGroup(s string, open int) (int, []string) {
	depth := 0
	var alts []string
	start := open + 1
	for j := open; j < len(s); j++ {
		switch s[j] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				if alts == nil {
					return j, nil
				}
				return j, append(alts, s[start:j])
			}
		case ',':
			if depth == 1 {
				alts = append(alts, s[start:j])
				start = j + 1
			}
		}
	}
	return -1, nil
}
