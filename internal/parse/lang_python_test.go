package parse

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/pragmataW/graphmaster/internal/model"
)

func parsePy(t *testing.T, src string) *model.Facts {
	t.Helper()
	e := NewEngine()
	defer e.Close()
	res, err := e.ParseLang(context.Background(), model.LangPython, []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if err := res.Facts.Validate(); err != nil {
		t.Fatalf("invalid facts: %v", err)
	}
	return res.Facts
}

func TestPythonRegistered(t *testing.T) {
	for _, p := range []string{"a.py", "pkg/b.pyi"} {
		if s, ok := ForPath(p); !ok || s.Lang() != model.LangPython {
			t.Errorf("ForPath(%q) not python", p)
		}
	}
}

func TestPythonIsTestPath(t *testing.T) {
	cases := map[string]bool{
		"tests/test_orders.py": true, "pkg/orders_test.py": true, "tests/conftest.py": true,
		"shop/models.py": false, "testing.py": false, "test_x.pyi": false, "tests/helpers.py": false,
	}
	for p, want := range cases {
		if got := IsTestPath(p); got != want {
			t.Errorf("IsTestPath(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestPythonImportExpansion(t *testing.T) {
	f := parsePy(t, `from __future__ import annotations
import os, a.b as c
from . import pricing
from ..models import (
    Item as I,  # the model
    Other,
)
from x.y import *
from .import z
def f():
    from shop.services.pricing import apply_discount
`)
	type imp struct {
		Spec, Alias string
		Names       []string
		Wildcard    bool
		Line        uint32
	}
	var got []imp
	for _, im := range f.Imports {
		got = append(got, imp{im.Spec, im.Alias, im.Names, im.Wildcard, im.Line})
	}
	want := []imp{
		{"os", "", nil, false, 2},
		{"a.b", "c", nil, false, 2},
		{".pricing", "", []string{"pricing"}, false, 3},
		{"..models.Item", "I", []string{"Item"}, false, 4},
		{"..models.Other", "", []string{"Other"}, false, 4},
		{"x.y", "", []string{"*"}, true, 8},
		{".z", "", []string{"z"}, false, 9},
		{"shop.services.pricing.apply_discount", "", []string{"apply_discount"}, false, 11},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("imports:\n got %+v\nwant %+v", got, want)
	}
}

func TestPythonParseFromImport(t *testing.T) {
	bad := []string{"from import x", "from a..b import x", "from a. import x", "from a import", "from a import x y", "from 1a import x"}
	for _, s := range bad {
		if _, _, _, ok := parseFromImport(normalizeFromImport(s)); ok {
			t.Errorf("parseFromImport(%q) accepted", s)
		}
	}
	mod, names, _, ok := parseFromImport(normalizeFromImport("from .. pkg . sub import (a,\n b as c,)"))
	if !ok || mod != "..pkg.sub" || len(names) != 2 || names[1] != (pyImportedName{"b", "c"}) {
		t.Errorf("got %q %v %v", mod, names, ok)
	}
}

func TestPythonRefFixups(t *testing.T) {
	f := parsePy(t, `import os.path
from . import pricing
from .models import Item as I
class Local: pass
def f(x: I) -> int:
    pricing.apply(1)
    os.path.join("a")
    I.make()
    Local.build()
    x.total()
    I()
    print(len([]))
    super().go()
`)
	var got []string
	for _, r := range f.Refs {
		got = append(got, r.Kind.String()+" "+r.Qualifier+"|"+r.Receiver+"|"+r.Name)
	}
	want := []string{
		"type ||Item", "call pricing||apply", "member os||path", "call os.path||join", "call Item||make",
		"call Local||build", "call |x|total", "call ||Item", "call |super()|go",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("refs:\n got %q\nwant %q", got, want)
	}
}

func TestPythonDefKinds(t *testing.T) {
	f := parsePy(t, `import enum
from typing import Protocol
LIMIT = 3
name = "x"
class Color(enum.Enum):
    RED = 1
class Shape(Protocol):
    def area(self) -> float: ...
class C:
    size: int = 0
    def __init__(self): pass
    @staticmethod
    async def run(): pass
    def _private(self):
        def inner(): pass
`)
	got := map[string]string{}
	for i, d := range f.Defs {
		got[strings.Join(f.QNameParts(int32(i)), ".")] = d.Kind.String() + " " + d.Flags.String()
	}
	want := map[string]string{
		"LIMIT": "constant exported", "name": "variable exported",
		"Color": "enum exported", "Color.RED": "enum_member exported",
		"Shape": "interface exported", "Shape.area": "method exported",
		"C": "class exported", "C.size": "field exported", "C.__init__": "constructor exported",
		"C.run": "method exported|async|static", "C._private": "method ", "C._private.inner": "function ",
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s: got %q, want %q", k, got[k], w)
		}
	}
}

func TestPythonModuleOf(t *testing.T) {
	repo := model.NewMemRepo(map[string]string{
		"python/pyproject.toml":            "",
		"python/shop/__init__.py":          "",
		"python/shop/models.py":            "",
		"python/shop/services/__init__.py": "",
		"python/shop/services/pricing.py":  "",
		"python/tests/test_orders.py":      "",
		"lib/setup.cfg":                    "",
		"lib/src/ns/tool.py":               "",
		"scripts/run.py":                   "",
		"stubs/pkg/__init__.pyi":           "",
		"stubs/pkg/mod.pyi":                "",
	})
	s := pythonSpec{}
	cases := map[string]string{
		"python/shop/services/pricing.py":  "shop.services.pricing",
		"python/shop/services/__init__.py": "shop.services",
		"python/shop/models.py":            "shop.models",
		"python/tests/test_orders.py":      "tests.test_orders",
		"lib/src/ns/tool.py":               "ns.tool",
		"scripts/run.py":                   "scripts.run",
		"stubs/pkg/mod.pyi":                "pkg.mod",
	}
	for p, want := range cases {
		if got := s.ModuleOf(p, nil, repo); got != want {
			t.Errorf("ModuleOf(%q) = %q, want %q", p, got, want)
		}
	}
	if got := s.ModuleOf("a/b.py", nil, nil); got != "a.b" {
		t.Errorf("ModuleOf without ctx = %q", got)
	}
}

func TestPythonResolveImport(t *testing.T) {
	repo := model.NewMemRepo(map[string]string{
		"python/pyproject.toml":            "",
		"python/shop/__init__.py":          "",
		"python/shop/models.py":            "",
		"python/shop/repository.py":        "",
		"python/shop/services/__init__.py": "",
		"python/shop/services/orders.py":   "",
		"python/shop/services/pricing.py":  "",
		"python/shop/ns/sub/x.py":          "",
		"python/tests/test_orders.py":      "",
		"python/tests/helpers.py":          "",
	})
	s := pythonSpec{}
	const orders = "python/shop/services/orders.py"
	const test = "python/tests/test_orders.py"
	cases := []struct {
		spec, from string
		want       model.ImportTarget
	}{
		{".pricing", orders, model.ImportTarget{Kind: model.TargetFile, Path: "python/shop/services/pricing.py", Module: "shop.services.pricing"}},
		{"..models.Item", orders, model.ImportTarget{Kind: model.TargetFile, Path: "python/shop/models.py", Module: "shop.models"}},
		{"..repository", orders, model.ImportTarget{Kind: model.TargetFile, Path: "python/shop/repository.py", Module: "shop.repository"}},
		{".", orders, model.ImportTarget{Kind: model.TargetFile, Path: "python/shop/services/__init__.py", Module: "shop.services"}},
		{".nothing", orders, model.ImportTarget{Kind: model.TargetFile, Path: "python/shop/services/__init__.py", Module: "shop.services"}},
		{"....toofar", orders, model.ImportTarget{Kind: model.TargetUnresolved, Module: "....toofar"}},
		{"shop.services.orders.OrderService", test, model.ImportTarget{Kind: model.TargetFile, Path: orders, Module: "shop.services.orders"}},
		{"shop.services", test, model.ImportTarget{Kind: model.TargetFile, Path: "python/shop/services/__init__.py", Module: "shop.services"}},
		{"shop.ns.sub", test, model.ImportTarget{Kind: model.TargetModule, Path: "python/shop/ns/sub", Module: "shop.ns.sub"}},
		{"helpers", test, model.ImportTarget{Kind: model.TargetFile, Path: "python/tests/helpers.py", Module: "tests.helpers"}},
		{"shop.missing.thing", test, model.ImportTarget{Kind: model.TargetUnresolved, Module: "shop.missing.thing"}},
		{"os.path", test, model.ImportTarget{Kind: model.TargetExternal, Module: "os.path"}},
		{"dataclasses.dataclass", "python/shop/models.py", model.ImportTarget{Kind: model.TargetExternal, Module: "dataclasses.dataclass"}},
		{"a..b", test, model.ImportTarget{Kind: model.TargetUnresolved, Module: "a..b"}},
	}
	for _, c := range cases {
		if got := s.ResolveImport(c.spec, c.from, repo); got != c.want {
			t.Errorf("ResolveImport(%q from %s) = %+v, want %+v", c.spec, c.from, got, c.want)
		}
	}
}

// TestPythonGarbageInput feeds malformed sources; extraction must never
// panic and must produce valid facts.
func TestPythonGarbageInput(t *testing.T) {
	inputs := []string{
		"", "\x00\xff\xfe", "def", "class (:", "from . import", "from import (", "import",
		"def f(:\n  return self.\n", "class A(B, metaclass=):\n  x: = 1\n",
		"from .. models import (a as, b)\n", "@\ndef", "async def f(*, **): ...",
		strings.Repeat("(", 5000), strings.Repeat("class A:\n ", 300) + "pass",
		"x = [i for i in", "try:\nexcept (A, as e:\n", "type X[T] = list[T]\n",
	}
	e := NewEngine()
	defer e.Close()
	for _, in := range inputs {
		res, err := e.ParseLang(context.Background(), model.LangPython, []byte(in))
		if err != nil {
			t.Fatal(err)
		}
		if err := res.Facts.Validate(); err != nil {
			t.Errorf("%q: invalid facts: %v", in, err)
		}
	}
}

// FuzzParsePython checks that arbitrary input never panics the extractor
// and always yields valid facts that survive the codec.
func FuzzParsePython(f *testing.F) {
	f.Add([]byte("from ..a import (b as c, d)\nclass X(c.Y, Generic[T]):\n  z: int = 1\n  def m(self): self.z.q()\n"))
	f.Add([]byte("@dec\nasync def test_x(a: A | None) -> list[B]:\n  try: pass\n  except (E, m.F) as e: raise G\n"))
	f.Add([]byte("import\x00 ((( def"))
	e := NewEngine()
	f.Cleanup(e.Close)
	f.Fuzz(func(t *testing.T, src []byte) {
		res, err := e.ParseLang(context.Background(), model.LangPython, src)
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
