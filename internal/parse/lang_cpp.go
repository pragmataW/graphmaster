package parse

import (
	_ "embed"
	"strings"
	"unsafe"

	sitter "github.com/tree-sitter/go-tree-sitter"
	tscpp "github.com/tree-sitter/tree-sitter-cpp/bindings/go"

	"github.com/pragmataW/graphmaster/internal/model"
)

//go:embed queries/cpp.scm
var cppQuery string

// cppSpec is the C++ language spec. Includes, module identity and the
// C-level constructs behave as in C (lang_c.go). On top of that:
//
//   - qnames follow namespaces and classes (geo::Canvas::add); out-of-line
//     member definitions (void Canvas::add() {}) are attached to their class
//     when it is defined in the same file and otherwise keep the class chain
//     in Def.Scope, prefixed by the file's single using-directive namespace
//     when the qualifier does not name it (heuristic);
//   - in-class functions become methods/constructors, out-of-line ones
//     methods unless their qualifier is a namespace known in the file;
//   - using-directives are wildcard imports, using-declarations of std names
//     are named imports of the external std namespace;
//   - GoogleTest TEST/TEST_F/TEST_P bodies are tests named after the test,
//     scoped by the suite, whose tested-name hint is the suite without its
//     Test suffix.
type cppSpec struct{}

func init() { Register(cppSpec{}) }

func (cppSpec) Lang() model.Lang             { return model.LangCPP }
func (cppSpec) Extensions() []string         { return model.LangCPP.Extensions() }
func (cppSpec) Grammar() unsafe.Pointer      { return tscpp.Language() }
func (cppSpec) QuerySource() string          { return cppQuery }
func (cppSpec) ExtractorVersion() int        { return 1 }
func (cppSpec) IsTestPath(p string) bool     { return cfamIsTestPath(p) }
func (cppSpec) importSpec(raw string) string { return cfamImportSpec(raw) }
func (cppSpec) postProcess(f *model.Facts)   { cfamPostProcess(f, true) }

// QName joins the container chain with "::" and omits the module.
func (cppSpec) QName(_ string, parts []string) string { return strings.Join(parts, "::") }

func (cppSpec) isExported(d *model.Def, n *sitter.Node, src []byte) bool {
	return cfamRefine(d, n, src, true)
}

func (cppSpec) signature(k model.NodeKind, n *sitter.Node, src []byte) (string, bool) {
	return cfamSignature(k, n, src)
}

// ModuleOf implements model.LanguageSpec.
func (cppSpec) ModuleOf(p string, _ *model.Facts, ctx model.RepoContext) string {
	return cfamModuleOf(p, ctx)
}

// ResolveImport implements model.LanguageSpec.
func (cppSpec) ResolveImport(spec, fromFile string, ctx model.RepoContext) model.ImportTarget {
	return cfamResolveImport(spec, fromFile, ctx)
}

// cppPostProcessDefs normalizes names and scopes and assigns member kinds.
func cppPostProcessDefs(f *model.Facts) {
	namespaces := cppKnownNamespaces(f)
	for i := range f.Defs {
		d := &f.Defs[i]
		if d.Kind == model.KindNamespace && strings.Contains(d.Name, "::") {
			if segs := cfamSplitQualified(d.Name); len(segs) > 0 {
				d.Name = segs[len(segs)-1]
				d.Scope = append(append([]string(nil), segs[:len(segs)-1]...), d.Scope...)
			}
			continue
		}
		if len(d.Scope) > 0 && d.Kind != model.KindTest {
			// The extractor split the captured qualified declarator on "::";
			// rejoin and split again outside template arguments, then drop
			// the trailing name: "ns::Cls<T>::m" -> [ns Cls].
			segs := cfamSplitQualified(strings.Join(d.Scope, "::"))
			if n := len(segs); n > 0 && segs[n-1] == strings.Join(strings.Fields(d.Name), " ") {
				segs = segs[:n-1]
			}
			d.Scope = segs
			if len(d.Scope) == 0 {
				d.Scope = nil
			}
		}
		if d.Kind != model.KindFunction {
			continue
		}
		switch p := d.Parent; {
		case p >= 0 && f.Defs[p].Kind.IsType():
			d.Kind = model.KindMethod
			if d.Name == f.Defs[p].Name {
				d.Kind = model.KindConstructor
			}
		case len(d.Scope) > 0:
			last := d.Scope[len(d.Scope)-1]
			switch {
			case d.Name == last:
				d.Kind = model.KindConstructor
			case !namespaces[last]:
				d.Kind = model.KindMethod
			}
		}
	}
}

// cppKnownNamespaces collects namespace names declared or used in the file.
func cppKnownNamespaces(f *model.Facts) map[string]bool {
	ns := map[string]bool{"std": true}
	for i := range f.Defs {
		if f.Defs[i].Kind == model.KindNamespace {
			for _, s := range cfamSplitQualified(f.Defs[i].Name) {
				ns[s] = true
			}
		}
	}
	for _, im := range f.Imports {
		if rest, ok := strings.CutPrefix(im.Spec, "using namespace "); ok {
			for _, s := range cfamSplitQualified(rest) {
				ns[s] = true
			}
		}
	}
	return ns
}

// cppUsingNamespace returns the segments of the file's only non-std
// using-directive, or nil when there is none or more than one.
func cppUsingNamespace(f *model.Facts) []string {
	var found []string
	for _, im := range f.Imports {
		rest, ok := strings.CutPrefix(im.Spec, "using namespace ")
		if !ok {
			continue
		}
		segs := cfamSplitQualified(rest)
		if len(segs) == 0 || cfamExternalNS[segs[0]] {
			continue
		}
		if found != nil && strings.Join(found, "::") != strings.Join(segs, "::") {
			return nil
		}
		found = segs
	}
	return found
}

// cppPlaceOutOfLine attaches out-of-line definitions to a same-file class
// named by their qualifier, and otherwise lifts them out of namespace
// blocks so that their Scope carries the full container chain and they are
// recognized as members of the class across files.
func cppPlaceOutOfLine(f *model.Facts) {
	classes := map[string]int32{}
	for i := range f.Defs {
		if f.Defs[i].Kind.IsType() {
			k := strings.Join(f.QNameParts(int32(i)), "::")
			if _, dup := classes[k]; !dup {
				classes[k] = int32(i)
			}
		}
	}
	using := cppUsingNamespace(f)
	for i := range f.Defs {
		d := &f.Defs[i]
		if len(d.Scope) == 0 {
			continue
		}
		switch d.Kind {
		case model.KindFunction, model.KindMethod, model.KindConstructor:
		default:
			continue
		}
		var chain []string
		if d.Parent >= 0 {
			chain = append(chain, f.QNameParts(d.Parent)...)
		}
		chain = append(chain, d.Scope...)
		attach := func(c []string) bool {
			if ci, ok := classes[strings.Join(c, "::")]; ok && ci < int32(i) {
				d.Parent, d.Scope = ci, nil
				return true
			}
			return false
		}
		if attach(chain) {
			continue
		}
		if d.Parent == model.NoParent && using != nil && chain[0] != using[0] {
			chain = append(append([]string(nil), using...), chain...)
			if attach(chain) {
				continue
			}
		}
		if d.Parent >= 0 && f.Defs[d.Parent].Kind.IsType() {
			continue
		}
		d.Parent, d.Scope = model.NoParent, chain
	}
}

// cppPostProcessRefs reduces qualifiers captured as whole qualified names
// to the part before the referenced name: "std::make_unique<T>" for
// make_unique becomes "std".
func cppPostProcessRefs(f *model.Facts) {
	for i := range f.Refs {
		r := &f.Refs[i]
		if r.Qualifier == "" {
			continue
		}
		segs := cfamSplitQualified(r.Qualifier)
		if n := len(segs); n > 0 && segs[n-1] == r.Name {
			segs = segs[:n-1]
		}
		r.Qualifier = strings.Join(segs, "::")
	}
}

// cppPostProcessTests derives the tested-name hint of GoogleTest tests from
// their suite: TEST(CanvasTest, Area) -> "Canvas".
func cppPostProcessTests(f *model.Facts) {
	for i := range f.Tests {
		t := &f.Tests[i]
		d := &f.Defs[t.Def]
		if d.Kind != model.KindTest {
			continue
		}
		suite := ""
		switch {
		case len(d.Scope) > 0:
			suite = d.Scope[len(d.Scope)-1]
		case d.Parent >= 0 && f.Defs[d.Parent].Kind.IsType():
			suite = f.Defs[d.Parent].Name
		default:
			continue
		}
		target := suite
		for _, suf := range []string{"Tests", "Test", "Fixture"} {
			if s := strings.TrimSuffix(suite, suf); s != suite && s != "" {
				target = strings.TrimRight(s, "_")
				break
			}
		}
		t.Target = target
	}
}
