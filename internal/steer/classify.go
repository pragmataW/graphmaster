package steer

import (
	"regexp"
	"sort"
	"strings"
)

// PatternClass is the result of classifying a search pattern.
type PatternClass struct {
	// SymbolLike is true when the pattern looks like a lookup of a code
	// symbol (identifier, qualified name, call, keyword-prefixed definition).
	SymbolLike bool
	// Idents are the unique identifier tokens in first-seen order, without
	// definition keywords or a leading '$'. Filled best-effort for
	// non-symbol patterns too.
	Idents []string
	// Queries are the symbol expressions worth looking up, one per
	// alternative / ".*" fragment, e.g. "pkg.Func" or "Foo::bar".
	Queries []string
	// Norm (pnorm) is the sorted unique Idents joined by a space, case kept.
	// Equal norms mean "the same search" for retry detection.
	Norm string
	// Reason explains a non-symbol verdict; empty when SymbolLike.
	Reason string
}

// Non-symbol reasons.
const (
	reasonEmpty      = "too short"
	reasonWords      = "multiple words"
	reasonPunct      = "punctuation"
	reasonMarker     = "TODO marker"
	reasonURL        = "url"
	reasonPath       = "path"
	reasonKebab      = "kebab-case"
	reasonDigits     = "digits only"
	reasonClass      = "character class or escape"
	reasonQuantifier = "quantifier"
	reasonGroup      = "group or lookaround"
	reasonKeyword    = "keyword only"
	reasonNonASCII   = "non-ascii"
)

// defKeywords may prefix (or, like "struct" in "type Foo struct", follow) a
// symbol name in a definition search.
var defKeywords = map[string]bool{
	"func": true, "def": true, "class": true, "fn": true, "type": true,
	"struct": true, "interface": true, "trait": true, "impl": true,
	"enum": true, "const": true, "let": true, "var": true,
	"function": true, "public": true, "private": true, "namespace": true,
	"protected": true, "static": true, "async": true, "export": true,
	"pub": true, "abstract": true, "final": true, "virtual": true,
	"override": true, "typedef": true, "union": true, "mod": true,
	"package": true, "module": true, "record": true, "val": true,
	"object": true,
}

var (
	markerRe    = regexp.MustCompile(`\b(TODO|FIXME|XXX|HACK)\b`)
	inlineFlags = regexp.MustCompile(`^\(\?[a-zA-Z]+\)`)
	wsQuant     = regexp.MustCompile(`\s[+*?]`)
	identRunRe  = regexp.MustCompile(`\$?[A-Za-z_][A-Za-z0-9_]*`)
)

// classMarker replaces escapes like \w or \d in the cleaned pattern so they
// cannot be mistaken for identifier characters.
const classMarker = '\x00'

// ClassifyPattern classifies a regular-expression search pattern (grep,
// grep -E, rg, the Grep tool).
func ClassifyPattern(p string) PatternClass { return classify(p, false) }

// ClassifyFixed classifies a fixed-string pattern (grep -F, rg -F): no
// anchors, escapes, alternation or ".*" are interpreted.
func ClassifyFixed(p string) PatternClass { return classify(p, true) }

// ClassifyPatterns classifies several patterns of one search (e.g. multiple
// -e flags). The set is symbol-like only if every pattern is.
func ClassifyPatterns(ps []string, fixed bool) PatternClass {
	if len(ps) == 0 {
		return PatternClass{Reason: reasonEmpty}
	}
	var out PatternClass
	out.SymbolLike = true
	var idents, queries []string
	for _, p := range ps {
		c := classify(p, fixed)
		if !c.SymbolLike && out.SymbolLike {
			out.SymbolLike = false
			out.Reason = c.Reason
		}
		idents = append(idents, c.Idents...)
		queries = append(queries, c.Queries...)
	}
	out.Idents = uniq(idents)
	if out.SymbolLike {
		out.Queries = uniq(queries)
	}
	out.Norm = normOf(out.Idents)
	return out
}

func classify(p string, fixed bool) PatternClass {
	var cleaned string
	var reason string
	if fixed {
		cleaned = strings.TrimSpace(p)
	} else {
		cleaned, reason = cleanRegex(p)
	}
	out := PatternClass{Idents: extractIdents(cleaned)}
	out.Norm = normOf(out.Idents)
	if reason == "" {
		var queries []string
		queries, reason = classifyCleaned(cleaned, fixed)
		if reason == "" {
			out.SymbolLike = true
			out.Queries = queries
			return out
		}
	}
	out.Reason = reason
	return out
}

// cleanRegex strips anchors, inline flags, word boundaries and escapes. It
// returns a non-empty reason when an escape makes the pattern non-symbol.
func cleanRegex(p string) (string, string) {
	s := strings.TrimSpace(p)
	s = inlineFlags.ReplaceAllString(s, "")
	s = strings.TrimLeft(s, "^")
	if strings.HasSuffix(s, "$") && !strings.HasSuffix(s, `\$`) {
		s = strings.TrimRight(s, "$")
	}
	var b strings.Builder
	reason := ""
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' {
			b.WriteByte(c)
			continue
		}
		if i+1 >= len(s) {
			return b.String(), reasonPunct // trailing lone backslash
		}
		i++
		e := s[i]
		switch {
		case e == 'b' || e == 'B' || e == '<' || e == '>' || e == 'A' || e == 'z' || e == 'Z':
			// Word boundary / anchor: drop.
		case e == 's':
			b.WriteByte(' ')
			if i+1 < len(s) && strings.IndexByte("+*?", s[i+1]) >= 0 {
				i++
			}
		case isIdentByte(e):
			b.WriteByte(classMarker)
			reason = reasonClass
		default:
			b.WriteByte(e) // escaped literal: \. \( \| \$
		}
	}
	s = wsQuant.ReplaceAllString(b.String(), " ")
	return strings.TrimSpace(s), reason
}

// classifyCleaned applies the symbol-like grammar to a cleaned pattern and
// returns the lookup queries, or a reason when it is not symbol-like.
func classifyCleaned(s string, fixed bool) ([]string, string) {
	if s == "" {
		return nil, reasonEmpty
	}
	if markerRe.MatchString(s) {
		return nil, reasonMarker
	}
	if strings.Contains(s, "://") || strings.HasPrefix(strings.ToLower(s), "www.") {
		return nil, reasonURL
	}
	if !fixed {
		s = stripEdges(s)
	}
	if s == "" {
		return nil, reasonEmpty
	}
	var alts []string
	if fixed {
		alts = strings.Split(s, "\n")
	} else {
		alts = strings.FieldsFunc(s, func(r rune) bool { return r == '\n' })
		alts = splitAlternation(alts)
	}
	var queries []string
	for _, a := range alts {
		if !fixed {
			a = stripEdges(a)
		}
		q, reason := classifyAlt(strings.TrimSpace(a), fixed)
		if reason != "" {
			return nil, reason
		}
		queries = append(queries, q...)
	}
	if len(queries) == 0 {
		return nil, reasonEmpty
	}
	return uniq(queries), ""
}

// splitAlternation splits each line on '|'.
func splitAlternation(lines []string) []string {
	var out []string
	for _, l := range lines {
		out = append(out, strings.Split(l, "|")...)
	}
	return out
}

// stripEdges removes a group wrapping the whole pattern and leading or
// trailing ".*", repeatedly.
func stripEdges(s string) string {
	for {
		prev := s
		s = strings.TrimSpace(s)
		for strings.HasPrefix(s, ".*") {
			s = s[2:]
		}
		for strings.HasSuffix(s, ".*") && !strings.HasSuffix(s, `\.*`) {
			s = s[:len(s)-2]
		}
		if inner, ok := unwrapGroup(s); ok {
			s = inner
		}
		if s == prev {
			return s
		}
	}
}

// unwrapGroup strips "(...)" or "(?:...)" when the parentheses enclose the
// whole string.
func unwrapGroup(s string) (string, bool) {
	if len(s) < 2 || s[0] != '(' || s[len(s)-1] != ')' {
		return s, false
	}
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 && i != len(s)-1 {
				return s, false
			}
		}
	}
	inner := s[1 : len(s)-1]
	return strings.TrimPrefix(inner, "?:"), true
}

// classifyAlt checks one alternative: keyword words plus exactly one symbol
// expression.
func classifyAlt(a string, fixed bool) ([]string, string) {
	words := strings.Fields(a)
	if len(words) == 0 {
		return nil, reasonEmpty
	}
	expr := ""
	for _, w := range words {
		if len(words) > 1 && defKeywords[w] {
			continue
		}
		if expr != "" {
			return nil, reasonWords
		}
		expr = w
	}
	if expr == "" {
		return nil, reasonKeyword
	}
	return parseExpr(expr, fixed)
}

// parseExpr parses ident((\.|::|#|->|\.\*)ident)*\(?\)? and returns one query
// per ".*"-separated fragment.
func parseExpr(e string, fixed bool) ([]string, string) {
	if strings.HasSuffix(e, "()") {
		e = e[:len(e)-2]
	} else if strings.HasSuffix(e, "(") {
		e = e[:len(e)-1]
	}
	if e == "" {
		return nil, reasonEmpty
	}
	var (
		frags    [][]string // fragments split on ".*", each a list of idents
		cur      []string
		curText  strings.Builder
		texts    []string
		identLen int
		needID   = true
	)
	i := 0
	for i < len(e) {
		if needID {
			n := scanIdent(e[i:])
			if n == 0 {
				return nil, badCharReason(e, i)
			}
			id := strings.TrimPrefix(e[i:i+n], "$")
			cur = append(cur, id)
			curText.WriteString(e[i : i+n])
			identLen += len(id)
			i += n
			needID = false
			continue
		}
		sep := scanSep(e[i:], fixed)
		if sep == "" {
			return nil, badCharReason(e, i)
		}
		i += len(sep)
		needID = true
		if sep == ".*" {
			frags = append(frags, cur)
			texts = append(texts, curText.String())
			cur = nil
			curText.Reset()
			continue
		}
		curText.WriteString(sep)
	}
	if needID {
		return nil, reasonPunct // dangling separator
	}
	frags = append(frags, cur)
	texts = append(texts, curText.String())

	var queries []string
	kept := 0
	for k, f := range frags {
		if len(f) == 1 && defKeywords[f[0]] {
			identLen -= len(f[0])
			continue // "func.*Handler": keyword fragment acts as prefix
		}
		kept++
		queries = append(queries, texts[k])
	}
	if kept == 0 {
		return nil, reasonKeyword
	}
	if identLen < 3 {
		return nil, reasonEmpty
	}
	return queries, ""
}

// scanIdent returns the length of an identifier (optionally '$'-prefixed)
// at the start of s, or 0.
func scanIdent(s string) int {
	i := 0
	if i < len(s) && s[i] == '$' {
		i++
	}
	if i >= len(s) || (s[i] != '_' && (s[i] < 'a' || s[i] > 'z') && (s[i] < 'A' || s[i] > 'Z')) {
		return 0
	}
	for i < len(s) && isIdentByte(s[i]) {
		i++
	}
	return i
}

// scanSep returns the qualifier separator at the start of s, or "".
func scanSep(s string, fixed bool) string {
	seps := []string{".*", "::", "->", ".", "#"}
	if fixed {
		seps = seps[1:]
	}
	for _, sep := range seps {
		if strings.HasPrefix(s, sep) {
			return sep
		}
	}
	return ""
}

// badCharReason explains why parsing stopped at e[i].
func badCharReason(e string, i int) string {
	if isAllDigits(e) {
		return reasonDigits
	}
	c := e[i]
	switch {
	case c == classMarker || c == '[' || c == ']':
		return reasonClass
	case c == '+' || c == '*' || c == '?' || c == '{' || c == '}':
		return reasonQuantifier
	case c == '(' || c == ')':
		return reasonGroup
	case c == '/':
		return reasonPath
	case c == '-' && i > 0 && i+1 < len(e) && isIdentByte(e[i-1]) && isIdentByte(e[i+1]):
		return reasonKebab
	case c >= 0x80:
		return reasonNonASCII
	case c >= '0' && c <= '9':
		return reasonDigits
	default:
		return reasonPunct
	}
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if (s[i] < '0' || s[i] > '9') && s[i] != '.' {
			return false
		}
	}
	return true
}

// extractIdents returns unique identifier runs (keywords and the class
// marker excluded, leading '$' stripped) in first-seen order.
func extractIdents(s string) []string {
	var out []string
	for _, m := range identRunRe.FindAllString(s, -1) {
		m = strings.TrimPrefix(m, "$")
		if defKeywords[m] {
			continue
		}
		out = append(out, m)
	}
	return uniq(out)
}

func normOf(idents []string) string {
	s := append([]string(nil), idents...)
	sort.Strings(s)
	return strings.Join(s, " ")
}

// uniq removes duplicates preserving first-seen order.
func uniq(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(in))
	out := in[:0:0]
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
