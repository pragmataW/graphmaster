package model

import (
	"testing"
)

func TestLangStringRoundTrip(t *testing.T) {
	for _, l := range AllLangs() {
		got, err := ParseLang(l.String())
		if err != nil {
			t.Fatalf("ParseLang(%q): %v", l.String(), err)
		}
		if got != l {
			t.Fatalf("ParseLang(%q) = %v, want %v", l.String(), got, l)
		}
		if !l.Valid() {
			t.Fatalf("%v should be valid", l)
		}
	}
	if len(AllLangs()) != 11 {
		t.Fatalf("AllLangs() = %d langs, want 11", len(AllLangs()))
	}
}

func TestLangStableValues(t *testing.T) {
	// Values are persisted in graph.db; they must never be renumbered.
	want := map[Lang]uint8{
		LangUnknown: 0, LangGo: 1, LangPython: 2, LangTypeScript: 3, LangTSX: 4,
		LangJavaScript: 5, LangRust: 6, LangJava: 7, LangC: 8, LangCPP: 9,
		LangCSharp: 10, LangPHP: 11,
	}
	for l, v := range want {
		if uint8(l) != v {
			t.Errorf("%v = %d, want %d", l, uint8(l), v)
		}
	}
}

func TestParseLangAliases(t *testing.T) {
	cases := map[string]Lang{
		"golang": LangGo, "GO": LangGo, "py": LangPython, "ts": LangTypeScript,
		"js": LangJavaScript, "rs": LangRust, "c++": LangCPP, "cxx": LangCPP,
		"cs": LangCSharp, "c#": LangCSharp, " php ": LangPHP, "Tsx": LangTSX, //nolint:gocritic // surrounding whitespace is part of the test
	}
	for in, want := range cases {
		got, err := ParseLang(in)
		if err != nil || got != want {
			t.Errorf("ParseLang(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "cobol", "unknown"} {
		if _, err := ParseLang(bad); err == nil {
			t.Errorf("ParseLang(%q) should fail", bad)
		}
	}
}

func TestLangFromPath(t *testing.T) {
	cases := []struct {
		path string
		want Lang
	}{
		{"main.go", LangGo},
		{"pkg/a/b_test.go", LangGo},
		{"x.py", LangPython},
		{"stubs/x.pyi", LangPython},
		{"src/app.ts", LangTypeScript},
		{"types/index.d.ts", LangTypeScript},
		{"esm.mts", LangTypeScript},
		{"cjs.cts", LangTypeScript},
		{"ui/App.tsx", LangTSX},
		{"a.js", LangJavaScript},
		{"a.mjs", LangJavaScript},
		{"a.cjs", LangJavaScript},
		{"a.jsx", LangJavaScript},
		{"lib.rs", LangRust},
		{"Foo.java", LangJava},
		{"a.c", LangC},
		{"a.h", LangC},
		{"a.cpp", LangCPP},
		{"a.cc", LangCPP},
		{"a.cxx", LangCPP},
		{"a.hpp", LangCPP},
		{"a.hh", LangCPP},
		{"a.hxx", LangCPP},
		{"A.C", LangCPP},
		{"A.H", LangCPP},
		{"Program.cs", LangCSharp},
		{"index.php", LangPHP},
		{"MAIN.GO", LangGo},
		{"README.md", LangUnknown},
		{"Makefile", LangUnknown},
		{".gitignore", LangUnknown},
		{"dir.go/file", LangUnknown},
		{`win\path\x.rs`, LangRust},
	}
	for _, c := range cases {
		if got := LangFromPath(c.path); got != c.want {
			t.Errorf("LangFromPath(%q) = %v, want %v", c.path, got, c.want)
		}
		if got := IsCodePath(c.path); got != (c.want != LangUnknown) {
			t.Errorf("IsCodePath(%q) = %v", c.path, got)
		}
	}
}

func TestLangExtensionsConsistent(t *testing.T) {
	seen := map[string]Lang{}
	for _, l := range AllLangs() {
		exts := l.Extensions()
		if len(exts) == 0 {
			t.Fatalf("%v has no extensions", l)
		}
		for _, e := range exts {
			if prev, dup := seen[e]; dup {
				t.Fatalf("extension %q mapped to both %v and %v", e, prev, l)
			}
			seen[e] = l
			if got := LangFromExt(e); got != l {
				t.Errorf("LangFromExt(%q) = %v, want %v", e, got, l)
			}
		}
	}
	// Returned slices must be copies.
	LangGo.Extensions()[0] = ".mutated"
	if LangFromExt(".go") != LangGo {
		t.Fatal("Extensions() leaked internal slice")
	}
}

func TestLangUnknownString(t *testing.T) {
	if LangUnknown.String() != "unknown" {
		t.Fatalf("got %q", LangUnknown.String())
	}
	if Lang(200).Valid() {
		t.Fatal("Lang(200) must be invalid")
	}
	if Lang(200).String() != "lang(200)" {
		t.Fatalf("got %q", Lang(200).String())
	}
}

func TestLangTextMarshal(t *testing.T) {
	b, err := LangTSX.MarshalText()
	if err != nil || string(b) != "tsx" {
		t.Fatalf("MarshalText = %q, %v", b, err)
	}
	var l Lang
	if err := l.UnmarshalText([]byte("csharp")); err != nil || l != LangCSharp {
		t.Fatalf("UnmarshalText = %v, %v", l, err)
	}
	if err := l.UnmarshalText([]byte("nope")); err == nil {
		t.Fatal("expected error")
	}
}
