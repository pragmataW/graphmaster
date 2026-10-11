package resolve

import (
	"strings"

	"github.com/pragmataW/graphmaster/internal/model"
	"github.com/pragmataW/graphmaster/internal/parse"
)

// javaResolver adds Java scoping to the generic resolver:
//   - imports bind no qualifier roots: a single-type import brings its type
//     name into scope (Import.Names, handled by the framework) and on-demand
//     imports are wildcards ("*");
//   - type names follow Java's precedence: same file, single-type import,
//     same package, on-demand imports, java.lang; types are matched by
//     qname (package + class chain), so nested and fully qualified names
//     resolve exactly;
//   - implicit this is handled by the receiver hook (enclosing types and
//     their ancestors, then static imports), not by MembersInScope, so a
//     type reference never resolves to a same-named constructor;
//   - receiver typing (T3) reads the method body with tree-sitter (see
//     javatypes.go): locals, parameters, fields, this/super, new, casts and
//     method results (return types from signatures).
type javaResolver struct{ Generic }

func init() { Register(model.LangJava, javaResolver{}) }

// ImportBindings implements LangResolver: Java imports bind no qualifier
// roots (a.b.C binds C by name, which the framework handles).
func (javaResolver) ImportBindings(Import) []string { return nil }

// ResolveType implements LangResolver.
func (r javaResolver) ResolveType(s *Session, from *File, qual, name string) TypeResult {
	if from == nil || name == "" {
		return TypeResult{}
	}
	qual = strings.Trim(normQual(qual), ".")
	if qual != "" {
		return r.qualifiedType(s, from, qual, name)
	}
	types := javaTypes(s, name)

	// Same file: the type itself or a nested type of the file.
	if in := filter(types, func(d *Def) bool { return d.FileID == from.ID }); len(in) > 0 {
		return typeResult(javaPreferTop(in), ConfLexical, false)
	}
	// Single-type import (shadows same-package types).
	for _, im := range s.Imports(from) {
		if im.Wildcard() || !javaHasName(im.Names, name) {
			continue
		}
		if in := filter(types, func(d *Def) bool { return javaQName(d) == im.Spec }); len(in) > 0 {
			return typeResult(in, ConfImport, true)
		}
		return TypeResult{External: true}
	}
	// Same package.
	pkg := s.Module(from)
	if in := filter(types, func(d *Def) bool { return javaQName(d) == javaJoinQName(pkg, name) && s.javaSamePackage(d, from) }); len(in) > 0 {
		return typeResult(in, ConfLexical, false)
	}
	// On-demand imports: a.b.* (package) or a.b.C.* (member types).
	externalWildcard := false
	for _, im := range s.Imports(from) {
		if !im.Wildcard() {
			continue
		}
		if in := filter(types, func(d *Def) bool { return javaQName(d) == im.Spec+"."+name }); len(in) > 0 {
			return typeResult(in, ConfImport, false)
		}
		if !s.ImportTarget(from, im.Spec).Resolved() {
			externalWildcard = true
		}
	}
	if javaLang[name] || externalWildcard {
		return TypeResult{External: true}
	}
	// Last resort (inherited member types, broken code): a unique Java type
	// anywhere, unless the name is common.
	pool := filter(types, func(d *Def) bool { return sameFamily(s, d, from) })
	if len(pool) == 0 || s.Common(name) {
		return TypeResult{}
	}
	return typeResult(pool, ConfUnique, false)
}

// qualifiedType resolves qual.name where qual is a package, a type
// (Outer.Inner) or an imported type's nested path.
func (r javaResolver) qualifiedType(s *Session, from *File, qual, name string) TypeResult {
	types := javaTypes(s, name)
	root, rest := splitQual(qual)
	suffix := strings.Join(append(rest, name), ".")

	// Fully qualified: com.x.Cls or com.x.Outer.Inner.
	if in := filter(types, func(d *Def) bool { return javaQName(d) == qual+"."+name }); len(in) > 0 {
		return typeResult(in, ConfImport, false)
	}
	// The root names a type in scope: Outer.Inner, Map.Entry.
	if parse.JavaExternalName(qual) {
		return TypeResult{External: true}
	}
	if !javaLowerIdent(root) {
		rt := r.ResolveType(s, from, "", root)
		if rt.External {
			return TypeResult{External: true}
		}
		var in []*Def
		for _, o := range rt.Defs {
			in = appendUnique(in, filter(types, func(d *Def) bool { return javaQName(d) == javaQName(o)+"."+suffix })...)
		}
		if len(in) > 0 {
			return typeResult(in, min(rt.Conf, ConfLexical), false)
		}
		if rt.Found() {
			return TypeResult{}
		}
	}
	// An unknown package path (a third-party library).
	if javaLowerIdent(root) {
		return TypeResult{External: true}
	}
	return TypeResult{}
}

// javaTypes returns the Java type definitions called name.
func javaTypes(s *Session, name string) []*Def {
	return filter(s.DefsNamed(name), func(d *Def) bool {
		f := s.File(d.FileID)
		return d.Kind.IsType() && f != nil && f.Lang == model.LangJava
	})
}

// javaPreferTop keeps top-level types when a file declares both a top-level
// and nested types of one name.
func javaPreferTop(ds []*Def) []*Def {
	if top := filter(ds, func(d *Def) bool { return d.ParentID == 0 }); len(top) > 0 {
		return top
	}
	return ds
}

// javaSamePackage reports whether d is declared in from's package. The
// default package ("") is scoped to the directory.
func (s *Session) javaSamePackage(d *Def, from *File) bool {
	f := s.File(d.FileID)
	if f == nil || f.Lang != model.LangJava {
		return false
	}
	if pkg := s.Module(from); pkg != "" {
		return s.Module(f) == pkg
	}
	return s.Module(f) == "" && javaDirOf(f.Path) == javaDirOf(from.Path)
}

func javaDirOf(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[:i]
	}
	return ""
}

func javaJoinQName(pkg, name string) string {
	if pkg == "" {
		return name
	}
	return pkg + "." + name
}

func javaHasName(names []string, name string) bool {
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}

func javaLowerIdent(s string) bool {
	return s != "" && s[0] >= 'a' && s[0] <= 'z'
}

// Receiver implements LangResolver.
func (r javaResolver) Receiver(s *Session, site *Site) Receiver {
	return javaReceiver(s, r, site)
}

// javaLang lists java.lang types (implicitly imported); unless shadowed by
// a repository type in scope they are external.
var javaLang = javaSetOf(
	"AbstractMethodError", "Appendable", "ArithmeticException", "ArrayIndexOutOfBoundsException",
	"ArrayStoreException", "AssertionError", "AutoCloseable", "Boolean", "Byte", "CharSequence",
	"Character", "Class", "ClassCastException", "ClassLoader", "ClassNotFoundException",
	"CloneNotSupportedException", "Cloneable", "Comparable", "Deprecated", "Double", "Enum",
	"Error", "Exception", "Float", "FunctionalInterface", "IllegalAccessException",
	"IllegalArgumentException", "IllegalMonitorStateException", "IllegalStateException",
	"IndexOutOfBoundsException", "InheritableThreadLocal", "InstantiationException", "Integer",
	"InterruptedException", "Iterable", "LinkageError", "Long", "Math", "Module",
	"NegativeArraySizeException", "NoSuchFieldException", "NoSuchMethodException",
	"NullPointerException", "Number", "NumberFormatException", "Object", "OutOfMemoryError",
	"Override", "Package", "Process", "ProcessBuilder", "Readable", "Record",
	"ReflectiveOperationException", "Runnable", "Runtime", "RuntimeException", "SafeVarargs",
	"SecurityException", "Short", "StackOverflowError", "StackTraceElement", "StrictMath",
	"String", "StringBuffer", "StringBuilder", "StringIndexOutOfBoundsException",
	"SuppressWarnings", "System", "Thread", "ThreadGroup", "ThreadLocal", "Throwable",
	"TypeNotPresentException", "UnsupportedOperationException", "VirtualMachineError", "Void",
)

func javaSetOf(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

// javaQName is d's qualified name (package + class chain). A default
// package type may be stored with a leading "." by some writers.
func javaQName(d *Def) string { return strings.TrimPrefix(d.QName, ".") }
