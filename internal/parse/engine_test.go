package parse

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/pragmataW/graphmaster/internal/model"
)

func parseGo(t *testing.T, src string) Result {
	t.Helper()
	e := NewEngine()
	defer e.Close()
	res, err := e.ParseLang(context.Background(), model.LangGo, []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if err := res.Facts.Validate(); err != nil {
		t.Fatalf("invalid facts: %v", err)
	}
	return res
}

func defByName(f *model.Facts, qname string) (int32, *model.Def) {
	for i := range f.Defs {
		if strings.Join(f.QNameParts(int32(i)), ".") == qname {
			return int32(i), &f.Defs[i]
		}
	}
	return -1, nil
}

func TestEngineSyntaxErrorsBestEffort(t *testing.T) {
	res := parseGo(t, "package p\n\nfunc Good() {}\n\nfunc Bad( {\n")
	if !res.SyntaxErrors {
		t.Fatal("expected SyntaxErrors")
	}
	if _, d := defByName(res.Facts, "Good"); d == nil {
		t.Fatalf("Good not extracted from erroneous file:\n%s", DumpFacts(res.Facts))
	}
}

func TestEngineEmptyAndCancelled(t *testing.T) {
	res := parseGo(t, "")
	if len(res.Facts.Defs) != 0 || res.Facts.Lang != model.LangGo {
		t.Fatalf("empty source: %+v", res.Facts)
	}
	e := NewEngine()
	defer e.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.ParseLang(ctx, model.LangGo, []byte("package p")); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want Canceled", err)
	}
	// The engine stays usable after a cancelled parse.
	if _, err := e.ParseLang(context.Background(), model.LangGo, []byte("package p")); err != nil {
		t.Fatal(err)
	}
}

func TestEngineClosed(t *testing.T) {
	e := NewEngine()
	if _, err := e.ParseLang(context.Background(), model.LangGo, []byte("package p")); err != nil {
		t.Fatal(err)
	}
	e.Close()
	e.Close() // idempotent
	if _, err := e.ParseLang(context.Background(), model.LangGo, nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("err = %v, want ErrClosed", err)
	}
	if _, err := NewEngine().ParseLang(context.Background(), model.Lang(200), nil); err == nil {
		t.Fatal("unregistered language must fail")
	}
}

func TestEngineSpecSwap(t *testing.T) {
	// A different query for the same language rebuilds the engine state.
	e := NewEngine()
	defer e.Close()
	fake := fakeSpec{lang: model.LangGo, query: "(function_declaration name: (identifier) @name) @def.function"}
	res, err := e.Parse(context.Background(), fake, []byte("package p\nfunc A() { B() }\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Facts.Defs) != 1 || len(res.Facts.Refs) != 0 {
		t.Fatalf("fake query facts: %s", DumpFacts(res.Facts))
	}
	res, err = e.ParseLang(context.Background(), model.LangGo, []byte("package p\nfunc A() { B() }\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Facts.Refs) != 1 {
		t.Fatalf("real query facts: %s", DumpFacts(res.Facts))
	}
}

func TestGoMethodAttachment(t *testing.T) {
	f := parseGo(t, `package p
func (s *S) A() {}
type S struct{ x int }
func (o Other) B() {}
func (l List[K, V]) C(k K) V { var z V; return z }
type List[K comparable, V any] struct{}
`).Facts
	si, s := defByName(f, "S")
	if s == nil {
		t.Fatal("S missing")
	}
	ai, a := defByName(f, "S.A")
	if a == nil || a.Parent != si || len(a.Scope) != 0 || ai < si {
		t.Fatalf("S.A not attached after S: %s", DumpFacts(f))
	}
	if _, b := defByName(f, "Other.B"); b == nil || b.Parent != model.NoParent || !reflect.DeepEqual(b.Scope, []string{"Other"}) {
		t.Fatalf("Other.B scope wrong: %s", DumpFacts(f))
	}
	if _, c := defByName(f, "List.C"); c == nil {
		t.Fatalf("generic receiver not attached: %s", DumpFacts(f))
	}
	for _, r := range f.Refs {
		if r.Name == "K" || r.Name == "V" {
			t.Errorf("type parameter %s leaked as a reference", r.Name)
		}
	}
}

func TestGoSignatureTruncated(t *testing.T) {
	params := strings.Repeat("a int, ", 80)
	f := parseGo(t, "package p\nfunc Long("+params+"z int) {}\n").Facts
	sig := f.Defs[0].Sig
	if len(sig) > model.MaxSigLen || !strings.HasSuffix(sig, "…") || !strings.HasPrefix(sig, "func Long(a int, ") {
		t.Fatalf("sig not truncated: %q (%d bytes)", sig, len(sig))
	}
}

func TestGoQualifierVersusReceiver(t *testing.T) {
	f := parseGo(t, `package p
import (
	"net/http"
	yaml "gopkg.in/yaml.v3"
	"github.com/x/go-thing/v2"
)
func F(c *http.Client) {
	http.Get("")
	c.Do(nil)
	yaml.Marshal(nil)
	thing.New()
	len(nil)
}
`).Facts
	want := map[string][2]string{ // name -> {qualifier, receiver}
		"Get": {"http", ""}, "Do": {"", "c"}, "Marshal": {"yaml", ""}, "New": {"thing", ""},
	}
	got := map[string][2]string{}
	for _, r := range f.Refs {
		if r.Kind == model.RefCall {
			got[r.Name] = [2]string{r.Qualifier, r.Receiver}
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
}

func TestGoPackageName(t *testing.T) {
	cases := map[string]string{
		"fmt": "fmt", "net/http": "http", "gopkg.in/yaml.v3": "yaml",
		"github.com/x/y/v2": "y", "github.com/tree-sitter/go-tree-sitter": "tree_sitter",
		"github.com/a/b-go": "b", "v2": "v2",
	}
	for in, want := range cases {
		if got := goPackageName(in); got != want {
			t.Errorf("goPackageName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGoTestTarget(t *testing.T) {
	cases := map[string]string{
		"TestParse": "Parse", "TestServer_Start": "Server.Start", "BenchmarkX": "X",
		"FuzzY": "Y", "Test": "", "Test_a_b": "a.b",
	}
	for in, want := range cases {
		if got := goTestTarget(in); got != want {
			t.Errorf("goTestTarget(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGoModuleOfAndResolveImport(t *testing.T) {
	repo := model.NewMemRepo(map[string]string{
		"go.mod":                 "// comment\nmodule example.com/app // trailing\n\ngo 1.25\n",
		"main.go":                "package main",
		"internal/store/db.go":   "package store",
		"tools/go.mod":           "module \"example.com/tools\"\n",
		"tools/gen/gen.go":       "package gen",
		"nomod/../internal/x.go": "",
	})
	g := goSpec{}
	mods := map[string]string{
		"main.go":              "example.com/app",
		"internal/store/db.go": "example.com/app/internal/store",
		"tools/gen/gen.go":     "example.com/tools/gen",
	}
	for p, want := range mods {
		if got := g.ModuleOf(p, nil, repo); got != want {
			t.Errorf("ModuleOf(%q) = %q, want %q", p, got, want)
		}
	}
	if got := g.ModuleOf("internal/store/db_test.go", &model.Facts{Package: "store_test"}, repo); got != "example.com/app/internal/store_test" {
		t.Errorf("external test package module = %q", got)
	}
	if got := g.ModuleOf("a/b.go", nil, model.NewMemRepo(nil)); got != "a" {
		t.Errorf("no go.mod: %q", got)
	}
	if got := g.ModuleOf("b.go", nil, nil); got != "." {
		t.Errorf("root without ctx: %q", got)
	}

	cases := []struct {
		spec, from string
		want       model.ImportTarget
	}{
		{"example.com/app/internal/store", "main.go", model.ImportTarget{Kind: model.TargetModule, Path: "internal/store", Module: "example.com/app/internal/store"}},
		{"example.com/app", "internal/store/db.go", model.ImportTarget{Kind: model.TargetModule, Path: "", Module: "example.com/app"}},
		{"example.com/app/missing", "main.go", model.ImportTarget{Kind: model.TargetUnresolved, Module: "example.com/app/missing"}},
		{"example.com/application", "main.go", model.ImportTarget{Kind: model.TargetExternal, Module: "example.com/application"}},
		{"fmt", "main.go", model.ImportTarget{Kind: model.TargetExternal, Module: "fmt"}},
		{"example.com/tools/gen", "tools/gen/gen.go", model.ImportTarget{Kind: model.TargetModule, Path: "tools/gen", Module: "example.com/tools/gen"}},
	}
	for _, c := range cases {
		if got := g.ResolveImport(c.spec, c.from, repo); got != c.want {
			t.Errorf("ResolveImport(%q, %q) = %+v, want %+v", c.spec, c.from, got, c.want)
		}
	}
}

// FuzzParseGo checks that arbitrary input never panics the extractor and
// always yields structurally valid facts that survive the codec.
func FuzzParseGo(f *testing.F) {
	f.Add([]byte("package p\nfunc (s *S) M() { s.x.Y(); pkg.Z[int]() }\ntype S struct{ A; *b.C }\n"))
	f.Add([]byte("package p\nfunc TestX(t *testing.T) { func() { type q struct{ r int } }() }\n"))
	f.Add([]byte("package\x00 (((func"))
	e := NewEngine()
	f.Cleanup(e.Close)
	f.Fuzz(func(t *testing.T, src []byte) {
		res, err := e.ParseLang(context.Background(), model.LangGo, src)
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

func BenchmarkParseGo(b *testing.B) {
	src := genGoSource(1)
	for range 6 {
		src = append(src, src...)
	}
	src = append([]byte("package p\n"), bytes.ReplaceAll(src, []byte("package p1\n"), nil)...)
	e := NewEngine()
	defer e.Close()
	b.SetBytes(int64(len(src)))
	for b.Loop() {
		if _, err := e.ParseLang(context.Background(), model.LangGo, src); err != nil {
			b.Fatal(err)
		}
	}
}
