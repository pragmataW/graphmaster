package steer

import (
	"reflect"
	"testing"
)

func TestClassifyPatternSymbolLike(t *testing.T) {
	// pattern -> expected Norm (sorted unique identifier tokens).
	cases := []struct {
		pat  string
		norm string
	}{
		// Bare identifiers.
		{"HandleRequest", "HandleRequest"},
		{"handle_request", "handle_request"},
		{"parseArgs", "parseArgs"},
		{"Foo", "Foo"},
		{"foo", "foo"},
		{"_private", "_private"},
		{"__init__", "__init__"},
		{"MAX_SIZE", "MAX_SIZE"},
		{"Config2", "Config2"},
		{"x86_64", "x86_64"},
		{"error", "error"},
		{"$scope", "scope"},
		{"abc", "abc"},
		// Qualified names.
		{"pkg.Func", "Func pkg"},
		{"http.HandlerFunc", "HandlerFunc http"},
		{"os.path.join", "join os path"},
		{"std::vector", "std vector"},
		{"Foo::bar", "Foo bar"},
		{"ns::Klass::method", "Klass method ns"},
		{"Array#map", "Array map"},
		{"obj->method", "method obj"},
		{"$this->repo", "repo this"},
		{`pkg\.Func`, "Func pkg"},
		// Call forms.
		{"NewServer(", "NewServer"},
		{`NewServer\(`, "NewServer"},
		{"NewServer()", "NewServer"},
		{"s.Handle(", "Handle s"},
		{`fmt\.Println\(`, "Println fmt"},
		// Anchors and word boundaries are stripped.
		{"^Server", "Server"},
		{"^HandleRequest$", "HandleRequest"},
		{`\bFoo\b`, "Foo"},
		{`\<Foo\>`, "Foo"},
		{"(?i)handler", "handler"},
		{`^\s*def foo`, "foo"},
		// Keyword prefixes.
		{"func HandleRequest", "HandleRequest"},
		{"func NewServer(", "NewServer"},
		{"def parse_args", "parse_args"},
		{"class UserService", "UserService"},
		{"fn main", "main"},
		{"type Config", "Config"},
		{"type Config struct", "Config"},
		{"struct Point", "Point"},
		{"interface Reader", "Reader"},
		{"trait Display", "Display"},
		{"impl Display", "Display"},
		{"enum Color", "Color"},
		{"const MaxSize", "MaxSize"},
		{"let counter", "counter"},
		{"var handler", "handler"},
		{"function render", "render"},
		{"public class Foo", "Foo"},
		{"private void", "void"},
		{"namespace App", "App"},
		{"pub fn new", "new"},
		{"export function render", "render"},
		{"async def fetch", "fetch"},
		{`func\s+Handle`, "Handle"},
		{`def\s+parse_args\(`, "parse_args"},
		{`class\s+Foo`, "Foo"},
		{"func  Handle", "Handle"},
		{`fn +parse`, "parse"},
		{"package main", "main"},
		// Alternation.
		{"Foo|Bar", "Bar Foo"},
		{"Bar|Foo", "Bar Foo"},
		{`Foo\|Bar`, "Bar Foo"},
		{"(Foo|Bar)", "Bar Foo"},
		{"(?:Foo|Bar)", "Bar Foo"},
		{"func Foo|func Bar", "Bar Foo"},
		{"NewServer|NewClient|NewProxy", "NewClient NewProxy NewServer"},
		{"Foo|Foo", "Foo"},
		{"pkg.Foo|other.Bar", "Bar Foo other pkg"},
		{"\\bFoo\\b|\\bBar\\b", "Bar Foo"},
		// .* joined identifier fragments.
		{"get.*User", "User get"},
		{"Handle.*Request", "Handle Request"},
		{".*Handler", "Handler"},
		{"Handler.*", "Handler"},
		{"func.*Handler", "Handler"},
		{"New.*Server.*Option", "New Option Server"},
		{"a.b.*Client", "Client a b"},
		// Surrounding whitespace.
		{"  HandleRequest  ", "HandleRequest"},
	}
	for _, c := range cases {
		got := ClassifyPattern(c.pat)
		if !got.SymbolLike {
			t.Errorf("ClassifyPattern(%q): SymbolLike=false (reason %q), want true", c.pat, got.Reason)
			continue
		}
		if got.Norm != c.norm {
			t.Errorf("ClassifyPattern(%q).Norm = %q, want %q", c.pat, got.Norm, c.norm)
		}
	}
}

func TestClassifyPatternNonSymbol(t *testing.T) {
	cases := []string{
		// Whitespace between non-keyword words.
		"hello world",
		"return nil",
		"if err != nil",
		"user not found",
		"Foo Bar",
		"func (s *Server) Handle",
		"func Foo Bar",
		// Quotes and punctuation.
		`"hello"`,
		"'abc'",
		"foo,bar",
		"foo;",
		"foo:bar",
		"a=b",
		"x == y",
		"Foo<T>",
		"println!",
		"@Override",
		"foo@bar",
		"#include",
		"key: value",
		"foo%",
		"err != nil",
		"foo&&bar",
		// Markers.
		"TODO",
		"FIXME",
		"XXX",
		"HACK",
		"TODO:",
		"TODO(alice)",
		"TODO|FIXME",
		"Foo|TODO",
		// URLs.
		"https://example.com",
		"http://localhost:8080",
		"www.example.com",
		"git@github.com:x/y",
		// Paths.
		"src/main",
		"internal/steer",
		"./foo",
		"/usr/bin",
		"a/b.go",
		// kebab-case.
		"foo-bar",
		"my-component",
		"--verbose",
		"-v",
		// Too short.
		"",
		" ",
		"a",
		"id",
		"ok",
		"x(",
		"fn x",
		"a.b",
		// Digits only.
		"123",
		"0",
		"42|43",
		"8080",
		"1.2.3",
		// Character classes / escapes.
		"[A-Z]",
		"[a-z]+",
		"Foo[0-9]",
		`\w+`,
		`\d+`,
		`Foo\w*`,
		`foo\sbar`,
		`\S+`,
		`foo\nbar`,
		`\x41BC`,
		`\p{Lu}`,
		// Quantifiers.
		"Foo+",
		"Foo?",
		"Foo*",
		"Fo{2}",
		"a{1,3}",
		"colou?r",
		"(Foo)+",
		"Foo.+Bar",
		"Foo.?",
		// Lookarounds and groups.
		"(?=Foo)",
		"(?!Foo)Bar",
		"(?<=x)Foo",
		"(?<!x)Foo",
		"(?P<name>Foo)",
		"(Foo",
		"Foo)",
		// Wildcard dot alone, odd separators.
		".",
		".*",
		"..",
		"Foo..Bar",
		"Foo.",
		".Foo",
		"Foo::",
		"::Foo",
		"Foo->",
		"#Foo",
		"Foo|",
		"|Foo",
		"Foo||Bar",
		// Escapes at end, odd unicode.
		`Foo\`,
		"héllo",
		"Foo\tBar",
		"日本語",
		// Keywords with nothing else meaningful.
		"func",
		"def ",
		"public static",
		"class Foo extends Bar",
	}
	for _, p := range cases {
		got := ClassifyPattern(p)
		if got.SymbolLike {
			t.Errorf("ClassifyPattern(%q): SymbolLike=true (norm %q), want false", p, got.Norm)
		}
		if got.Reason == "" {
			t.Errorf("ClassifyPattern(%q): empty Reason for non-symbol", p)
		}
	}
}

func TestClassifyPatternIdentsAndQueries(t *testing.T) {
	cases := []struct {
		pat     string
		idents  []string
		queries []string
	}{
		{"pkg.Func", []string{"pkg", "Func"}, []string{"pkg.Func"}},
		{"Foo::bar(", []string{"Foo", "bar"}, []string{"Foo::bar"}},
		{"Foo|Bar", []string{"Foo", "Bar"}, []string{"Foo", "Bar"}},
		{"get.*User", []string{"get", "User"}, []string{"get", "User"}},
		{"a.b.*Client", []string{"a", "b", "Client"}, []string{"a.b", "Client"}},
		{"func Foo|func Foo", []string{"Foo"}, []string{"Foo"}},
		{"$this->repo", []string{"this", "repo"}, []string{"$this->repo"}},
		{`^\s*class Foo\b`, []string{"Foo"}, []string{"Foo"}},
	}
	for _, c := range cases {
		got := ClassifyPattern(c.pat)
		if !reflect.DeepEqual(got.Idents, c.idents) {
			t.Errorf("ClassifyPattern(%q).Idents = %q, want %q", c.pat, got.Idents, c.idents)
		}
		if !reflect.DeepEqual(got.Queries, c.queries) {
			t.Errorf("ClassifyPattern(%q).Queries = %q, want %q", c.pat, got.Queries, c.queries)
		}
	}
}

func TestClassifyPatternNonSymbolStillExtractsIdents(t *testing.T) {
	got := ClassifyPattern("user not found")
	if got.SymbolLike {
		t.Fatal("want non-symbol")
	}
	want := []string{"user", "not", "found"}
	if !reflect.DeepEqual(got.Idents, want) {
		t.Errorf("Idents = %q, want %q", got.Idents, want)
	}
	if got.Norm != "found not user" {
		t.Errorf("Norm = %q", got.Norm)
	}
}

func TestClassifyFixed(t *testing.T) {
	cases := []struct {
		pat  string
		want bool
	}{
		{"HandleRequest", true},
		{"pkg.Func", true},
		{"NewServer(", true},
		{"Foo|Bar", false}, // literal pipe in -F mode
		{"^Foo", false},    // literal caret
		{"Foo$", false},
		{`Foo\b`, false},
		{"Foo.*Bar", false}, // literal ".*"
		{"func Foo", true},
		{"Foo\nBar", true}, // newline separates fixed patterns
		{"hello world", false},
		{"TODO", false},
	}
	for _, c := range cases {
		if got := ClassifyFixed(c.pat); got.SymbolLike != c.want {
			t.Errorf("ClassifyFixed(%q).SymbolLike = %v (reason %q), want %v", c.pat, got.SymbolLike, got.Reason, c.want)
		}
	}
}

func TestClassifyPatterns(t *testing.T) {
	got := ClassifyPatterns([]string{"Foo", "pkg.Bar"}, false)
	if !got.SymbolLike || got.Norm != "Bar Foo pkg" {
		t.Errorf("ClassifyPatterns = %+v", got)
	}
	got = ClassifyPatterns([]string{"Foo", "hello world"}, false)
	if got.SymbolLike {
		t.Error("one non-symbol pattern should make the set non-symbol")
	}
	if ClassifyPatterns(nil, false).SymbolLike {
		t.Error("no patterns should be non-symbol")
	}
}

func TestNormIgnoresOrderAndDuplicates(t *testing.T) {
	a := ClassifyPattern("Foo|Bar|Foo").Norm
	b := ClassifyPattern("(Bar|Foo)").Norm
	c := ClassifyPattern(`\bBar\b|\bFoo\b`).Norm
	if a != b || b != c {
		t.Errorf("norms differ: %q %q %q", a, b, c)
	}
	if ClassifyPattern("foo").Norm == ClassifyPattern("Foo").Norm {
		t.Error("norm must keep case")
	}
}

func FuzzClassifyPattern(f *testing.F) {
	for _, s := range []string{"Foo", "pkg.Func(", "Foo|Bar", `\bx\b`, "(?i)a", "func Foo", "a.*b", `\`, "((", "(?:", "|||"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		r := ClassifyPattern(s)
		if r.SymbolLike && len(r.Idents) == 0 {
			t.Fatalf("symbol-like %q without idents", s)
		}
		_ = ClassifyFixed(s)
	})
}
