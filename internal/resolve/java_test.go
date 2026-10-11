package resolve

import (
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/pragmataW/graphmaster/internal/model"
	"github.com/pragmataW/graphmaster/internal/testutil"
)

// javaFixture returns the Java sources of the fixture repository.
func javaFixture(t *testing.T) map[string]string {
	t.Helper()
	root := testutil.FixtureDir()
	files := map[string]string{}
	err := filepath.WalkDir(filepath.Join(root, "java"), func(p string, d fs.DirEntry, err error) error {
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
	if len(files) < 7 {
		t.Fatalf("java fixture has %d files", len(files))
	}
	return files
}

const (
	zooMain = "java/src/main/java/com/example/zoo/"
	zooTest = "java/src/test/java/com/example/zoo/"
)

func TestJavaFixtureExpectedEdges(t *testing.T) {
	r := fullBuild(t, javaFixture(t), nil)
	exps := loadExpectations(t, "java")
	if len(exps) < 15 {
		t.Fatalf("only %d Java expectations loaded", len(exps))
	}
	checkExpectations(t, r, exps)
}

// edgeCase pins every edge of a reference: src -> name resolves to dst
// with conf ("" / 0 = unresolved).
type edgeCase struct {
	src, name, dst string
	conf           int
}

func checkEdgeCases(t *testing.T, es []resolvedEdge, cases []edgeCase) {
	t.Helper()
	for _, c := range cases {
		got := edgesFrom(es, c.src, c.name)
		if len(got) == 0 {
			t.Errorf("%s -> %s: no edge%s", c.src, c.name, describeFrom(es, c.src))
		}
		for _, e := range got {
			if e.Dst != c.dst || e.Conf != c.conf {
				t.Errorf("%s -> %s (line %d): got %q conf %d, want %q conf %d", c.src, c.name, e.Line, e.Dst, e.Conf, c.dst, c.conf)
			}
		}
	}
}

// TestJavaFixtureConfidence pins exact tiers for representative references.
func TestJavaFixtureConfidence(t *testing.T) {
	r := fullBuild(t, javaFixture(t), nil)
	es := r.edges()
	const (
		animal  = zooMain + "Animal.java::"
		dog     = zooMain + "Dog.java::"
		cat     = zooMain + "Cat.java::"
		keeper  = zooMain + "keeper/Keeper.java::"
		strs    = zooMain + "util/Strings.java::"
		dogTest = zooTest + "DogTest.java::"
	)
	checkEdgeCases(t, es, []edgeCase{
		{animal + "Animal.name", "capitalize", strs + "Strings.capitalize", 100},
		{animal + "Animal.describe", "name", animal + "Animal.name", 80},
		{animal + "Animal.describe", "sound", animal + "Animal.sound", 80},
		{animal + "Animal", "Named", zooMain + "Named.java::Named", 90},
		{animal + "Animal.name", "String", "", 0},
		{dog + "Dog.fetch", "describe", animal + "Animal.describe", 80},
		{dog + "Dog", "Animal", animal + "Animal", 90},
		{cat + "Cat.compareTo", "Cat", cat + "Cat", 90},
		{cat + "Cat.compareTo", "lives", cat + "Cat.lives", 80},
		{cat + "Cat.compareTo", "compare", "", 0},
		{cat + "Cat", "Comparable", "", 0},
		{keeper + "Keeper.animals", "Animal", animal + "Animal", 100},
		{keeper + "Keeper.animals", "ArrayList", "", 0},
		{keeper + "Keeper.admit", "add", "", 0},
		{keeper + "Keeper.rollCall", "describe", animal + "Animal.describe", 80},
		{keeper + "Keeper.main", "Keeper", keeper + "Keeper", 90},
		{keeper + "Keeper.main", "admit", keeper + "Keeper.admit", 80},
		{keeper + "Keeper.main", "rollCall", keeper + "Keeper.rollCall", 80},
		{keeper + "Keeper.main", "Dog", dog + "Dog.Dog", 80},
		{keeper + "Keeper.main", "Cat", cat + "Cat.Cat", 80},
		{keeper + "Keeper.main", "forEach", "", 0},
		{keeper + "Keeper.main", "println", "", 0},
		{keeper + "Keeper.main", "out", "", 0},
		{strs + "Strings.capitalize", "isEmpty", "", 0},
		{strs + "Strings.capitalize", "toUpperCase", "", 0},
		{dogTest + "DogTest.describeIncludesSound", "describe", animal + "Animal.describe", 80},
		{dogTest + "DogTest.fetchMentionsItem", "fetch", dog + "Dog.fetch", 80},
		{dogTest + "DogTest.fetchMentionsItem", "assertEquals", "", 0},
		{dogTest + "DogTest.describeIncludesSound", "Test", "", 0},
	})

	// Dog dog = new Dog("rex"): the type and the constructor.
	var dogRefs []string
	for _, e := range edgesFrom(es, dogTest+"DogTest.describeIncludesSound", "Dog") {
		dogRefs = append(dogRefs, fmt.Sprintf("%s %d", e.Dst, e.Conf))
	}
	slices.Sort(dogRefs)
	if want := []string{dog + "Dog 90", dog + "Dog.Dog 80"}; !slices.Equal(dogRefs, want) {
		t.Errorf("Dog refs = %v, want %v", dogRefs, want)
	}

	// this.name in the constructor: field and accessor share the name, and a
	// member reference cannot tell them apart (T5).
	got := edgesFrom(es, animal+"Animal.Animal", "name")
	if len(got) != 2 || got[0].Conf != 15 {
		t.Errorf("this.name = %v", got)
	}
	for _, e := range es {
		if e.Kind == model.EdgeExtends || e.Kind == model.EdgeImplements {
			if e.Dst != "" && e.Conf != 90 {
				t.Errorf("inheritance %v", e)
			}
		}
	}
}

func TestJavaFixtureIncrementalEquivalence(t *testing.T) {
	files := javaFixture(t)
	const (
		animalPath = zooMain + "Animal.java"
		dogPath    = zooMain + "Dog.java"
		keeperPath = zooMain + "keeper/Keeper.java"
	)
	partial := maps.Clone(files)
	delete(partial, animalPath)
	inc := newTestRepo(t, partial)
	inc.index()
	inc.resolveAll(Options{})

	steps := []struct {
		name  string
		apply func(r *testRepo) []string
		del   []string
	}{
		{name: "add Animal", apply: func(r *testRepo) []string {
			r.files[animalPath] = files[animalPath]
			return []string{animalPath}
		}},
		{name: "Dog overrides describe", apply: func(r *testRepo) []string {
			r.files[dogPath] = strings.Replace(r.files[dogPath], "public String fetch",
				"@Override\n    public String describe() {\n        return \"dog\";\n    }\n\n    public String fetch", 1)
			return []string{dogPath}
		}},
		{name: "Keeper.admit takes Dog", apply: func(r *testRepo) []string {
			r.files[keeperPath] = strings.Replace(r.files[keeperPath], "for (Animal a : animals)", "for (Dog a : dogs())", 1) +
				"\nclass Extra { }\n"
			return []string{keeperPath}
		}},
		{name: "second Strings", apply: func(r *testRepo) []string {
			p := zooMain + "Strings.java"
			r.files[p] = "package com.example.zoo;\n\nclass Strings {\n    static String capitalize(String s) { return s; }\n}\n"
			return []string{p}
		}},
		{name: "delete Dog", del: []string{dogPath}},
		{name: "delete Animal", del: []string{animalPath}},
	}
	for _, step := range steps {
		if step.apply != nil {
			inc.index(step.apply(inc)...)
		}
		if len(step.del) > 0 {
			inc.remove(step.del...)
		}
		inc.resolvePending(Options{BatchNames: 5})
		full := fullBuild(t, inc.files, nil)
		if got, want := inc.dump(), full.dump(); got != want {
			t.Fatalf("step %q: incremental != full:\n%s", step.name, diffLines(want, got))
		}
	}
}

const javaFeaturesSrc = `package com.acme.app;

import static com.acme.util.Checks.verify;
import static com.acme.util.Checks.*;

import com.acme.model.*;
import com.acme.model.Shape.Kind;
import com.acme.util.Checks;
import java.util.List;
import org.lib.Remote;

public class App extends Base implements Runnable {
    private final Helper helper = new Helper();
    private Circle[] circles;
    private List<Circle> list;
    private Remote remote;

    public void run() {
        helper.assist(1);
        this.helper.assist(2);
        var h = new Helper();
        h.assist(3);
        super.reset();
        reset();
        verify(1);
        positive(2);
        Shape s = new Circle(1.0);
        s.area();
        ((Circle) s).radius();
        circles[0].radius();
        list.get(0).radius();
        remote.call();
        Kind k = Kind.ROUND;
        k.label();
        com.acme.util.Checks.verify(3);
        Checks.verify(4);
        make().area();
        Shape.Kind.ROUND.label();
        for (Circle c : circles) {
            c.radius();
        }
        Runnable r = () -> helper.assist(5);
        new Runnable() {
            public void run() {
                assist(6);
                helper.assist(7);
            }
        };
        Object o = s;
        if (o instanceof Circle c2) {
            c2.radius();
        }
        java.util.Objects.requireNonNull(s).hashCode();
        Inner in = new Inner();
        in.poke();
    }

    void assist(int n) {}

    Circle make() {
        return new Circle(2.0);
    }

    static class Helper {
        void assist(int n) {}
    }

    class Inner {
        void poke() {
            assist(8);
        }
    }
}
`

func javaFeatureRepo() map[string]string {
	return map[string]string{
		"src/main/java/com/acme/app/App.java": javaFeaturesSrc,
		"src/main/java/com/acme/app/Base.java": `package com.acme.app;

public class Base {
    protected void reset() {}
}
`,
		"src/main/java/com/acme/model/Shape.java": `package com.acme.model;

public interface Shape {
    double area();

    enum Kind {
        ROUND, SQUARE;

        public String label() { return name(); }
    }
}
`,
		"src/main/java/com/acme/model/Circle.java": `package com.acme.model;

public class Circle implements Shape {
    private final double r;

    public Circle(double r) { this.r = r; }

    public double area() { return r * r; }

    public double radius() { return r; }
}
`,
		"src/main/java/com/acme/util/Checks.java": `package com.acme.util;

public final class Checks {
    public static void verify(int n) {}

    public static void positive(int n) {}
}
`,
		// A same-named class elsewhere must not capture unrelated references.
		"src/main/java/com/other/Helper.java": `package com.other;

public class Helper {
    public void assist(int n) {}
}
`,
	}
}

func TestJavaReceiverInference(t *testing.T) {
	r := fullBuild(t, javaFeatureRepo(), nil)
	es := r.edges()
	const (
		app    = "src/main/java/com/acme/app/App.java::"
		circle = "src/main/java/com/acme/model/Circle.java::"
		shape  = "src/main/java/com/acme/model/Shape.java::"
		checks = "src/main/java/com/acme/util/Checks.java::"
	)
	type key struct {
		src, name string
		line      uint32
	}
	idx := map[key][]resolvedEdge{}
	for _, e := range es {
		k := key{strings.TrimPrefix(e.Src, app), e.Name, e.Line}
		idx[k] = append(idx[k], e)
	}
	expect := []struct {
		src, name string
		line      uint32
		dst       string
		conf      int
	}{
		{"App.run", "assist", 19, app + "App.Helper.assist", 80},                         // field
		{"App.run", "assist", 20, app + "App.Helper.assist", 80},                         // this.field
		{"App.run", "assist", 22, app + "App.Helper.assist", 80},                         // var h = new Helper()
		{"App.run", "Helper", 21, app + "App.Helper", 80},                                // new on a nested class without ctor
		{"App.run", "reset", 23, "src/main/java/com/acme/app/Base.java::Base.reset", 80}, // super.
		{"App.run", "reset", 24, "src/main/java/com/acme/app/Base.java::Base.reset", 80}, // inherited, implicit this
		{"App.run", "verify", 25, checks + "Checks.verify", 80},                          // static single import
		{"App.run", "positive", 26, checks + "Checks.positive", 80},                      // static on-demand import
		{"App.run", "Shape", 27, shape + "Shape", 100},                                   // on-demand package import
		{"App.run", "Circle", 27, circle + "Circle.Circle", 80},                          // constructor
		{"App.run", "area", 28, shape + "Shape.area", 80},                                // declared type, not runtime type
		{"App.run", "radius", 29, circle + "Circle.radius", 80},                          // cast
		{"App.run", "radius", 30, circle + "Circle.radius", 80},                          // array element
		{"App.run", "get", 31, "", 0},                                                    // java.util.List
		{"App.run", "radius", 31, "", 0},                                                 // result of an external call
		{"App.run", "call", 32, "", 0},                                                   // field of an external type
		{"App.run", "Kind", 33, shape + "Shape.Kind", 80},                                // imported nested type
		{"App.run", "ROUND", 33, shape + "Shape.Kind.ROUND", 100},                        // Kind.ROUND via the import
		{"App.run", "label", 34, shape + "Shape.Kind.label", 80},                         // enum-typed local
		{"App.run", "verify", 35, checks + "Checks.verify", 90},                          // fully qualified
		{"App.run", "verify", 36, checks + "Checks.verify", 100},                         // imported class
		{"App.run", "area", 37, circle + "Circle.area", 80},                              // method result
		{"App.run", "label", 38, shape + "Shape.Kind.label", 80},                         // enum constant chain
		{"App.run", "radius", 40, circle + "Circle.radius", 80},                          // enhanced for
		{"App.run", "assist", 42, app + "App.Helper.assist", 80},                         // lambda body
		{"App.run.run", "assist", 45, app + "App.assist", 80},                            // anonymous class -> outer
		{"App.run.run", "assist", 46, app + "App.Helper.assist", 80},                     // outer field
		{"App.run", "radius", 51, circle + "Circle.radius", 80},                          // pattern variable
		{"App.run", "hashCode", 53, "", 0},                                               // java.util.Objects result
		{"App.run", "Inner", 54, app + "App.Inner", 80},                                  // nested type reference
		{"App.run", "poke", 55, app + "App.Inner.poke", 80},
		{"App.Inner.poke", "assist", 70, app + "App.assist", 80}, // inner class -> outer method
	}
	for _, x := range expect {
		got := idx[key{x.src, x.name, x.line}]
		if len(got) == 0 {
			t.Errorf("%s -> %s@%d: no edge", x.src, x.name, x.line)
			continue
		}
		for _, e := range got {
			if e.Dst != x.dst || e.Conf != x.conf {
				t.Errorf("%s -> %s@%d: got %q conf %d, want %q conf %d", x.src, x.name, x.line, e.Dst, e.Conf, x.dst, x.conf)
			}
		}
	}
	for _, e := range es {
		if strings.HasPrefix(e.Dst, "src/main/java/com/other/") {
			t.Errorf("edge into unrelated package: %+v", e)
		}
	}
}

func TestJavaTypeResolution(t *testing.T) {
	files := map[string]string{
		"a/src/main/java/p/Outer.java": `package p;

public class Outer {
    public static class Node {}
    public Node head;
}
`,
		"a/src/main/java/p/User.java": `package p;

import q.Node;

class User {
    Node n;
    Outer.Node on;
    p.Outer.Node fq;
    String s;
    UnknownThing u;
}
`,
		"b/src/main/java/q/Node.java": "package q;\n\npublic class Node {}\n",
		"b/src/main/java/q/Wild.java": `package q;

import java.util.*;

class Wild {
    List<String> l;
    Node local;
}
`,
		"Main.java":     "class Main { void m() { Helper.make(); } }\n",
		"Helper.java":   "class Helper { static void make() {} }\n",
		"x/Helper.java": "class Helper { static void make() {} }\n",
	}
	r := fullBuild(t, files, nil)
	checkEdgeCases(t, r.edges(), []edgeCase{
		// Single-type import of a class in another module (cross-root).
		{"a/src/main/java/p/User.java::User.n", "Node", "b/src/main/java/q/Node.java::Node", 100},
		{"a/src/main/java/p/User.java::User.on", "Node", "a/src/main/java/p/Outer.java::Outer.Node", 90},
		{"a/src/main/java/p/User.java::User.fq", "Node", "a/src/main/java/p/Outer.java::Outer.Node", 90},
		{"a/src/main/java/p/User.java::User.s", "String", "", 0},
		{"a/src/main/java/p/User.java::User.u", "UnknownThing", "", 0},
		{"a/src/main/java/p/Outer.java::Outer.head", "Node", "a/src/main/java/p/Outer.java::Outer.Node", 80},
		{"b/src/main/java/q/Wild.java::Wild.l", "List", "", 0},
		{"b/src/main/java/q/Wild.java::Wild.local", "Node", "b/src/main/java/q/Node.java::Node", 90},
		// Default package: same directory only.
		{"Main.java::Main.m", "make", "Helper.java::Helper.make", 90},
	})
}

// TestJavaMalformedSources indexes broken and stale Java files; resolution
// must not panic and must leave consistent edges.
func TestJavaMalformedSources(t *testing.T) {
	files := javaFixture(t)
	broken := map[string]string{}
	for p, src := range files {
		if !strings.HasSuffix(p, ".java") {
			continue
		}
		for i, cut := range []int{len(src) / 3, len(src) / 2, len(src) - 3} {
			broken[fmt.Sprintf("%s.broken%d.java", strings.TrimSuffix(p, ".java"), i)] = src[:max(cut, 0)]
		}
		broken[strings.TrimSuffix(p, ".java")+".garbled.java"] = strings.NewReplacer("{", "(", ";", "").Replace(src)
	}
	broken["weird/Empty.java"] = ""
	broken["weird/Noise.java"] = "\x00\xff class { new ( . :: this.super. x[ ] -> @ }"
	broken["weird/Deep.java"] = "class D { void m() { " + strings.Repeat("(", 500) + "a.b()" + strings.Repeat(")", 500) + "; } }"
	maps.Copy(broken, files)
	r := newTestRepo(t, broken)
	r.index()
	r.resolveAll(Options{})
	_ = r.edges()

	// Stale sources: the file content no longer matches the index.
	for p := range r.files {
		if strings.HasSuffix(p, ".java") {
			r.files[p] = "class Changed {}"
		}
	}
	if _, err := r.st.DB().Exec(`INSERT OR IGNORE INTO pending_names(name_id) SELECT DISTINCT name_id FROM edges WHERE name_id IS NOT NULL`); err != nil {
		t.Fatal(err)
	}
	r.resolvePending(Options{})
}

func TestJavaDeclType(t *testing.T) {
	cases := map[string]string{
		"public static String capitalize(String s)":              "String",
		"private final List<Animal> animals":                     "List<Animal>",
		"public static <T extends Comparable<T>> List<T> sort()": "List<T>",
		"public Dog(String name)":                                "",
		"int x":                                                  "int",
		"String[] names":                                         "String[]",
		"Map<String, List<Integer>> index":                       "Map<String, List<Integer>>",
		"default V fallback()":                                   "V",
		"protected abstract java.util.Set<String> keys()":        "java.util.Set<String>",
		"":       "",
		"public": "",
	}
	for sig, want := range cases {
		if got := javaDeclType(sig); got != want {
			t.Errorf("javaDeclType(%q) = %q, want %q", sig, got, want)
		}
	}
}

func TestJavaTypeName(t *testing.T) {
	cases := []struct {
		in, qual, name string
		dims           int
	}{
		{"String", "", "String", 0},
		{"java.util.List<String>", "java.util", "List", 0},
		{"Map.Entry<K, V>[]", "Map", "Entry", 1},
		{"@NonNull Foo", "", "Foo", 0},
		{"@Size(max = 3) int[][]", "", "int", 2},
		{"String...", "", "String", 1},
		{"", "", "", 0},
	}
	for _, c := range cases {
		q, n, d := javaTypeName(c.in)
		if q != c.qual || n != c.name || d != c.dims {
			t.Errorf("javaTypeName(%q) = %q %q %d", c.in, q, n, d)
		}
	}
}

func TestJavaSigTypeParams(t *testing.T) {
	cases := map[string][]string{
		"public class Box<T extends Comparable<T>>":     {"T"},
		"public static <K, V> Map<K, V> zip(List<K> k)": {"K", "V"},
		"interface Visitor<R>":                          {"R"},
		"public String name()":                          nil,
	}
	for sig, want := range cases {
		got := javaSigTypeParams(sig)
		for _, w := range want {
			if !got[w] {
				t.Errorf("javaSigTypeParams(%q) = %v, missing %s", sig, got, w)
			}
		}
		if len(want) == 0 && len(got) != 0 {
			t.Errorf("javaSigTypeParams(%q) = %v", sig, got)
		}
	}
}

// TestJavaFixtureImports checks the indexer-owned imports expectations
// against the spec's ResolveImport.
func TestJavaFixtureImports(t *testing.T) {
	files := javaFixture(t)
	rc := model.NewMemRepo(files)
	spec, ok := lookupSpec(model.LangJava)
	if !ok {
		t.Fatal("java spec not registered")
	}
	n := 0
	for _, x := range loadExpectations(t, "java") {
		if x.Kind != model.EdgeImports {
			continue
		}
		n++
		found := false
		for _, line := range strings.Split(files[x.Src], "\n") {
			imp, ok := strings.CutPrefix(strings.TrimSpace(line), "import ")
			if !ok {
				continue
			}
			tgt := spec.ResolveImport(strings.TrimSuffix(imp, ";"), x.Src, rc)
			if tgt.Kind == model.TargetFile && tgt.Path == x.Dst {
				found = true
			}
		}
		if found == x.Negative {
			t.Errorf("line %d: %v", x.Line, x)
		}
	}
	if n == 0 {
		t.Fatal("no imports expectations")
	}
}
