package parse

import (
	"context"
	"strings"
	"testing"

	"github.com/pragmataW/graphmaster/internal/model"
)

func TestCPPMembersAndOutOfLine(t *testing.T) {
	f := parseCFam(t, model.LangCPP, `
namespace geo {
class Shape {
public:
    virtual ~Shape();
    virtual double area() const = 0;
    Shape(int x);
private:
    int x_;
};
std::string Shape::name() const { return "s"; }
}
Shape::~Shape() = default;
void geo::Shape::reset() { this->x_ = 0; }
`)
	if d := cfamDef(t, f, "geo.Shape.area"); d.Kind != model.KindMethod ||
		!d.Flags.Has(model.FlagDeclaration|model.FlagAbstract|model.FlagExported) {
		t.Errorf("area = %v [%v]", d.Kind, d.Flags)
	}
	if d := cfamDef(t, f, "geo.Shape.Shape"); d.Kind != model.KindConstructor {
		t.Errorf("ctor kind = %v", d.Kind)
	}
	if d := cfamDef(t, f, "geo.Shape.x_"); d.Flags.Has(model.FlagExported) {
		t.Errorf("private field exported")
	}
	// Out-of-line definitions in the same file attach to the class.
	name := cfamDef(t, f, "geo.Shape.name")
	if name.Parent < 0 || f.Defs[name.Parent].Name != "Shape" || name.Kind != model.KindMethod {
		t.Errorf("Shape::name not attached: %+v", name)
	}
	cfamDef(t, f, "geo.Shape.reset")
	// Shape::~Shape at file scope names the global Shape (no using).
	if d := cfamDef(t, f, "Shape.~Shape"); d.Kind != model.KindMethod || len(d.Scope) != 1 {
		t.Errorf("~Shape = %v scope %v", d.Kind, d.Scope)
	}
}

func TestCPPOutOfLineAcrossFiles(t *testing.T) {
	f := parseCFam(t, model.LangCPP, `
#include "canvas.hpp"
using namespace geo;
void Canvas::add(int x) { items_.push_back(x); }
namespace geo {
Rect::Rect(double w) : w_(w) {}
namespace util { double clamp(double v) { return v; } }
}
double geo::util::twice(double v) { return 2 * v; }
`)
	add := cfamDef(t, f, "geo.Canvas.add")
	if add.Parent != model.NoParent || strings.Join(add.Scope, "::") != "geo::Canvas" || add.Kind != model.KindMethod {
		t.Errorf("add = %+v", add)
	}
	ctor := cfamDef(t, f, "geo.Rect.Rect")
	if ctor.Parent != model.NoParent || ctor.Kind != model.KindConstructor {
		t.Errorf("ctor = %+v", ctor)
	}
	if d := cfamDef(t, f, "geo.util.clamp"); d.Kind != model.KindFunction || d.Parent < 0 {
		t.Errorf("clamp = %+v", d)
	}
	if d := cfamDef(t, f, "geo.util.twice"); d.Kind != model.KindFunction {
		t.Errorf("namespace-qualified function kind = %v", d.Kind)
	}
	if len(f.Imports) != 2 || f.Imports[1].Spec != "geo" || !f.Imports[1].Wildcard {
		t.Errorf("imports = %+v", f.Imports)
	}
}

func TestCPPQualifiersAndReceivers(t *testing.T) {
	f := parseCFam(t, model.LangCPP, `
void f(Canvas& c, Canvas* p) {
    geo::util::clamp(1);
    std::make_unique<geo::Square>(2.0);
    c.add(1);
    p->size();
    this->g();
    auto* r = new geo::Rect(1);
    geo::a::Canvas k;
}
`)
	type key struct{ name, qual, recv string }
	got := map[key]model.RefKind{}
	for _, r := range f.Refs {
		got[key{r.Name, r.Qualifier, r.Receiver}] = r.Kind
	}
	want := map[key]model.RefKind{
		{"clamp", "geo::util", ""}: model.RefCall,
		{"make_unique", "std", ""}: model.RefCall,
		{"Square", "geo", ""}:      model.RefType,
		{"add", "", "c"}:           model.RefCall,
		{"size", "", "p"}:          model.RefCall,
		{"g", "", "this"}:          model.RefCall,
		{"Rect", "geo", ""}:        model.RefCall,
		{"Canvas", "geo::a", ""}:   model.RefType,
	}
	for k, kind := range want {
		if got[k] != kind {
			t.Errorf("missing ref %+v (%v); got:\n%s", k, kind, DumpFacts(f))
		}
	}
}

func TestCPPUsingDeclarations(t *testing.T) {
	f := parseCFam(t, model.LangCPP, `
using namespace std;
using namespace a::b;
using std::cout;
using geo::Canvas;
namespace fs = std::filesystem;
`)
	var got []string
	for _, im := range f.Imports {
		s := im.Spec
		if im.Wildcard {
			s += " *"
		}
		if len(im.Names) > 0 && !im.Wildcard {
			s += " names=" + strings.Join(im.Names, ",")
		}
		if im.Alias != "" {
			s += " as " + im.Alias
		}
		got = append(got, s)
	}
	want := "std * | a::b * | std names=cout | geo::Canvas | std::filesystem as fs"
	if strings.Join(got, " | ") != want {
		t.Errorf("imports = %s\nwant      %s", strings.Join(got, " | "), want)
	}
	spec, _ := Lookup(model.LangCPP)
	if k := spec.ResolveImport("std", "a.cpp", nil).Kind; k != model.TargetExternal {
		t.Errorf("std -> %v", k)
	}
	if k := spec.ResolveImport("geo", "a.cpp", nil).Kind; k != model.TargetUnresolved {
		t.Errorf("geo -> %v", k)
	}
}

func TestCPPGoogleTest(t *testing.T) {
	f := parseCFam(t, model.LangCPP, `
#include <gtest/gtest.h>
TEST(CanvasTest, TotalArea) { EXPECT_EQ(area(), 1); }
TEST_F(RectFixture, Area) {}
void test_free() {}
`)
	if !f.TestFile {
		t.Error("TestFile not set")
	}
	targets := map[string]string{}
	for _, tm := range f.Tests {
		d := f.Defs[tm.Def]
		targets[strings.Join(f.QNameParts(tm.Def), ".")] = tm.Target
		if d.Kind != model.KindTest || !d.Flags.Has(model.FlagTest) {
			t.Errorf("%s: kind %v flags %v", d.Name, d.Kind, d.Flags)
		}
	}
	want := map[string]string{"CanvasTest.TotalArea": "Canvas", "RectFixture.Area": "Rect", "test_free": "free"}
	for k, v := range want {
		if targets[k] != v {
			t.Errorf("test %s target %q, want %q (all: %v)", k, targets[k], v, targets)
		}
	}
	for _, d := range f.Defs {
		if d.Name == "TEST" || d.Name == "TEST_F" {
			t.Errorf("macro name extracted as def: %+v", d)
		}
	}
	// The call inside the test body is attributed to the test.
	for _, r := range f.Refs {
		if r.Name == "area" && (r.Enclosing < 0 || f.Defs[r.Enclosing].Name != "TotalArea") {
			t.Errorf("area call enclosed by %d", r.Enclosing)
		}
	}
}

func TestCPPInheritance(t *testing.T) {
	f := parseCFam(t, model.LangCPP, `
class A {};
class B : public A, protected ns::Mixin<B>, private virtual ::C {};
`)
	var got []string
	for _, in := range f.Inherits {
		got = append(got, f.Defs[in.Child].Name+"->"+in.Qualifier+"|"+in.Name)
	}
	if s := strings.Join(got, " "); s != "B->|A B->ns|Mixin B->|C" {
		t.Errorf("inherits = %s", s)
	}
}

func TestCPPQName(t *testing.T) {
	spec, _ := Lookup(model.LangCPP)
	q := spec.(interface{ QName(string, []string) string })
	if got := q.QName("cpp", []string{"geo", "Canvas", "add"}); got != "geo::Canvas::add" {
		t.Errorf("QName = %q", got)
	}
}

func TestCFamStripTemplateArgs(t *testing.T) {
	for in, want := range map[string]string{
		"std::vector<std::pair<int, int>>::size": "std::vector::size",
		"Foo<T>::operator<":                      "Foo::operator<",
		"Foo::operator<<":                        "Foo::operator<<",
		"a<b":                                    "a",
	} {
		if got := cfamStripTemplateArgs(in); got != want {
			t.Errorf("cfamStripTemplateArgs(%q) = %q, want %q", in, got, want)
		}
	}
}

func FuzzParseCPP(f *testing.F) {
	f.Add([]byte("namespace a::b { class C : public D<int> { void m(); }; }\nvoid a::b::C::m() { this->x(); }\n"))
	f.Add([]byte("TEST(S, N) { auto p = std::make_unique<T>(); p->f(); }\nusing namespace x;\ntemplate<class T> T g(T t);\n"))
	f.Add([]byte("class { ; Foo::~Foo( = ;; operator<<<(::\x00"))
	e := NewEngine()
	f.Cleanup(e.Close)
	f.Fuzz(func(t *testing.T, src []byte) {
		res, err := e.ParseLang(context.Background(), model.LangCPP, src)
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
