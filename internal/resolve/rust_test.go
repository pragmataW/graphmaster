package resolve

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/pragmataW/graphmaster/internal/model"
	"github.com/pragmataW/graphmaster/internal/testutil"
)

// rustFixture returns the Rust part of the fixture repository.
func rustFixture(t *testing.T) map[string]string {
	t.Helper()
	root := testutil.FixtureDir()
	files := map[string]string{}
	err := filepath.WalkDir(filepath.Join(root, "rust"), func(p string, d fs.DirEntry, err error) error {
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
	if len(files) < 8 {
		t.Fatalf("rust fixture has %d files", len(files))
	}
	return files
}

func TestRustFixtureExpectedEdges(t *testing.T) {
	r := fullBuild(t, rustFixture(t), nil)
	exps := loadExpectations(t, "rust")
	if len(exps) < 20 {
		t.Fatalf("only %d Rust expectations loaded", len(exps))
	}
	checkExpectations(t, r, exps)
}

// TestRustFixtureConfidence pins exact tiers for representative references.
func TestRustFixtureConfidence(t *testing.T) {
	r := fullBuild(t, rustFixture(t), nil)
	es := r.edges()
	const (
		main   = "rust/src/main.rs::main"
		circle = "rust/src/shapes/circle.rs::"
		square = "rust/src/shapes/square.rs::"
		geo    = "rust/src/geometry.rs::"
		shapes = "rust/src/shapes/mod.rs::"
	)
	cases := []struct {
		kind           model.EdgeKind
		src, name, dst string
		conf           int
	}{
		{model.EdgeCalls, main, "total_area", geo + "total_area", 100},
		{model.EdgeImplements, circle + "Circle", "Shape", shapes + "Shape", 100},
		{model.EdgeCalls, square + "Square.area", "square_of", geo + "square_of", 100},
		{model.EdgeCalls, geo + "tests.square_of_negative", "square_of", geo + "square_of", 90},
		{model.EdgeReferences, circle + "Circle.area", "radius", circle + "Circle.radius", 80},
		{model.EdgeReferences, circle + "Circle.new", "Circle", circle + "Circle", 90},
	}
	for _, c := range cases {
		var got []string
		ok := false
		for _, e := range edgesFrom(es, c.src, c.name) {
			if e.Kind != c.kind {
				continue
			}
			got = append(got, fmt.Sprintf("%s %d", e.Dst, e.Conf))
			if e.Dst == c.dst && e.Conf == c.conf {
				ok = true
			}
		}
		if !ok {
			t.Errorf("%v %s -[%s]-> %s conf %d; got %v", c.kind, c.src, c.name, c.dst, c.conf, got)
		}
	}
	// Box::new must stay unresolved rather than fan out to Circle.new and
	// Square.new.
	for _, e := range edgesFrom(es, main, "new") {
		if e.Dst != "" && !strings.HasSuffix(e.Dst, ".new") {
			t.Errorf("new resolved to %s", e.Dst)
		}
	}
	var unresolvedNew int
	for _, e := range edgesFrom(es, main, "new") {
		if e.Dst == "" {
			unresolvedNew++
		}
	}
	if unresolvedNew != 2 {
		t.Errorf("Box::new references unresolved = %d, want 2", unresolvedNew)
	}
}

const rustCargo = "[package]\nname = \"app\"\n"

// rustScenario is a small crate exercising the resolver hooks.
var rustScenario = map[string]string{
	"Cargo.toml": rustCargo,
	"src/lib.rs": `pub mod model;
pub mod util;
pub mod ext;

pub fn top() -> u32 { 1 }

pub fn run(c: &model::Circle, s: &dyn model::Shape, items: Vec<model::Circle>) {
    let local = model::Circle::new();
    local.area();
    c.area();
    s.name();
    let boxed: Box<model::Circle> = Box::new(model::Circle::new());
    boxed.area();
    crate::util::helper();
    util::helper();
    self::util::helper();
    let x: String = String::new();
    x.len();
    let v = Vec::new();
    v.push(1);
    items.len();
}
`,
	"src/model.rs": `pub trait Shape {
    fn area(&self) -> f64;
    fn name(&self) -> String { String::from("shape") }
}

pub struct Circle { pub r: f64 }

pub enum Color { Red, Green }

impl Circle {
    pub fn new() -> Self { Circle { r: 1.0 } }
    fn scale(&self) -> f64 { self.r * Self::factor() }
    fn factor() -> f64 { 2.0 }
}

impl Shape for Circle {
    fn area(&self) -> f64 { self.scale() + super::top() as f64 }
}

pub fn pick() -> Color { Color::Red }
`,
	"src/ext.rs": `use crate::model::{Circle, Shape};

impl Circle {
    pub fn diameter(&self) -> f64 { self.r * 2.0 }
}

pub fn use_it(c: Circle) -> f64 {
    c.diameter() + c.name().len() as f64
}
`,
	"src/util.rs": `pub fn helper() {}

pub fn top() {}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_helper() {
        helper();
        super::helper();
        crate::top();
    }
}
`,
}

func TestRustScenario(t *testing.T) {
	r := fullBuild(t, rustScenario, nil)
	es := r.edges()
	type want struct {
		src, name, dst string
		conf           int // 0 = must stay unresolved
	}
	cases := []want{
		{"src/lib.rs::run", "new", "src/model.rs::Circle.new", 100},
		{"src/lib.rs::run", "area", "src/model.rs::Circle.area", 80},
		{"src/lib.rs::run", "name", "src/model.rs::Shape.name", 80},
		{"src/lib.rs::run", "helper", "src/util.rs::helper", 100},
		{"src/lib.rs::run", "Circle", "src/model.rs::Circle", 100},
		{"src/lib.rs::run", "Shape", "src/model.rs::Shape", 100},
		{"src/lib.rs::run", "len", "", 0},
		{"src/lib.rs::run", "push", "", 0},
		{"src/lib.rs::run", "String", "", 0},
		{"src/model.rs::Circle.scale", "r", "src/model.rs::Circle.r", 80},
		{"src/model.rs::Circle.scale", "factor", "src/model.rs::Circle.factor", 90},
		{"src/model.rs::Circle.area", "scale", "src/model.rs::Circle.scale", 80},
		{"src/model.rs::Circle.area", "top", "src/lib.rs::top", 100},
		{"src/model.rs::pick", "Red", "src/model.rs::Color.Red", 90},
		{"src/ext.rs::Circle.diameter", "r", "src/model.rs::Circle.r", 80},
		{"src/ext.rs::use_it", "diameter", "src/ext.rs::Circle.diameter", 50}, // impl in another module: T4
		{"src/ext.rs::use_it", "name", "src/model.rs::Shape.name", 80},
		{"src/util.rs::tests.test_helper", "helper", "src/util.rs::helper", 90},
		{"src/util.rs::tests.test_helper", "top", "src/lib.rs::top", 100},
	}
	for _, c := range cases {
		got := edgesFrom(es, c.src, c.name)
		if len(got) == 0 {
			t.Errorf("%s: no reference %q; edges: %s", c.src, c.name, describeFrom(es, c.src))
			continue
		}
		var desc []string
		ok := true
		for _, e := range got {
			desc = append(desc, fmt.Sprintf("%s %d", e.Dst, e.Conf))
			if c.conf == 0 && e.Dst != "" {
				ok = false
			}
		}
		if c.conf != 0 {
			ok = slices.ContainsFunc(got, func(e resolvedEdge) bool { return e.Dst == c.dst && e.Conf == c.conf })
		}
		if !ok {
			t.Errorf("%s -[%s]-> %q conf %d; got %v", c.src, c.name, c.dst, c.conf, desc)
		}
	}
	// super::helper() inside the inline tests module means util::helper,
	// crate::top() the crate root's top (not util::top).
	for _, e := range edgesFrom(es, "src/util.rs::tests.test_helper", "top") {
		if e.Dst != "src/lib.rs::top" {
			t.Errorf("crate::top resolved to %s", e.Dst)
		}
	}
	for _, e := range es {
		if e.Kind == model.EdgeImplements && e.Src == "src/model.rs::Circle" && e.Dst != "src/model.rs::Shape" {
			t.Errorf("implements edge to %q", e.Dst)
		}
	}
}

// TestRustIncremental checks that re-resolving after a change converges to
// the same graph as a full build.
func TestRustIncremental(t *testing.T) {
	files := rustFixture(t)
	r := fullBuild(t, files, nil)
	const sq = "rust/src/shapes/square.rs"
	orig := files[sq]
	r.files[sq] = strings.Replace(orig, "geometry::square_of(self.side)", "self.side * self.side", 1)
	r.index(sq)
	r.resolvePending(Options{})
	for _, e := range edgesFrom(r.edges(), "rust/src/shapes/square.rs::Square.area", "square_of") {
		t.Errorf("stale edge after edit: %+v", e)
	}
	r.files[sq] = orig
	r.index(sq)
	r.resolvePending(Options{})
	want := fullBuild(t, files, nil)
	if got, exp := r.dump(), want.dump(); got != exp {
		t.Errorf("incremental graph differs from full build")
	}
}

func TestRustImportBindings(t *testing.T) {
	r := rustResolver{}
	cases := []struct {
		imp  Import
		want []string
	}{
		{Import{Spec: "crate::geometry", Names: []string{"geometry"}}, []string{"geometry"}},
		{Import{Spec: "crate::shapes::Shape", Names: []string{"Shape"}}, nil},
		{Import{Spec: "std::fmt", Alias: "f"}, []string{"f"}},
		{Import{Spec: "a::B", Alias: "C"}, nil},
		{Import{Spec: "super", Names: []string{"*"}}, nil},
		{Import{Spec: "crate::geometry", Alias: "/crate/geometry"}, []string{"/crate/geometry"}},
		{Import{Spec: "super", Alias: "/super"}, []string{"/super"}},
		{Import{Spec: "super", Alias: "super"}, nil},
		{Import{Spec: "std::io::Write", Alias: "_"}, nil},
		{Import{Spec: "self"}, nil},
	}
	for _, c := range cases {
		if got := r.ImportBindings(c.imp); !slices.Equal(got, c.want) {
			t.Errorf("ImportBindings(%+v) = %v, want %v", c.imp, got, c.want)
		}
	}
}
