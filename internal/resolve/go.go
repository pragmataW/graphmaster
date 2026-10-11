package resolve

import (
	"strings"

	"github.com/pragmataW/graphmaster/internal/model"
)

// goResolver adds Go scoping to the generic resolver:
//   - an import binds its package name (alias, or the guessed name of the
//     import path); blank and dot imports bind nothing;
//   - methods and fields are members and never visible as bare names;
//     top-level methods belong to the receiver type named in their sig;
//   - unqualified type names live in the same package (no global guessing),
//     and predeclared types are external;
//   - receiver typing (T3) reads the function body with tree-sitter (see
//     gotypes.go).
type goResolver struct{ Generic }

func init() { Register(model.LangGo, goResolver{}) }

// ImportBindings implements LangResolver.
func (goResolver) ImportBindings(imp Import) []string {
	switch imp.Alias {
	case "_", ".":
		return nil
	case "":
		return []string{goPackageName(imp.Spec)}
	}
	return []string{imp.Alias}
}

// IsMember implements LangResolver.
func (goResolver) IsMember(s *Session, d *Def) bool {
	if d.Kind == model.KindMethod || d.Kind == model.KindField {
		return true
	}
	p := s.Parent(d)
	return p != nil && p.Kind.IsType()
}

// OwnerName implements LangResolver: the receiver type of a top-level
// method.
func (goResolver) OwnerName(_ *Session, d *Def) string {
	if d.ParentID != 0 || d.Kind != model.KindMethod {
		return ""
	}
	return goReceiverTypeName(d.Sig)
}

// ResolveType implements LangResolver.
func (r goResolver) ResolveType(s *Session, from *File, qual, name string) TypeResult {
	types := func(keep func(*Def) bool) []*Def {
		return filter(s.DefsNamed(name), func(d *Def) bool {
			return d.Kind.IsType() && !r.IsMember(s, d) && keep(d)
		})
	}
	if qual != "" {
		t, ok := s.Binding(from, qual)
		if !ok {
			return TypeResult{}
		}
		if !t.Resolved() {
			return TypeResult{External: true}
		}
		return typeResult(types(func(d *Def) bool { return s.InTarget(d, t) }), ConfImport, true)
	}
	if goPredeclared[name] {
		return TypeResult{External: true}
	}
	if in := types(func(d *Def) bool { return d.FileID == from.ID }); len(in) > 0 {
		return typeResult(in, ConfLexical, false)
	}
	return typeResult(types(func(d *Def) bool {
		f := s.File(d.FileID)
		return f != nil && f.Lang == model.LangGo && s.SameModule(f, from)
	}), ConfLexical, false)
}

// Receiver implements LangResolver.
func (goResolver) Receiver(s *Session, site *Site) Receiver {
	return goReceiver(s, site)
}

// goReceiverTypeName extracts the receiver base type from a method sig:
// "func (m *Memory[T]) Get(...)" -> "Memory".
func goReceiverTypeName(sig string) string {
	rest, ok := strings.CutPrefix(strings.TrimSpace(sig), "func")
	if !ok {
		return ""
	}
	rest = strings.TrimSpace(rest)
	if !strings.HasPrefix(rest, "(") {
		return ""
	}
	end := matchClose(rest, 0)
	if end < 0 {
		return ""
	}
	recv := strings.TrimSpace(rest[1:end])
	if i := strings.LastIndexAny(recv, " \t"); i >= 0 {
		recv = recv[i+1:]
	}
	_, name, ok := goNamed(recv)
	if !ok {
		return ""
	}
	return name
}

// goPackageName guesses the package name of an import path, mirroring the
// parse layer's rule used to turn selector operands into qualifiers: the
// last element, skipping a /vN suffix and gopkg.in ".vN" suffixes, with
// "go-"/"-go" affixes removed and other dashes/dots as underscores.
func goPackageName(importPath string) string {
	segs := strings.Split(importPath, "/")
	name := segs[len(segs)-1]
	if len(segs) > 1 && isGoMajorVersion(name) {
		name = segs[len(segs)-2]
	}
	if i := strings.LastIndex(name, ".v"); i > 0 && isDigits(name[i+2:]) {
		name = name[:i]
	}
	name = strings.TrimSuffix(strings.TrimPrefix(name, "go-"), "-go")
	return strings.NewReplacer("-", "_", ".", "_").Replace(name)
}

func isGoMajorVersion(s string) bool { return len(s) >= 2 && s[0] == 'v' && isDigits(s[1:]) }

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// goPredeclared lists Go's predeclared types; they never resolve into the
// repository.
var goPredeclared = map[string]bool{
	"any": true, "bool": true, "byte": true, "comparable": true, "complex64": true,
	"complex128": true, "error": true, "float32": true, "float64": true, "int": true,
	"int8": true, "int16": true, "int32": true, "int64": true, "rune": true,
	"string": true, "uint": true, "uint8": true, "uint16": true, "uint32": true,
	"uint64": true, "uintptr": true,
}

// goBuiltinFuncs lists predeclared functions.
var goBuiltinFuncs = map[string]bool{
	"append": true, "cap": true, "clear": true, "close": true, "complex": true,
	"copy": true, "delete": true, "imag": true, "len": true, "make": true,
	"max": true, "min": true, "new": true, "panic": true, "print": true,
	"println": true, "real": true, "recover": true,
}
