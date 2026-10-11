package repo

import (
	"bytes"
	"strings"
)

// Pattern flags, mirroring git's PATTERN_FLAG_*.
const (
	patNegative  = 1 << iota // "!pattern"
	patMustBeDir             // trailing "/"
	patNoDir                 // no "/" inside: match the basename only
	patEndsWith              // "*literal": suffix compare fast path
)

// ignorePattern is one parsed gitignore line.
type ignorePattern struct {
	pattern string
	// noWildcardLen is the length of the literal prefix (git's
	// nowildcardlen); equal to len(pattern) when there are no wildcards.
	noWildcardLen int
	flags         uint8
	// base is the directory of the source file relative to the repo root,
	// "" for the root and for info/exclude, core.excludesFile and config
	// excludes.
	base string
}

var utf8BOM = []byte{0xef, 0xbb, 0xbf}

// parseIgnore parses gitignore content whose patterns are relative to base.
// It follows git's add_patterns_from_buffer: a UTF-8 BOM is skipped, CRLF
// line endings are accepted, blank lines and "#" comments are ignored, and
// unescaped trailing spaces are trimmed.
func parseIgnore(content []byte, base string) []ignorePattern {
	content = bytes.TrimPrefix(content, utf8BOM)
	var out []ignorePattern
	for len(content) > 0 {
		var line []byte
		if i := bytes.IndexByte(content, '\n'); i >= 0 {
			line, content = content[:i], content[i+1:]
		} else {
			line, content = content, nil
		}
		if len(line) == 0 || line[0] == '#' {
			continue
		}
		line = bytes.TrimSuffix(line, []byte{'\r'})
		if p, ok := parsePattern(trimTrailingSpaces(string(line)), base); ok {
			out = append(out, p)
		}
	}
	return out
}

// parsePatterns parses individual patterns (e.g. from config) at the root.
func parsePatterns(lines []string) []ignorePattern {
	var out []ignorePattern
	for _, l := range lines {
		if l == "" || l[0] == '#' {
			continue
		}
		if p, ok := parsePattern(trimTrailingSpaces(l), ""); ok {
			out = append(out, p)
		}
	}
	return out
}

// trimTrailingSpaces removes trailing spaces unless escaped with '\'.
func trimTrailingSpaces(s string) string {
	lastSpace := -1
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case ' ':
			if lastSpace < 0 {
				lastSpace = i
			}
		case '\\':
			i++
			if i >= len(s) {
				return s
			}
			lastSpace = -1
		default:
			lastSpace = -1
		}
	}
	if lastSpace >= 0 {
		return s[:lastSpace]
	}
	return s
}

// parsePattern mirrors git's parse_path_pattern.
func parsePattern(p, base string) (ignorePattern, bool) {
	var flags uint8
	if strings.HasPrefix(p, "!") {
		flags |= patNegative
		p = p[1:]
	}
	if strings.HasSuffix(p, "/") {
		flags |= patMustBeDir
		p = p[:len(p)-1]
	}
	if !strings.Contains(p, "/") {
		flags |= patNoDir
	} else if strings.HasPrefix(p, "/") {
		// Anchored: match_pathname skips the leading slash.
		p = p[1:]
	}
	if p == "" {
		return ignorePattern{}, false
	}
	nwl := simpleLength(p)
	if strings.HasPrefix(p, "*") && simpleLength(p[1:]) == len(p)-1 {
		flags |= patEndsWith
	}
	return ignorePattern{pattern: p, noWildcardLen: nwl, flags: flags, base: base}, true
}

// simpleLength is the length of the prefix free of glob specials.
func simpleLength(s string) int {
	for i := 0; i < len(s); i++ {
		if isGlobSpecial(s[i]) {
			return i
		}
	}
	return len(s)
}

// match reports whether the pattern matches path (repo-relative, slash
// separated) whose last component is basename.
func (p *ignorePattern) match(path, basename string, isDir, icase bool) bool {
	if p.flags&patMustBeDir != 0 && !isDir {
		return false
	}
	if p.flags&patNoDir != 0 {
		return p.matchBasename(basename, icase)
	}
	return p.matchPathname(path, icase)
}

func (p *ignorePattern) matchBasename(basename string, icase bool) bool {
	switch {
	case p.noWildcardLen == len(p.pattern):
		return pathEqual(p.pattern, basename, icase)
	case p.flags&patEndsWith != 0:
		suffix := p.pattern[1:]
		return len(suffix) <= len(basename) && pathEqual(suffix, basename[len(basename)-len(suffix):], icase)
	default:
		return wildmatch(p.pattern, basename, wmFlags(0, icase))
	}
}

func (p *ignorePattern) matchPathname(path string, icase bool) bool {
	name := path
	if p.base != "" {
		if len(path) <= len(p.base) || path[len(p.base)] != '/' || !pathEqual(path[:len(p.base)], p.base, icase) {
			return false
		}
		name = path[len(p.base)+1:]
	}
	pat := p.pattern
	if prefix := p.noWildcardLen; prefix > 0 {
		if prefix > len(name) || !pathEqual(pat[:prefix], name[:prefix], icase) {
			return false
		}
		pat, name = pat[prefix:], name[prefix:]
		if pat == "" && name == "" {
			return true
		}
	}
	return wildmatch(pat, name, wmFlags(wmPathname, icase))
}

func wmFlags(f int, icase bool) int {
	if icase {
		f |= wmCasefold
	}
	return f
}

// pathEqual compares paths, folding ASCII case only when icase is set (git's
// fspathcmp uses strncasecmp, not Unicode folding).
func pathEqual(a, b string, icase bool) bool {
	if !icase || len(a) != len(b) {
		return a == b
	}
	for i := 0; i < len(a); i++ {
		if toLowerASCII(a[i]) != toLowerASCII(b[i]) {
			return false
		}
	}
	return true
}

// matchList returns the verdict of the last matching pattern in list:
// +1 excluded, -1 re-included (negated), 0 no match.
func matchList(list []ignorePattern, path, basename string, isDir, icase bool) int {
	for i := len(list) - 1; i >= 0; i-- {
		p := &list[i]
		if p.match(path, basename, isDir, icase) {
			if p.flags&patNegative != 0 {
				return -1
			}
			return 1
		}
	}
	return 0
}
