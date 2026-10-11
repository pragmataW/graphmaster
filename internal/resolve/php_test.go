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

// phpFixture returns the PHP part of the materialized fixture without
// vendor/ (excluded by the default config).
func phpFixture(t *testing.T) map[string]string {
	t.Helper()
	root := testutil.Materialize(t)
	files := map[string]string{}
	err := filepath.WalkDir(filepath.Join(root, "php"), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel == "php/vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		files[rel] = string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := files["php/composer.json"]; !ok {
		t.Fatal("php/composer.json missing from materialized fixture")
	}
	return files
}

func TestPHPFixtureExpectedEdges(t *testing.T) {
	r := fullBuild(t, phpFixture(t), nil)
	exps := loadExpectations(t, "php")
	if len(exps) < 15 {
		t.Fatalf("only %d PHP expectations loaded", len(exps))
	}
	checkExpectations(t, r, exps)
}

// TestPHPFixtureConfidence pins exact targets and tiers per reference.
func TestPHPFixtureConfidence(t *testing.T) {
	r := fullBuild(t, phpFixture(t), nil)
	es := r.edges()
	const (
		cart    = "php/src/Service/Cart.php::"
		product = "php/src/Model/Product.php::"
		test    = "php/tests/CartTest.php::CartTest."
	)
	cases := []struct {
		src, name, dst string
		conf           int
	}{
		{"php/src/Model/DigitalProduct.php::DigitalProduct.price", "price", product + "Product.price", 80},
		{cart + "Cart.total", "applyCoupon", cart + "Cart.applyCoupon", 80},
		{cart + "Cart.total", "items", cart + "Cart.items", 80},
		{cart + "Cart.summary", "total", cart + "Cart.total", 80},
		{cart + "Cart.summary", "format_money", "php/src/helpers.php::format_money", 100},
		{cart + "Cart.add", "Product", product + "Product", 100},
		{cart + "Cart.add", "items", cart + "Cart.items", 80},
		{product + "Product.price", "cents", "", 0}, // promoted property: no def
		{test + "testTotalSumsPrices", "Cart", cart + "Cart", 100},
		{test + "testTotalSumsPrices", "Product", product + "Product", 100},
		{test + "testTotalSumsPrices", "add", cart + "Cart.add", 80},
		{test + "testTotalSumsPrices", "total", cart + "Cart.total", 80},
		{test + "testTotalSumsPrices", "assertSame", "", 0}, // PHPUnit TestCase
		{test + "testSummaryFormatsMoney", "summary", cart + "Cart.summary", 80},
	}
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
	// $item->price() on an untyped foreach value fans out (T5).
	price := edgesFrom(es, cart+"Cart.total", "price")
	if len(price) != 3 {
		t.Errorf("Cart.total -> price: %d edges, want 3 (T5): %v", len(price), price)
	}
	for _, e := range price {
		if e.Conf != 10 {
			t.Errorf("Cart.total -> price %s conf %d, want 10", e.Dst, e.Conf)
		}
	}
}

// TestPHPReceiverTyping covers the T3 sources and their limits.
func TestPHPReceiverTyping(t *testing.T) {
	files := map[string]string{
		"composer.json": `{"autoload": {"psr-4": {"App\\": "src/"}}}`,
		"src/Model/Item.php": `<?php
namespace App\Model;

interface Named { public function name(): string; }

trait Stamps { public function stamp(): void {} }

class Item implements Named {
    use Stamps;
    public function name(): string { return ''; }
    public function price(): int { return 1; }
    public static function make(): static { return new static(); }
}

class Special extends Item {
    public function price(): int { return parent::price() + self::bonus() + static::bonus(); }
    private static function bonus(): int { return 1; }
}
`,
		"src/Shop/Basket.php": `<?php
namespace App\Shop;

use App\Model\Item;
use App\Model\Special as Fancy;
use Vendor\Thing;

class Basket {
    private ?Item $first = null;

    public function __construct(private Fancy $fancy, protected Thing $thing) {}

    public function run(Item $a, ?Fancy $b, int $n, $untyped, Item|Fancy $u): void {
        $a->price();
        $b?->price();
        $n->price();
        $untyped->price();
        $u->name();
        $c = new Item();
        $c->stamp();
        $c = $this->other();
        $c->price();
        /** @var Fancy $d */
        $d = load();
        $d->price();
        $this->first->name();
        $this->fancy->price();
        $this->thing->price();
        $this->missing->price();
        foreach ([] as $e) { $e->price(); }
        try {} catch (Fancy $f) { $f->price(); }
        $fn = function (Item $g) { return $g->name(); };
        $this->other()->price();
        Item::make();
        Fancy::make();
        $h = new Fancy;
        $h
            ->price();
    }

    private function other(): Item { return new Item(); }
}
`,
	}
	r := fullBuild(t, files, nil)
	es := r.edges()
	const (
		run  = "src/Shop/Basket.php::Basket.run"
		item = "src/Model/Item.php::"
	)
	type want = phpEdge
	// Edges named name from run, by line.
	byLine := func(name string) map[uint32][]want {
		m := map[uint32][]want{}
		for _, e := range edgesFrom(es, run, name) {
			m[e.Line] = append(m[e.Line], want{e.Dst, e.Conf})
		}
		return m
	}
	price, nameE := byLine("price"), byLine("name")
	check := func(what string, got []want, exp ...want) {
		t.Helper()
		if len(got) != len(exp) {
			t.Errorf("%s: got %v, want %v", what, got, exp)
			return
		}
		for i := range exp {
			if got[i] != exp[i] {
				t.Errorf("%s: got %v, want %v", what, got, exp)
			}
		}
	}
	itemPrice := want{item + "Item.price", 80}
	specialPrice := want{item + "Special.price", 80}
	check("typed param", price[14], itemPrice)
	check("nullable alias param", price[15], specialPrice)
	check("scalar param", price[16], want{"", 0})
	if len(price[17]) == 0 || price[17][0].conf >= 50 {
		t.Errorf("untyped param: got %v, want T5 fan-out", price[17])
	}
	check("union param", nameE[18], want{item + "Item.name", 80})
	check("new assignment + trait member", edgesFrom2(es, run, "stamp", 20), want{item + "Stamps.stamp", 80})
	if got := price[22]; len(got) == 0 || got[0].conf >= 50 {
		t.Errorf("reassigned from call: got %v, want untyped fan-out", got)
	}
	check("@var annotation", price[25], specialPrice)
	check("typed property", nameE[26], want{item + "Item.name", 80})
	check("promoted property", price[27], specialPrice)
	check("vendor-typed promoted property", price[28], want{"", 0})
	if got := price[29]; len(got) == 0 || got[0].conf >= 50 {
		t.Errorf("missing property: got %v, want untyped fan-out", got)
	}
	if got := price[30]; len(got) == 0 || got[0].conf >= 50 {
		t.Errorf("foreach value: got %v, want untyped fan-out", got)
	}
	check("catch variable", price[31], specialPrice)
	check("closure param", nameE[32], want{item + "Item.name", 80})
	check("static call via import", edgesFrom2(es, run, "make", 34), want{item + "Item.make", 90})
	check("static call via alias inherits", edgesFrom2(es, run, "make", 35), want{item + "Item.make", 90})
	check("multi-line fluent call", price[38], specialPrice)

	sp := "src/Model/Item.php::Special.price"
	check("parent::", edgesFrom2(es, sp, "price", 16), itemPrice)
	check("self:: and static::", edgesFrom2(es, sp, "bonus", 16), want{item + "Special.bonus", 80}, want{item + "Special.bonus", 80})

	// Inheritance through a use alias and a same-namespace interface.
	var inh []string
	for _, e := range es {
		if e.Kind.IsInheritance() {
			inh = append(inh, e.Kind.String()+" "+e.Src+" -> "+e.Dst)
		}
	}
	got := strings.Join(inh, "\n")
	for _, w := range []string{
		"implements src/Model/Item.php::Item -> src/Model/Item.php::Named",
		"embeds src/Model/Item.php::Item -> src/Model/Item.php::Stamps",
		"extends src/Model/Item.php::Special -> src/Model/Item.php::Item",
	} {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in\n%s", w, got)
		}
	}
}

// phpEdge is a resolved target with its confidence.
type phpEdge struct {
	dst  string
	conf int
}

// edgesFrom2 returns the targets of edges named name from src on line.
func edgesFrom2(es []resolvedEdge, src, name string, line uint32) []phpEdge {
	var out []phpEdge
	for _, e := range edgesFrom(es, src, name) {
		if e.Line == line {
			out = append(out, phpEdge{e.Dst, e.Conf})
		}
	}
	return out
}

// TestPHPNamespacesAndFunctions covers function resolution: same
// namespace (T2), imported and fully qualified (T1), global fallback (T4)
// and external namespaces.
func TestPHPNamespacesAndFunctions(t *testing.T) {
	files := map[string]string{
		"lib/a.php": "<?php\nnamespace Lib;\nfunction helper() {}\nfunction shared() {}\n",
		"lib/b.php": "<?php\nnamespace Lib;\nfunction caller() { helper(); \\Lib\\shared(); \\Other\\shared(); globalfn(); Sub\\deep(); }\n",
		"lib/c.php": "<?php\nnamespace Lib\\Sub;\nfunction deep() {}\n",
		"g.php":     "<?php\nfunction globalfn() {}\n",
		"app.php":   "<?php\nnamespace App;\nuse function Lib\\helper as h;\nuse function Lib\\shared;\nfunction main() { shared(); h(); \\globalfn(); }\n",
	}
	r := fullBuild(t, files, nil)
	es := r.edges()
	cases := []struct {
		src, name, dst string
		conf           int
	}{
		{"lib/b.php::caller", "helper", "lib/a.php::helper", 90},
		{"lib/b.php::caller", "globalfn", "g.php::globalfn", 50},
		{"lib/b.php::caller", "deep", "lib/c.php::deep", 100},
		{"app.php::main", "helper", "lib/a.php::helper", 100},
		{"app.php::main", "globalfn", "g.php::globalfn", 100},
	}
	for _, c := range cases {
		got := edgesFrom(es, c.src, c.name)
		if len(got) != 1 || got[0].Dst != c.dst || got[0].Conf != c.conf {
			t.Errorf("%s -> %s: got %v, want %s conf %d", c.src, c.name, got, c.dst, c.conf)
		}
	}
	// \Lib\shared resolves, \Other\shared stays external; both named shared.
	var shared []string
	for _, e := range edgesFrom(es, "lib/b.php::caller", "shared") {
		shared = append(shared, e.Dst)
	}
	if strings.Join(shared, ",") != "lib/a.php::shared," && strings.Join(shared, ",") != ",lib/a.php::shared" {
		t.Errorf("caller -> shared = %q", shared)
	}
	if got := edgesFrom(es, "app.php::main", "shared"); len(got) != 1 || got[0].Dst != "lib/a.php::shared" || got[0].Conf != 100 {
		t.Errorf("main -> shared = %v", got)
	}
}

// TestPHPIncrementalEquivalence edits the fixture step by step and compares
// pending-only resolution with a full build.
func TestPHPIncrementalEquivalence(t *testing.T) {
	files := phpFixture(t)
	const (
		productPath = "php/src/Model/Product.php"
		cartPath    = "php/src/Service/Cart.php"
		helpersPath = "php/src/helpers.php"
	)
	partial := maps.Clone(files)
	delete(partial, productPath)
	inc := newTestRepo(t, partial)
	inc.index()
	inc.resolveAll(Options{})

	steps := []struct {
		name  string
		apply func(r *testRepo) []string
		del   []string
	}{
		{name: "add Product.php", apply: func(r *testRepo) []string {
			r.files[productPath] = files[productPath]
			return []string{productPath}
		}},
		{name: "rename applyCoupon", apply: func(r *testRepo) []string {
			r.files[cartPath] = strings.ReplaceAll(r.files[cartPath], "applyCoupon", "discount")
			return []string{cartPath}
		}},
		{name: "move format_money to another namespace", apply: func(r *testRepo) []string {
			r.files[helpersPath] = strings.Replace(r.files[helpersPath], "namespace App;", "namespace App\\Util;", 1)
			return []string{helpersPath}
		}},
		{name: "delete Product.php", del: []string{productPath}},
	}
	for _, step := range steps {
		if step.apply != nil {
			inc.index(step.apply(inc)...)
		}
		if len(step.del) > 0 {
			inc.remove(step.del...)
		}
		inc.resolvePending(Options{BatchNames: 5})
		full := fullBuild(t, inc.files, map[string]model.FileStatus{})
		if got, want := inc.dump(), full.dump(); got != want {
			t.Fatalf("step %q: incremental != full:\n%s", step.name, diffLines(want, got))
		}
	}
}

// TestPHPReceiverGarbage feeds malformed sources and positions to the
// receiver scanner; it must never panic.
func TestPHPReceiverGarbage(t *testing.T) {
	files := map[string]string{
		"a.php": "<?php\nclass A { function f() { $x->; ->g(); ::h(); $ = new; $y = new class {}; $y->i(); } }\n",
		"b.php": "<?php\nfunction k($z) { $z\n->\n m(); (new A)->f(); A::$p->q(); $$v->w(); }\n",
		"c.php": "\x00\xff<?php $a->b(); class { function ( { $this->",
	}
	r := fullBuild(t, files, nil)
	_ = r.edges()
	for _, body := range []string{"", "$", "$x = ", "$x =", "@var", "(", "new"} {
		_, _ = phpVariableType([]byte(body), "x")
	}
	if got := phpNewType([]byte("new \\A\\B(1)")); got != `\A\B` {
		t.Errorf("phpNewType = %q", got)
	}
	if got := phpDeclaredType("public function __construct(private readonly ?A $a)", "a"); got != "?A" {
		t.Errorf("phpDeclaredType = %q", got)
	}
	if got := phpDeclaredType("var $name;", "name"); got != "" {
		t.Errorf("phpDeclaredType(var) = %q", got)
	}
}
