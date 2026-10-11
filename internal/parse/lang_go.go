package parse

import (
	_ "embed"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"
	"unsafe"

	sitter "github.com/tree-sitter/go-tree-sitter"
	tsgo "github.com/tree-sitter/tree-sitter-go/bindings/go"

	"github.com/pragmataW/graphmaster/internal/model"
)

//go:embed queries/go.scm
var goQuery string

// goSpec is the Go language spec. Module identity is the import path derived
// from the nearest go.mod; package scope is the directory.
type goSpec struct{}

func init() { Register(goSpec{}) }

func (goSpec) Lang() model.Lang              { return model.LangGo }
func (goSpec) Extensions() []string          { return model.LangGo.Extensions() }
func (goSpec) Grammar() unsafe.Pointer       { return tsgo.Language() }
func (goSpec) QuerySource() string           { return goQuery }
func (goSpec) ExtractorVersion() int         { return 1 }
func (goSpec) IsTestPath(p string) bool      { return strings.HasSuffix(p, "_test.go") }
func (goSpec) testTarget(name string) string { return goTestTarget(name) }
func (goSpec) isExported(d *model.Def, _ *sitter.Node, _ []byte) bool {
	r, _ := utf8.DecodeRuneInString(d.Name)
	return unicode.IsUpper(r)
}

// signature renders type, field, const and var signatures; functions and
// methods use the default (text up to the body).
func (goSpec) signature(_ model.NodeKind, n *sitter.Node, src []byte) (string, bool) {
	text := func(a, b uint) string { return string(src[a:b]) }
	switch n.Kind() {
	case "type_spec":
		if t := n.ChildByFieldName("type"); t != nil {
			switch t.Kind() {
			case "struct_type":
				return "type " + text(n.StartByte(), t.StartByte()) + "struct", true
			case "interface_type":
				return "type " + text(n.StartByte(), t.StartByte()) + "interface", true
			}
		}
		return "type " + firstLine(text(n.StartByte(), n.EndByte())), true
	case "type_alias":
		return "type " + firstLine(text(n.StartByte(), n.EndByte())), true
	case "field_declaration":
		end := n.EndByte()
		if tag := n.ChildByFieldName("tag"); tag != nil {
			end = tag.StartByte()
		}
		return firstLine(text(n.StartByte(), end)), true
	case "const_spec":
		return "const " + firstLine(text(n.StartByte(), n.EndByte())), true
	case "var_spec":
		return "var " + firstLine(text(n.StartByte(), n.EndByte())), true
	}
	return "", false
}

// goTestTarget maps TestServer_Start -> Server.Start, BenchmarkMap -> Map.
func goTestTarget(name string) string {
	for _, p := range []string{"Test", "Benchmark", "Fuzz"} {
		if strings.HasPrefix(name, p) {
			name = name[len(p):]
			break
		}
	}
	name = strings.TrimLeft(name, "_")
	return strings.Replace(name, "_", ".", 1)
}

// goPredeclared lists builtin types and functions; unqualified references
// to them can never resolve to repository code.
var goPredeclared = map[string]bool{
	"any": true, "bool": true, "byte": true, "comparable": true, "complex64": true,
	"complex128": true, "error": true, "float32": true, "float64": true, "int": true,
	"int8": true, "int16": true, "int32": true, "int64": true, "rune": true,
	"string": true, "uint": true, "uint8": true, "uint16": true, "uint32": true,
	"uint64": true, "uintptr": true,
	"append": true, "cap": true, "clear": true, "close": true, "complex": true,
	"copy": true, "delete": true, "imag": true, "len": true, "make": true,
	"max": true, "min": true, "new": true, "panic": true, "print": true,
	"println": true, "real": true, "recover": true,
}

// postProcess turns selector operands that name an imported package into
// qualifiers (pkg.Foo is a package-qualified name, s.Foo a member of a
// value), drops builtins, and treats external test packages as test files.
func (goSpec) postProcess(f *model.Facts) {
	pkgs := map[string]bool{}
	for _, im := range f.Imports {
		if name := goImportName(im); name != "" {
			pkgs[name] = true
		}
	}
	refs := f.Refs[:0]
	for _, r := range f.Refs {
		if r.Qualifier == "" && r.Receiver != "" && pkgs[r.Receiver] {
			r.Qualifier, r.Receiver = r.Receiver, ""
		}
		if r.Qualifier == "" && r.Receiver == "" && goPredeclared[r.Name] {
			continue
		}
		refs = append(refs, r)
	}
	f.Refs = refs

	inh := f.Inherits[:0]
	for _, in := range f.Inherits {
		if in.Qualifier == "" && goPredeclared[in.Name] {
			continue
		}
		inh = append(inh, in)
	}
	f.Inherits = inh
	if len(f.Inherits) == 0 {
		f.Inherits = nil
	}

	if strings.HasSuffix(f.Package, "_test") {
		f.TestFile = true
	}
}

// goImportName is the local name an import binds, or "" for blank and dot
// imports.
func goImportName(im model.Import) string {
	switch im.Alias {
	case "_", ".":
		return ""
	case "":
		return goPackageName(im.Spec)
	}
	return im.Alias
}

// goPackageName guesses the package name of an import path: the last
// element, skipping a /vN major-version suffix and gopkg.in ".vN" suffixes,
// with "go-" / "-go" affixes removed and other dashes as underscores.
func goPackageName(importPath string) string {
	segs := strings.Split(importPath, "/")
	name := segs[len(segs)-1]
	if len(segs) > 1 && isMajorVersion(name) {
		name = segs[len(segs)-2]
	}
	if i := strings.LastIndex(name, ".v"); i > 0 && isDigits(name[i+2:]) {
		name = name[:i]
	}
	name = strings.TrimSuffix(strings.TrimPrefix(name, "go-"), "-go")
	return strings.NewReplacer("-", "_", ".", "_").Replace(name)
}

func isMajorVersion(s string) bool {
	return len(s) >= 2 && s[0] == 'v' && isDigits(s[1:])
}

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

// ModuleOf returns the Go import path of the file's directory, derived from
// the nearest go.mod. Without a go.mod the directory itself ("." for the
// root) is the identity. External test packages (package x_test) get a
// "_test" suffix because they are distinct packages.
func (goSpec) ModuleOf(p string, facts *model.Facts, ctx model.RepoContext) string {
	dir := model.CleanRel(path.Dir(model.CleanRel(p)))
	id := dir
	if ctx != nil {
		if modFile, ok := model.FindUp(ctx, dir, "go.mod"); ok {
			if mod := goModulePath(ctx, modFile); mod != "" {
				id = joinImport(mod, relDir(model.CleanRel(path.Dir(modFile)), dir))
			}
		}
	}
	if id == "" {
		id = "."
	}
	if facts != nil && strings.HasSuffix(facts.Package, "_test") {
		id += "_test"
	}
	return id
}

// ResolveImport maps an import path under the enclosing module to its
// directory; everything else (stdlib, third-party) is external.
func (goSpec) ResolveImport(spec, fromFile string, ctx model.RepoContext) model.ImportTarget {
	if ctx != nil {
		dir := model.CleanRel(path.Dir(model.CleanRel(fromFile)))
		if modFile, ok := model.FindUp(ctx, dir, "go.mod"); ok {
			mod := goModulePath(ctx, modFile)
			if mod != "" && (spec == mod || strings.HasPrefix(spec, mod+"/")) {
				modDir := model.CleanRel(path.Dir(modFile))
				target := model.CleanRel(path.Join(modDir, strings.TrimPrefix(strings.TrimPrefix(spec, mod), "/")))
				if ctx.DirExists(target) {
					return model.ImportTarget{Kind: model.TargetModule, Path: target, Module: spec}
				}
				return model.ImportTarget{Kind: model.TargetUnresolved, Module: spec}
			}
		}
	}
	return model.ImportTarget{Kind: model.TargetExternal, Module: spec}
}

// goModulePath reads the module directive of a go.mod file.
func goModulePath(ctx model.RepoContext, modFile string) string {
	b, err := ctx.ReadFile(modFile)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		rest, ok := strings.CutPrefix(line, "module")
		if !ok || rest == "" || (rest[0] != ' ' && rest[0] != '\t') {
			continue
		}
		if i := strings.Index(rest, "//"); i >= 0 {
			rest = rest[:i]
		}
		return unquote(strings.TrimSpace(rest))
	}
	return ""
}

// relDir returns dir relative to base, both clean repo-relative paths with
// dir inside base.
func relDir(base, dir string) string {
	if base == "" {
		return dir
	}
	if dir == base {
		return ""
	}
	return strings.TrimPrefix(dir, base+"/")
}

func joinImport(mod, rel string) string {
	if rel == "" {
		return mod
	}
	return mod + "/" + rel
}
