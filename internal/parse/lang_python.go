package parse

import (
	_ "embed"
	"path"
	"strings"
	"unsafe"

	sitter "github.com/tree-sitter/go-tree-sitter"
	tspython "github.com/tree-sitter/tree-sitter-python/bindings/go"

	"github.com/pragmataW/graphmaster/internal/model"
)

//go:embed queries/python.scm
var pythonQuery string

// pythonSpec is the Python language spec. Module identity is the dotted
// module path (shop.services.pricing) derived from the __init__.py package
// chain, or else from the source root (pyproject.toml / setup.py / setup.cfg
// directory, or its src/ layout). See lang_python_module.go for import
// resolution.
//
// Imports are recorded one per imported name: "import a.b as c" is
// Import{Spec: "a.b", Alias: "c"}; "from .m import x as y" is
// Import{Spec: ".m.x", Names: ["x"], Alias: "y"} and ResolveImport maps it to
// the submodule .m.x when that exists, else to the module .m. "from m
// import *" is Import{Spec: "m", Names: ["*"], Wildcard: true}.
type pythonSpec struct{}

func init() { Register(pythonSpec{}) }

func (pythonSpec) Lang() model.Lang        { return model.LangPython }
func (pythonSpec) Extensions() []string    { return model.LangPython.Extensions() }
func (pythonSpec) Grammar() unsafe.Pointer { return tspython.Language() }
func (pythonSpec) QuerySource() string     { return pythonQuery }
func (pythonSpec) ExtractorVersion() int   { return 1 }

// IsTestPath implements TestPathMatcher with pytest's default discovery
// patterns (test_*.py, *_test.py) plus conftest.py.
func (pythonSpec) IsTestPath(p string) bool {
	base := path.Base(strings.ReplaceAll(p, `\`, "/"))
	stem, ok := strings.CutSuffix(base, ".py")
	if !ok {
		return false
	}
	return strings.HasPrefix(stem, "test_") || strings.HasSuffix(stem, "_test") || stem == "conftest"
}

// isExported: public names (no leading underscore, or dunder) that are not
// local to a function.
func (pythonSpec) isExported(d *model.Def, n *sitter.Node, _ []byte) bool {
	if !pyPublicName(d.Name) {
		return false
	}
	for p := n.Parent(); p != nil; p = p.Parent() {
		if p.Kind() == "function_definition" {
			return false
		}
	}
	return true
}

func pyPublicName(name string) bool {
	if !strings.HasPrefix(name, "_") {
		return true
	}
	return len(name) > 4 && strings.HasPrefix(name, "__") && strings.HasSuffix(name, "__")
}

// signature renders functions and classes as their header without the
// trailing colon, prefixed with their decorators ("@property def x(self) ->
// int"); assignments and aliases use their first line.
func (pythonSpec) signature(_ model.NodeKind, n *sitter.Node, src []byte) (string, bool) {
	var decorators strings.Builder
	if n.Kind() == "decorated_definition" {
		for i := uint(0); i < n.NamedChildCount(); i++ {
			if c := n.NamedChild(i); c != nil && c.Kind() == "decorator" {
				decorators.WriteString(firstLine(string(src[c.StartByte():c.EndByte()])))
				decorators.WriteByte(' ')
			}
		}
		if n = n.ChildByFieldName("definition"); n == nil {
			return "", false
		}
	}
	switch n.Kind() {
	case "function_definition", "class_definition":
		end := n.EndByte()
		if body := n.ChildByFieldName("body"); body != nil {
			end = body.StartByte()
		}
		head := strings.TrimSpace(string(src[n.StartByte():end]))
		return decorators.String() + strings.TrimSpace(strings.TrimSuffix(head, ":")), true
	}
	return "", false
}

// testTarget derives the tested name: test_place_order -> place_order,
// TestPricing / PricingTests / PricingTestCase -> Pricing.
func (pythonSpec) testTarget(name string) string {
	for _, p := range []string{"test", "Test"} {
		if rest, ok := strings.CutPrefix(name, p); ok && rest != "" {
			return strings.TrimLeft(rest, "_")
		}
	}
	for _, s := range []string{"TestCase", "Tests", "Test", "_test"} {
		if rest, ok := strings.CutSuffix(name, s); ok && rest != "" {
			return strings.TrimRight(rest, "_")
		}
	}
	return name
}

// importSpec keeps from-import statements whole (normalized) for
// postProcess and returns plain dotted names as written.
func (pythonSpec) importSpec(raw string) string {
	if isFromImport(raw) {
		return normalizeFromImport(raw)
	}
	return strings.Join(strings.Fields(raw), "")
}

// postProcess applies the Python-specific fixups described on pythonSpec
// and in queries/python.scm.
func (pythonSpec) postProcess(f *model.Facts) {
	f.Imports = expandPyImports(f.Imports)
	pyClassifyDefs(f)
	pyFixRefs(f)
	pyFixInherits(f)
}
