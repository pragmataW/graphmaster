package store

import (
	"strings"
	"unicode"
)

// TokenizeIdent expands an identifier into the text indexed by sym_fts: the
// identifier itself followed by its lowercase camel/snake/kebab parts, e.g.
// "getUserByID" -> "getUserByID get user by id". Identifiers that consist of
// a single part are returned unchanged.
func TokenizeIdent(name string) string {
	parts := splitIdent(name)
	if len(parts) == 0 || (len(parts) == 1 && strings.EqualFold(parts[0], name)) {
		return name
	}
	return name + " " + strings.Join(parts, " ")
}

// MatchExpr turns free-form user input into an FTS5 expression for the
// sym_fts tokens column. Each word matches either as a whole token or as the
// conjunction of its identifier parts; words are ANDed. With prefix set every
// term is a prefix query. Returns "" when the input has no searchable words.
func MatchExpr(query string, prefix bool) string {
	words := strings.FieldsFunc(query, func(r rune) bool { return !isIdentRune(r) })
	terms := make([]string, 0, len(words))
	for _, w := range words {
		lw := strings.ToLower(w)
		parts := splitIdent(w)
		switch {
		case len(parts) == 0 || (len(parts) == 1 && parts[0] == lw):
			terms = append(terms, ftsTerm(lw, prefix))
		case len(parts) == 1:
			terms = append(terms, "("+ftsTerm(lw, prefix)+" OR "+ftsTerm(parts[0], prefix)+")")
		default:
			sub := make([]string, len(parts))
			for i, p := range parts {
				sub[i] = ftsTerm(p, prefix)
			}
			terms = append(terms, "("+ftsTerm(lw, prefix)+" OR ("+strings.Join(sub, " AND ")+"))")
		}
	}
	return strings.Join(terms, " AND ")
}

// ftsTerm quotes s as an FTS5 string, optionally as a prefix query.
func ftsTerm(s string, prefix bool) string {
	t := `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
	if prefix {
		t += "*"
	}
	return t
}

func isIdentRune(r rune) bool {
	return r == '_' || r == '$' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

type runeClass uint8

const (
	clSep runeClass = iota
	clUpper
	clLower // also non-cased letters
	clDigit
)

func classify(r rune) runeClass {
	switch {
	case unicode.IsUpper(r):
		return clUpper
	case unicode.IsLetter(r):
		return clLower
	case unicode.IsDigit(r):
		return clDigit
	}
	return clSep
}

// splitIdent splits an identifier into lowercase parts on separators
// (anything that is not a letter or digit) and case transitions:
// "fooBar" -> foo|bar, "HTTPServer" -> http|server, "MD5Hash" -> md5|hash.
// Digits stick to the preceding letters.
func splitIdent(s string) []string {
	rs := []rune(s)
	var parts []string
	start := -1
	flush := func(end int) {
		if start >= 0 && end > start {
			parts = append(parts, strings.ToLower(string(rs[start:end])))
		}
		start = -1
	}
	for i, r := range rs {
		c := classify(r)
		if c == clSep {
			flush(i)
			continue
		}
		if start < 0 {
			start = i
			continue
		}
		prev := classify(rs[i-1])
		boundary := false
		if c == clUpper {
			switch prev {
			case clLower, clDigit:
				boundary = true // fooBar, md5Hash
			case clUpper:
				// End of an acronym: the last upper of a run starts a new
				// word when followed by a lowercase letter (HTTPServer).
				boundary = i+1 < len(rs) && classify(rs[i+1]) == clLower
			}
		}
		if boundary {
			flush(i)
			start = i
		}
	}
	flush(len(rs))
	return parts
}
