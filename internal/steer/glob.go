package steer

import "strings"

// GlobKind is the steering class of a file-name glob.
type GlobKind int

const (
	// GlobExplore is a broad, code-scoped scan such as "**/*.ts" or "src/**".
	GlobExplore GlobKind = iota
	// GlobLiteral names a specific file, e.g. "**/user_service.go".
	GlobLiteral
	// GlobNonCode can only match non-code files, e.g. "**/*.md".
	GlobNonCode
)

func (k GlobKind) String() string {
	switch k {
	case GlobLiteral:
		return "literal"
	case GlobNonCode:
		return "noncode"
	default:
		return "explore"
	}
}

// GlobClass is the result of ClassifyGlob.
type GlobClass struct {
	Kind GlobKind
	// Name is the literal last segment (GlobLiteral only).
	Name string
	// Stem is Name without its extension when the glob has a single literal
	// expansion; empty otherwise.
	Stem string
	// Hint is the longest identifier-like literal fragment of a wildcard
	// file name (GlobExplore only), e.g. "Service" for "*Service*.java".
	Hint string
}

// Allow reports whether the glob should pass through without steering.
func (g GlobClass) Allow() bool { return g.Kind != GlobExplore }

// ClassifyGlob decides whether a glob is a literal file lookup, a non-code
// scan or a broad code exploration. Negated globs ("!x") never widen a search
// and are reported as GlobNonCode.
func ClassifyGlob(glob string, exts ExtSet) GlobClass {
	glob = strings.TrimSpace(glob)
	if strings.HasPrefix(glob, "!") {
		return GlobClass{Kind: GlobNonCode}
	}
	if glob == "" || strings.HasSuffix(glob, "/") || strings.HasSuffix(glob, `\`) {
		return GlobClass{Kind: GlobExplore}
	}
	last := baseName(glob)
	alts := expandBraces(last)

	wild := false
	for _, a := range alts {
		if strings.ContainsAny(a, globMeta) {
			wild = true
			break
		}
	}
	if !wild {
		c := GlobClass{Kind: GlobLiteral, Name: last}
		if len(alts) == 1 {
			c.Stem = stemOf(alts[0])
		}
		return c
	}

	if onlyNonCodeExts(alts, exts) {
		return GlobClass{Kind: GlobNonCode}
	}
	return GlobClass{Kind: GlobExplore, Hint: identHint(stemOf(alts[0]))}
}

// onlyNonCodeExts reports whether every alternative has a concrete,
// non-wildcard extension that is not a code extension.
func onlyNonCodeExts(alts []string, exts ExtSet) bool {
	for _, a := range alts {
		ext, ok := extOf(a)
		if !ok || strings.ContainsAny(ext, globMeta) || exts.Has(ext) {
			return false
		}
	}
	return true
}

// stemOf strips the extension from a base name; dotfiles keep their name.
func stemOf(base string) string {
	if ext, ok := extOf(base); ok {
		return base[:len(base)-len(ext)]
	}
	return base
}

// identHint returns the longest run of identifier characters (at least 3
// long) outside bracket expressions.
func identHint(s string) string {
	best, cur := "", strings.Builder{}
	flush := func() {
		if cur.Len() >= 3 && cur.Len() > len(best) {
			best = cur.String()
		}
		cur.Reset()
	}
	inClass := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case inClass:
			if ch == ']' {
				inClass = false
			}
		case ch == '[':
			flush()
			inClass = true
		case isIdentByte(ch):
			cur.WriteByte(ch)
		default:
			flush()
		}
	}
	flush()
	return best
}

func isIdentByte(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}
