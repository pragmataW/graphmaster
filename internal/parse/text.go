package parse

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// unquote strips one layer of matching quotes ("x", 'x' or `x`).
func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		switch q := s[0]; q {
		case '"', '\'', '`':
			if s[len(s)-1] == q {
				return s[1 : len(s)-1]
			}
		}
	}
	return s
}

// firstLine returns s up to the first newline, without trailing "\r".
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimRight(s, "\r")
}

// compactText collapses whitespace runs and caps s at limit bytes on a rune
// boundary.
func compactText(s string, limit int) string {
	s = strings.Join(strings.FieldsFunc(s, unicode.IsSpace), " ")
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// splitScope splits a scope path such as "Server", "ns::Cls" or "a.B" into
// segments.
func splitScope(s string) []string {
	s = strings.ReplaceAll(s, "::", ".")
	var out []string
	for _, p := range strings.Split(s, ".") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// splitTypeName reduces a type expression to (qualifier, name): pointer and
// reference markers and generic arguments are dropped, and the last "." or
// "::" separates the qualifier, e.g. "*pkg.Base[T]" -> ("pkg", "Base").
func splitTypeName(s string) (qualifier, name string) {
	s = strings.TrimSpace(s)
	s = strings.TrimLeft(s, "*&^~ \t")
	if i := strings.IndexAny(s, "[<("); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, "::"); i >= 0 {
		return s[:i], s[i+2:]
	}
	if i := strings.LastIndexByte(s, '.'); i >= 0 {
		return s[:i], s[i+1:]
	}
	return "", s
}

// defaultTestTarget strips a leading "test"/"Test" and underscores:
// "test_parse_foo" -> "parse_foo", "TestParse" -> "Parse".
func defaultTestTarget(name string) string {
	for _, p := range []string{"Test", "test"} {
		if strings.HasPrefix(name, p) {
			name = name[len(p):]
			break
		}
	}
	return strings.TrimLeft(name, "_")
}
