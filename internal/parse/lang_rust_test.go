package parse

import (
	"context"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/pragmataW/graphmaster/internal/model"
)

func parseRust(t testing.TB, src string) *model.Facts {
	t.Helper()
	e := NewEngine()
	defer e.Close()
	res, err := e.ParseLang(context.Background(), model.LangRust, []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if err := res.Facts.Validate(); err != nil {
		t.Fatalf("invalid facts: %v\n%s", err, DumpFacts(res.Facts))
	}
	return res.Facts
}

// rustDef finds a def by its dotted qualified name.
func rustDef(t *testing.T, f *model.Facts, qname string) *model.Def {
	t.Helper()
	for i := range f.Defs {
		if strings.Join(f.QNameParts(int32(i)), ".") == qname {
			return &f.Defs[i]
		}
	}
	t.Fatalf("no def %s in\n%s", qname, DumpFacts(f))
	return nil
}

func rustFixtureRepo() *model.MemRepo {
	return model.NewMemRepo(map[string]string{
		"rust/Cargo.toml": "[package]\nname = \"fixture-shapes\"\n\n[lib]\npath = \"src/lib.rs\"\n\n" +
			"[[bin]]\nname = \"shapes\"\npath = \"src/main.rs\"\n",
		"rust/src/lib.rs":              "pub mod geometry;\npub mod shapes;\npub use shapes::Shape;\npub use shapes::square::*;\n",
		"rust/src/main.rs":             "fn main() {}\n",
		"rust/src/geometry.rs":         "pub fn square_of(x: f64) -> f64 { x * x }\n",
		"rust/src/shapes/mod.rs":       "pub mod circle;\npub mod square;\npub trait Shape {}\n",
		"rust/src/shapes/circle.rs":    "pub struct Circle;\n",
		"rust/src/shapes/square.rs":    "pub struct Square;\n",
		"rust/src/bin/tool.rs":         "fn main() {}\n",
		"rust/src/bin/multi/main.rs":   "mod cli;\nfn main() {}\n",
		"rust/src/bin/multi/cli.rs":    "pub fn run() {}\n",
		"rust/tests/area_test.rs":      "#[test]\nfn t() {}\n",
		"rust/tests/common/mod.rs":     "pub fn setup() {}\n",
		"rust/build.rs":                "fn main() {}\n",
		"binonly/Cargo.toml":           "[package]\nname = \"bin-only\"\n",
		"binonly/src/main.rs":          "mod cmd;\nfn main() {}\n",
		"binonly/src/cmd.rs":           "pub fn go() {}\n",
		"loose/a/b.rs":                 "fn f() {}\n",
		"broken/Cargo.toml":            "[package\nname = = \n",
		"broken/src/lib.rs":            "pub fn x() {}\n",
		"broken/src/inner/deep/mod.rs": "pub fn y() {}\n",
		// ripgrep layout: targets rooted outside src/.
		"custom/Cargo.toml": "[package]\nname = \"custom-tool\"\n\n[lib]\npath = \"lib/root.rs\"\n\n" +
			"[[bin]]\nname = \"rg\"\npath = \"crates/core/main.rs\"\n",
		"custom/crates/core/main.rs":      "mod search;\nfn main() {}\n",
		"custom/crates/core/search.rs":    "pub fn run() {}\n",
		"custom/crates/core/flags/mod.rs": "pub fn parse() {}\n",
		"custom/lib/root.rs":              "pub mod util;\n",
		"custom/lib/util.rs":              "pub fn u() {}\n",
		"custom/crates/other/x.rs":        "fn x() {}\n",
		"custom/tests/t.rs":               "#[test]\nfn t() {}\n",
		// Workspace with path dependencies between members.
		"ws/Cargo.toml": "[workspace]\nmembers = [\"crates/*\"]\n\n" +
			"[workspace.dependencies]\nws-core = { path = \"crates/core-lib\", version = \"1\" }\n",
		"ws/crates/core-lib/Cargo.toml":  "[package]\nname = \"ws-core\"\n",
		"ws/crates/core-lib/src/lib.rs":  "pub mod sink;\npub use crate::sink::Sink;\n",
		"ws/crates/core-lib/src/sink.rs": "pub trait Sink {}\n",
		"ws/crates/unixdep/Cargo.toml":   "[package]\nname = \"unixdep\"\n",
		"ws/crates/unixdep/src/lib.rs":   "pub fn x() {}\n",
		"ws/crates/nolib/Cargo.toml":     "[package]\nname = \"nolib\"\n",
		"ws/crates/nolib/src/main.rs":    "fn main() {}\n",
		"ws/crates/printer/src/lib.rs":   "use ws_core::Sink;\n",
		"ws/crates/printer/Cargo.toml": "[package]\nname = \"ws-printer\"\n\n[dependencies]\n" +
			"ws-core = { workspace = true }\nrenamed = { path = \"../core-lib\", package = \"ws-core\" }\n" +
			"nolib = { path = \"../nolib\" }\nserde = \"1\"\n\n" +
			"[target.'cfg(unix)'.dependencies]\nunixdep = { path = \"../unixdep\" }\n",
	})
}

func TestRustRegistered(t *testing.T) {
	spec, ok := ForPath("src/lib.rs")
	if !ok || spec.Lang() != model.LangRust {
		t.Fatalf("ForPath(.rs) = %v, %v", spec, ok)
	}
	if spec.ExtractorVersion() < 1 {
		t.Fatal("extractor version must be positive")
	}
	if _, err := compileSpec(spec); err != nil {
		t.Fatal(err)
	}
}

func TestRustIsTestPath(t *testing.T) {
	cases := map[string]bool{
		"rust/tests/area_test.rs": true,
		"tests/common/mod.rs":     true,
		"src/tests.rs":            true,
		"src/parser_test.rs":      true,
		"src/parser_tests.rs":     true,
		`crate\tests\it.rs`:       true,
		"src/lib.rs":              false,
		"src/testing.rs":          false,
		"src/contests/mod.rs":     false,
		"tests/fixture.txt":       false,
		"src/attest.rs":           false,
	}
	for p, want := range cases {
		if got := IsTestPath(p); got != want {
			t.Errorf("IsTestPath(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestRustModuleOf(t *testing.T) {
	rc := rustFixtureRepo()
	spec, _ := Lookup(model.LangRust)
	cases := map[string]string{
		"rust/src/lib.rs":                 "fixture_shapes",
		"rust/src/geometry.rs":            "fixture_shapes::geometry",
		"rust/src/shapes/mod.rs":          "fixture_shapes::shapes",
		"rust/src/shapes/circle.rs":       "fixture_shapes::shapes::circle",
		"rust/src/main.rs":                "shapes",
		"rust/src/bin/tool.rs":            "tool",
		"rust/src/bin/multi/main.rs":      "multi",
		"rust/src/bin/multi/cli.rs":       "multi::cli",
		"rust/tests/area_test.rs":         "area_test",
		"rust/tests/common/mod.rs":        "common",
		"rust/build.rs":                   "build_script_build",
		"binonly/src/main.rs":             "bin_only",
		"binonly/src/cmd.rs":              "bin_only::cmd",
		"loose/a/b.rs":                    "crate::loose::a::b",
		"broken/src/lib.rs":               "broken",
		"broken/src/inner/deep/mod.rs":    "broken::inner::deep",
		"broken/src/inner/deep/../x.rs":   "broken::inner::x",
		"custom/crates/core/main.rs":      "rg",
		"custom/crates/core/search.rs":    "rg::search",
		"custom/crates/core/flags/mod.rs": "rg::flags",
		"custom/lib/root.rs":              "custom_tool",
		"custom/lib/util.rs":              "custom_tool::util",
		"custom/crates/other/x.rs":        "custom_tool::crates::other::x",
		"custom/tests/t.rs":               "t",
	}
	for p, want := range cases {
		if got := spec.ModuleOf(p, nil, rc); got != want {
			t.Errorf("ModuleOf(%q) = %q, want %q", p, got, want)
		}
	}
	// No repository context: path-based identity, never a panic.
	if got := spec.ModuleOf("src/a/b.rs", nil, nil); got != "crate::a::b" {
		t.Errorf("ModuleOf without ctx = %q", got)
	}
}

func TestRustResolveImport(t *testing.T) {
	rc := rustFixtureRepo()
	spec, _ := Lookup(model.LangRust)
	type want struct {
		kind   model.ImportTargetKind
		path   string
		module string
	}
	file := func(p, m string) want { return want{model.TargetFile, p, m} }
	ext := func(m string) want { return want{model.TargetExternal, "", m} }
	cases := []struct {
		spec, from string
		want       want
	}{
		{"crate::geometry::square_of", "rust/src/shapes/circle.rs", file("rust/src/geometry.rs", "fixture_shapes::geometry")},
		{"crate::geometry", "rust/src/shapes/square.rs", file("rust/src/geometry.rs", "fixture_shapes::geometry")},
		{"super::Shape", "rust/src/shapes/circle.rs", file("rust/src/shapes/mod.rs", "fixture_shapes::shapes")},
		{"super", "rust/src/geometry.rs", file("rust/src/lib.rs", "fixture_shapes")},
		{"self::circle", "rust/src/shapes/mod.rs", file("rust/src/shapes/circle.rs", "fixture_shapes::shapes::circle")},
		{"self", "rust/src/geometry.rs", file("rust/src/geometry.rs", "fixture_shapes::geometry")},
		{"circle::Circle", "rust/src/shapes/mod.rs", file("rust/src/shapes/circle.rs", "fixture_shapes::shapes::circle")},
		{"shapes::Shape", "rust/src/lib.rs", file("rust/src/shapes/mod.rs", "fixture_shapes::shapes")},
		{"geometry", "rust/src/shapes/circle.rs", file("rust/src/geometry.rs", "fixture_shapes::geometry")}, // 2015 path
		{"fixture_shapes::geometry::total_area", "rust/src/main.rs", file("rust/src/geometry.rs", "fixture_shapes::geometry")},
		{"fixture_shapes::shapes::circle::Circle", "rust/tests/area_test.rs", file("rust/src/shapes/circle.rs", "fixture_shapes::shapes::circle")},
		// Re-exports: named and glob `pub use` in lib.rs.
		{"fixture_shapes::Shape", "rust/src/main.rs", file("rust/src/shapes/mod.rs", "fixture_shapes::shapes")},
		{"fixture_shapes::Square", "rust/tests/area_test.rs", file("rust/src/shapes/square.rs", "fixture_shapes::shapes::square")},
		{"fixture_shapes::Missing", "rust/src/main.rs", file("rust/src/lib.rs", "fixture_shapes")},
		{"::fixture_shapes::geometry", "rust/src/main.rs", file("rust/src/geometry.rs", "fixture_shapes::geometry")},
		{"self::cli::run", "rust/src/bin/multi/main.rs", file("rust/src/bin/multi/cli.rs", "multi::cli")},
		{"crate::cmd", "binonly/src/main.rs", file("binonly/src/cmd.rs", "bin_only::cmd")},
		// Path dependencies: workspace-inherited, renamed, target-specific.
		{"ws_core::Sink", "ws/crates/printer/src/lib.rs", file("ws/crates/core-lib/src/sink.rs", "ws_core::sink")},
		{"ws_core::sink::Sink", "ws/crates/printer/src/lib.rs", file("ws/crates/core-lib/src/sink.rs", "ws_core::sink")},
		{"renamed::sink", "ws/crates/printer/src/lib.rs", file("ws/crates/core-lib/src/sink.rs", "ws_core::sink")},
		{"unixdep::x", "ws/crates/printer/src/lib.rs", file("ws/crates/unixdep/src/lib.rs", "unixdep")},
		{"nolib::x", "ws/crates/printer/src/lib.rs", ext("nolib")},
		{"serde::Serialize", "ws/crates/printer/src/lib.rs", ext("serde")},
		{"ws_core::Sink", "rust/src/lib.rs", ext("ws_core")},
		{"std::collections::HashMap", "rust/src/lib.rs", ext("std")},
		{"core::fmt", "rust/src/lib.rs", ext("core")},
		{"serde::Serialize", "rust/src/lib.rs", ext("serde")},
		{"super::super::x", "rust/src/geometry.rs", want{model.TargetUnresolved, "", "super::super::x"}},
		{"crate::", "rust/src/lib.rs", want{model.TargetUnresolved, "", "crate::"}},
		{"", "rust/src/lib.rs", want{model.TargetUnresolved, "", ""}},
	}
	for _, c := range cases {
		got := spec.ResolveImport(c.spec, c.from, rc)
		if got.Kind != c.want.kind || got.Path != c.want.path || got.Module != c.want.module {
			t.Errorf("ResolveImport(%q, %q) = %+v, want %+v", c.spec, c.from, got, c.want)
		}
	}
	if got := spec.ResolveImport("crate::x", "src/lib.rs", nil); got.Resolved() {
		t.Errorf("resolved without ctx: %+v", got)
	}
}

// TestRustReexportCycle checks that mutually re-exporting modules terminate.
func TestRustReexportCycle(t *testing.T) {
	rc := model.NewMemRepo(map[string]string{
		"Cargo.toml": "[package]\nname = \"cyc\"\n",
		"src/lib.rs": "pub mod a;\npub mod b;\npub use a::*;\n",
		"src/a.rs":   "pub use crate::b::Thing;\npub use super::*;\n",
		"src/b.rs":   "pub use crate::a::Thing;\n",
	})
	spec, _ := Lookup(model.LangRust)
	got := spec.ResolveImport("cyc::Thing", "src/lib.rs", rc)
	if got.Kind != model.TargetFile {
		t.Fatalf("got %+v", got)
	}
}

func TestRustParseUseTree(t *testing.T) {
	type leaf struct {
		Path     string
		Alias    string
		Wildcard bool
	}
	cases := map[string][]leaf{
		"crate::a::b":                     {{"crate::a::b", "", false}},
		"a::b as c":                       {{"a::b", "c", false}},
		"super::*":                        {{"super", "", true}},
		"self::x::{self, y as z, w::*}":   {{"self::x", "", false}, {"self::x::y", "z", false}, {"self::x::w", "", true}},
		"std::{fmt::{self, Display}, io}": {{"std::fmt", "", false}, {"std::fmt::Display", "", false}, {"std::io", "", false}},
		"::ext::Thing":                    {{"ext::Thing", "", false}},
		"{a, b::c}":                       {{"a", "", false}, {"b::c", "", false}},
		"r#type::r#fn":                    {{"type::fn", "", false}},
		"a::{":                            nil,
		"a::{b,,c}}}":                     {{"a::b", "", false}, {"a::c", "", false}},
		"* as":                            nil,
		"":                                nil,
	}
	for in, want := range cases {
		var got []leaf
		for _, l := range parseUseTree(in) {
			got = append(got, leaf{strings.Join(l.path, "::"), l.alias, l.wildcard})
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("parseUseTree(%q) = %+v, want %+v", in, got, want)
		}
	}
	deep := strings.Repeat("a::{", 200) + "b" + strings.Repeat("}", 200)
	_ = parseUseTree(deep) // must terminate without overflowing the stack
}

func TestRustImports(t *testing.T) {
	f := parseRust(t, `use std::collections::{HashMap, hash_map::Entry as E};
use super::*;
use std::fmt::Write as _;
pub mod geometry;
mod tests {
    use super::*;
    use self::deep::X;
    mod deep { use super::super::Top; }
}
`)
	type imp struct {
		Spec, Alias, Names string
		Wildcard           bool
	}
	var got []imp
	for _, im := range f.Imports {
		got = append(got, imp{im.Spec, im.Alias, strings.Join(im.Names, ","), im.Wildcard})
	}
	want := []imp{
		{"std::collections::HashMap", "", "HashMap", false},
		{"std::collections::hash_map::Entry", "E", "Entry", false},
		{"super", "", "*", true},
		{"std::fmt::Write", "_", "", false},
		{"self::geometry", "", "geometry", false},
		{"self", "", "*", true},
		{"self::tests::deep::X", "", "X", false},
		{"self::Top", "", "Top", false},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("imports:\n got %+v\nwant %+v", got, want)
	}
}

func TestRustImplMerge(t *testing.T) {
	f := parseRust(t, `impl Shape for Circle {
    fn area(&self) -> f64 { Self::helper() }
}
pub struct Circle { r: f64 }
impl Circle {
    pub fn new() -> Self { Circle { r: 1.0 } }
    fn helper() -> f64 { 0.0 }
}
impl Remote {
    pub fn far(&self) { Self::other(); }
}
impl<T: Debug> Loud for T {
    fn shout(&self) {}
}
mod m {
    pub struct Inner;
    impl Inner { pub fn go() {} }
}
`)
	area := rustDef(t, f, "Circle.area")
	if area.Kind != model.KindMethod {
		t.Errorf("Circle.area kind = %v", area.Kind)
	}
	rustDef(t, f, "Circle.new")
	rustDef(t, f, "Circle.r")
	far := rustDef(t, f, "Remote.far")
	if far.Parent != model.NoParent || !slices.Equal(far.Scope, []string{"Remote"}) {
		t.Errorf("Remote.far parent=%d scope=%v", far.Parent, far.Scope)
	}
	if shout := rustDef(t, f, "shout"); len(shout.Scope) != 0 {
		t.Errorf("blanket impl item scoped: %v", shout.Scope)
	}
	rustDef(t, f, "m.Inner.go")
	for i := range f.Defs {
		if rustIsImplDef(&f.Defs[i]) {
			t.Errorf("impl def left behind: %+v", f.Defs[i])
		}
	}
	if len(f.Inherits) != 1 || f.Inherits[0].Name != "Shape" || f.Defs[f.Inherits[0].Child].Name != "Circle" {
		t.Errorf("inherits = %+v", f.Inherits)
	}
	quals := map[string]string{}
	for _, r := range f.Refs {
		if r.Kind == model.RefCall {
			quals[r.Name] = r.Qualifier
		}
	}
	if quals["helper"] != "Circle" || quals["other"] != "Remote" {
		t.Errorf("Self qualifiers not rewritten: %v", quals)
	}
	for _, r := range f.Refs {
		if r.Name == "Self" {
			t.Errorf("Self type ref kept: %+v", r)
		}
	}
}

func TestRustQualifiedPaths(t *testing.T) {
	f := parseRust(t, `use crate::shapes;
fn f() {
    crate::geometry::square_of(1.0);
    super::helper();
    shapes::circle::Circle::new();
    std::mem::drop(1);
    geometry::hypot(1.0, 2.0);
    Vec::new();
}
mod tests {
    fn t() { super::f(); }
}
`)
	quals := map[string]string{}
	for _, r := range f.Refs {
		if r.Kind == model.RefCall {
			quals[r.Name] = r.Qualifier
		}
	}
	want := map[string]string{
		"square_of": "/crate/geometry",
		"helper":    "/super",
		"new":       "Vec",
		"drop":      "/std/mem",
		"hypot":     "geometry",
		"f":         "/self",
	}
	for name, q := range want {
		if quals[name] != q {
			t.Errorf("qualifier of %s = %q, want %q", name, quals[name], q)
		}
	}
	aliases := map[string]string{}
	for _, im := range f.Imports {
		if im.Alias != "" {
			aliases[im.Alias] = im.Spec
		}
	}
	wantAliases := map[string]string{
		"/crate/geometry":      "crate::geometry",
		"/super":               "super",
		"/crate/shapes/circle": "crate::shapes::circle",
		"/std/mem":             "std::mem",
		"/self":                "self",
	}
	if !reflect.DeepEqual(aliases, wantAliases) {
		t.Errorf("synthetic imports = %v, want %v", aliases, wantAliases)
	}
	var circleNew bool
	for _, r := range f.Refs {
		if r.Name == "new" && r.Qualifier == "/crate/shapes/circle::Circle" {
			circleNew = true
		}
	}
	if !circleNew {
		t.Errorf("shapes::circle::Circle::new not rewritten:\n%s", DumpFacts(f))
	}
}

func TestRustTests(t *testing.T) {
	src := `pub fn square_of(x: f64) -> f64 { x * x }
#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn test_square_of() { assert_eq!(square_of(2.0), 4.0); }
    #[tokio::test]
    #[ignore]
    async fn slow() {}
    fn helper() {}
}
`
	f := parseRust(t, src)
	if f.TestFile {
		t.Error("inline test module made the file a test file")
	}
	mod := rustDef(t, f, "tests")
	if mod.Kind != model.KindModule || !mod.Flags.Has(model.FlagTest) {
		t.Errorf("tests module = %v %v", mod.Kind, mod.Flags)
	}
	if d := rustDef(t, f, "tests.test_square_of"); d.Kind != model.KindTest {
		t.Errorf("test kind = %v", d.Kind)
	}
	if d := rustDef(t, f, "tests.slow"); d.Kind != model.KindTest {
		t.Errorf("tokio test kind = %v", d.Kind)
	}
	if d := rustDef(t, f, "tests.helper"); d.Kind != model.KindFunction {
		t.Errorf("helper kind = %v", d.Kind)
	}
	if len(f.Tests) != 2 || f.Tests[0].Target != "square_of" {
		t.Errorf("tests = %+v", f.Tests)
	}
	var called bool
	for _, r := range f.Refs {
		if r.Name == "square_of" && r.Kind == model.RefCall {
			called = true
		}
		if r.Name == "assert_eq" {
			t.Errorf("std macro call kept: %+v", r)
		}
	}
	if !called {
		t.Error("call inside assert_eq! not extracted")
	}

	g := parseRust(t, "use x::y;\n#[test]\nfn only() { y(); }\n")
	if !g.TestFile {
		t.Error("file of only tests is not a test file")
	}
}

func TestRustExported(t *testing.T) {
	f := parseRust(t, `pub struct A { pub x: i32, y: i32 }
struct B;
pub(crate) fn c() {}
pub enum E { V }
pub trait T { fn m(&self); }
`)
	for q, want := range map[string]bool{"A": true, "A.x": true, "A.y": false, "B": false, "c": true, "E.V": true, "T.m": true} {
		if got := rustDef(t, f, q).Flags.Has(model.FlagExported); got != want {
			t.Errorf("%s exported = %v, want %v", q, got, want)
		}
	}
}

// TestRustGarbage feeds malformed and truncated input; extraction must
// never panic and must keep the facts invariants.
func TestRustGarbage(t *testing.T) {
	seed, err := os.ReadFile(filepath.Join(goldenRoot, "rust", "features.rs"))
	if err != nil {
		t.Fatal(err)
	}
	inputs := []string{
		"", "impl", "impl for {", "impl<T Foo for T {", "use ::{{{;", "use a::{b as;",
		"mod;", "#[test]", "#[cfg(test)] mod", "macro_rules! { ( => }", "fn f() { a!(b(c(d(e(f(", "\x00\xff\xfe",
		"impl Self for Self { fn f() { Self::g() } }", "use super::super::super::*;",
		strings.Repeat("mod m { ", 300), strings.Repeat("f(", 500),
	}
	r := rand.New(rand.NewPCG(7, 23))
	for range 60 {
		cut := r.IntN(len(seed))
		b := []byte(string(seed[:cut]))
		for range 5 {
			if len(b) > 0 {
				b[r.IntN(len(b))] = "{}()<>:;,!#*&"[r.IntN(13)]
			}
		}
		inputs = append(inputs, string(b))
	}
	e := NewEngine()
	defer e.Close()
	for _, in := range inputs {
		res, err := e.ParseLang(context.Background(), model.LangRust, []byte(in))
		if err != nil {
			t.Fatal(err)
		}
		if err := res.Facts.Validate(); err != nil {
			t.Fatalf("invalid facts for %q: %v", in, err)
		}
		if _, err := DecodeFacts(EncodeFacts(res.Facts)); err != nil {
			t.Fatal(err)
		}
	}
}

func FuzzParseRust(f *testing.F) {
	f.Add([]byte("impl<T> Tr for S<T> { fn m(&self) { Self::x(); self.y.z(); } }\nstruct S<T>(T);\n"))
	f.Add([]byte("use a::{b::{self, c as d}, *};\nmod t { use super::super::x; #[test] fn f() { m!(g(h::i(1))); } }\n"))
	f.Add([]byte("impl\x00 for (((fn"))
	e := NewEngine()
	f.Cleanup(e.Close)
	f.Fuzz(func(t *testing.T, src []byte) {
		res, err := e.ParseLang(context.Background(), model.LangRust, src)
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

// TestRustFixtureImports checks the "imports" lines of the [rust] section
// of expected_edges.txt (the resolver tests skip them): every listed import
// target is reached by some import of the source file.
func TestRustFixtureImports(t *testing.T) {
	const fixture = "../../testdata/fixture-repo"
	files := map[string]string{}
	err := filepath.WalkDir(filepath.Join(fixture, "rust"), func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(fixture, p)
		files[filepath.ToSlash(rel)] = string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	rc := model.NewMemRepo(files)
	spec, _ := Lookup(model.LangRust)
	targets := map[string]map[string]bool{}
	for p, src := range files {
		if !strings.HasSuffix(p, ".rs") {
			continue
		}
		targets[p] = map[string]bool{}
		for _, im := range parseRust(t, src).Imports {
			if tg := spec.ResolveImport(im.Spec, p, rc); tg.Resolved() {
				targets[p][tg.Path] = true
			}
		}
	}
	b, err := os.ReadFile(filepath.Join(fixture, "expected_edges.txt"))
	if err != nil {
		t.Fatal(err)
	}
	section, n := "", 0
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			section = line
			continue
		}
		f := strings.Fields(line)
		if section != "[rust]" || len(f) < 4 || f[0] != "imports" {
			continue
		}
		n++
		if !targets[f[1]][f[3]] {
			t.Errorf("missing import %s -> %s; got %v", f[1], f[3], targets[f[1]])
		}
	}
	if n < 5 {
		t.Fatalf("only %d rust import expectations", n)
	}
}
