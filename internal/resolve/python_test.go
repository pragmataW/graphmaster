package resolve

import (
	"context"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pragmataW/graphmaster/internal/model"
	"github.com/pragmataW/graphmaster/internal/store"
	"github.com/pragmataW/graphmaster/internal/testutil"
)

// pythonFixture returns the Python part of the materialized fixture
// (tracked and untracked, without __pycache__) and the indexer statuses.
func pythonFixture(t *testing.T) (map[string]string, map[string]model.FileStatus) {
	t.Helper()
	root := testutil.Materialize(t)
	files := map[string]string{}
	err := filepath.WalkDir(filepath.Join(root, "python"), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "__pycache__" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := files["python/shop/experimental.py"]; !ok {
		t.Fatal("untracked experimental.py missing from materialized fixture")
	}
	return files, map[string]model.FileStatus{testutil.BigFilePath: model.StatusTooBig}
}

// pythonBuild is fullBuild plus the tests edges the indexer derives from
// test marks (the shared test indexer writes refs, inherits and imports
// only).
func pythonBuild(t *testing.T, files map[string]string, status map[string]model.FileStatus) *testRepo {
	t.Helper()
	r := newTestRepo(t, files)
	maps.Copy(r.status, status)
	r.index()
	addTestEdges(t, r)
	r.resolveAll(Options{})
	return r
}

func addTestEdges(t *testing.T, r *testRepo) {
	t.Helper()
	ctx := context.Background()
	var edges []*store.Edge
	for _, p := range r.codePaths() {
		if model.LangFromPath(p) != model.LangPython || !r.status[p].Indexed() {
			continue
		}
		res, err := r.eng.ParseLang(ctx, model.LangPython, []byte(r.files[p]))
		if err != nil {
			t.Fatal(err)
		}
		for _, tm := range res.Facts.Tests {
			d := res.Facts.Defs[tm.Def]
			e := &store.Edge{Kind: model.EdgeTests, Name: tm.Target, Line: d.StartLine}
			err := r.st.DB().QueryRow(`SELECT n.file_id, n.id FROM nodes n JOIN files f ON f.id = n.file_id
				WHERE f.path = ? AND n.start_byte = ? AND n.kind = ?`, p, d.StartByte, int(d.Kind)).Scan(&e.FileID, &e.Src)
			if err != nil {
				t.Fatalf("test node %s %s: %v", p, d.Name, err)
			}
			edges = append(edges, e)
		}
	}
	w, err := r.st.NewWriter(ctx, store.WriterOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range edges {
		if _, err := w.InsertEdge(e); err != nil {
			t.Fatal(err)
		}
		if err := w.EnqueuePending(e.NameID); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPythonFixtureExpectedEdges(t *testing.T) {
	files, status := pythonFixture(t)
	r := pythonBuild(t, files, status)
	exps := loadExpectations(t, "python")
	if len(exps) < 20 {
		t.Fatalf("only %d Python expectations loaded", len(exps))
	}
	checkExpectations(t, r, exps)
}

// TestPythonFixtureConfidence pins exact targets and tiers for
// representative references (every site of the name from src).
func TestPythonFixtureConfidence(t *testing.T) {
	files, status := pythonFixture(t)
	r := pythonBuild(t, files, status)
	es := r.edges()

	const (
		models  = "python/shop/models.py::"
		repo    = "python/shop/repository.py::"
		orders  = "python/shop/services/orders.py::"
		pricing = "python/shop/services/pricing.py::"
		tests   = "python/tests/test_orders.py::"
		place   = orders + "OrderService.place_order"
	)
	cases := []struct {
		src, name, dst string
		conf           int
	}{
		{place, "repo", "", 0}, // self.repo: instance attribute, no def
		{place, "get", repo + "Repository.get", 80},
		{place, "_can_fulfil", orders + "OrderService._can_fulfil", 80},
		{place, "OutOfStock", orders + "OutOfStock", 90},
		{place, "quantity", models + "Item.quantity", 80},
		{place, "price", models + "Item.price", 80},
		{place, "apply_discount", pricing + "apply_discount", 100},
		{orders + "OrderService.__init__", "Repository", repo + "Repository", 100},
		{orders + "OrderService._can_fulfil", "Item", models + "Item", 100},
		{models + "Item.total", "price", models + "Item.price", 80},
		{models + "Item.describe", "sku", models + "Item.sku", 80},
		{models + "PerishableItem.total", "total", models + "Item.total", 80},
		{models + "PerishableItem.total", "days_left", models + "PerishableItem.days_left", 80},
		{repo + "Repository.__init__", "Item", models + "Item", 100},
		{repo + "Repository.add", "sku", models + "Item.sku", 80},
		{repo + "Repository.get", "get", "", 0}, // dict.get
		{pricing + "apply_discount", "round_cents", pricing + "round_cents", 90},
		{"python/shop/experimental.py::flash_sale", "apply_discount", pricing + "apply_discount", 100},
		{tests + "make_service", "OrderService", orders + "OrderService", 100},
		{tests + "make_service", "Repository", repo + "Repository", 100},
		{tests + "make_service", "add", repo + "Repository.add", 80},
		{tests + "test_place_order", "make_service", tests + "make_service", 90},
		{tests + "test_place_order", "place_order", place, 80},
		{tests + "test_place_order_out_of_stock", "raises", "", 0},
		{tests + "test_place_order_out_of_stock", "place_order", place, 80},
		{tests + "TestPricing.test_discount_applies", "apply_discount", pricing + "apply_discount", 100},
	}
	for _, c := range cases {
		var got []resolvedEdge
		for _, e := range edgesFrom(es, c.src, c.name) {
			if e.Kind != model.EdgeTests {
				got = append(got, e)
			}
		}
		if len(got) == 0 {
			t.Errorf("%s -> %s: no edge", c.src, c.name)
		}
		for _, e := range got {
			if e.Dst != c.dst || e.Conf != c.conf {
				t.Errorf("%s -> %s (line %d): got %q conf %d, want %q conf %d", c.src, c.name, e.Line, e.Dst, e.Conf, c.dst, c.conf)
			}
		}
	}

	// tests edges: test_place_order -> place_order is a unique member (T4);
	// TestPricing's hint "Pricing" names nothing.
	testCases := []struct {
		src, name, dst string
		conf           int
	}{
		{tests + "test_place_order", "place_order", place, 50},
		{tests + "TestPricing", "Pricing", "", 0},
		{tests + "TestPricing.test_discount_applies", "discount_applies", "", 0},
	}
	for _, c := range testCases {
		var got []resolvedEdge
		for _, e := range edgesFrom(es, c.src, c.name) {
			if e.Kind == model.EdgeTests {
				got = append(got, e)
			}
		}
		if len(got) != 1 || got[0].Dst != c.dst || got[0].Conf != c.conf {
			t.Errorf("tests %s -> %s: got %v, want %q conf %d", c.src, c.name, got, c.dst, c.conf)
		}
	}
}

// TestPythonResolution covers import forms, aliases and receiver typing on
// a synthetic package.
func TestPythonResolution(t *testing.T) {
	files := map[string]string{
		"pyproject.toml":  "[project]\nname = \"app\"\n",
		"app/__init__.py": "",
		"app/models.py": `from enum import Enum


class Color(Enum):
    RED = 1

    def label(self) -> str:
        return self.name


class Base:
    def save(self) -> None:
        pass


class User(Base):
    name: str

    def __init__(self, name: str) -> None:
        self.name = name

    @classmethod
    def make(cls, name: str) -> "User":
        return cls(name)

    @property
    def manager(self) -> "User | None":
        return None

    def greet(self) -> str:
        return self.name
`,
		"app/util.py": `def slug(s: str) -> str:
    return s


def len(x):
    return 0
`,
		"app/service.py": `import os.path
import app.util
import app.models as m
from app import util as u
from app.models import User as Account, Color
from . import models


class Service:
    def __init__(self, owner: Account, color: Color = Color.RED) -> None:
        self.owner = owner
        self.color = color
        self.helper = Helper()

    def run(self, other: "Account | None" = None) -> None:
        os.path.join("a", "b")
        app.util.slug("x")
        u.slug("y")
        m.User.make("z").greet()
        Account.make("w").save()
        models.User("q").manager.greet()
        self.owner.greet()
        self.color.label()
        self.helper.assist()
        other.save()
        acc = Account("k")
        acc.greet()
        for item in []:
            item.greet()
        print(len([]))


class Helper:
    def assist(self) -> None:
        pass
`,
		"app/local_len.py": `from app.util import len


def count() -> int:
    return len([1])
`,
	}
	r := fullBuild(t, files, nil)
	es := r.edges()
	const (
		run    = "app/service.py::Service.run"
		models = "app/models.py::"
	)
	cases := []struct {
		src, name, dst string
		conf           int
	}{
		{run, "join", "", 0},
		{run, "slug", "app/util.py::slug", 100},  // app.util.slug and u.slug
		{run, "make", models + "User.make", 100}, // via the imported class
		{run, "save", models + "Base.save", 80},
		{run, "manager", models + "User.manager", 80},
		{run, "owner", "", 0},
		{run, "label", models + "Color.label", 80},
		{run, "assist", "app/service.py::Helper.assist", 80},
		{run, "User", models + "User", 100},
		{run, "Account", "", 0}, // de-aliased by the parser: never a name
		{"app/service.py::Service.__init__", "RED", models + "Color.RED", 100},
		{"app/service.py::Service.__init__", "Helper", "app/service.py::Helper", 90},
		{"app/local_len.py::count", "len", "app/util.py::len", 100},
	}
	for _, c := range cases {
		got := edgesFrom(es, c.src, c.name)
		if len(got) == 0 && c.dst != "" {
			t.Errorf("%s -> %s: no edge", c.src, c.name)
		}
		for _, e := range got {
			if e.Dst != c.dst || e.Conf != c.conf {
				t.Errorf("%s -> %s (line %d): got %q conf %d, want %q conf %d", c.src, c.name, e.Line, e.Dst, e.Conf, c.dst, c.conf)
			}
		}
	}
	// The builtin len() in service.py is dropped by the parser; it must not
	// be linked to the repository's shadowing len.
	if got := edgesFrom(es, run, "len"); len(got) != 0 {
		t.Errorf("builtin len resolved: %v", got)
	}
	// make().greet, manager.greet, owner.greet and acc.greet are typed;
	// item (a loop variable) is not and falls back to the unique member.
	confs := map[int]int{}
	for _, e := range edgesFrom(es, run, "greet") {
		if e.Dst != models+"User.greet" {
			t.Errorf("greet line %d -> %q", e.Line, e.Dst)
		}
		confs[e.Conf]++
	}
	if confs[80] != 4 || confs[50] != 1 || len(confs) != 2 {
		t.Errorf("greet confidences = %v, want 4x80 1x50", confs)
	}
}

func TestPythonImportBindings(t *testing.T) {
	r := pyResolver{}
	cases := []struct {
		imp  Import
		want []string
	}{
		{Import{Spec: "os"}, []string{"os"}},
		{Import{Spec: "os.path"}, nil},
		{Import{Spec: "numpy", Alias: "np"}, []string{"np"}},
		{Import{Spec: "a.b", Alias: "c"}, []string{"c"}},
		{Import{Spec: ".pricing", Names: []string{"pricing"}}, nil},
		{Import{Spec: "x.y", Alias: "z", Names: []string{"y"}}, nil},
		{Import{Spec: "x", Names: []string{"*"}}, nil},
	}
	for _, c := range cases {
		if got := r.ImportBindings(c.imp); strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("ImportBindings(%+v) = %v, want %v", c.imp, got, c.want)
		}
	}
}

func TestPythonSigHelpers(t *testing.T) {
	rets := map[string]string{
		"def f(a, b=(1, 2)) -> Item":               "Item",
		"@property async def g(self) -> list[int]": "list[int]",
		"def h(self)":                                 "",
		"@classmethod def make(cls) -> \"User\"":      "\"User\"",
		"def long(" + strings.Repeat("x, ", 70) + "…": "",
	}
	for sig, want := range rets {
		if got := pyReturnAnnotation(sig); got != want {
			t.Errorf("pyReturnAnnotation(%q) = %q, want %q", sig, got, want)
		}
	}
	vars := map[[2]string]string{
		{"count: int = 0", "count"}:              "int",
		{"m: dict[str, int] = {}", "m"}:          "dict[str, int]",
		{"x = 1", "x"}:                           "",
		{"label: Optional[str] = None", "label"}: "Optional[str]",
	}
	for in, want := range vars {
		if got := pyVarAnnotation(in[0], in[1]); got != want {
			t.Errorf("pyVarAnnotation(%q) = %q, want %q", in[0], got, want)
		}
	}
	if base, args, ok := pyGeneric("Union[A, dict[str, B], None]"); !ok || base != "Union" || len(args) != 3 || args[1] != "dict[str, B]" {
		t.Errorf("pyGeneric = %q %q %v", base, args, ok)
	}
}

// TestPythonFixtureIncrementalEquivalence applies edits one by one,
// resolving only pending names, and compares every step with a full build.
func TestPythonFixtureIncrementalEquivalence(t *testing.T) {
	files, status := pythonFixture(t)
	const (
		pricingPath = "python/shop/services/pricing.py"
		repoPath    = "python/shop/repository.py"
		modelsPath  = "python/shop/models.py"
		ordersPath  = "python/shop/services/orders.py"
	)
	partial := maps.Clone(files)
	delete(partial, pricingPath)
	inc := newTestRepo(t, partial)
	maps.Copy(inc.status, status)
	inc.index()
	inc.resolveAll(Options{})

	steps := []struct {
		name  string
		apply func(r *testRepo) []string
		del   []string
	}{
		{name: "add pricing.py", apply: func(r *testRepo) []string {
			r.files[pricingPath] = files[pricingPath]
			return []string{pricingPath}
		}},
		{name: "get returns Repository", apply: func(r *testRepo) []string {
			r.files[repoPath] = strings.Replace(r.files[repoPath], "-> Item | None:", "-> Repository:", 1)
			return []string{repoPath}
		}},
		{name: "rename round_cents", apply: func(r *testRepo) []string {
			r.files[pricingPath] = strings.ReplaceAll(r.files[pricingPath], "round_cents", "to_cents")
			return []string{pricingPath}
		}},
		{name: "Item gains get", apply: func(r *testRepo) []string {
			r.files[modelsPath] = strings.Replace(r.files[modelsPath],
				"    def describe(self) -> str:", "    def get(self) -> int:\n        return 0\n\n    def describe(self) -> str:", 1)
			return []string{modelsPath}
		}},
		{name: "delete experimental.py", del: []string{"python/shop/experimental.py"}},
		{name: "delete models.py", del: []string{modelsPath}},
		{name: "orders without imports", apply: func(r *testRepo) []string {
			r.files[ordersPath] = "class OrderService:\n    def place_order(self):\n        return apply_discount(1)\n"
			return []string{ordersPath}
		}},
	}
	for _, step := range steps {
		if step.apply != nil {
			inc.index(step.apply(inc)...)
		}
		if len(step.del) > 0 {
			inc.remove(step.del...)
		}
		inc.resolvePending(Options{BatchNames: 5})
		full := fullBuild(t, inc.files, status)
		if got, want := inc.dump(), full.dump(); got != want {
			t.Fatalf("step %q: incremental != full:\n%s", step.name, diffLines(want, got))
		}
	}
}

// TestPythonMalformedAndCyclic resolves broken and self-referential code:
// it must terminate without panicking.
func TestPythonMalformedAndCyclic(t *testing.T) {
	files := map[string]string{
		"pkg/__init__.py": "from . import (\n",
		"pkg/cycle.py": `class A(B):
    def __init__(self):
        self.a = self.b
        self.b = self.a

    def m(self):
        self.a.go()
        x = x.next()
        x.go()
        return self.m().go()


class B(A):
    def go(self) -> "B":
        return super().go().go()
`,
		"pkg/broken.py": "def f(:\n    return self.\nclass (:\n  def g(self) -> : pass\nimport\nfrom .. import\n\x00\xff",
		"pkg/deep.py":   "x = " + strings.Repeat("(", 300) + "1" + strings.Repeat(")", 300) + "\nx.y.z()\n",
	}
	r := fullBuild(t, files, map[string]model.FileStatus{"pkg/broken.py": model.StatusParseErr})
	for _, e := range r.edges() {
		if e.Name == "go" && e.Dst != "" && !strings.HasSuffix(e.Dst, "B.go") {
			t.Errorf("go resolved to %s", e.Dst)
		}
	}
}
