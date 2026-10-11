package resolve

import (
	"slices"
	"strings"

	"github.com/pragmataW/graphmaster/internal/model"
)

// cppResolver adds C++ scoping to the C resolver:
//   - namespaces and classes qualify names (ns::Cls::m); a qualifier that
//     names a namespace resolves to its blocks, same-file blocks first, so
//     util::clamp() inside namespace geo finds geo::util::clamp;
//   - out-of-line member definitions (void Cls::m() {}) are members of Cls
//     (OwnerName from the signature) and see Cls's members as bare names;
//   - implicit this inside class bodies (MembersInScope) and this->m();
//   - std (and other external namespaces), builtin types and
//     using-declarations of std names never resolve into the repository;
//     using-directives of repository namespaces add nothing beyond the
//     lexical and global tiers (namespaces are not files).
type cppResolver struct{ cResolver }

func init() { Register(model.LangCPP, cppResolver{}) }

// cppExternalNS are namespaces that never live in the repository.
var cppExternalNS = map[string]bool{"std": true, "__gnu_cxx": true, "__cxxabiv1": true}

// cppBuiltinTypes are fundamental types; they have no members.
var cppBuiltinTypes = map[string]bool{
	"void": true, "bool": true, "char": true, "wchar_t": true, "char8_t": true, "char16_t": true,
	"char32_t": true, "short": true, "int": true, "long": true, "float": true, "double": true,
	"signed": true, "unsigned": true, "auto": true, "size_t": true, "nullptr_t": true,
}

// ImportBindings implements LangResolver: a namespace alias of an external
// namespace (namespace fs = std::filesystem) binds its alias, so fs::x is
// known to be external. Includes and using-directives bind nothing.
func (cppResolver) ImportBindings(imp Import) []string {
	if imp.Alias == "" {
		return nil
	}
	if segs := cfamQualSegs(imp.Spec); len(segs) > 0 && cppExternalNS[segs[0]] {
		return []string{imp.Alias}
	}
	return nil
}

// IsMember implements LangResolver: out-of-line member definitions lifted
// to the top level are members too.
func (cppResolver) IsMember(s *Session, d *Def) bool {
	if cfamIsMember(s, d) {
		return true
	}
	return d.ParentID == 0 && (d.Kind == model.KindMethod || d.Kind == model.KindConstructor)
}

// MembersInScope implements LangResolver: implicit this.
func (cppResolver) MembersInScope() bool { return true }

// ResolveType implements LangResolver. Types matching the qualifier's
// trailing segments are chosen by locality (file, module, global); when no
// type matches, namespaces are returned so qualified references such as
// util::clamp resolve to the namespace's members.
func (cppResolver) ResolveType(s *Session, from *File, qual, name string) TypeResult {
	segs := cfamQualSegs(qual)
	if len(segs) > 0 && cppExternalNS[segs[0]] || len(segs) == 0 && cppExternalNS[name] {
		return TypeResult{External: true}
	}
	if len(segs) == 0 && cppBuiltinTypes[name] {
		return TypeResult{External: true}
	}
	cands := filter(s.DefsNamed(name), func(d *Def) bool { return sameFamily(s, d, from) && cfamChainEndsWith(s, d, segs) })
	if types := filter(cands, func(d *Def) bool { return d.Kind.IsType() }); len(types) > 0 {
		return s.typeByLocality(from, name, types, len(segs) == 0)
	}
	nss := filter(cands, func(d *Def) bool { return d.Kind == model.KindNamespace })
	if len(nss) == 0 {
		return TypeResult{}
	}
	// All blocks of a namespace are one entity, so several are not
	// ambiguous; the same file's blocks come first (lexical scope).
	if same := filter(nss, func(d *Def) bool { return d.FileID == from.ID }); len(same) > 0 {
		return TypeResult{Defs: same, Conf: ConfLexical}
	}
	return TypeResult{Defs: nss, Conf: ConfLexical}
}

// Receiver implements LangResolver. Besides cfamReceiver's typing, bare
// names follow C++ unqualified lookup across files: the owning class of an
// out-of-line member first, then the enclosing namespaces (innermost
// first) and the file's using-directives, whose blocks may live in other
// files. Names found in the same file or the enclosing class body are left
// to the lexical tier, and namespace members that are only partly
// declarations are left to the global tier (which prefers definitions).
func (r cppResolver) Receiver(s *Session, site *Site) Receiver {
	rv := cfamReceiver(s, site, r, r.ownerTypes)
	if rv.Kind != RecvNone {
		return rv
	}
	if enc := s.EnclosingType(site.Src); enc != nil {
		if len(s.Members(enc, site.Name)) > 0 {
			return rv
		}
	} else if owners := r.ownerTypes(s, site.Src); len(owners) > 0 {
		for _, o := range owners {
			if len(s.Members(o, site.Name)) > 0 {
				return Receiver{Kind: RecvTyped, Types: owners}
			}
		}
	}
	cands := s.DefsNamed(site.Name)
	if len(cands) == 0 || slices.ContainsFunc(cands, func(d *Def) bool { return d.FileID == site.File.ID }) {
		return rv
	}
	for _, chain := range r.scopeNamespaces(s, site) {
		blocks := cppNamespaceBlocks(s, site.File, chain)
		var ms []*Def
		for _, b := range blocks {
			ms = appendUnique(ms, s.Members(b, site.Name)...)
		}
		if len(ms) == 0 {
			continue
		}
		if slices.ContainsFunc(ms, func(d *Def) bool { return d.Flags.Has(model.FlagDeclaration) }) {
			return rv
		}
		return Receiver{Kind: RecvTyped, Types: blocks}
	}
	return rv
}

// scopeNamespaces returns the namespaces visible from site, innermost
// first: the namespace prefixes of the referencing definition's qualified
// name, then the file's using-directives.
func (cppResolver) scopeNamespaces(s *Session, site *Site) [][]string {
	var out [][]string
	if chain := cfamChain(s, site.Src); len(chain) > 1 {
		for n := len(chain) - 1; n >= 1; n-- {
			out = append(out, chain[:n])
		}
	}
	for _, im := range s.Imports(site.File) {
		if im.Wildcard() {
			if segs := cfamQualSegs(im.Spec); len(segs) > 0 && !cppExternalNS[segs[0]] {
				out = append(out, segs)
			}
		}
	}
	return out
}

// cppNamespaceBlocks returns the blocks of the namespace whose qualified name
// is chain, in any file of the language family.
func cppNamespaceBlocks(s *Session, from *File, chain []string) []*Def {
	if len(chain) == 0 {
		return nil
	}
	want := strings.Join(chain, ".")
	return filter(s.DefsNamed(chain[len(chain)-1]), func(d *Def) bool {
		return d.Kind == model.KindNamespace && sameFamily(s, d, from) &&
			strings.Join(cfamChain(s, d), ".") == want
	})
}

// ownerTypes returns the class an out-of-line member definition d belongs
// to, resolved from its qualified name (geo::Canvas::add -> geo::Canvas),
// or the enclosing class of a nested definition.
func (r cppResolver) ownerTypes(s *Session, d *Def) []*Def {
	if d == nil {
		return nil
	}
	if enc := s.EnclosingType(d); enc != nil {
		return []*Def{enc}
	}
	owner := cfamOwnerName(d)
	if owner == "" {
		return nil
	}
	chain := cfamChain(s, d)
	if len(chain) < 2 || chain[len(chain)-2] != owner {
		return nil
	}
	f := s.File(d.FileID)
	if f == nil {
		return nil
	}
	tr := r.ResolveType(s, f, strings.Join(chain[:len(chain)-2], "::"), owner)
	return filter(tr.Defs, func(t *Def) bool { return t.Kind.IsType() })
}
