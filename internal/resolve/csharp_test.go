package resolve

import (
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pragmataW/graphmaster/internal/model"
	"github.com/pragmataW/graphmaster/internal/testutil"
)

// csFixture returns the C# part of the materialized fixture.
func csFixture(t *testing.T) map[string]string {
	t.Helper()
	root := testutil.Materialize(t)
	files := map[string]string{}
	err := filepath.WalkDir(filepath.Join(root, "csharp"), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		files[filepath.ToSlash(rel)] = string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := files["csharp/Fixture.csproj"]; !ok {
		t.Fatal("Fixture.csproj missing from materialized fixture")
	}
	return files
}

func TestCSharpFixtureExpectedEdges(t *testing.T) {
	r := fullBuild(t, csFixture(t), nil)
	exps := loadExpectations(t, "csharp")
	if len(exps) < 15 {
		t.Fatalf("only %d C# expectations loaded", len(exps))
	}
	checkExpectations(t, r, exps)
}

// csCase pins the single resolution of every reference named name from src.
type csCase struct {
	src, name, dst string
	conf           int
}

func checkCSCases(t *testing.T, es []resolvedEdge, cases []csCase) {
	t.Helper()
	for _, c := range cases {
		got := edgesFrom(es, c.src, c.name)
		if len(got) == 0 {
			t.Errorf("%s -> %s: no edge", c.src, c.name)
		}
		for _, e := range got {
			if e.Dst != c.dst || e.Conf != c.conf {
				t.Errorf("%s -> %s (line %d): got %q conf %d, want %q conf %d", c.src, c.name, e.Line, e.Dst, e.Conf, c.dst, c.conf)
			}
		}
	}
}

func TestCSharpFixtureConfidence(t *testing.T) {
	r := fullBuild(t, csFixture(t), nil)
	const (
		main   = "csharp/src/App/Program.cs::Fixture.App.Program.Main"
		inv    = "csharp/src/Billing/Invoice.cs::Fixture.Billing.Invoice"
		tax    = "csharp/src/Billing/Invoice.Tax.cs::Fixture.Billing.Invoice"
		charge = "csharp/src/Billing/CardPayment.cs::Fixture.Billing.CardPayment.Charge"
		tests  = "csharp/tests/InvoiceTests.cs::Fixture.Tests.InvoiceTests."
	)
	checkCSCases(t, r.edges(), []csCase{
		{main, "Invoice", tax, 90},
		{main, "AddLine", inv + ".AddLine", 80},
		{main, "Total", inv + ".Total", 80},
		{main, "Charge", "csharp/src/Billing/IPayment.cs::Fixture.Billing.IPayment.Charge", 80},
		{main, "CardPayment", "csharp/src/Billing/CardPayment.cs::Fixture.Billing.CardPayment", 90},
		{main, "Pay", "", 0}, // alias type reference: no definition named Pay
		{main, "WriteLine", "", 0},
		{inv + ".Total", "ApplyTax", tax + ".ApplyTax", 90},
		{inv + ".Total", "Sum", "", 0},
		{charge, "WithFee", "csharp/src/Billing/PaymentBase.cs::Fixture.Billing.PaymentBase.WithFee", 90},
		{tax + ".ApplyTax", "Round", "", 0},
		{tests + "TotalIncludesTax", "Invoice", tax, 90}, // same project: T2
		{tests + "CardPaymentAddsFee", "Charge", charge, 80},
		{tests + "CardPaymentAddsFee", "Equal", "", 0},
	})
}

// TestCSharpLookup covers namespaces, usings, aliases, nested types,
// using static, global usings and this/base receivers.
func TestCSharpLookup(t *testing.T) {
	files := map[string]string{
		"App.csproj":       "<Project/>",
		"Models/A/Item.cs": `namespace Shop.A { public class Item { public int Count; public void Ship() {} public static Item Make() => new Item(); } }`,
		"Models/B/Item.cs": `namespace Shop.B { public class Item { public void Ship() {} public class Part { public void Fit() {} } } }`,
		"Util/MathX.cs": `namespace Shop.Util;
public static class MathX { public static int Twice(int x) => x * 2; }
public class Holder { public Shop.A.Item Held { get; set; } public Shop.A.Item Get() => Held; }`,
		"Util/Globals.cs": "global using Shop.B;\n",
		"Use/Use.cs": `using static Shop.Util.MathX;
using AItem = Shop.A.Item;
using Shop.Util;

namespace Shop.Use
{
    public class Base { public virtual void Hook() {} }

    public class User : Base
    {
        private Holder holder;

        public override void Hook()
        {
            base.Hook();
            Twice(2);
            var a = new AItem();
            a.Ship();
            Item b = null;
            b.Ship();
            Shop.A.Item c = AItem.Make();
            c.Ship();
            Item.Part p = new Item.Part();
            p.Fit();
            holder.Get().Ship();
            this.holder.Held.Ship();
            var n = a.Count;
        }
    }
}`,
	}
	r := fullBuild(t, files, nil)
	const hook = "Use/Use.cs::Shop.Use.User.Hook"
	es := r.edges()
	checkCSCases(t, es, []csCase{
		{hook, "Twice", "Util/MathX.cs::Shop.Util.MathX.Twice", 80},
		{hook, "Fit", "Models/B/Item.cs::Shop.B.Item.Part.Fit", 80},
		{hook, "Make", "Models/A/Item.cs::Shop.A.Item.Make", 80},
		{hook, "Get", "Util/MathX.cs::Shop.Util.Holder.Get", 80},
		{hook, "Held", "Util/MathX.cs::Shop.Util.Holder.Held", 80},
		{hook, "Count", "Models/A/Item.cs::Shop.A.Item.Count", 80},
		{"Use/Use.cs::Shop.Use.User", "Base", "Use/Use.cs::Shop.Use.Base", 90},
	})
	// Ship: a (AItem alias) and c (fully qualified) and holder results are
	// Shop.A.Item; b is Shop.B.Item through the global using.
	want := map[uint32]string{
		18: "Models/A/Item.cs::Shop.A.Item.Ship",
		20: "Models/B/Item.cs::Shop.B.Item.Ship",
		22: "Models/A/Item.cs::Shop.A.Item.Ship",
		25: "Models/A/Item.cs::Shop.A.Item.Ship",
		26: "Models/A/Item.cs::Shop.A.Item.Ship",
	}
	for _, e := range edgesFrom(es, hook, "Ship") {
		if w, ok := want[e.Line]; !ok || e.Dst != w || e.Conf != 80 {
			t.Errorf("Ship at line %d -> %q conf %d, want %q", e.Line, e.Dst, e.Conf, w)
		}
		delete(want, e.Line)
	}
	if len(want) > 0 {
		t.Errorf("missing Ship edges: %v", want)
	}
	for _, e := range edgesFrom(es, hook, "Hook") {
		if e.Dst != "Use/Use.cs::Shop.Use.Base.Hook" || e.Conf != 80 {
			t.Errorf("base.Hook -> %q conf %d", e.Dst, e.Conf)
		}
	}
}

// TestCSharpPartialIncremental adds and removes partial parts one by one and
// compares every step with a from-scratch build.
func TestCSharpPartialIncremental(t *testing.T) {
	files := csFixture(t)
	const (
		taxPath  = "csharp/src/Billing/Invoice.Tax.cs"
		invPath  = "csharp/src/Billing/Invoice.cs"
		mainPath = "csharp/src/App/Program.cs"
		extra    = "csharp/src/Billing/Invoice.Audit.cs"
	)
	partial := maps.Clone(files)
	delete(partial, taxPath)
	inc := newTestRepo(t, partial)
	inc.index()
	inc.resolveAll(Options{})

	steps := []struct {
		name  string
		apply func(r *testRepo) []string
		del   []string
	}{
		{name: "add Invoice.Tax.cs", apply: func(r *testRepo) []string {
			r.files[taxPath] = files[taxPath]
			return []string{taxPath}
		}},
		{name: "third part sorts first", apply: func(r *testRepo) []string {
			r.files[extra] = "namespace Fixture.Billing { public partial class Invoice { public void Audit() { Total(); } } }\n"
			return []string{extra}
		}},
		{name: "rename ApplyTax", apply: func(r *testRepo) []string {
			r.files[taxPath] = strings.ReplaceAll(r.files[taxPath], "ApplyTax", "WithTax")
			r.files[invPath] = strings.ReplaceAll(r.files[invPath], "ApplyTax", "WithTax")
			return []string{taxPath, invPath}
		}},
		{name: "delete Invoice.Audit.cs", del: []string{extra}},
		{name: "main uses alias for Invoice", apply: func(r *testRepo) []string {
			r.files[mainPath] = strings.Replace(r.files[mainPath], "using Pay", "using Inv = Fixture.Billing.Invoice;\nusing Pay", 1)
			r.files[mainPath] = strings.Replace(r.files[mainPath], "var invoice = new Invoice();", "Inv invoice = new Inv();", 1)
			return []string{mainPath}
		}},
		{name: "delete Invoice.cs", del: []string{invPath}},
	}
	for _, step := range steps {
		if step.apply != nil {
			inc.index(step.apply(inc)...)
		}
		if len(step.del) > 0 {
			inc.remove(step.del...)
		}
		inc.resolvePending(Options{BatchNames: 5})
		full := fullBuild(t, inc.files, nil)
		if got, want := inc.dump(), full.dump(); got != want {
			t.Fatalf("step %q: incremental != full:\n%s", step.name, diffLines(want, got))
		}
	}
	// After the alias step and deleting Invoice.cs, Main still reaches Total
	// through the remaining parts... which no longer declare it.
	for _, e := range edgesFrom(inc.edges(), "csharp/src/App/Program.cs::Fixture.App.Program.Main", "Total") {
		if e.Dst != "" {
			t.Errorf("Total resolved to deleted part: %q", e.Dst)
		}
	}
}

// TestCSharpMalformedSources must never panic: broken files are indexed
// best-effort and resolved like any other.
func TestCSharpMalformedSources(t *testing.T) {
	files := map[string]string{
		"a.cs": "namespace A { class X : { void M( { this. } ",
		"b.cs": "using static ; using = ; class Y { void N() { new (). ; base.; X x = ; x.M(); } }",
		"c.cs": "partial class X { void M() { (((M)))(); typeof(X).M(); } } partial class X : X {}",
		"d.cs": "\x00\xff namespace { record struct ( : ; }",
	}
	r := fullBuild(t, files, map[string]model.FileStatus{})
	_ = r.edges()
}

// TestCSharpLocalScopes: a local declared in a finished sibling block does
// not shadow a field; parameters and lambda parameters are locals.
func TestCSharpLocalScopes(t *testing.T) {
	files := map[string]string{
		"P.csproj": "<Project/>",
		"T.cs": `namespace N
{
    public class A { public void Go() {} }
    public class B { public void Go() {} }
    public class C
    {
        private A x;
        public void M(B p)
        {
            { var x = new B(); }
            x.Go();
            p.Go();
            System.Action<A> f = (A q) => q.Go();
        }
    }
}`,
	}
	r := fullBuild(t, files, nil)
	want := map[uint32]string{11: "T.cs::N.A.Go", 12: "T.cs::N.B.Go", 13: "T.cs::N.A.Go"}
	for _, e := range edgesFrom(r.edges(), "T.cs::N.C.M", "Go") {
		if e.Dst != want[e.Line] || e.Conf != 80 {
			t.Errorf("Go at line %d -> %q conf %d, want %q", e.Line, e.Dst, e.Conf, want[e.Line])
		}
		delete(want, e.Line)
	}
	if len(want) > 0 {
		t.Errorf("missing: %v", want)
	}
}
