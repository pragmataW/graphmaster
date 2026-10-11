package resolve

import (
	"cmp"
	"database/sql"
	"slices"
	"strings"

	"github.com/pragmataW/graphmaster/internal/model"
	"github.com/pragmataW/graphmaster/internal/parse"
)

// csResolver adds C# scoping to the generic resolver:
//   - module identity is the project (.csproj); namespaces are the leading
//     qname segments of top-level types, and type names are looked up by
//     namespace: same file, enclosing namespaces (inner first), using
//     aliases, using namespaces (including the project's global usings),
//     then a unique global type (T4);
//   - members are visible as bare names (implicit this), through the
//     ancestor walk of the enclosing types;
//   - partial types: every part embeds its sibling parts (the parse layer
//     emits an embeds edge qualified parse.CSharpPartialQual), so members
//     of all parts are found through any part; for bare-name lookups only
//     one part (the primary: lowest path, then offset) is visible, so
//     references to the type do not fan out over its parts;
//   - receivers are typed from the method body (csharp_infer.go).
type csResolver struct{ Generic }

func init() { Register(model.LangCSharp, csResolver{}) }

// ImportBindings implements LangResolver. C# usings never bind a qualifier
// root for the generic tiers: aliases and namespaces are interpreted by
// ResolveType and the receiver typing, which know about namespaces.
func (csResolver) ImportBindings(Import) []string { return nil }

// MembersInScope implements LangResolver: implicit this.
func (csResolver) MembersInScope() bool { return true }

// IsMember implements LangResolver. Besides nested definitions, every part
// of a partial type except its primary part counts as a member, which keeps
// it out of bare-name lookups.
func (csResolver) IsMember(s *Session, d *Def) bool {
	if p := s.Parent(d); p != nil && p.Kind.IsType() {
		return true
	}
	if d.ParentID == 0 && d.Kind.IsType() && parse.CSharpIsPartialSig(d.Sig) {
		return csPrimaryPart(s, d) != d.ID
	}
	return false
}

// ResolveType implements LangResolver.
func (csResolver) ResolveType(s *Session, from *File, qual, name string) TypeResult {
	if qual == parse.CSharpPartialQual {
		return csPartialSiblings(s, from, name)
	}
	return csLookupFor(s, from, nil).types(qual, name, 0)
}

// Receiver implements LangResolver.
func (csResolver) Receiver(s *Session, site *Site) Receiver {
	return csReceiver(s, site)
}

// ---------------------------------------------------------------- state

// csState is the run-wide C# state.
type csState struct {
	infer     *csParserState
	primary   map[int64]int64 // partial part -> primary part id
	files     map[int64]*csFileInfo
	globals   []csGlobalUsing
	globalsOK bool
	nsSet     map[string]bool // every namespace (and prefix) declaring a type
}

func (c *csState) Close() {
	if c.infer != nil {
		c.infer.Close()
	}
}

func csStateOf(s *Session) *csState {
	if c, ok := s.State(model.LangCSharp).(*csState); ok {
		return c
	}
	c := &csState{primary: map[int64]int64{}, files: map[int64]*csFileInfo{}}
	s.SetState(model.LangCSharp, c)
	return c
}

// csFileInfo is what a file contributes to name lookup.
type csFileInfo struct {
	namespaces []string          // namespaces of the file's top-level types
	usings     []string          // using N; (file and project-global)
	statics    []string          // using static T;
	aliases    map[string]string // using A = T;
}

type csGlobalUsing struct {
	file int64
	imp  Import
}

// csFileOf returns (cached) lookup information of f.
func csFileOf(s *Session, f *File) *csFileInfo {
	st := csStateOf(s)
	if fi, ok := st.files[f.ID]; ok {
		return fi
	}
	fi := &csFileInfo{aliases: map[string]string{}}
	for _, d := range s.scanDefs(`file_id = ? AND parent_id IS NULL`, f.ID) {
		if d.Kind.IsType() {
			if ns := csNamespaceOf(s, d); !slices.Contains(fi.namespaces, ns) {
				fi.namespaces = append(fi.namespaces, ns)
			}
		}
	}
	slices.Sort(fi.namespaces) // def ids differ between builds; keep lookups stable
	add := func(im Import) {
		spec := csClean(im.Spec)
		switch {
		case im.Alias != "":
			if _, dup := fi.aliases[im.Alias]; !dup {
				fi.aliases[im.Alias] = spec
			}
		case slices.Contains(im.Names, "static"):
			fi.statics = append(fi.statics, spec)
		case im.Wildcard():
			fi.usings = append(fi.usings, spec)
		}
	}
	for _, im := range s.Imports(f) {
		add(im)
	}
	mod := s.Module(f)
	for _, g := range csGlobalUsings(s) {
		if g.file != f.ID && s.Module(s.File(g.file)) == mod {
			add(g.imp)
		}
	}
	st.files[f.ID] = fi
	return fi
}

// csGlobalUsings loads every "global using" of the repository once per run.
func csGlobalUsings(s *Session) []csGlobalUsing {
	st := csStateOf(s)
	if st.globalsOK {
		return st.globals
	}
	st.globalsOK = true
	err := s.rows(`SELECT i.file_id, i.spec, coalesce(i.alias, ''), coalesce(i.names, ''), i.line
		FROM imports i JOIN files f ON f.id = i.file_id
		WHERE f.lang = ? AND i.names LIKE '%global%' ORDER BY i.file_id, i.line, i.spec`, func(r *sql.Rows) error {
		var g csGlobalUsing
		var names string
		if err := r.Scan(&g.file, &g.imp.Spec, &g.imp.Alias, &names, &g.imp.Line); err != nil {
			return err
		}
		g.imp.Names = strings.Split(names, ",")
		if slices.Contains(g.imp.Names, "global") {
			st.globals = append(st.globals, g)
		}
		return nil
	}, int(model.LangCSharp))
	s.fail(err)
	// File ids differ between builds: order by path for determinism.
	slices.SortStableFunc(st.globals, func(a, b csGlobalUsing) int {
		return cmp.Compare(csFilePath(s, a.file), csFilePath(s, b.file))
	})
	return st.globals
}

// csKnownNamespace reports whether some repository type is declared in
// namespace ns or below it.
func csKnownNamespace(s *Session, ns string) bool {
	st := csStateOf(s)
	if st.nsSet == nil {
		st.nsSet = map[string]bool{}
		type row struct {
			fileID      int64
			qname, name string
		}
		var rows []row
		err := s.rows(`SELECT n.file_id, n.qname, m.name FROM nodes n
			JOIN files f ON f.id = n.file_id JOIN names m ON m.id = n.name_id
			WHERE f.lang = ? AND n.parent_id IS NULL`, func(r *sql.Rows) error {
			var x row
			if err := r.Scan(&x.fileID, &x.qname, &x.name); err != nil {
				return err
			}
			rows = append(rows, x)
			return nil
		}, int(model.LangCSharp))
		s.fail(err)
		for _, x := range rows {
			ns := csNamespaceFromQName(s.Module(s.File(x.fileID)), x.qname, x.name)
			for ns != "" {
				st.nsSet[ns] = true
				i := strings.LastIndexByte(ns, '.')
				if i < 0 {
					break
				}
				ns = ns[:i]
			}
		}
	}
	return st.nsSet[ns]
}

// ---------------------------------------------------------------- namespaces

// csNamespaceOf returns the namespace of d's outermost type ("" for the
// global namespace).
func csNamespaceOf(s *Session, d *Def) string {
	for i := 0; d != nil && d.ParentID != 0 && i < maxDepth; i++ {
		d = s.Parent(d)
	}
	if d == nil {
		return ""
	}
	return csNamespaceFromQName(s.Module(s.File(d.FileID)), d.QName, d.Name)
}

// csNamespaceFromQName strips the def name and, when present, the module
// identity from a top-level qname. Stored qnames are "Ns.Type" (the C#
// spec's QName) or "module.Ns.Type"; module identities contain ".csproj" or
// are ".", so they never collide with a namespace.
func csNamespaceFromQName(module, qname, name string) string {
	ns, ok := strings.CutSuffix(qname, "."+name)
	if !ok {
		return ""
	}
	if module != "" {
		if rest, ok := strings.CutPrefix(ns, module+"."); ok {
			return rest
		}
		if ns == module {
			return ""
		}
	}
	return ns
}

// csNamespaceChain returns ns and its enclosing namespaces, innermost
// first, ending with the global namespace "".
func csNamespaceChain(ns string) []string {
	var out []string
	for ns != "" {
		out = append(out, ns)
		i := strings.LastIndexByte(ns, '.')
		if i < 0 {
			break
		}
		ns = ns[:i]
	}
	return append(out, "")
}

// csClean strips whitespace, "global::" and generic arguments.
func csClean(s string) string {
	s = strings.Join(strings.Fields(s), "")
	s = strings.TrimPrefix(s, "global::")
	if i := strings.IndexByte(s, '<'); i >= 0 {
		s = s[:i]
	}
	return s
}

func csSplitLast(s string) (string, string) {
	if i := strings.LastIndexByte(s, '.'); i >= 0 {
		return s[:i], s[i+1:]
	}
	return "", s
}

// ---------------------------------------------------------------- partial types

// csParts returns every part of the logical type d belongs to (same
// project and qname), d included.
func csParts(s *Session, d *Def) []*Def {
	if d.ParentID != 0 || !d.Kind.IsType() {
		return []*Def{d}
	}
	mod := s.Module(s.File(d.FileID))
	parts := filter(s.DefsNamed(d.Name), func(o *Def) bool {
		if o.ParentID != 0 || !o.Kind.IsType() || o.QName != d.QName {
			return false
		}
		f := s.File(o.FileID)
		return f != nil && f.Lang == model.LangCSharp && s.Module(f) == mod
	})
	if !slices.Contains(parts, d) {
		parts = append(parts, d)
	}
	return parts
}

// csPrimaryPart returns the id of the part of d's logical type with the
// lowest (path, start byte): a choice independent of indexing order.
func csPrimaryPart(s *Session, d *Def) int64 {
	st := csStateOf(s)
	if id, ok := st.primary[d.ID]; ok {
		return id
	}
	parts := csParts(s, d)
	best := slices.MinFunc(parts, func(a, b *Def) int {
		pa, pb := csPath(s, a), csPath(s, b)
		return cmp.Or(cmp.Compare(pa, pb), cmp.Compare(a.StartByte, b.StartByte), cmp.Compare(a.ID, b.ID))
	})
	for _, p := range parts {
		st.primary[p.ID] = best.ID
	}
	return best.ID
}

func csPath(s *Session, d *Def) string { return csFilePath(s, d.FileID) }

func csFilePath(s *Session, id int64) string {
	if f := s.File(id); f != nil {
		return f.Path
	}
	return ""
}

// csPartialSiblings resolves the synthetic partial embeds edge of the type
// called name in file from: the parts of the same logical type declared in
// other files.
func csPartialSiblings(s *Session, from *File, name string) TypeResult {
	var out []*Def
	for _, d := range s.DefsNamed(name) {
		if d.FileID != from.ID || d.ParentID != 0 || !d.Kind.IsType() {
			continue
		}
		for _, p := range csParts(s, d) {
			if p.FileID != from.ID {
				out = appendUnique(out, p)
			}
		}
	}
	if len(out) == 0 {
		return TypeResult{}
	}
	return TypeResult{Defs: out, Conf: ConfLexical}
}

// csTypeResult groups defs into logical types (partial parts together) and
// applies the uniqueness rule per logical type: one keeps conf, 2..5 get
// ConfAmbig/n, more resolve to nothing.
func csTypeResult(s *Session, defs []*Def, conf uint8) TypeResult {
	if len(defs) == 0 {
		return TypeResult{}
	}
	groups := map[[2]string]bool{}
	for _, d := range defs {
		groups[[2]string{s.Module(s.File(d.FileID)), d.QName}] = true
	}
	switch n := len(groups); {
	case n == 1:
		return TypeResult{Defs: defs, Conf: conf}
	case n <= MaxAmbiguous:
		return TypeResult{Defs: defs, Conf: ConfAmbig / uint8(n)}
	}
	return TypeResult{}
}

// ---------------------------------------------------------------- type lookup

// csLookup resolves type names as seen from one place: a file and the
// namespaces enclosing the reference.
type csLookup struct {
	s    *Session
	file *File
	info *csFileInfo
	nss  []string // enclosing namespaces, most specific first
}

// csLookupFor builds the lookup context of file f; at, when known, is the
// definition the reference occurs in (its namespace is searched first).
func csLookupFor(s *Session, f *File, at *Def) *csLookup {
	l := &csLookup{s: s, file: f, info: csFileOf(s, f)}
	if at != nil {
		l.nss = append(l.nss, csNamespaceOf(s, at))
	}
	for _, ns := range l.info.namespaces {
		if !slices.Contains(l.nss, ns) {
			l.nss = append(l.nss, ns)
		}
	}
	return l
}

// typeCands returns the C# types called name.
func (l *csLookup) typeCands(name string) []*Def {
	return filter(l.s.DefsNamed(name), func(d *Def) bool {
		if !d.Kind.IsType() {
			return false
		}
		f := l.s.File(d.FileID)
		return f != nil && f.Lang == model.LangCSharp
	})
}

// inNamespace keeps the top-level types declared in namespace ns.
func (l *csLookup) inNamespace(cands []*Def, ns string) []*Def {
	return filter(cands, func(d *Def) bool { return d.ParentID == 0 && csNamespaceOf(l.s, d) == ns })
}

// types resolves the type name qual.name (qual may be "", a namespace, an
// alias, an enclosing type or a mix).
func (l *csLookup) types(qual, name string, depth int) TypeResult {
	qual, name = csClean(qual), csClean(name)
	if name == "" || depth > maxDepth {
		return TypeResult{}
	}
	if qual == "" {
		if target, ok := l.info.aliases[name]; ok {
			q, n := csSplitLast(target)
			return l.qualified(q, n, depth+1, true)
		}
	}
	if qual != "" {
		return l.qualified(qual, name, depth, false)
	}
	cands := l.typeCands(name)
	if len(cands) == 0 {
		return TypeResult{}
	}
	if in := filter(cands, func(d *Def) bool { return d.FileID == l.file.ID }); len(in) > 0 {
		return csTypeResult(l.s, in, ConfLexical)
	}
	for _, ns := range l.chain() {
		if in := l.inNamespace(cands, ns); len(in) > 0 {
			return csTypeResult(l.s, in, ConfLexical)
		}
	}
	var in []*Def
	for _, u := range l.info.usings {
		in = appendUnique(in, l.inNamespace(cands, u)...)
	}
	if len(in) > 0 {
		return csTypeResult(l.s, in, ConfImport)
	}
	if l.s.Common(name) {
		return TypeResult{}
	}
	return csTypeResult(l.s, filter(cands, func(d *Def) bool { return d.ParentID == 0 }), ConfUnique)
}

// chain returns every enclosing namespace (inner first), then "".
func (l *csLookup) chain() []string {
	var out []string
	for _, ns := range l.nss {
		for _, c := range csNamespaceChain(ns) {
			if !slices.Contains(out, c) {
				out = append(out, c)
			}
		}
	}
	if len(out) == 0 {
		out = []string{""}
	}
	return out
}

// qualified resolves qual.name: a fully qualified or enclosing-namespace
// relative type, or a type nested in the type qual names. An alias root of
// qual is expanded first; aliased marks a lookup that came from an alias
// target, which is always fully qualified.
func (l *csLookup) qualified(qual, name string, depth int, aliased bool) TypeResult {
	if qual == "" {
		if aliased {
			return csTypeResult(l.s, l.inNamespace(l.typeCands(name), ""), ConfImport)
		}
		return l.types("", name, depth+1)
	}
	if !aliased {
		root, rest, _ := strings.Cut(qual, ".")
		if target, ok := l.info.aliases[root]; ok {
			qual, aliased = target, true
			if rest != "" {
				qual += "." + rest
			}
		}
	}
	cands := l.typeCands(name)
	if len(cands) == 0 {
		return TypeResult{}
	}
	if in := l.inNamespace(cands, qual); len(in) > 0 {
		return csTypeResult(l.s, in, ConfImport)
	}
	if !aliased {
		for _, ns := range l.chain() {
			if ns == "" {
				continue
			}
			if in := l.inNamespace(cands, ns+"."+qual); len(in) > 0 {
				return csTypeResult(l.s, in, ConfLexical)
			}
		}
	}
	// A nested type: qual names its enclosing type.
	q, last := csSplitLast(qual)
	var outer TypeResult
	if aliased {
		outer = l.qualified(q, last, depth+1, true)
	} else {
		outer = l.types(q, last, depth+1)
	}
	if !outer.Found() {
		return TypeResult{}
	}
	var parts []*Def
	for _, o := range outer.Defs {
		parts = appendUnique(parts, csParts(l.s, o)...)
	}
	in := filter(cands, func(d *Def) bool {
		return slices.ContainsFunc(parts, func(p *Def) bool { return p.ID == d.ParentID })
	})
	tr := csTypeResult(l.s, in, outer.Conf)
	if tr.Found() && outer.Conf < tr.Conf {
		tr.Conf = outer.Conf
	}
	return tr
}
