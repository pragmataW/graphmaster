package query

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/pragmataW/graphmaster/internal/model"
	"github.com/pragmataW/graphmaster/internal/store"
)

// qnameSeps are the separators languages use inside qualified names.
var qnameSeps = []string{"::", "->", ".", "\\", "/", "#", ":"}

// maxCandidates bounds the numbered list shown for an ambiguous argument.
const maxCandidates = 20

// resolveSymbol interprets a symbol argument, trying in order: a 16-digit
// skey, path:line (innermost definition spanning the line), an exact
// qualified name, a qualified-name suffix ("Cls.method") and a bare name.
// It returns the matches of the first form that matched; more than one
// means the argument is ambiguous.
func (q *querier) resolveSymbol(ctx context.Context, arg string) ([]*sym, error) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return nil, invalidf("symbol is required")
	}
	if isSKey(arg) {
		key, _ := hex.DecodeString(arg)
		ss, err := q.symsWhere(ctx, `n.skey = ?`, key)
		if err != nil || len(ss) > 0 {
			return ss, err
		}
	}
	if path, line, ok := splitPathLine(arg); ok {
		f, err := q.st.FileByPath(ctx, path)
		switch {
		case err == nil:
			return q.symsWhere(ctx, `n.file_id = ? AND n.start_line <= ? AND n.end_line >= ?
				ORDER BY n.end_line - n.start_line, n.start_line DESC LIMIT 1`, f.ID, line, line)
		case !errors.Is(err, store.ErrNotFound):
			return nil, err
		}
	}
	name := lastSegment(arg)
	cands, err := q.symsByName(ctx, name)
	if err != nil {
		return nil, err
	}
	var exact, suffix []*sym
	for _, s := range cands {
		switch {
		case s.QName == arg:
			exact = append(exact, s)
		case name != arg && hasQNameSuffix(s.QName, arg):
			suffix = append(suffix, s)
		}
	}
	switch {
	case name == arg:
		// A bare name: every definition with that name, even where the
		// qualified name happens to equal it (C functions).
	case len(exact) > 0:
		cands = exact
	default:
		cands = suffix
		if len(cands) == 0 { // a name that itself contains separators
			if cands, err = q.symsByName(ctx, arg); err != nil {
				return nil, err
			}
		}
	}
	return preferDefinitions(cands), nil
}

// preferDefinitions drops declarations (C prototypes, forward decls) when
// a full definition is among the matches, and orders the rest for display.
func preferDefinitions(ss []*sym) []*sym {
	var defs []*sym
	for _, s := range ss {
		if !s.Flags.Has(model.FlagDeclaration) {
			defs = append(defs, s)
		}
	}
	if len(defs) > 0 && len(defs) < len(ss) {
		ss = defs
	}
	sort.SliceStable(ss, func(i, j int) bool {
		a, b := ss[i], ss[j]
		if a.Exported() != b.Exported() {
			return a.Exported()
		}
		if a.IsTest() != b.IsTest() {
			return !a.IsTest()
		}
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		return a.Start < b.Start
	})
	return ss
}

// symbolArg resolves arg for a tool that needs exactly one symbol. When it
// does not resolve to one, it writes the not-found or ambiguity message
// and returns nil.
func (q *querier) symbolArg(ctx context.Context, o *output, arg string) (*sym, error) {
	ss, err := q.resolveSymbol(ctx, arg)
	if err != nil {
		return nil, err
	}
	switch len(ss) {
	case 1:
		return ss[0], nil
	case 0:
		return nil, q.notFound(ctx, o, arg)
	}
	writeAmbiguous(o, arg, ss)
	return nil, nil
}

// writeAmbiguous prints the numbered candidate list for an ambiguous
// symbol argument; each entry carries its skey as an exact handle.
func writeAmbiguous(o *output, arg string, ss []*sym) {
	o.linef("ambiguous symbol %q: %d candidates; pass a qname, path:line or skey:", arg, len(ss))
	shown := min(len(ss), maxCandidates)
	o.list(shown, func(i int) string {
		return fmt.Sprintf("%2d. %s  skey %s", i+1, ss[i].short(), ss[i].skeyHex())
	}, len(ss)-shown)
}

// notFound reports an unknown symbol with a few full-text suggestions.
func (q *querier) notFound(ctx context.Context, o *output, arg string) error {
	o.linef("no symbol matches %q", arg)
	ids, err := q.suggest(ctx, lastSegment(arg))
	if err != nil || len(ids) == 0 {
		return err
	}
	ss, err := q.symsInOrder(ctx, ids)
	if err != nil {
		return err
	}
	sortByLocation(ss)
	o.line("did you mean:")
	for _, s := range ss {
		if !o.line("  " + s.short()) {
			break
		}
	}
	return nil
}

// suggest finds near misses for an unknown name: a token-prefix search,
// then substring searches on ever shorter leading parts (typos tend to
// sit at the end of what was typed).
func (q *querier) suggest(ctx context.Context, name string) ([]int64, error) {
	ids, err := q.st.SearchSymbols(ctx, name, 5)
	if err != nil || len(ids) > 0 {
		return ids, err
	}
	rs := []rune(name)
	for n := len(rs) - 1; n >= 4 && n >= len(rs)-4; n-- {
		if ids, err = q.st.SearchSubstring(ctx, string(rs[:n]), 5); err != nil || len(ids) > 0 {
			return ids, err
		}
	}
	return nil, nil
}

func isSKey(s string) bool {
	if len(s) != 16 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// splitPathLine splits "path:line" when the suffix is a positive number.
func splitPathLine(s string) (string, int, bool) {
	i := strings.LastIndexByte(s, ':')
	if i <= 0 || i == len(s)-1 {
		return "", 0, false
	}
	n, err := strconv.Atoi(s[i+1:])
	if err != nil || n <= 0 {
		return "", 0, false
	}
	return strings.TrimPrefix(s[:i], "./"), n, true
}

// lastSegment returns the simple name at the end of a qualified name.
func lastSegment(s string) string {
	pos := 0
	for _, sep := range qnameSeps {
		if i := strings.LastIndex(s, sep); i >= 0 && i+len(sep) > pos && i+len(sep) < len(s) {
			pos = i + len(sep)
		}
	}
	return s[pos:]
}

// hasQNameSuffix reports whether qname ends with suffix at a segment
// boundary ("a.B.c" has suffix "B.c" but not "c" of "Bc").
func hasQNameSuffix(qname, suffix string) bool {
	if !strings.HasSuffix(qname, suffix) || len(qname) == len(suffix) {
		return false
	}
	pre := qname[:len(qname)-len(suffix)]
	for _, sep := range qnameSeps {
		if strings.HasSuffix(pre, sep) {
			return true
		}
	}
	return false
}
