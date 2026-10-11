package resolve

import (
	"bytes"
	"path"
	"strings"

	"github.com/pragmataW/graphmaster/internal/model"
)

// Generic is the language-neutral resolver used for languages without a
// registered LangResolver. Language resolvers embed it and override hooks.
type Generic struct{}

var _ LangResolver = Generic{}

// ImportBindings binds the alias, or else the last segment of the spec
// ("./utils/format" -> "format", "shop.models" -> "models").
func (Generic) ImportBindings(imp Import) []string {
	if imp.Alias != "" {
		return []string{imp.Alias}
	}
	if imp.Wildcard() {
		return nil
	}
	if seg := lastSegment(imp.Spec); seg != "" {
		return []string{seg}
	}
	return nil
}

// lastSegment returns the last path/dotted segment of an import spec
// without a file extension.
func lastSegment(spec string) string {
	spec = strings.Trim(spec, `"'<>`)
	if ext := path.Ext(spec); model.LangFromExt(ext) != model.LangUnknown {
		spec = strings.TrimSuffix(spec, ext)
	}
	spec = strings.NewReplacer("::", "/", `\`, "/", ".", "/").Replace(spec)
	spec = strings.TrimRight(spec, "/")
	if i := strings.LastIndexByte(spec, '/'); i >= 0 {
		spec = spec[i+1:]
	}
	return spec
}

// IsMember reports whether d is nested in a type.
func (Generic) IsMember(s *Session, d *Def) bool {
	p := s.Parent(d)
	return p != nil && p.Kind.IsType()
}

// OwnerName is "" for the generic resolver: members are lexically nested.
func (Generic) OwnerName(*Session, *Def) string { return "" }

// MembersInScope is false: members need an explicit receiver.
func (Generic) MembersInScope() bool { return false }

// ResolveType applies DefaultResolveType.
func (Generic) ResolveType(s *Session, from *File, qual, name string) TypeResult {
	return s.DefaultResolveType(from, qual, name)
}

// Receiver recognizes member access by scanning the source text before the
// name for ".", "->" or "?.", and types the receivers this/self/$this
// (enclosing type) and super/parent (its ancestors). Other receivers are
// untyped.
func (Generic) Receiver(s *Session, site *Site) Receiver {
	return s.TextReceiver(site)
}

// TextReceiver implements Generic.Receiver; language resolvers may reuse it.
func (s *Session) TextReceiver(site *Site) Receiver {
	src := s.Source(site.File)
	if src == nil {
		return Receiver{Kind: RecvUnknownShape}
	}
	line := lineAt(src, site.Line)
	if line == nil || int(site.Col) > len(line) || !bytes.HasPrefix(line[site.Col:], []byte(site.Name)) {
		return Receiver{Kind: RecvUnknownShape}
	}
	before := bytes.TrimRight(line[:site.Col], " \t")
	switch {
	case bytes.HasSuffix(before, []byte("?.")):
		before = before[:len(before)-2]
	case bytes.HasSuffix(before, []byte("->")):
		before = before[:len(before)-2]
	case bytes.HasSuffix(before, []byte(".")) && !bytes.HasSuffix(before, []byte("..")):
		before = before[:len(before)-1]
	default:
		return Receiver{Kind: RecvNone}
	}
	before = bytes.TrimRight(before, " \t")
	if bytes.HasSuffix(before, []byte("super()")) { // Python super().m()
		before = before[:len(before)-2]
	}
	i := len(before)
	for i > 0 && isIdentByte(before[i-1]) {
		i--
	}
	root := string(before[i:])
	if root == "" || (i > 0 && strings.ContainsRune(".>:", rune(before[i-1]))) {
		return Receiver{Kind: RecvUntyped} // call result, index or longer chain
	}
	enc := s.EnclosingType(site.Src)
	switch root {
	case "this", "self", "$this":
		if enc != nil {
			return Receiver{Kind: RecvTyped, Types: []*Def{enc}}
		}
	case "super", "parent":
		if enc != nil {
			if anc := s.Ancestors(enc); len(anc) > 0 {
				return Receiver{Kind: RecvTyped, Types: anc}
			}
			return Receiver{Kind: RecvExternal} // superclass outside the repo
		}
	}
	return Receiver{Kind: RecvUntyped}
}

func isIdentByte(c byte) bool {
	return c == '_' || c == '$' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}

// lineAt returns the 1-based line n of src without its terminator.
func lineAt(src []byte, n uint32) []byte {
	if n == 0 {
		return nil
	}
	for i := uint32(1); i < n; i++ {
		j := bytes.IndexByte(src, '\n')
		if j < 0 {
			return nil
		}
		src = src[j+1:]
	}
	if j := bytes.IndexByte(src, '\n'); j >= 0 {
		src = src[:j]
	}
	return bytes.TrimSuffix(src, []byte("\r"))
}

// DefaultResolveType is the generic type-name resolution:
//
//   - qualified: an import-bound root resolves inside the import target
//     (T1, External if the target is outside the repo); a fully qualified
//     module path matches by module (T1); otherwise the qualifier names an
//     enclosing type (Outer.Inner) and the type is chosen by locality.
//   - unqualified: named import (T1), lexical (same file, then module; T2),
//     wildcard import (T1), then unique global type (T4) or T5.
func (s *Session) DefaultResolveType(from *File, qual, name string) TypeResult {
	cands := filter(s.DefsNamed(name), func(d *Def) bool { return d.Kind.IsType() })
	lr := For(from.Lang)
	if qual != "" {
		root, rest := splitQual(qual)
		if t, ok := s.Binding(from, root); ok {
			if !t.Resolved() {
				return TypeResult{External: true}
			}
			in := filter(cands, func(d *Def) bool { return s.InTarget(d, t) && s.ownedBy(lr, d, rest) })
			return typeResult(in, ConfImport, true)
		}
		if t, ok := s.NamedImport(from, root); ok {
			if !t.Resolved() {
				return TypeResult{External: true}
			}
			in := filter(cands, func(d *Def) bool {
				return s.InTarget(d, t) && s.ownedBy(lr, d, append([]string{root}, rest...))
			})
			return typeResult(in, ConfImport, true)
		}
		if in := filter(cands, func(d *Def) bool {
			return s.Module(s.File(d.FileID)) == normQual(qual) && !lr.IsMember(s, d)
		}); len(in) > 0 {
			return typeResult(in, ConfImport, false)
		}
		segs := append([]string{root}, rest...)
		cands = filter(cands, func(d *Def) bool { return s.ownedBy(lr, d, segs[len(segs)-1:]) })
		return s.typeByLocality(from, name, cands, false)
	}
	if t, ok := s.NamedImport(from, name); ok {
		if !t.Resolved() {
			return TypeResult{External: true}
		}
		return typeResult(filter(cands, func(d *Def) bool { return s.InTarget(d, t) }), ConfImport, true)
	}
	return s.typeByLocality(from, name, cands, true)
}

// typeByLocality picks types by file, then module, then wildcard imports,
// then globally (T4/T5) unless the name is common.
func (s *Session) typeByLocality(from *File, name string, cands []*Def, wildcard bool) TypeResult {
	if in := filter(cands, func(d *Def) bool { return d.FileID == from.ID }); len(in) > 0 {
		return typeResult(in, ConfLexical, false)
	}
	if in := filter(cands, func(d *Def) bool { return s.SameModule(s.File(d.FileID), from) }); len(in) > 0 {
		return typeResult(in, ConfLexical, false)
	}
	if wildcard {
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
	}
	pool := filter(cands, func(d *Def) bool { return sameFamily(s, d, from) })
	if len(pool) == 0 || s.Common(name) {
		return TypeResult{}
	}
	return typeResult(pool, ConfUnique, false)
}

// typeResult applies the uniqueness rule: one def keeps conf, 2..5 become
// T5, more resolve to nothing. stop marks a bound qualifier whose target
// lacks the type, which must not fall back to guessing.
func typeResult(ds []*Def, conf uint8, stop bool) TypeResult {
	switch {
	case len(ds) == 1:
		return TypeResult{Defs: ds, Conf: conf}
	case len(ds) > 1 && len(ds) <= MaxAmbiguous:
		return TypeResult{Defs: ds, Conf: ConfAmbig / uint8(len(ds))}
	case len(ds) == 0:
		return TypeResult{External: stop}
	}
	return TypeResult{External: stop}
}
