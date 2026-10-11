package parse

import (
	"context"
	"strings"
	"testing"

	"github.com/pragmataW/graphmaster/internal/model"
)

func parseCS(t *testing.T, src string) Result {
	t.Helper()
	e := NewEngine()
	defer e.Close()
	res, err := e.ParseLang(context.Background(), model.LangCSharp, []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if err := res.Facts.Validate(); err != nil {
		t.Fatalf("invalid facts: %v\n%s", err, DumpFacts(res.Facts))
	}
	return res
}

func TestCSharpRegistered(t *testing.T) {
	spec, ok := ForPath("src/App/Program.cs")
	if !ok || spec.Lang() != model.LangCSharp {
		t.Fatalf("ForPath(.cs) = %v, %v", spec, ok)
	}
	if _, ok := spec.(interface {
		QName(string, []string) string
	}); !ok {
		t.Fatal("csharp spec must implement the indexer's QNamer")
	}
}

func TestCSharpNamespaceFolding(t *testing.T) {
	res := parseCS(t, `
namespace A { namespace B.C { class X { void M() {} } } class Y {} }
`)
	for _, want := range []string{"A.B.C.X", "A.B.C.X.M", "A.Y"} {
		if _, d := defByName(res.Facts, want); d == nil {
			t.Errorf("missing %s:\n%s", want, DumpFacts(res.Facts))
		}
	}
	for _, d := range res.Facts.Defs {
		if d.Kind == model.KindNamespace {
			t.Fatalf("namespace def kept: %+v", d)
		}
	}
	if res.Facts.Package != "A" {
		t.Errorf("package = %q", res.Facts.Package)
	}

	res = parseCS(t, "using X;\nnamespace P.Q;\nclass Z { }\n")
	if _, d := defByName(res.Facts, "P.Q.Z"); d == nil {
		t.Fatalf("file-scoped namespace not applied:\n%s", DumpFacts(res.Facts))
	}
	if res.Facts.Package != "P.Q" {
		t.Errorf("package = %q", res.Facts.Package)
	}
}

func TestCSharpPartialEmbeds(t *testing.T) {
	res := parseCS(t, "namespace N { partial class P { } class Q { } }")
	if len(res.Facts.Inherits) != 1 {
		t.Fatalf("inherits:\n%s", DumpFacts(res.Facts))
	}
	in := res.Facts.Inherits[0]
	if in.Kind != model.EdgeEmbeds || in.Name != "P" || in.Qualifier != CSharpPartialQual {
		t.Fatalf("partial embed = %+v", in)
	}
}

func TestCSharpInheritKinds(t *testing.T) {
	res := parseCS(t, `
class A : IFoo, IBar {}
class B : Base, IFoo {}
struct S : IFoo, Other {}
interface I : IFoo, J {}
record R(int X) : Base(X), IFoo;
`)
	got := map[string]string{}
	for _, in := range res.Facts.Inherits {
		got[res.Facts.Defs[in.Child].Name+"->"+in.Name] = in.Kind.String()
	}
	want := map[string]string{
		"A->IFoo": "implements", "A->IBar": "implements",
		"B->Base": "extends", "B->IFoo": "implements",
		"S->IFoo": "implements", "S->Other": "implements",
		"I->IFoo": "extends", "I->J": "extends",
		"R->Base": "extends", "R->IFoo": "implements",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

func TestCSharpModuleOfAndImports(t *testing.T) {
	repo := model.NewMemRepo(map[string]string{
		"app/App.csproj":                   `<Project><PropertyGroup><RootNamespace>Shop.App</RootNamespace></PropertyGroup></Project>`,
		"app/Program.cs":                   "",
		"app/sub/Util.cs":                  "",
		"Shop.Billing/Shop.Billing.csproj": "<Project/>",
		"Shop.Billing/Invoice.cs":          "",
		"src/Shop.Data/Shop.Data.csproj":   "<Project/>",
		"loose/Script.cs":                  "",
	})
	spec, _ := Lookup(model.LangCSharp)
	for p, want := range map[string]string{
		"app/Program.cs":          "app/App.csproj",
		"app/sub/Util.cs":         "app/App.csproj",
		"Shop.Billing/Invoice.cs": "Shop.Billing/Shop.Billing.csproj",
		"loose/Script.cs":         ".",
	} {
		if got := spec.ModuleOf(p, nil, repo); got != want {
			t.Errorf("ModuleOf(%s) = %q, want %q", p, got, want)
		}
	}

	cases := []struct {
		spec, from string
		want       model.ImportTarget
	}{
		{"System.Linq", "app/Program.cs", model.ImportTarget{Kind: model.TargetExternal, Module: "System.Linq"}},
		{"global::System", "app/Program.cs", model.ImportTarget{Kind: model.TargetExternal, Module: "System"}},
		{"Shop.App.Models", "app/sub/Util.cs", model.ImportTarget{Kind: model.TargetModule, Path: "app", Module: "app/App.csproj"}},
		{"App", "app/Program.cs", model.ImportTarget{Kind: model.TargetModule, Path: "app", Module: "app/App.csproj"}},
		{"Shop.Billing.Models", "app/Program.cs", model.ImportTarget{Kind: model.TargetModule, Path: "Shop.Billing", Module: "Shop.Billing/Shop.Billing.csproj"}},
		{"Shop.Data", "app/Program.cs", model.ImportTarget{Kind: model.TargetModule, Path: "src/Shop.Data", Module: "src/Shop.Data/Shop.Data.csproj"}},
		{"Xunit", "app/Program.cs", model.ImportTarget{Kind: model.TargetUnresolved, Module: "Xunit"}},
	}
	for _, c := range cases {
		if got := spec.ResolveImport(c.spec, c.from, repo); got != c.want {
			t.Errorf("ResolveImport(%q, %q) = %+v, want %+v", c.spec, c.from, got, c.want)
		}
	}
	if got := spec.ResolveImport("Shop.X", "a.cs", nil); got.Kind != model.TargetUnresolved {
		t.Errorf("nil ctx: %+v", got)
	}
	if got := spec.ModuleOf("a.cs", nil, nil); got != "." {
		t.Errorf("nil ctx module = %q", got)
	}
}

func TestCSharpTestPathAndTarget(t *testing.T) {
	cs := csharpSpec{}
	for p, want := range map[string]bool{
		"tests/InvoiceTests.cs":         true,
		"src/Foo.Tests/Bar.cs":          true,
		"src/App/ParserTest.cs":         true,
		"test/Helpers.cs":               true,
		"src/App/Program.cs":            false,
		"src/Contest/Contest.cs":        false,
		`src\Shop.UnitTests\x\Thing.cs`: true,
	} {
		if got := cs.IsTestPath(p); got != want {
			t.Errorf("IsTestPath(%s) = %v", p, got)
		}
	}
	for name, want := range map[string]string{
		"Add_ReturnsSum":   "Add",
		"TestParse":        "Parse",
		"ParseTests":       "Parse",
		"TotalIncludesTax": "TotalIncludesTax",
		"Test":             "Test",
	} {
		if got := cs.testTarget(name); got != want {
			t.Errorf("testTarget(%s) = %q, want %q", name, got, want)
		}
	}
}

// TestCSharpGarbage feeds malformed and truncated input: extraction must
// never panic and always produce valid facts.
func TestCSharpGarbage(t *testing.T) {
	inputs := []string{
		"",
		"namespace",
		"namespace ;",
		"namespace A.B; namespace C.D; class X {}",
		"namespace A { class { void () } ",
		"class X : { partial class : , }",
		"using = ; using static ; global using ;",
		"[Fact] public void M( { this.",
		"class C { void M() { a?.b?.c(); new (); new X.Y<>(); x is ; } }",
		"\x00\xff\xfe namespace \x00 { }",
		strings.Repeat("namespace N { ", 300),
		strings.Repeat("{(", 500),
	}
	for _, src := range inputs {
		res := parseCS(t, src)
		if _, err := DecodeFacts(EncodeFacts(res.Facts)); err != nil {
			t.Fatalf("codec %q: %v", src, err)
		}
	}
}

func FuzzParseCSharp(f *testing.F) {
	f.Add([]byte("namespace A.B; partial class C : Base, IX { int f; void M() { this.N<int>(); x?.Y(); } }"))
	f.Add([]byte("namespace A { namespace B { record struct R(int X) : I; enum E { A = 1 } } }"))
	f.Add([]byte("using P = X.Y; class T { [Fact] void Test1() { new P().Q(); } }"))
	f.Add([]byte("namespace\x00 {{ class"))
	e := NewEngine()
	f.Cleanup(e.Close)
	f.Fuzz(func(t *testing.T, src []byte) {
		res, err := e.ParseLang(context.Background(), model.LangCSharp, src)
		if err != nil {
			t.Fatal(err)
		}
		if err := res.Facts.Validate(); err != nil {
			t.Fatalf("invalid facts: %v\n%s", err, DumpFacts(res.Facts))
		}
		if _, err := DecodeFacts(EncodeFacts(res.Facts)); err != nil {
			t.Fatal(err)
		}
	})
}
