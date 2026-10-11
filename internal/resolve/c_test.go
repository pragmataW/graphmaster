package resolve

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/pragmataW/graphmaster/internal/model"
	"github.com/pragmataW/graphmaster/internal/parse"
	"github.com/pragmataW/graphmaster/internal/testutil"
)

// cfamFixture returns the files of the fixture's top-level directory dir
// ("c" or "cpp").
func cfamFixture(t *testing.T, dir string) map[string]string {
	t.Helper()
	root := testutil.FixtureDir()
	files := map[string]string{}
	err := filepath.WalkDir(filepath.Join(root, dir), func(p string, d fs.DirEntry, err error) error {
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
	return files
}

// cfamSplitStructural separates includes/declares lines (checked here) from
// resolver edges (checked by checkExpectations).
func cfamSplitStructural(exps []expectation) (edges, includes, declares []expectation) {
	for _, x := range exps {
		switch x.Kind {
		case model.EdgeIncludes:
			includes = append(includes, x)
		case model.EdgeDeclares:
			declares = append(declares, x)
		default:
			edges = append(edges, x)
		}
	}
	return edges, includes, declares
}

// cfamCheckIncludes verifies includes lines against the language spec's
// ResolveImport for the imports parsed from the source file.
func cfamCheckIncludes(t *testing.T, r *testRepo, exps []expectation) {
	t.Helper()
	eng := parse.NewEngine()
	defer eng.Close()
	for _, x := range exps {
		lang := model.LangFromPath(x.Src)
		res, err := eng.ParseLang(context.Background(), lang, []byte(r.files[x.Src]))
		if err != nil {
			t.Fatal(err)
		}
		spec, _ := parse.Lookup(lang)
		var got []string
		for _, im := range res.Facts.Imports {
			if t := spec.ResolveImport(im.Spec, x.Src, r.rc()); t.Kind == model.TargetFile {
				got = append(got, t.Path)
			}
		}
		if has := slices.Contains(got, x.Dst); has == x.Negative {
			t.Errorf("line %d: %v: resolved includes %v", x.Line, x, got)
		}
	}
}

// cfamCheckDeclares verifies declares lines through DeclarationResolver.
func cfamCheckDeclares(t *testing.T, r *testRepo, exps []expectation) {
	t.Helper()
	s := newSession(context.Background(), r.st.DB(), r.rc(), Options{})
	defer s.close()
	got := map[[2]string]bool{}
	var all []string
	for _, d := range s.scanDefs(`flags & ? != 0`, int(model.FlagDeclaration)) {
		f := s.File(d.FileID)
		dr, ok := For(f.Lang).(DeclarationResolver)
		if !ok {
			t.Fatalf("%v resolver lacks DeclarationResolver", f.Lang)
		}
		for _, def := range dr.Definitions(s, d) {
			k := [2]string{r.nodeKey(d.ID), r.nodeKey(def.ID)}
			got[k] = true
			all = append(all, k[0]+" -> "+k[1])
		}
	}
	for _, x := range exps {
		has := got[[2]string{x.Src, x.Dst}]
		switch {
		case x.Negative && has:
			t.Errorf("line %d: unexpected %v", x.Line, x)
		case !x.Negative && !has && x.Optional:
			t.Logf("line %d: optional %v missing", x.Line, x)
		case !x.Negative && !has:
			t.Errorf("line %d: missing %v; declares found:\n  %s", x.Line, x, strings.Join(all, "\n  "))
		}
	}
	if s.err != nil {
		t.Fatal(s.err)
	}
}

func TestCFixtureExpectedEdges(t *testing.T) {
	r := fullBuild(t, cfamFixture(t, "c"), nil)
	exps := loadExpectations(t, "c")
	if len(exps) < 25 {
		t.Fatalf("only %d C expectations loaded", len(exps))
	}
	edges, includes, declares := cfamSplitStructural(exps)
	checkExpectations(t, r, append(edges, declares...))
	cfamCheckIncludes(t, r, includes)
	cfamCheckDeclares(t, r, declares)
}

func TestCDeclarationsNeverWinBareCalls(t *testing.T) {
	r := fullBuild(t, map[string]string{
		"p/api.h":  "int helper(int x);\nint only_declared(void);\n",
		"p/impl.c": "#include \"api.h\"\nint helper(int x) { return x; }\n",
		"p/use.c":  "#include \"api.h\"\nint use(void) { return helper(1) + only_declared(); }\n",
	}, nil)
	var helper, decl []resolvedEdge
	for _, e := range r.edges() {
		if e.Src == "p/use.c::use" && e.Name == "helper" {
			helper = append(helper, e)
		}
		if e.Src == "p/use.c::use" && e.Name == "only_declared" {
			decl = append(decl, e)
		}
	}
	if len(helper) != 1 || helper[0].Dst != "p/impl.c::helper" {
		t.Errorf("helper edges = %+v", helper)
	}
	if len(decl) != 1 || decl[0].Dst != "" {
		t.Errorf("declared-only function must stay unresolved: %+v", decl)
	}
}

func TestCEnumeratorsAndTypedefReceivers(t *testing.T) {
	r := fullBuild(t, map[string]string{
		"p/t.h": `typedef struct Node { int value; struct Node *next; } Node_t;
enum Kind { KIND_A, KIND_B };
enum class Scoped { S_A };
`,
		"p/u.c": `#include "t.h"
int use(Node_t *n) {
    Node_t head;
    head.value = KIND_A;
    return n->value + head.next->value;
}
`,
	}, nil)
	want := map[string]string{
		"KIND_A": "p/t.h::Kind.KIND_A",
		"value":  "p/t.h::Node.value",
	}
	typedLine5 := false
	for _, e := range r.edges() {
		if e.Src != "p/u.c::use" {
			continue
		}
		if w, ok := want[e.Name]; ok && e.Line == 4 {
			// Enumerators are nested in their enum, so cross-file uses are
			// global (T4) guesses; fields are receiver-typed (T3).
			if min := map[string]int{"KIND_A": 50, "value": 80}[e.Name]; e.Dst != w || e.Conf < min {
				t.Errorf("%s at line %d -> %q conf %d, want %s", e.Name, e.Line, e.Dst, e.Conf, w)
			}
		}
		if e.Name == "value" && e.Line == 5 {
			if e.Dst != "p/t.h::Node.value" {
				t.Errorf("value at line 5 -> %q", e.Dst)
			}
			typedLine5 = typedLine5 || e.Conf >= 80
		}
	}
	if !typedLine5 {
		t.Error("n->value (pointer parameter) not receiver-typed")
	}
}

func TestCGarbageInputNeverPanics(t *testing.T) {
	files := map[string]string{
		"g/a.c":   "int f( { #include \"\nstruct { ; typedef ;; x->y.z( \x00\xff",
		"g/b.h":   "#define X(\n typedef struct S S; S *f(void) ->-> . .",
		"g/c.cpp": "class : public ::{ void ::~(); }; namespace a:: { T<>::x() {} } this->->",
		"g/d.hpp": "template<> using = ; auto x = std::make_unique<(); TEST(,) {",
		"g/e.c":   strings.Repeat("a.b->c(", 200),
	}
	r := fullBuild(t, files, nil)
	_ = r.edges()
	cfamCheckDeclares(t, r, nil)
}

func TestCReceiverHelpers(t *testing.T) {
	for sig, want := range map[string]string{
		"void Canvas::add(std::unique_ptr<Shape> shape)": "Canvas",
		"std::string geo::Shape::label() const":          "Shape",
		"Shape::~Shape() = default;":                     "Shape",
		"void Box<T>::set(const T& v)":                   "Box",
		"bool Rect::operator==(const Rect& o) const":     "Rect",
		"int helper(int x)":                              "",
	} {
		name := ""
		switch {
		case strings.Contains(sig, "~Shape"):
			name = "~Shape"
		case strings.Contains(sig, "operator=="):
			name = "operator=="
		default:
			head, _, _ := strings.Cut(sig, "(")
			name = head[strings.LastIndex(head, ":")+1:]
			name = strings.TrimSpace(name[strings.LastIndex(name, " ")+1:])
		}
		if got := cfamSigOwner(sig, name); got != want {
			t.Errorf("cfamSigOwner(%q, %q) = %q, want %q", sig, name, got, want)
		}
	}
	for sig, want := range map[string]string{
		"typedef struct Node Node_t":              "Node",
		"typedef unsigned long id_t":              "unsigned long",
		"using ShapePtr = std::unique_ptr<Shape>": "std::unique_ptr<Shape>",
		"typedef int (*cb)(int)":                  "",
	} {
		if got := cfamAliasTarget(sig); got != want {
			t.Errorf("cfamAliasTarget(%q) = %q, want %q", sig, got, want)
		}
	}
	if got := cfamUnwrapSmartPointer("std::unique_ptr<geo::Rect>"); got != "geo::Rect" {
		t.Errorf("cfamUnwrapSmartPointer = %q", got)
	}
}
