package parse

import (
	"context"
	"strings"
	"testing"

	"github.com/pragmataW/graphmaster/internal/model"
)

func parsePHP(t testing.TB, src string) *model.Facts {
	t.Helper()
	e := NewEngine()
	defer e.Close()
	res, err := e.ParseLang(context.Background(), model.LangPHP, []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if err := res.Facts.Validate(); err != nil {
		t.Fatalf("invalid facts: %v", err)
	}
	return res.Facts
}

func TestPHPRegistered(t *testing.T) {
	spec, ok := ForPath("src/Model/Product.php")
	if !ok || spec.Lang() != model.LangPHP {
		t.Fatalf("ForPath(.php) = %v, %v", spec, ok)
	}
	if !IsTestPath("tests/CartTest.php") || IsTestPath("src/Cart.php") || IsTestPath("src/Testing.php") {
		t.Error("IsTestPath mismatch")
	}
}

func TestPHPNameResolution(t *testing.T) {
	f := parsePHP(t, `<?php
namespace App\Shop;

use App\Model\{Product, Discount as Promo};
use function App\Util\money;
use Vendor\Lib;

class Cart extends Base implements \Countable, Lib\Sized {
    use Promo;
    public function add(Product $p): Promo {
        money(1);
        \App\Util\remote();
        Sub\helper();
        namespace\local();
        unqualified();
        strlen('x');
        Promo::make();
        Lib\Factory::create();
        parent::add($p);
        return new Promo();
    }
}
`)
	type ref struct{ kind, qual, recv, name string }
	var got []ref
	for _, r := range f.Refs {
		got = append(got, ref{r.Kind.String(), r.Qualifier, r.Receiver, r.Name})
	}
	want := []ref{
		{"type", `\App\Model`, "", "Product"},
		{"type", `\App\Model`, "", "Discount"},
		{"call", `\App\Util`, "", "money"},
		{"call", `\App\Util`, "", "remote"},
		{"call", `\App\Shop\Sub`, "", "helper"},
		{"call", `\App\Shop`, "", "local"},
		{"call", "", "", "unqualified"},
		{"type", `\App\Model`, "", "Discount"},
		{"call", `\App\Model\Discount`, "", "make"},
		{"type", `\Vendor\Lib`, "", "Factory"},
		{"call", `\Vendor\Lib\Factory`, "", "create"},
		{"call", "", "parent", "add"},
		{"type", `\App\Model`, "", "Discount"},
	}
	if len(got) != len(want) {
		t.Fatalf("refs = %+v\nwant %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("ref %d = %+v, want %+v", i, got[i], want[i])
		}
	}

	type inh struct{ kind, qual, name string }
	var inhs []inh
	for _, in := range f.Inherits {
		inhs = append(inhs, inh{in.Kind.String(), in.Qualifier, in.Name})
	}
	wantInh := []inh{
		{"extends", `\App\Shop`, "Base"},
		{"implements", `\`, "Countable"},
		{"implements", `\Vendor\Lib`, "Sized"},
		{"embeds", `\App\Model`, "Discount"},
	}
	if len(inhs) != len(wantInh) {
		t.Fatalf("inherits = %+v", inhs)
	}
	for i := range wantInh {
		if inhs[i] != wantInh[i] {
			t.Errorf("inherit %d = %+v, want %+v", i, inhs[i], wantInh[i])
		}
	}

	var specs []string
	for _, im := range f.Imports {
		specs = append(specs, im.Spec+"|"+im.Alias)
	}
	if got, want := strings.Join(specs, " "), `App\Model\Product| App\Model\Discount|Promo App\Util\money| Vendor\Lib|`; got != want {
		t.Errorf("imports = %s, want %s", got, want)
	}
}

func TestPHPIncludeSpecs(t *testing.T) {
	var s phpSpec
	cases := map[string]string{
		`'src/a.php'`:                          "src/a.php",
		`"src/a.php"`:                          "src/a.php",
		`('lib/b.php')`:                        "lib/b.php",
		`__DIR__ . '/vendor/autoload.php'`:     "./vendor/autoload.php",
		`dirname(__FILE__) . "/c.php"`:         "./c.php",
		`dirname(__DIR__) . '/d.php'`:          "../d.php",
		`dirname(__DIR__, 2) . '/e.php'`:       "../../e.php",
		`(__DIR__ . '/f.inc')`:                 "./f.inc",
		`$dir . '/x.php'`:                      "",
		`"$dir/x.php"`:                         "",
		`FOO . '/x.php'`:                       "",
		`'a' . 'b.php'`:                        "",
		`__DIR__ . '/'`:                        "",
		`App\Model\Product`:                    `App\Model\Product`,
		`function App\format_money`:            `function App\format_money`,
		"Discount  as\n Promo":                 "Discount as Promo",
		`dirname(__DIR__, 99) . '/x.php'`:      "",
		`dirname(dirname(__DIR__)) . '/x.php'`: "../../x.php",
	}
	for raw, want := range cases {
		if got := s.importSpec(raw); got != want {
			t.Errorf("importSpec(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestPHPUseParsing(t *testing.T) {
	cases := []struct{ clause, decl, kind, fqn, alias string }{
		{`App\Model\Product`, "", "", `App\Model\Product`, ""},
		{`\App\Model\Product as P`, "", "", `App\Model\Product`, "P"},
		{`function App\money`, "", "function", `App\money`, ""},
		{`Discount as Promo`, `use App\Model\{Product, Discount as Promo};`, "", `App\Model\Discount`, "Promo"},
		{`slug`, `use function App\Util\{slug, trim};`, "function", `App\Util\slug`, ""},
		{`const X`, `use App\{const X, Y};`, "const", `App\X`, ""},
		{`a b c d`, "", "", "", ""},
	}
	for _, c := range cases {
		k, fqn, alias := parsePHPUse(c.clause, c.decl)
		if k != c.kind || fqn != c.fqn || alias != c.alias {
			t.Errorf("parsePHPUse(%q, %q) = %q %q %q", c.clause, c.decl, k, fqn, alias)
		}
	}
}

func TestPHPNameScope(t *testing.T) {
	var s PHPNameScope
	s.Namespace = `App`
	s.AddUse("", `Lib\Thing`, "")
	s.AddUse("", `\Lib\Other`, "O")
	s.AddUse("function", `Lib\fn`, "")
	cls := map[string]string{
		"Thing": `Lib\Thing`, "thing": `Lib\Thing`, "O": `Lib\Other`, `O\Inner`: `Lib\Other\Inner`,
		"Local": `App\Local`, `\Abs\X`: `Abs\X`, `namespace\Y`: `App\Y`, `Sub\Z`: `App\Sub\Z`,
	}
	for in, want := range cls {
		if got, ok := s.Class(in); !ok || got != want {
			t.Errorf("Class(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, special := range []string{"self", "STATIC", "parent", ""} {
		if _, ok := s.Class(special); ok {
			t.Errorf("Class(%q) resolved", special)
		}
	}
	if got, ok := s.Function("fn"); !ok || got != `Lib\fn` {
		t.Errorf("Function(fn) = %q %v", got, ok)
	}
	if _, ok := s.Function("other"); ok {
		t.Error("unqualified function without import must stay unresolved")
	}
	if q, n := PHPSplitFQN(`\A\B\C`); q != `\A\B` || n != "C" {
		t.Errorf("PHPSplitFQN = %q %q", q, n)
	}
	if q, n := PHPSplitFQN(`C`); q != `\` || n != "C" {
		t.Errorf("PHPSplitFQN global = %q %q", q, n)
	}
	if m := PHPNamespaceModule(`\App\Model`); m != "App.Model" {
		t.Errorf("PHPNamespaceModule = %q", m)
	}
}

func TestPHPModuleOfAndResolveImport(t *testing.T) {
	composer := `{"autoload": {"psr-4": {"App\\": "src/", "Multi\\": ["a/", "b/"]}, "psr-0": {"Old_": "legacy/"}},
		"autoload-dev": {"psr-4": {"Tests\\": "tests/"}}}`
	rc := model.NewMemRepo(map[string]string{
		"php/composer.json":            composer,
		"php/bootstrap.php":            "<?php\nrequire 'src/helpers.php';\n",
		"php/src/helpers.php":          "<?php\n\ndeclare(strict_types=1);\n\nnamespace App;\n",
		"php/src/Model/Product.php":    "<?php\nnamespace App\\Model;\nclass Product {}\n",
		"php/src/Model/Nons.php":       "<?php\nclass Nons {}\n",
		"php/b/Thing.php":              "<?php namespace Multi;",
		"php/tests/CartTest.php":       "<?php\nnamespace Tests;\n",
		"php/vendor/autoload.php":      "<?php\n",
		"php/lib/util.php":             "<?php\n",
		"other/x.php":                  "<?php\nnamespace Braced {\n}\n",
		"other/composer.json":          `not json`,
		"nocomposer/Foo/Bar.php":       "<?php\n",
		"php/src/Service/Cart.php":     "<?php\nnamespace App\\Service;\n",
		"php/src/Service/Cart.php.bak": "",
	})
	var s phpSpec

	mods := map[string]string{
		"php/src/helpers.php":       "App",
		"php/src/Model/Product.php": "App.Model",
		"php/src/Model/Nons.php":    "",
		"php/bootstrap.php":         "",
		"php/tests/CartTest.php":    "Tests",
		"other/x.php":               "Braced",
		// Unreadable files fall back to PSR-4 by directory.
		"php/src/Gone/Missing.php": "App.Gone",
		"php/tests/Unit/X.php":     "Tests.Unit",
	}
	for p, want := range mods {
		if got := s.ModuleOf(p, nil, rc); got != want {
			t.Errorf("ModuleOf(%s) = %q, want %q", p, got, want)
		}
	}
	if got := s.ModuleOf("x.php", &model.Facts{Package: `A\B`}, rc); got != "A.B" {
		t.Errorf("ModuleOf(facts) = %q", got)
	}

	cases := []struct {
		spec, from string
		want       model.ImportTarget
	}{
		{`App\Model\Product`, "php/src/Service/Cart.php", model.ImportTarget{Kind: model.TargetFile, Path: "php/src/Model/Product.php", Module: "App.Model"}},
		{`App\Model`, "php/src/Service/Cart.php", model.ImportTarget{Kind: model.TargetModule, Path: "php/src/Model", Module: "App.Model"}},
		{`App\format_money`, "php/src/Service/Cart.php", model.ImportTarget{Kind: model.TargetModule, Path: "php/src", Module: "App"}},
		{`App\Missing\Thing`, "php/src/Service/Cart.php", model.ImportTarget{Kind: model.TargetUnresolved, Module: "App.Missing.Thing"}},
		{`Multi\Thing`, "php/bootstrap.php", model.ImportTarget{Kind: model.TargetFile, Path: "php/b/Thing.php", Module: "Multi"}},
		{`PHPUnit\Framework\TestCase`, "php/tests/CartTest.php", model.ImportTarget{Kind: model.TargetExternal, Module: `PHPUnit\Framework\TestCase`}},
		{`Foo\Bar`, "nocomposer/x.php", model.ImportTarget{Kind: model.TargetModule, Module: "Foo"}},
		{`Foo\Bar`, "other/x.php", model.ImportTarget{Kind: model.TargetModule, Module: "Foo"}},
		{"src/helpers.php", "php/bootstrap.php", model.ImportTarget{Kind: model.TargetFile, Path: "php/src/helpers.php"}},
		{"lib/util.php", "php/src/Model/Product.php", model.ImportTarget{Kind: model.TargetFile, Path: "php/lib/util.php"}},
		{"./vendor/autoload.php", "php/bootstrap.php", model.ImportTarget{Kind: model.TargetExternal, Module: "./vendor/autoload.php"}},
		{"../helpers.php", "php/src/Model/Product.php", model.ImportTarget{Kind: model.TargetFile, Path: "php/src/helpers.php"}},
		{"./missing.php", "php/bootstrap.php", model.ImportTarget{Kind: model.TargetUnresolved, Module: "./missing.php"}},
		{"/etc/passwd.php", "php/bootstrap.php", model.ImportTarget{Kind: model.TargetExternal, Module: "/etc/passwd.php"}},
		{"../../../../x.php", "php/bootstrap.php", model.ImportTarget{Kind: model.TargetUnresolved, Module: "../../../../x.php"}},
		{"", "php/bootstrap.php", model.ImportTarget{Kind: model.TargetUnresolved}},
	}
	for _, c := range cases {
		if got := s.ResolveImport(c.spec, c.from, rc); got != c.want {
			t.Errorf("ResolveImport(%q, %s) = %+v, want %+v", c.spec, c.from, got, c.want)
		}
	}
	if got := s.ResolveImport(`A\B`, "x.php", nil); got.Kind != model.TargetModule || got.Module != "A" {
		t.Errorf("ResolveImport without ctx = %+v", got)
	}
}

func TestPHPSniffNamespace(t *testing.T) {
	cases := map[string]string{
		"<?php namespace A\\B;":                `A\B`,
		"<?php\n// namespace X;\nnamespace Y;": "Y",
		"<?php\nnamespace\\foo();\n":           "",
		"<?php\nnamespace {\n}":                "",
		"<?php\nnamespace  Z\\W {\n}":          `Z\W`,
		"":                                     "",
	}
	for src, want := range cases {
		if got := sniffPHPNamespace([]byte(src)); got != want {
			t.Errorf("sniff(%q) = %q, want %q", src, got, want)
		}
	}
}

func TestPHPGarbageInput(t *testing.T) {
	inputs := []string{
		"", "<?php", "<?php namespace", "<?php use", "<?php use A\\{", "<?php class extends {",
		"<?php function (", "<?php $a->", "<?php A::", "<?php new", "<?php require", "\x00\xff<?php ???",
		"<?php namespace A { namespace B { class C extends \\ {} } }",
		"<?php use function ;\nuse const \\;\nclass X { use ; }",
		strings.Repeat("<?php class A { function f() { $this->", 50),
	}
	e := NewEngine()
	defer e.Close()
	for _, in := range inputs {
		res, err := e.ParseLang(context.Background(), model.LangPHP, []byte(in))
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if err := res.Facts.Validate(); err != nil {
			t.Errorf("%q: invalid facts: %v", in, err)
		}
	}
}

// FuzzParsePHP checks that arbitrary input never panics the extractor and
// always yields structurally valid facts that survive the codec.
func FuzzParsePHP(f *testing.F) {
	f.Add([]byte("<?php namespace A; use B\\{C, D as E}; class F extends C { use E; function g(C $c) { $c->h(); parent::i(); \\J\\k(); } }"))
	f.Add([]byte("<?php require __DIR__ . '/x.php'; include dirname(__DIR__, 2) . \"/y.php\";"))
	f.Add([]byte("<html><?php namespace { function a() {} } ?>"))
	e := NewEngine()
	f.Cleanup(e.Close)
	var s phpSpec
	rc := model.NewMemRepo(map[string]string{"composer.json": `{"autoload":{"psr-4":{"A\\":""}}}`})
	f.Fuzz(func(t *testing.T, src []byte) {
		res, err := e.ParseLang(context.Background(), model.LangPHP, src)
		if err != nil {
			t.Fatal(err)
		}
		if err := res.Facts.Validate(); err != nil {
			t.Fatalf("invalid facts: %v\n%s", err, DumpFacts(res.Facts))
		}
		if _, err := DecodeFacts(EncodeFacts(res.Facts)); err != nil {
			t.Fatal(err)
		}
		for _, im := range res.Facts.Imports {
			_ = s.ResolveImport(im.Spec, "x/y.php", rc)
		}
		_ = s.ModuleOf("x/y.php", nil, model.NewMemRepo(map[string]string{"x/y.php": string(src)}))
	})
}
