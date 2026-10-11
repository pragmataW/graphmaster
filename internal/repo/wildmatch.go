package repo

// This file is a port of git's wildmatch.c (originally from rsync), which
// defines gitignore glob semantics: "**" spanning directories only when it
// forms a whole path component, "[!...]" and "[^...]" negation, POSIX
// character classes and backslash escapes. Go's path.Match differs in all
// of these, which is why it is not used.

const (
	wmMatch = iota
	wmNoMatch
	wmAbortAll
	wmAbortToStarStar
)

// Wildmatch flags.
const (
	// wmPathname makes '*', '?' and bracket expressions not match '/'.
	wmPathname = 1 << iota
	// wmCasefold compares ASCII letters case-insensitively.
	wmCasefold
)

// wildmatch reports whether text matches the glob pattern.
func wildmatch(pattern, text string, flags int) bool {
	return dowild(pattern, 0, text, 0, flags) == wmMatch
}

// at returns s[i] or 0 past the end, emulating C string termination.
func at(s string, i int) byte {
	if i < len(s) {
		return s[i]
	}
	return 0
}

func toLowerASCII(c byte) byte {
	if 'A' <= c && c <= 'Z' {
		return c + 'a' - 'A'
	}
	return c
}

func toUpperASCII(c byte) byte {
	if 'a' <= c && c <= 'z' {
		return c - ('a' - 'A')
	}
	return c
}

func isGlobSpecial(c byte) bool {
	return c == '*' || c == '?' || c == '[' || c == '\\'
}

func indexByteFrom(s string, from int, c byte) int {
	for i := from; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}

// dowild is a line-by-line port of git's dowild; keep it that way.
func dowild(p string, pi int, text string, ti int, flags int) int {
	fold := flags&wmCasefold != 0
	for ; at(p, pi) != 0; ti, pi = ti+1, pi+1 {
		pCh := at(p, pi)
		tCh := at(text, ti)
		if tCh == 0 && pCh != '*' {
			return wmAbortAll
		}
		if fold {
			tCh = toLowerASCII(tCh)
			pCh = toLowerASCII(pCh)
		}
		switch pCh {
		case '\\':
			// Literal match with the following character (not case-folded,
			// as in git).
			pi++
			pCh = at(p, pi)
			if tCh != pCh {
				return wmNoMatch
			}
			continue
		case '?':
			if flags&wmPathname != 0 && tCh == '/' {
				return wmNoMatch
			}
			continue
		case '*':
			var matchSlash bool
			pi++
			if at(p, pi) == '*' {
				prev := pi - 2
				for pi++; at(p, pi) == '*'; pi++ {
				}
				if (prev < 0 || p[prev] == '/') &&
					(at(p, pi) == 0 || at(p, pi) == '/' || (at(p, pi) == '\\' && at(p, pi+1) == '/')) {
					// "**/" may match zero directories: try the rest of the
					// pattern right here first.
					if at(p, pi) == '/' && dowild(p, pi+1, text, ti, flags) == wmMatch {
						return wmMatch
					}
					matchSlash = true
				} else {
					matchSlash = false
				}
			} else {
				matchSlash = flags&wmPathname == 0
			}
			if at(p, pi) == 0 {
				// Trailing "**" matches everything; trailing "*" only if
				// no slash remains.
				if !matchSlash && indexByteFrom(text, ti, '/') >= 0 {
					return wmNoMatch
				}
				return wmMatch
			}
			if !matchSlash && at(p, pi) == '/' {
				// A single '*' followed by '/' matches up to the next slash.
				slash := indexByteFrom(text, ti, '/')
				if slash < 0 {
					return wmNoMatch
				}
				ti = slash
				// The loop increment consumes the slash in both strings.
				continue
			}
			for tCh != 0 {
				// Fast-forward to the next occurrence of a literal that
				// follows the star.
				if !isGlobSpecial(at(p, pi)) {
					want := at(p, pi)
					if fold {
						want = toLowerASCII(want)
					}
					for {
						tCh = at(text, ti)
						if tCh == 0 || (!matchSlash && tCh == '/') {
							break
						}
						if fold {
							tCh = toLowerASCII(tCh)
						}
						if tCh == want {
							break
						}
						ti++
					}
					if tCh != want {
						// Hitting a slash with a single star must let an
						// enclosing "**" retry from a later directory, as
						// the unaccelerated rsync loop would.
						if !matchSlash && tCh == '/' {
							return wmAbortToStarStar
						}
						return wmAbortAll
					}
				}
				if m := dowild(p, pi, text, ti, flags); m != wmNoMatch {
					if !matchSlash || m != wmAbortToStarStar {
						return m
					}
				} else if !matchSlash && tCh == '/' {
					return wmAbortToStarStar
				}
				ti++
				tCh = at(text, ti)
				if fold {
					tCh = toLowerASCII(tCh)
				}
			}
			return wmAbortAll
		case '[':
			r, npi := matchBracket(p, pi, tCh, flags)
			if r != wmMatch {
				return r
			}
			pi = npi
			continue
		default:
			if tCh != pCh {
				return wmNoMatch
			}
			continue
		}
	}
	if ti < len(text) {
		return wmNoMatch
	}
	return wmMatch
}

// matchBracket evaluates the bracket expression starting at p[pi] == '['
// against tCh (already case-folded when wmCasefold is set). On success it
// returns wmMatch and the index of the closing ']'.
//
// The structure mirrors the C implementation.
func matchBracket(p string, pi int, tCh byte, flags int) (int, int) {
	fold := flags&wmCasefold != 0
	pi++
	pCh := at(p, pi)
	if pCh == '^' {
		pCh = '!'
	}
	negated := pCh == '!'
	if negated {
		pi++
		pCh = at(p, pi)
	}
	var prevCh byte
	matched := false
	for {
		if pCh == 0 {
			return wmAbortAll, pi
		}
		switch {
		case pCh == '\\':
			pi++
			pCh = at(p, pi)
			if pCh == 0 {
				return wmAbortAll, pi
			}
			if tCh == pCh {
				matched = true
			}
		case pCh == '-' && prevCh != 0 && at(p, pi+1) != 0 && at(p, pi+1) != ']':
			pi++
			pCh = at(p, pi)
			if pCh == '\\' {
				pi++
				pCh = at(p, pi)
				if pCh == 0 {
					return wmAbortAll, pi
				}
			}
			if tCh <= pCh && tCh >= prevCh {
				matched = true
			} else if fold && 'a' <= tCh && tCh <= 'z' {
				up := toUpperASCII(tCh)
				if up <= pCh && up >= prevCh {
					matched = true
				}
			}
			pCh = 0 // resets prevCh
		case pCh == '[' && at(p, pi+1) == ':':
			s := pi + 2
			pi = s
			for at(p, pi) != 0 && at(p, pi) != ']' {
				pi++
			}
			if at(p, pi) == 0 {
				return wmAbortAll, pi
			}
			n := pi - s - 1
			if n < 0 || p[pi-1] != ':' {
				// Not a "[:class:]": treat '[' as a literal.
				pi = s - 2
				pCh = '['
				if tCh == pCh {
					matched = true
				}
				break
			}
			ok, valid := charClass(p[s:s+n], tCh, fold)
			if !valid {
				return wmAbortAll, pi
			}
			if ok {
				matched = true
			}
			pCh = 0
		default:
			if tCh == pCh {
				matched = true
			}
		}
		prevCh = pCh
		pi++
		pCh = at(p, pi)
		if pCh == ']' {
			break
		}
	}
	if matched == negated || (flags&wmPathname != 0 && tCh == '/') {
		return wmNoMatch, pi
	}
	return wmMatch, pi
}

// charClass evaluates a POSIX class name against c. valid is false for an
// unknown class name.
func charClass(name string, c byte, fold bool) (ok, valid bool) {
	isLower := 'a' <= c && c <= 'z'
	isUpper := 'A' <= c && c <= 'Z'
	isDigit := '0' <= c && c <= '9'
	isAlpha := isLower || isUpper
	isPrint := c >= 0x20 && c < 0x7f
	switch name {
	case "alnum":
		return isAlpha || isDigit, true
	case "alpha":
		return isAlpha, true
	case "blank":
		return c == ' ' || c == '\t', true
	case "cntrl":
		return c < 0x20 || c == 0x7f, true
	case "digit":
		return isDigit, true
	case "graph":
		return isPrint && c != ' ', true
	case "lower":
		return isLower, true
	case "print":
		return isPrint, true
	case "punct":
		return isPrint && c != ' ' && !isAlpha && !isDigit, true
	case "space":
		return c == ' ' || (c >= '\t' && c <= '\r'), true
	case "upper":
		return isUpper || (fold && isLower), true
	case "xdigit":
		return isDigit || ('a' <= c && c <= 'f') || ('A' <= c && c <= 'F'), true
	}
	return false, false
}
