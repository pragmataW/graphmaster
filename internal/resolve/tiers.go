package resolve

import (
	"slices"
	"strings"

	"github.com/pragmataW/graphmaster/internal/model"
)

// resolveGroup resolves one reference and returns its targets and tier.
func (s *Session) resolveGroup(g *group) ([]target, Tier) {
	f := s.File(g.key.file)
	src := s.Def(g.key.src)
	if f == nil || src == nil {
		return nil, TierNone
	}
	site := &Site{
		File: f, Src: src, Kind: g.key.kind, Name: s.name(g.key.nameID),
		Qual: g.key.qual, Line: g.key.line, Col: g.key.col,
	}
	return s.resolveSite(site)
}

// resolveSite applies the tiers to one reference.
func (s *Session) resolveSite(site *Site) ([]target, Tier) {
	lr := For(site.File.Lang)
	if site.Kind.IsInheritance() {
		return fromTypeResult(lr.ResolveType(s, site.File, site.Qual, site.Name))
	}
	if site.Kind == model.EdgeDeclares {
		dr, ok := lr.(DeclarationResolver)
		if !ok {
			return nil, TierNone
		}
		return pick(dr.Definitions(s, site.Src), anyKind, ConfLexical, Tier2Lexical)
	}
	cands := s.DefsNamed(site.Name)
	if len(cands) == 0 {
		return nil, TierNone
	}
	pref := kindPref(site.Kind)
	if site.Qual != "" {
		return s.resolveQualified(lr, site, cands, pref)
	}

	recv := lr.Receiver(s, site)
	bare := recv.Kind == RecvNone || recv.Kind == RecvUnknownShape
	// T1: the bare name is brought into scope by a named import.
	if t, ok := s.NamedImport(site.File, site.Name); ok && bare {
		if !t.Resolved() {
			return nil, TierNone
		}
		in := filter(cands, func(d *Def) bool { return s.InTarget(d, t) && !lr.IsMember(s, d) })
		if len(in) == 0 {
			in = s.reExported(lr, t, site.Name, cands, nil)
		}
		return pick(in, pref, ConfImport, Tier1Import)
	}

	switch recv.Kind {
	case RecvExternal:
		return nil, TierNone
	case RecvTyped:
		var ms []*Def
		for _, t := range recv.Types {
			ms = appendUnique(ms, s.Members(t, site.Name)...)
		}
		return pick(ms, pref, ConfReceiver, Tier3Receiver)
	case RecvUntyped:
		pool := filter(cands, func(d *Def) bool { return sameFamily(s, d, site.File) && lr.IsMember(s, d) })
		return s.global(site.Name, pool, pref)
	}

	// T2: lexical scope, then the same module.
	if ds := s.lexical(lr, site, cands, pref); len(ds) > 0 {
		return pick(ds, pref, ConfLexical, Tier2Lexical)
	}
	// T1 via wildcard imports (Java a.b.*, C# using, ...).
	for _, im := range s.Imports(site.File) {
		if !im.Wildcard() {
			continue
		}
		t := s.ImportTarget(site.File, im.Spec)
		if !t.Resolved() {
			continue
		}
		in := filter(cands, func(d *Def) bool { return s.InTarget(d, t) && !lr.IsMember(s, d) })
		if best(in, pref) != nil {
			return pick(in, pref, ConfImport, Tier1Import)
		}
	}
	pool := filter(cands, func(d *Def) bool {
		return sameFamily(s, d, site.File) && (recv.Kind == RecvUnknownShape || site.Kind == model.EdgeTests || !lr.IsMember(s, d))
	})
	return s.global(site.Name, pool, pref)
}

// resolveQualified handles pkg.Name / Type.Name / ns::Name references.
func (s *Session) resolveQualified(lr LangResolver, site *Site, cands []*Def, pref func(model.NodeKind) int) ([]target, Tier) {
	root, rest := splitQual(site.Qual)
	// T1: the qualifier root is bound by an import.
	if t, ok := s.Binding(site.File, root); ok {
		if !t.Resolved() {
			return nil, TierNone
		}
		in := filter(cands, func(d *Def) bool {
			return s.InTarget(d, t) && s.ownedBy(lr, d, rest)
		})
		if len(in) == 0 {
			in = s.reExported(lr, t, site.Name, cands, rest)
		}
		return pick(in, pref, ConfImport, Tier1Import)
	}
	// Member of an imported type: from x import Cls; Cls.method().
	if t, ok := s.NamedImport(site.File, root); ok {
		if !t.Resolved() {
			return nil, TierNone
		}
		owners := append([]string{root}, rest...)
		in := filter(cands, func(d *Def) bool {
			return s.InTarget(d, t) && s.ownedBy(lr, d, owners)
		})
		if len(in) == 0 {
			in = s.reExported(lr, t, site.Name, cands, owners)
		}
		return pick(in, pref, ConfImport, Tier1Import)
	}
	// Fully qualified module path (Java com.x.Cls, PHP App\Model).
	if in := filter(s.defsInModule(site.Name, normQual(site.Qual)), func(d *Def) bool {
		return !lr.IsMember(s, d)
	}); best(in, pref) != nil {
		return pick(in, pref, ConfImport, Tier1Import)
	}
	// Type-qualified: Type.member / ns::Type::member.
	segs := append([]string{root}, rest...)
	typePrefix := strings.Join(segs[:len(segs)-1], ".")
	tr := lr.ResolveType(s, site.File, typePrefix, segs[len(segs)-1])
	if tr.External {
		return nil, TierNone
	}
	if tr.Found() {
		var ms []*Def
		for _, t := range tr.Defs {
			ms = appendUnique(ms, s.Members(t, site.Name)...)
		}
		if len(ms) > 0 {
			return pick(ms, pref, min(tr.Conf, ConfLexical), tierOf(min(tr.Conf, ConfLexical)))
		}
	}
	// Fallback: any member whose owner has the qualifier's last name.
	last := segs[len(segs)-1]
	pool := filter(cands, func(d *Def) bool {
		return sameFamily(s, d, site.File) && s.ownedBy(lr, d, []string{last})
	})
	return s.global(site.Name, pool, pref)
}

// reExported resolves name through re-exports of import target t (see
// ReExporter) after none of t's own definitions matched. containers is the
// owner chain below t, outermost first (empty for a top-level name); its
// first element is looked up through the re-exports and name must be a
// member of it, declared in the same file.
func (s *Session) reExported(lr LangResolver, t model.ImportTarget, name string, cands []*Def, containers []string) []*Def {
	rx, ok := lr.(ReExporter)
	if !ok {
		return nil
	}
	top := func(d *Def) bool { return !lr.IsMember(s, d) }
	if len(containers) == 0 {
		return rx.ReExported(s, t, name, top)
	}
	var out []*Def
	for _, o := range rx.ReExported(s, t, containers[0], top) {
		out = appendUnique(out, filter(cands, func(d *Def) bool {
			return d.FileID == o.FileID && s.ownedBy(lr, d, containers)
		})...)
	}
	return out
}

// lexical returns the T2 candidates: same-file definitions visible from the
// reference (innermost scope first), members of enclosing types for
// languages with implicit this, then same-module top-level definitions.
func (s *Session) lexical(lr LangResolver, site *Site, cands []*Def, pref func(model.NodeKind) int) []*Def {
	// Ancestors of the referencing definition, innermost first.
	var chain []*Def
	for d := site.Src; d != nil && len(chain) < maxDepth; d = s.Parent(d) {
		chain = append(chain, d)
	}
	// Nested definitions in enclosing non-type scopes (local functions).
	for _, a := range chain {
		if a.Kind.IsType() {
			continue
		}
		in := filter(cands, func(d *Def) bool { return d.ParentID == a.ID })
		if best(in, pref) != nil {
			return in
		}
	}
	if lr.MembersInScope() {
		for _, a := range chain {
			if !a.Kind.IsType() {
				continue
			}
			if ms := s.Members(a, site.Name); best(ms, pref) != nil {
				return ms
			}
		}
	}
	top := func(d *Def) bool { return d.ParentID == 0 && !lr.IsMember(s, d) }
	if in := filter(cands, func(d *Def) bool { return d.FileID == site.File.ID && top(d) }); best(in, pref) != nil {
		return in
	}
	in := filter(cands, func(d *Def) bool {
		return top(d) && sameFamily(s, d, site.File) && s.SameModule(s.File(d.FileID), site.File)
	})
	if site.Kind == model.EdgeTests && best(in, pref) == nil {
		// A test target hint may name a method (TestServer_Start).
		in = filter(cands, func(d *Def) bool {
			return sameFamily(s, d, site.File) && s.SameModule(s.File(d.FileID), site.File)
		})
	}
	return in
}

// global applies T4/T5 to pool unless name is common.
func (s *Session) global(name string, pool []*Def, pref func(model.NodeKind) int) ([]target, Tier) {
	if len(pool) == 0 || s.Common(name) {
		return nil, TierNone
	}
	return pick(pool, pref, ConfUnique, Tier4Unique)
}

// pick keeps the candidates of the best kind rank and turns them into
// targets: one -> conf/tier, 2..MaxAmbiguous -> T5, more -> none.
func pick(cands []*Def, pref func(model.NodeKind) int, conf uint8, tier Tier) ([]target, Tier) {
	ds := best(cands, pref)
	switch {
	case len(ds) == 0:
		return nil, TierNone
	case len(ds) == 1:
		return []target{{dst: ds[0].ID, conf: conf}}, tier
	case len(ds) <= MaxAmbiguous:
		out := make([]target, len(ds))
		for i, d := range ds {
			out[i] = target{dst: d.ID, conf: ConfAmbig / uint8(len(ds))}
		}
		return out, Tier5Ambiguous
	}
	return nil, TierNone
}

// best returns the candidates with the lowest non-negative rank. A bare
// declaration (model.FlagDeclaration: a C/C++ prototype or an in-class
// member defined out of line) is dropped when a definition with the same
// qualified name is among them, so a call does not fan out to both. Other
// declarations (pure virtual methods, unmatched prototypes) stay.
func best(cands []*Def, pref func(model.NodeKind) int) []*Def {
	bestRank := -1
	var out []*Def
	for _, d := range cands {
		r := pref(d.Kind)
		switch {
		case r < 0:
		case bestRank < 0 || r < bestRank:
			bestRank, out = r, []*Def{d}
		case r == bestRank:
			out = append(out, d)
		}
	}
	return dropDefinedDeclarations(out)
}

// dropDefinedDeclarations removes the declarations of ds that a definition
// in ds (same qname) implements.
func dropDefinedDeclarations(ds []*Def) []*Def {
	if len(ds) < 2 {
		return ds
	}
	defined := map[string]bool{}
	for _, d := range ds {
		if !d.Flags.Has(model.FlagDeclaration) {
			defined[d.QName] = true
		}
	}
	if len(defined) == 0 {
		return ds
	}
	return filter(ds, func(d *Def) bool { return !d.Flags.Has(model.FlagDeclaration) || !defined[d.QName] })
}

// anyKind ranks every candidate equally.
func anyKind(model.NodeKind) int { return 0 }

// kindPref ranks candidate kinds for an edge kind (lower is better, -1
// excludes).
func kindPref(k model.EdgeKind) func(model.NodeKind) int {
	switch k {
	case model.EdgeCalls, model.EdgeTests:
		return func(n model.NodeKind) int {
			switch {
			case n.IsCallable():
				return 0
			case n.IsType():
				return 1 // constructor call or conversion
			case n == model.KindVariable || n == model.KindConstant || n == model.KindField || n == model.KindProperty:
				return 2 // function values
			}
			return -1
		}
	}
	return func(n model.NodeKind) int {
		if n == model.KindPackage || n == model.KindModule || n == model.KindFile {
			return -1
		}
		return 0
	}
}

func fromTypeResult(tr TypeResult) ([]target, Tier) {
	if !tr.Found() {
		return nil, TierNone
	}
	out := make([]target, len(tr.Defs))
	for i, d := range tr.Defs {
		out[i] = target{dst: d.ID, conf: tr.Conf}
	}
	if len(out) > 1 {
		return out, Tier5Ambiguous
	}
	return out, tierOf(tr.Conf)
}

func tierOf(conf uint8) Tier {
	switch {
	case conf >= ConfImport:
		return Tier1Import
	case conf >= ConfLexical:
		return Tier2Lexical
	case conf >= ConfReceiver:
		return Tier3Receiver
	case conf >= ConfUnique:
		return Tier4Unique
	case conf > 0:
		return Tier5Ambiguous
	}
	return TierNone
}

// Members returns the members of type t called name: direct members first,
// otherwise the members of the nearest ancestors (extends/implements/embeds,
// breadth first). Members are nested definitions or, for languages that
// declare them outside the type (Go methods), definitions whose OwnerName
// is t's name in t's module.
func (s *Session) Members(t *Def, name string) []*Def {
	if t == nil {
		return nil
	}
	cands := s.DefsNamed(name)
	if len(cands) == 0 {
		return nil
	}
	seen := map[int64]bool{t.ID: true}
	level := []*Def{t}
	for depth := 0; depth < maxDepth && len(level) > 0; depth++ {
		var found []*Def
		for _, typ := range level {
			found = appendUnique(found, s.directMembers(typ, cands)...)
		}
		if len(found) > 0 {
			return found
		}
		var next []*Def
		for _, typ := range level {
			for _, a := range s.Ancestors(typ) {
				if !seen[a.ID] {
					seen[a.ID] = true
					next = append(next, a)
				}
			}
		}
		level = next
	}
	return nil
}

func (s *Session) directMembers(t *Def, cands []*Def) []*Def {
	tf := s.File(t.FileID)
	var lr LangResolver = Generic{}
	if tf != nil {
		lr = For(tf.Lang)
	}
	return filter(cands, func(d *Def) bool {
		if d.ParentID != 0 {
			return d.ParentID == t.ID
		}
		return lr.OwnerName(s, d) == t.Name && s.SameModule(s.File(d.FileID), tf)
	})
}

// Ancestors resolves the direct supertypes of t from its inheritance
// references, in declaration order.
func (s *Session) Ancestors(t *Def) []*Def {
	tf := s.File(t.FileID)
	if tf == nil {
		return nil
	}
	lr := For(tf.Lang)
	var out []*Def
	for _, in := range s.Inherits(t) {
		out = appendUnique(out, lr.ResolveType(s, tf, in.Qual, in.Name).Defs...)
	}
	return out
}

// ownedBy reports whether d's owner chain ends with containers (outermost
// first); an empty chain means d must not be a member.
func (s *Session) ownedBy(lr LangResolver, d *Def, containers []string) bool {
	if len(containers) == 0 {
		return !lr.IsMember(s, d)
	}
	cur := d
	for i := len(containers) - 1; i >= 0; i-- {
		var owner string
		if p := s.Parent(cur); p != nil {
			owner, cur = p.Name, p
		} else {
			owner, cur = lr.OwnerName(s, cur), nil
		}
		if owner != containers[i] {
			return false
		}
		if cur == nil {
			return i == 0
		}
	}
	return true
}

// splitQual splits a qualifier on ".", "::", "\" and "->" into its first
// segment and the rest.
func splitQual(q string) (string, []string) {
	segs := strings.Split(normQual(q), ".")
	var out []string
	for _, p := range segs {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return "", nil
	}
	return out[0], out[1:]
}

var qualSeps = strings.NewReplacer("::", ".", `\`, ".", "->", ".")

func normQual(q string) string {
	return strings.Trim(qualSeps.Replace(q), ".")
}

// sameFamily reports whether d is in a language family that f can call
// into without an import (TS/TSX/JS and C/C++ interoperate).
func sameFamily(s *Session, d *Def, f *File) bool {
	df := s.File(d.FileID)
	return df != nil && langFamily(df.Lang) == langFamily(f.Lang)
}

func langFamily(l model.Lang) model.Lang {
	switch l {
	case model.LangTSX, model.LangJavaScript:
		return model.LangTypeScript
	case model.LangCPP:
		return model.LangC
	}
	return l
}

func filter(ds []*Def, keep func(*Def) bool) []*Def {
	var out []*Def
	for _, d := range ds {
		if keep(d) {
			out = append(out, d)
		}
	}
	return out
}

func appendUnique(dst []*Def, ds ...*Def) []*Def {
	for _, d := range ds {
		if !slices.Contains(dst, d) {
			dst = append(dst, d)
		}
	}
	return dst
}
