package query

import (
	"context"
	"sort"
	"strings"

	"github.com/pragmataW/graphmaster/internal/model"
)

// symFilter restricts symbol results by kind, language and path prefix.
type symFilter struct {
	kinds      map[model.NodeKind]bool
	langs      map[model.Lang]bool
	pathPrefix string
}

func (f *symFilter) active() bool {
	return len(f.kinds) > 0 || len(f.langs) > 0 || f.pathPrefix != ""
}

func (f *symFilter) match(s *sym) bool {
	return (len(f.kinds) == 0 || f.kinds[s.Kind] || f.kinds[s.displayKind()]) &&
		(len(f.langs) == 0 || f.langs[s.Lang]) &&
		strings.HasPrefix(s.Path, f.pathPrefix)
}

func (f *symFilter) addKind(name string) error {
	k, err := model.ParseNodeKind(name)
	if err != nil {
		return invalidf("%v", err)
	}
	if f.kinds == nil {
		f.kinds = map[model.NodeKind]bool{}
	}
	f.kinds[k] = true
	return nil
}

func (f *symFilter) setLang(name string) error {
	l, err := model.ParseLang(name)
	if err != nil {
		return invalidf("%v", err)
	}
	f.langs = map[model.Lang]bool{l: true}
	if l == model.LangTypeScript {
		f.langs[model.LangTSX] = true
	}
	return nil
}

func normPathPrefix(p string) string {
	p = strings.TrimSpace(p)
	p = strings.TrimPrefix(p, "./")
	if p == "." || p == "/" {
		return ""
	}
	return strings.TrimPrefix(p, "/")
}

// Match classes, best first.
const (
	matchExact = iota
	matchFold
	matchPrefixCase // prefix with the query's exact case
	matchPrefix
	matchFuzzy
)

// ranked is a candidate with its match class.
type ranked struct {
	*sym
	class int
}

// candidatePool sizes the full-text candidate sets; filtered searches
// need a deeper pool because filtering happens after retrieval.
func candidatePool(f *symFilter) int {
	if f.active() {
		return 2000
	}
	return 200
}

// rankSymbols collects definitions matching query from the name table, the
// token index (prefix search) and, unless exact, the trigram index, then
// orders them exact > case-insensitive > prefix > fuzzy, exported first.
func (q *querier) rankSymbols(ctx context.Context, query string, exact bool, f *symFilter) ([]ranked, error) {
	name := lastSegment(query)
	byName, err := q.symsByName(ctx, name)
	if err != nil {
		return nil, err
	}
	seen := map[int64]bool{}
	var out []ranked
	add := func(s *sym, class int) {
		if seen[s.ID] || !f.match(s) {
			return
		}
		seen[s.ID] = true
		out = append(out, ranked{sym: s, class: class})
	}
	for _, s := range byName {
		if query == name || s.QName == query || hasQNameSuffix(s.QName, query) {
			add(s, matchExact)
		}
	}
	if !exact {
		pool := candidatePool(f)
		ids, err := q.st.SearchSymbols(ctx, query, pool)
		if err != nil {
			return nil, err
		}
		if !strings.ContainsAny(name, " \t") {
			sub, err := q.st.SearchSubstring(ctx, name, pool)
			if err != nil {
				return nil, err
			}
			ids = append(ids, sub...)
		}
		ss, err := q.symsInOrder(ctx, ids)
		if err != nil {
			return nil, err
		}
		lname := strings.ToLower(name)
		for _, s := range ss {
			ln := strings.ToLower(s.Name)
			switch {
			case ln == lname:
				add(s, matchFold)
			case strings.HasPrefix(s.Name, name):
				add(s, matchPrefixCase)
			case strings.HasPrefix(ln, lname):
				add(s, matchPrefix)
			default:
				add(s, matchFuzzy)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.class != b.class {
			return a.class < b.class
		}
		if a.Exported() != b.Exported() {
			return a.Exported()
		}
		if a.IsTest() != b.IsTest() {
			return !a.IsTest()
		}
		if len(a.Name) != len(b.Name) {
			return len(a.Name) < len(b.Name)
		}
		if len(a.QName) != len(b.QName) {
			return len(a.QName) < len(b.QName)
		}
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		return a.Start < b.Start
	})
	return out, nil
}

// FindSymbol lists definitions by name: exact matches first, then prefix
// and fuzzy matches, exported definitions before private ones.
func (e *Engine) FindSymbol(ctx context.Context, in FindSymbolInput) (string, error) {
	return e.run(ctx, "find_symbol", in.MaxTokens, func(ctx context.Context, q *querier, o *output) error {
		query := strings.TrimSpace(in.Query)
		if query == "" {
			return invalidf("query is required")
		}
		f := &symFilter{pathPrefix: normPathPrefix(in.PathPrefix)}
		if in.Kind != "" {
			if err := f.addKind(in.Kind); err != nil {
				return err
			}
		}
		if in.Lang != "" {
			if err := f.setLang(in.Lang); err != nil {
				return err
			}
		}
		res, err := q.rankSymbols(ctx, query, in.Exact, f)
		if err != nil {
			return err
		}
		if len(res) == 0 {
			o.linef("no definitions match %q", query)
			return nil
		}
		shown := min(orDefault(in.Limit, 20, 500), len(res))
		o.list(shown, func(i int) string { return res[i].row() }, len(res)-shown)
		return nil
	})
}
