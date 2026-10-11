package parse

import (
	"context"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/pragmataW/graphmaster/internal/model"
)

func javaSpecForTest(t *testing.T) model.LanguageSpec {
	t.Helper()
	spec, ok := Lookup(model.LangJava)
	if !ok {
		t.Fatal("java not registered")
	}
	return spec
}

func TestJavaRegistered(t *testing.T) {
	spec, ok := ForPath("src/main/java/a/B.java")
	if !ok || spec.Lang() != model.LangJava {
		t.Fatalf("ForPath(.java) = %v, %v", spec, ok)
	}
	if javaSpecForTest(t).ExtractorVersion() < 1 {
		t.Fatal("extractor version must be positive")
	}
}

func TestJavaPackageOf(t *testing.T) {
	cases := map[string]string{
		"package a.b.c;\nclass X {}":                            "a.b.c",
		"\xef\xbb\xbf// header\n/* block\n */ package  a . b ;": "a.b",
		"@Deprecated\n@Foo(x = \"y\")\npackage a.info;\n":       "a.info",
		"import a.b.C;\nclass X {}":                             "",
		"class X {}":                                            "",
		"":                                                      "",
		"package":                                               "",
		"packagex a;":                                           "",
		"package a.b":                                           "",
		"/* unterminated":                                       "",
		"package 1a.b;":                                         "",
		"package a.b; // trailing\npackage c;":                  "a.b",
		"@A(\"(\") package a;":                                  "a",
	}
	for src, want := range cases {
		if got := JavaPackageOf([]byte(src)); got != want {
			t.Errorf("JavaPackageOf(%q) = %q, want %q", src, got, want)
		}
	}
}

func TestJavaModuleOf(t *testing.T) {
	spec := javaSpecForTest(t)
	rc := model.NewMemRepo(map[string]string{
		"src/main/java/com/x/A.java": "package com.x;\nclass A {}\n",
		"Main.java":                  "class Main {}\n",
	})
	if got := spec.ModuleOf("src/main/java/com/x/A.java", nil, rc); got != "com.x" {
		t.Errorf("ModuleOf(no facts) = %q", got)
	}
	if got := spec.ModuleOf("Main.java", nil, rc); got != "" {
		t.Errorf("default package = %q", got)
	}
	if got := spec.ModuleOf("whatever.java", &model.Facts{Package: "p.q"}, nil); got != "p.q" {
		t.Errorf("ModuleOf(facts) = %q", got)
	}
	if got := spec.ModuleOf("missing.java", nil, rc); got != "" {
		t.Errorf("missing file = %q", got)
	}
}

func TestJavaResolveImport(t *testing.T) {
	spec := javaSpecForTest(t)
	rc := model.NewMemRepo(map[string]string{
		"app/src/main/java/com/x/A.java":       "package com.x;\nimport com.x.util.U;\nclass A {}\n",
		"app/src/main/java/com/x/util/U.java":  "package com.x.util;\nclass U { static class N {} }\n",
		"app/src/test/java/com/x/ATest.java":   "package com.x;\nclass ATest {}\n",
		"app/src/test/java/com/x/Fixture.java": "package com.x;\nclass Fixture {}\n",
		"odd/Place.java":                       "package not.matching;\nclass Place {}\n",
		"Root.java":                            "class Root {}\n",
		"Sibling.java":                         "class Sibling {}\n",
	})
	from := "app/src/main/java/com/x/A.java"
	test := "app/src/test/java/com/x/ATest.java"
	cases := []struct {
		spec, from string
		want       model.ImportTarget
	}{
		{"com.x.util.U", from, model.ImportTarget{Kind: model.TargetFile, Path: "app/src/main/java/com/x/util/U.java", Module: "com.x.util"}},
		{"com.x.util.U.N", from, model.ImportTarget{Kind: model.TargetFile, Path: "app/src/main/java/com/x/util/U.java", Module: "com.x.util"}},
		{"com.x.util.U.helper", from, model.ImportTarget{Kind: model.TargetFile, Path: "app/src/main/java/com/x/util/U.java", Module: "com.x.util"}},
		{"com.x.util", from, model.ImportTarget{Kind: model.TargetModule, Path: "app/src/main/java/com/x/util", Module: "com.x.util"}},
		// Test sources see the main source set.
		{"com.x.util.U", test, model.ImportTarget{Kind: model.TargetFile, Path: "app/src/main/java/com/x/util/U.java", Module: "com.x.util"}},
		{"com.x.Fixture", from, model.ImportTarget{Kind: model.TargetFile, Path: "app/src/test/java/com/x/Fixture.java", Module: "com.x"}},
		{"java.util.List", from, model.ImportTarget{Kind: model.TargetExternal, Module: "java.util.List"}},
		{"javax.inject.Inject", from, model.ImportTarget{Kind: model.TargetExternal, Module: "javax.inject.Inject"}},
		// Not found under the roots: assumed to be the package it names.
		{"org.junit.jupiter.api.Test", from, model.ImportTarget{Kind: model.TargetModule, Module: "org.junit.jupiter.api"}},
		{"org.lib", from, model.ImportTarget{Kind: model.TargetModule, Module: "org.lib"}},
		{"Sibling", "Root.java", model.ImportTarget{Kind: model.TargetFile, Path: "Sibling.java"}},
		{"com.x.util.U", "odd/Place.java", model.ImportTarget{Kind: model.TargetModule, Module: "com.x.util"}},
		{"", from, model.ImportTarget{Kind: model.TargetExternal}},
	}
	for _, c := range cases {
		if got := spec.ResolveImport(c.spec, c.from, rc); got != c.want {
			t.Errorf("ResolveImport(%q from %s) = %+v, want %+v", c.spec, c.from, got, c.want)
		}
	}
	if got := spec.ResolveImport("a.B", from, nil); got.Kind != model.TargetModule || got.Module != "a" {
		t.Errorf("nil ctx = %+v", got)
	}
}

func TestJavaIsTestPath(t *testing.T) {
	yes := []string{
		"src/test/java/a/Util.java", "mod/src/test/java/a/B.java", "a/FooTest.java",
		"a/FooTests.java", "a/FooIT.java", "a/TestFoo.java", "a/FooTestCase.java",
	}
	no := []string{"src/main/java/a/Foo.java", "a/Test.java", "a/Testing.java", "a/Contest.java", "a/Latest.java"}
	spec := javaSpecForTest(t).(TestPathMatcher)
	for _, p := range yes {
		if !spec.IsTestPath(p) {
			t.Errorf("IsTestPath(%q) = false", p)
		}
	}
	for _, p := range no {
		if spec.IsTestPath(p) {
			t.Errorf("IsTestPath(%q) = true", p)
		}
	}
	if !IsTestPath("x/src/test/java/A.java") {
		t.Error("package-level IsTestPath ignores java")
	}
}

func TestJavaTestTarget(t *testing.T) {
	cases := map[string]string{
		"testFetchItem": "fetchItem", "test_fetch": "fetch", "testfoo": "", "test": "",
		"describesThing": "", "fetchWorks": "",
	}
	for in, want := range cases {
		if got := (javaSpec{}).testTarget(in); got != want {
			t.Errorf("testTarget(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestJavaTypeLike(t *testing.T) {
	yes := []string{"Strings", "Map.Entry", "com.x.Util", "A", "Outer.Inner"}
	no := []string{"s", "this", "this.Foo", "foo()", "new Dog(\"x\")", "MAX", "Color.RED", "a.b", "", "a..B"}
	for _, s := range yes {
		if !javaTypeLike(s) {
			t.Errorf("javaTypeLike(%q) = false", s)
		}
	}
	for _, s := range no {
		if javaTypeLike(s) {
			t.Errorf("javaTypeLike(%q) = true", s)
		}
	}
	if got := javaCleanQualifier("Map<K, Map<A,B>> . Entry"); got != "Map.Entry" {
		t.Errorf("javaCleanQualifier = %q", got)
	}
}

func TestJavaEdgeShapes(t *testing.T) {
	src := `package p;

import static q.Util.helper;

class A extends B implements C<D> {
    void m(Other o) {
        helper();
        o.run();
        Util.go();
        q.Util.go();
        MAX.size();
        this.field.go();
        new A().m(null);
        Runnable r = this::m;
    }
}
`
	e := NewEngine()
	defer e.Close()
	res, err := e.ParseLang(context.Background(), model.LangJava, []byte(src))
	if err != nil || res.SyntaxErrors {
		t.Fatalf("parse: %v %v", err, res.SyntaxErrors)
	}
	type key struct{ name, qual, recv string }
	got := map[key]model.RefKind{}
	for _, r := range res.Facts.Refs {
		got[key{r.Name, r.Qualifier, r.Receiver}] = r.Kind
	}
	want := map[key]model.RefKind{
		{"helper", "", ""}:       model.RefCall,
		{"run", "", "o"}:         model.RefCall,
		{"go", "Util", ""}:       model.RefCall,
		{"Util", "q", ""}:        model.RefType,
		{"go", "q.Util", ""}:     model.RefCall,
		{"size", "", "MAX"}:      model.RefCall,
		{"go", "", "this.field"}: model.RefCall,
		{"A", "", ""}:            model.RefCall,
		{"m", "", "new A()"}:     model.RefCall,
		{"m", "", "this"}:        model.RefCall,
		{"Other", "", ""}:        model.RefType,
	}
	for k, kind := range want {
		if got[k] != kind {
			t.Errorf("ref %+v: got kind %v, want %v (all: %v)", k, got[k], kind, res.Facts.Refs)
		}
	}
	if _, ok := got[key{"q", "", ""}]; ok {
		t.Error("package segment q reported as a reference")
	}
	if n := len(res.Facts.Inherits); n != 2 {
		t.Errorf("inherits = %v", res.Facts.Inherits)
	}
}

// javaGarbage returns inputs that stress error recovery: truncations and
// random mutations of the golden sources plus raw noise.
func javaGarbage(t testing.TB) [][]byte {
	files, _ := filepath.Glob(filepath.Join(goldenRoot, "java", "*.java"))
	var seeds [][]byte
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		seeds = append(seeds, b)
	}
	seeds = append(seeds,
		[]byte("class {"), []byte("package ;import static .*;"), []byte("@interface"),
		[]byte("record R(int x) { R { } }"), []byte("enum E { A { void f( } }"),
		[]byte("class A { void m() { a.b.c.d(); new (); x::; } }"),
		[]byte("\x00\xff\xfe class é { }"),
	)
	rng := rand.New(rand.NewSource(7))
	var out [][]byte
	for _, s := range seeds {
		out = append(out, s)
		for i := 0; i < 8 && len(s) > 0; i++ {
			out = append(out, s[:rng.Intn(len(s))])
			m := append([]byte(nil), s...)
			for j := 0; j < 5; j++ {
				m[rng.Intn(len(m))] = "{}();.<>@\"'/*\n x"[rng.Intn(16)]
			}
			out = append(out, m)
		}
	}
	return out
}

func checkJavaInput(t *testing.T, e *Engine, src []byte) {
	t.Helper()
	res, err := e.ParseLang(context.Background(), model.LangJava, src)
	if err != nil {
		t.Fatalf("parse %q: %v", src, err)
	}
	if err := res.Facts.Validate(); err != nil {
		t.Fatalf("invalid facts for %q: %v", src, err)
	}
	for _, d := range res.Facts.Defs {
		if len(d.Sig) > model.MaxSigLen {
			t.Fatalf("sig too long: %q", d.Sig)
		}
	}
	_ = DumpFacts(res.Facts)
}

func TestJavaGarbageInput(t *testing.T) {
	e := NewEngine()
	defer e.Close()
	for _, src := range javaGarbage(t) {
		checkJavaInput(t, e, src)
	}
}

func FuzzParseJava(f *testing.F) {
	for _, s := range javaGarbage(f) {
		f.Add(s)
	}
	e := NewEngine()
	f.Cleanup(e.Close)
	f.Fuzz(func(t *testing.T, src []byte) {
		checkJavaInput(t, e, src)
		_ = JavaPackageOf(src)
	})
}
