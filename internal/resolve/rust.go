package resolve

import (
	"bytes"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/pragmataW/graphmaster/internal/model"
)

// rustResolver adds Rust scoping to the generic resolver:
//   - a use declaration binds its last segment as a qualifier root when it
//     names a module (lowercase); types, traits and enums (CamelCase) are
//     named imports. The parse layer's synthetic path imports (aliases such
//     as "/crate/geometry" or "/super") bind their alias;
//   - methods, fields and enum variants are members. Items of an impl block
//     whose type lives in another file are top-level with the type name as
//     the second-to-last segment of their qualified name;
//   - type names come from imports, the same file or module, glob imports,
//     and are external for the prelude before any global guess; qualified
//     module paths (std::fmt::Display, other_crate::T) go through the
//     language spec's ResolveImport;
//   - receivers: self (also inside impls of types from other files),
//     parameters and let bindings with a written type or a constructor
//     (Type::new(..), Type { .. }).
type rustResolver struct{ Generic }

func init() { Register(model.LangRust, rustResolver{}) }

// rustPathKeywords are the path roots that are never plain identifiers.
var rustPathKeywords = map[string]bool{"crate": true, "self": true, "super": true}

// rustIsModuleName reports whether s looks like a module (or crate) name.
func rustIsModuleName(s string) bool {
	r, _ := utf8.DecodeRuneInString(s)
	return r == '_' || unicode.IsLower(r)
}

// ImportBindings implements LangResolver.
func (rustResolver) ImportBindings(imp Import) []string {
	if imp.Wildcard() || imp.Alias == "_" {
		return nil
	}
	local := imp.Alias
	if strings.HasPrefix(local, "/") {
		return []string{local} // synthetic path import from the parse layer
	}
	if local == "" {
		local = imp.Spec
		if i := strings.LastIndex(local, "::"); i >= 0 {
			local = local[i+2:]
		}
	}
	if local == "" || rustPathKeywords[local] || !rustIsModuleName(local) {
		return nil
	}
	return []string{local}
}

// IsMember implements LangResolver.
func (rustResolver) IsMember(s *Session, d *Def) bool {
	switch d.Kind {
	case model.KindMethod, model.KindField, model.KindEnumMember:
		return true
	}
	p := s.Parent(d)
	return p != nil && p.Kind.IsType()
}

// OwnerName implements LangResolver: the type of a top-level impl item
// (method or associated constant), read from its qualified name
// "<module>::Type::item".
func (rustResolver) OwnerName(s *Session, d *Def) string {
	if d.ParentID != 0 || (d.Kind != model.KindMethod && d.Kind != model.KindConstant) {
		return ""
	}
	q := d.QName
	if mod := s.Module(s.File(d.FileID)); mod != "" && strings.HasPrefix(q, mod) {
		q = q[len(mod):]
	}
	parts := strings.Split(normQual(q), ".")
	if len(parts) < 2 {
		return ""
	}
	return parts[len(parts)-2]
}

// rustPrelude lists std prelude types and traits that are in scope without
// an import; unqualified, unimported uses never resolve globally.
var rustPrelude = map[string]bool{
	"Option": true, "Some": true, "None": true, "Result": true, "Ok": true, "Err": true,
	"Vec": true, "String": true, "Box": true, "ToString": true, "ToOwned": true,
	"Clone": true, "Copy": true, "Send": true, "Sync": true, "Sized": true, "Unpin": true,
	"Drop": true, "Fn": true, "FnMut": true, "FnOnce": true, "Default": true,
	"Iterator": true, "IntoIterator": true, "DoubleEndedIterator": true,
	"ExactSizeIterator": true, "Extend": true, "FromIterator": true,
	"PartialEq": true, "Eq": true, "PartialOrd": true, "Ord": true,
	"AsRef": true, "AsMut": true, "Into": true, "From": true, "TryFrom": true, "TryInto": true,
	"Debug": true, "Hash": true,
}

// rustPrimitives are builtin types; method calls on them are external.
var rustPrimitives = map[string]bool{
	"bool": true, "char": true, "str": true, "i8": true, "i16": true, "i32": true, "i64": true,
	"i128": true, "isize": true, "u8": true, "u16": true, "u32": true, "u64": true, "u128": true,
	"usize": true, "f32": true, "f64": true,
}

// ResolveType implements LangResolver.
func (r rustResolver) ResolveType(s *Session, from *File, qual, name string) TypeResult {
	if name == "Self" || name == "" {
		return TypeResult{}
	}
	if qual == "" {
		return r.unqualifiedType(s, from, name)
	}
	root, _ := splitQual(qual)
	if _, ok := s.Binding(from, root); ok {
		return s.DefaultResolveType(from, qual, name)
	}
	if _, ok := s.NamedImport(from, root); ok {
		return s.DefaultResolveType(from, qual, name)
	}
	if !rustIsModuleName(root) {
		return s.DefaultResolveType(from, qual, name) // Type::Assoc
	}
	// A module path written in place: crate::a::T, std::fmt::Display.
	t := s.ImportTarget(from, strings.ReplaceAll(normQual(qual), ".", "::")+"::"+name)
	switch {
	case t.Resolved():
		in := filter(s.DefsNamed(name), func(d *Def) bool {
			return d.Kind.IsType() && !r.IsMember(s, d) && s.InTarget(d, t)
		})
		return typeResult(in, ConfImport, true)
	case t.Kind == model.TargetExternal:
		return TypeResult{External: true}
	}
	return TypeResult{}
}

// unqualifiedType resolves a bare type name: named import (T1), same file
// or module (T2), glob import (T1), prelude (external), unique global (T4).
func (r rustResolver) unqualifiedType(s *Session, from *File, name string) TypeResult {
	cands := filter(s.DefsNamed(name), func(d *Def) bool { return d.Kind.IsType() && !r.IsMember(s, d) })
	if t, ok := s.NamedImport(from, name); ok {
		if !t.Resolved() {
			return TypeResult{External: true}
		}
		return typeResult(filter(cands, func(d *Def) bool { return s.InTarget(d, t) }), ConfImport, true)
	}
	if in := filter(cands, func(d *Def) bool { return d.FileID == from.ID }); len(in) > 0 {
		return typeResult(in, ConfLexical, false)
	}
	if in := filter(cands, func(d *Def) bool { return s.SameModule(s.File(d.FileID), from) }); len(in) > 0 {
		return typeResult(in, ConfLexical, false)
	}
	for _, im := range s.Imports(from) {
		if !im.Wildcard() {
			continue
		}
		if t := s.ImportTarget(from, im.Spec); t.Resolved() {
			if in := filter(cands, func(d *Def) bool { return s.InTarget(d, t) }); len(in) > 0 {
				return typeResult(in, ConfImport, false)
			}
		}
	}
	if rustPrelude[name] || rustPrimitives[name] {
		return TypeResult{External: true}
	}
	pool := filter(cands, func(d *Def) bool { return sameFamily(s, d, from) })
	if len(pool) == 0 || s.Common(name) {
		return TypeResult{}
	}
	return typeResult(pool, ConfUnique, false)
}

// Receiver implements LangResolver.
func (r rustResolver) Receiver(s *Session, site *Site) Receiver {
	rec := s.TextReceiver(site)
	if rec.Kind != RecvUntyped {
		return r.withMembers(s, site, rec)
	}
	src := s.Source(site.File)
	root, off := rustReceiverRoot(src, site)
	if root == "" {
		return rec
	}
	if root == "self" {
		for d, i := site.Src, 0; d != nil && i < maxDepth; d, i = s.Parent(d), i+1 {
			if owner := r.OwnerName(s, d); owner != "" {
				return r.withMembers(s, site, r.typedReceiver(s, site.File, "", owner))
			}
		}
		return rec
	}
	if typ := rustLocalType(src, site.Src, root, off); typ != "" {
		if typ == "Self" {
			if t := s.EnclosingType(site.Src); t != nil {
				return r.withMembers(s, site, Receiver{Kind: RecvTyped, Types: []*Def{t}})
			}
			return rec
		}
		qual, name := rustSplitPath(typ)
		return r.withMembers(s, site, r.typedReceiver(s, site.File, qual, name))
	}
	return rec
}

func (r rustResolver) typedReceiver(s *Session, f *File, qual, name string) Receiver {
	if qual == "" && rustPrimitives[name] {
		return Receiver{Kind: RecvExternal}
	}
	tr := r.ResolveType(s, f, qual, name)
	switch {
	case tr.Found():
		return Receiver{Kind: RecvTyped, Types: tr.Defs}
	case tr.External:
		return Receiver{Kind: RecvExternal}
	}
	return Receiver{Kind: RecvUntyped}
}

// withMembers handles impl blocks in another module than their type, which
// Members cannot see (it requires the owner in the type's module): when the
// typed receiver has no such member but a top-level impl item of that name
// is owned by one of the types, candidates are chosen among all members
// (T4/T5) instead of resolving to nothing. Methods of std or derived traits
// keep the typed receiver and stay unresolved.
func (r rustResolver) withMembers(s *Session, site *Site, rec Receiver) Receiver {
	if rec.Kind != RecvTyped {
		return rec
	}
	for _, t := range rec.Types {
		if len(s.Members(t, site.Name)) > 0 {
			return rec
		}
	}
	for _, d := range s.DefsNamed(site.Name) {
		owner := r.OwnerName(s, d)
		for _, t := range rec.Types {
			if owner != "" && owner == t.Name && sameFamily(s, d, site.File) {
				return Receiver{Kind: RecvUntyped}
			}
		}
	}
	return rec
}

// rustReceiverRoot returns the identifier before ".name" at site and its
// byte offset in src, or "" for chains, calls and indexing.
func rustReceiverRoot(src []byte, site *Site) (string, int) {
	if src == nil || site.Line == 0 {
		return "", 0
	}
	start := 0
	for i := uint32(1); i < site.Line; i++ {
		j := bytes.IndexByte(src[start:], '\n')
		if j < 0 {
			return "", 0
		}
		start += j + 1
	}
	pos := start + int(site.Col)
	if pos > len(src) {
		return "", 0
	}
	before := bytes.TrimRight(src[start:pos], " \t")
	if !bytes.HasSuffix(before, []byte(".")) || bytes.HasSuffix(before, []byte("..")) {
		return "", 0
	}
	before = bytes.TrimRight(before[:len(before)-1], " \t")
	i := len(before)
	for i > 0 && isIdentByte(before[i-1]) {
		i--
	}
	if i == len(before) || (i > 0 && strings.ContainsRune(".:)]>", rune(before[i-1]))) {
		return "", 0
	}
	return string(before[i:]), start + i
}

// rustLocalType finds the written type of local root before offset off in
// the source of fn: the closest preceding parameter or let annotation
// ("root: &mut Type"), or a let initialized by a constructor
// ("let root = Type::new(..)" / "Type { .. }").
func rustLocalType(src []byte, fn *Def, root string, off int) string {
	if src == nil || fn == nil || int(fn.EndByte) > len(src) || int(fn.StartByte) >= off || off > len(src) {
		return ""
	}
	body := src[fn.StartByte:off]
	q := regexp.QuoteMeta(root)
	annot := regexp.MustCompile(`(?:^|[^\w.])` + q + `\s*:\s*([^=;,){]+)`)
	ctor := regexp.MustCompile(`\blet\s+(?:mut\s+)?` + q + `\s*=\s*&?\s*(?:mut\s+)?([A-Za-z_]\w*(?:\s*::\s*[A-Za-z_]\w*)*)\s*([({])`)
	best, bestAt := "", -1
	for _, m := range annot.FindAllSubmatchIndex(body, -1) {
		if t := rustCleanType(string(body[m[2]:m[3]])); t != "" && m[0] > bestAt {
			best, bestAt = t, m[0]
		}
	}
	for _, m := range ctor.FindAllSubmatchIndex(body, -1) {
		if m[0] <= bestAt {
			continue
		}
		path := strings.Join(strings.Fields(string(body[m[2]:m[3]])), "")
		segs := strings.Split(path, "::")
		if body[m[4]] == '(' {
			// Type::new(..): drop the constructor name; a plain call has no
			// known type.
			if len(segs) < 2 || rustIsModuleName(segs[len(segs)-2]) {
				continue
			}
			segs = segs[:len(segs)-1]
		}
		if rustIsModuleName(segs[len(segs)-1]) {
			continue
		}
		best, bestAt = strings.Join(segs, "::"), m[0]
	}
	return best
}

// rustCleanType reduces a written type to the path of its nominal type:
// references, mut, dyn/impl and Box/Rc/Arc wrappers are removed, generic
// arguments dropped. Tuples, slices, arrays and fn types give "".
func rustCleanType(t string) string {
	t = strings.TrimSpace(t)
	for range 8 {
		before := t
		t = strings.TrimSpace(strings.TrimLeft(t, "&"))
		if strings.HasPrefix(t, "'") { // lifetime
			if i := strings.IndexAny(t, " \t"); i >= 0 {
				t = strings.TrimSpace(t[i:])
			}
		}
		for _, p := range []string{"mut ", "dyn ", "impl "} {
			t = strings.TrimSpace(strings.TrimPrefix(t, p))
		}
		for _, w := range []string{"Box<", "Rc<", "Arc<", "std::boxed::Box<", "std::rc::Rc<", "std::sync::Arc<"} {
			if strings.HasPrefix(t, w) && strings.HasSuffix(t, ">") {
				t = strings.TrimSpace(t[len(w) : len(t)-1])
			}
		}
		if t == before {
			break
		}
	}
	if i := strings.IndexAny(t, "<+ \t"); i >= 0 {
		t = t[:i]
	}
	if t == "" || strings.ContainsAny(t, "([*!") {
		return ""
	}
	return t
}

// rustSplitPath splits "a::b::T" into ("a::b", "T").
func rustSplitPath(p string) (string, string) {
	if i := strings.LastIndex(p, "::"); i >= 0 {
		return p[:i], p[i+2:]
	}
	return "", p
}
