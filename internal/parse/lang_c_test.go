package parse

import (
	"context"
	"strings"
	"testing"

	"github.com/pragmataW/graphmaster/internal/model"
)

func parseCFam(t *testing.T, l model.Lang, src string) *model.Facts {
	t.Helper()
	e := NewEngine()
	defer e.Close()
	res, err := e.ParseLang(context.Background(), l, []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if err := res.Facts.Validate(); err != nil {
		t.Fatalf("invalid facts: %v\n%s", err, DumpFacts(res.Facts))
	}
	return res.Facts
}

// cfamDef returns the def whose dotted qname is q.
func cfamDef(t *testing.T, f *model.Facts, q string) *model.Def {
	t.Helper()
	if _, d := defByName(f, q); d != nil {
		return d
	}
	t.Fatalf("no def %q in\n%s", q, DumpFacts(f))
	return nil
}

func TestCDeclarationsAndDefinitions(t *testing.T) {
	f := parseCFam(t, model.LangC, `
int add(int a, int b);
static int twice(int x) { int local = x; return add(local, x); }
int add(int a, int b) { return a + b; }
extern int counter;
int counter = 0;
const int LIMIT = 3;
`)
	var decl, def *model.Def
	for i := range f.Defs {
		d := &f.Defs[i]
		if d.Name == "add" && d.Flags.Has(model.FlagDeclaration) {
			decl = d
		} else if d.Name == "add" {
			def = d
		}
		if d.Name == "local" {
			t.Errorf("local variable extracted as def: %+v", d)
		}
	}
	if decl == nil || def == nil {
		t.Fatalf("want prototype and definition of add:\n%s", DumpFacts(f))
	}
	if !decl.Flags.Has(model.FlagExported) || def.Flags.Has(model.FlagDeclaration) {
		t.Errorf("flags: decl=%v def=%v", decl.Flags, def.Flags)
	}
	if tw := cfamDef(t, f, "twice"); !tw.Flags.Has(model.FlagStatic) || tw.Flags.Has(model.FlagExported) {
		t.Errorf("static function flags = %v", tw.Flags)
	}
	if d := cfamDef(t, f, "LIMIT"); d.Kind != model.KindConstant {
		t.Errorf("const variable kind = %v", d.Kind)
	}
	var externDecl bool
	for _, d := range f.Defs {
		if d.Name == "counter" && d.Flags.Has(model.FlagDeclaration) {
			externDecl = true
		}
	}
	if !externDecl {
		t.Errorf("extern variable not flagged as declaration:\n%s", DumpFacts(f))
	}
	// The call inside twice is attributed to twice, not to the dropped local.
	for _, r := range f.Refs {
		if r.Name == "add" && (r.Enclosing < 0 || f.Defs[r.Enclosing].Name != "twice") {
			t.Errorf("call to add enclosed by %d", r.Enclosing)
		}
	}
}

func TestCTypedefsNestFields(t *testing.T) {
	f := parseCFam(t, model.LangC, `
typedef struct { int x; } Anon;
typedef struct Named { int y; } Named_t;
`)
	if d := cfamDef(t, f, "Anon"); d.Kind != model.KindStruct {
		t.Errorf("Anon kind = %v", d.Kind)
	}
	cfamDef(t, f, "Anon.x")
	cfamDef(t, f, "Named.y")
	if d := cfamDef(t, f, "Named_t"); d.Kind != model.KindType || d.Sig != "typedef struct Named Named_t" {
		t.Errorf("Named_t = %v %q", d.Kind, d.Sig)
	}
}

func TestCIncludeGuardIsNotAMacro(t *testing.T) {
	f := parseCFam(t, model.LangC, "#ifndef FOO_H\n#define FOO_H\n#define FEATURE_X\n#define N 3\n#endif\n")
	var names []string
	for _, d := range f.Defs {
		names = append(names, d.Name)
	}
	if got := strings.Join(names, ","); got != "FEATURE_X,N" {
		t.Errorf("macros = %s", got)
	}
}

func TestCUnityTests(t *testing.T) {
	f := parseCFam(t, model.LangC, `
void test_add(void) {}
void testSub() {}
static void test_mul(void **state) {}
int test_bit(int x) { return x; }
void test_with_args(int x) {}
`)
	got := map[string]bool{}
	for _, tm := range f.Tests {
		got[f.Defs[tm.Def].Name] = true
	}
	for _, n := range []string{"test_add", "testSub", "test_mul"} {
		if !got[n] {
			t.Errorf("%s not a test", n)
		}
	}
	for _, n := range []string{"test_bit", "test_with_args"} {
		if got[n] {
			t.Errorf("%s must not be a test", n)
		}
	}
	if !f.TestFile {
		t.Error("TestFile not set")
	}
}

func TestCIsTestPath(t *testing.T) {
	for p, want := range map[string]bool{
		"tests/foo.c":          true,
		"src/test/foo.cpp":     true,
		"src/test_vec.c":       true,
		"src/vec_test.cc":      true,
		"src/vec_unittest.cc":  true,
		"src/VecTest.cpp":      true,
		"src/vec.c":            false,
		"src/testing_helper.h": false,
		"src/attestation.c":    false,
	} {
		if got := IsTestPath(p); got != want {
			t.Errorf("IsTestPath(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestCModuleOf(t *testing.T) {
	rc := model.NewMemRepo(map[string]string{
		"c/src/main.c":              "",
		"c/include/vec.h":           "",
		"proj/CMakeLists.txt":       "",
		"proj/lib/a/CMakeLists.txt": "",
		"proj/lib/a/a.c":            "",
		"proj/include/a/a.h":        "",
		"drivers/net/eth.c":         "",
		"main.c":                    "",
		"src/x.c":                   "",
	})
	spec, _ := Lookup(model.LangC)
	cases := map[string]string{
		"c/src/main.c":       "c",
		"c/include/vec.h":    "c",
		"proj/lib/a/a.c":     "proj", // outermost build file wins
		"proj/include/a/a.h": "proj",
		"drivers/net/eth.c":  "drivers/net",
		"main.c":             ".",
		"src/x.c":            ".",
	}
	for p, want := range cases {
		if got := spec.ModuleOf(p, nil, rc); got != want {
			t.Errorf("ModuleOf(%q) = %q, want %q", p, got, want)
		}
	}
	if got := spec.ModuleOf("a/src/b.c", nil, nil); got != "a" {
		t.Errorf("ModuleOf without ctx = %q", got)
	}
}

func TestCResolveImport(t *testing.T) {
	rc := model.NewMemRepo(map[string]string{
		"c/src/main.c":               "",
		"c/src/util.h":               "",
		"c/include/vec.h":            "",
		"c/tests/test_vec.c":         "",
		"cpp/include/geo/canvas.hpp": "",
		"cpp/src/main.cpp":           "",
		"root.h":                     "",
	})
	spec, _ := Lookup(model.LangC)
	cases := []struct {
		spec, from string
		kind       model.ImportTargetKind
		path       string
	}{
		{`"util.h"`, "c/src/main.c", model.TargetFile, "c/src/util.h"},
		{`"vec.h"`, "c/src/main.c", model.TargetFile, "c/include/vec.h"},
		{`"../include/vec.h"`, "c/tests/test_vec.c", model.TargetFile, "c/include/vec.h"},
		{`"geo/canvas.hpp"`, "cpp/src/main.cpp", model.TargetFile, "cpp/include/geo/canvas.hpp"},
		{`"root.h"`, "c/src/main.c", model.TargetFile, "root.h"},
		{`<vec.h>`, "c/src/main.c", model.TargetFile, "c/include/vec.h"},
		{`<stdio.h>`, "c/src/main.c", model.TargetExternal, ""},
		{`"missing.h"`, "c/src/main.c", model.TargetUnresolved, ""},
		{`"../../../etc/passwd"`, "c/src/main.c", model.TargetUnresolved, ""},
		{`<util.h>`, "c/src/main.c", model.TargetExternal, ""}, // <> skips the file's directory
		{`FOO_HEADER`, "c/src/main.c", model.TargetUnresolved, ""},
	}
	for _, c := range cases {
		got := spec.ResolveImport(c.spec, c.from, rc)
		if got.Kind != c.kind || got.Path != c.path {
			t.Errorf("ResolveImport(%s from %s) = %+v, want %v %q", c.spec, c.from, got, c.kind, c.path)
		}
		if got.Kind == model.TargetFile && got.Module != spec.ModuleOf(got.Path, nil, rc) {
			t.Errorf("target module %q", got.Module)
		}
	}
}

func TestCResolveImportCompileCommands(t *testing.T) {
	cc := `[
	  {"directory": "/home/u/proj/build", "file": "/home/u/proj/src/main.c",
	   "command": "cc -I../third/inc -iquote /home/u/proj/gen -isystem /usr/include -c ../src/main.c"},
	  {"directory": "/home/u/proj/build", "file": "../src/other.c",
	   "arguments": ["cc", "-I", "/home/u/proj/api", "-c", "../src/other.c"]}
	]`
	rc := model.NewMemRepo(map[string]string{
		"build/compile_commands.json": cc,
		"src/main.c":                  "",
		"src/other.c":                 "",
		"third/inc/dep.h":             "",
		"gen/config.h":                "",
		"api/public.h":                "",
	})
	spec, _ := Lookup(model.LangC)
	for in, want := range map[string]string{
		`"dep.h"`:    "third/inc/dep.h",
		`<config.h>`: "gen/config.h",
		`"public.h"`: "api/public.h",
	} {
		got := spec.ResolveImport(in, "src/main.c", rc)
		if got.Kind != model.TargetFile || got.Path != want {
			t.Errorf("ResolveImport(%s) = %+v, want %s", in, got, want)
		}
	}
	// Malformed compile commands are ignored.
	bad := model.NewMemRepo(map[string]string{"compile_commands.json": "{nope", "src/a.c": ""})
	if got := spec.ResolveImport(`"x.h"`, "src/a.c", bad); got.Kind != model.TargetUnresolved {
		t.Errorf("malformed compile_commands: %+v", got)
	}
}

func TestCQName(t *testing.T) {
	spec, _ := Lookup(model.LangC)
	q, ok := spec.(interface{ QName(string, []string) string })
	if !ok {
		t.Fatal("C spec must implement QName")
	}
	if got := q.QName("c", []string{"Vec2", "x"}); got != "Vec2.x" {
		t.Errorf("QName = %q", got)
	}
}

func FuzzParseC(f *testing.F) {
	f.Add([]byte("#include \"a.h\"\ntypedef struct { int x; } T;\nstatic int f(T *t) { return t->x + g(1); }\n"))
	f.Add([]byte("#define F(x) x\nint (*fp)(int); struct S { union { int a; } u; } s; enum { A } e;\n"))
	f.Add([]byte("int f( { #if\n typedef struct S S; \x00\xff }}}"))
	e := NewEngine()
	f.Cleanup(e.Close)
	f.Fuzz(func(t *testing.T, src []byte) {
		res, err := e.ParseLang(context.Background(), model.LangC, src)
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
