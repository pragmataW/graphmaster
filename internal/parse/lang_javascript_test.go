package parse

import (
	"context"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pragmataW/graphmaster/internal/model"
)

func jsParse(t *testing.T, lang model.Lang, src string) *model.Facts {
	t.Helper()
	e := NewEngine()
	defer e.Close()
	res, err := e.ParseLang(context.Background(), lang, []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if err := res.Facts.Validate(); err != nil {
		t.Fatalf("invalid facts: %v\n%s", err, DumpFacts(res.Facts))
	}
	return res.Facts
}

func jsDefKinds(f *model.Facts) map[string]model.NodeKind {
	out := map[string]model.NodeKind{}
	for i := range f.Defs {
		out[strings.Join(f.QNameParts(int32(i)), ".")] = f.Defs[i].Kind
	}
	return out
}

func TestJSFamilyRegistered(t *testing.T) {
	cases := map[string]model.Lang{
		"a/b.ts": model.LangTypeScript, "a/b.mts": model.LangTypeScript, "a/b.d.ts": model.LangTypeScript,
		"a/b.tsx": model.LangTSX, "a/b.js": model.LangJavaScript, "a/b.mjs": model.LangJavaScript,
		"a/b.cjs": model.LangJavaScript, "a/b.jsx": model.LangJavaScript,
	}
	for p, want := range cases {
		s, ok := ForPath(p)
		if !ok || s.Lang() != want {
			t.Errorf("ForPath(%q) = %v, %v; want %v", p, s, ok, want)
		}
	}
	for _, l := range []model.Lang{model.LangTypeScript, model.LangTSX, model.LangJavaScript} {
		spec, ok := Lookup(l)
		if !ok {
			t.Fatalf("%v not registered", l)
		}
		if _, err := compileSpec(spec); err != nil {
			t.Errorf("%v query: %v", l, err)
		}
		if got := spec.ModuleOf(`src\a/../b.ts`, nil, nil); got != "src/b.ts" {
			t.Errorf("%v ModuleOf = %q", l, got)
		}
	}
}

func TestJSIsTestPath(t *testing.T) {
	yes := []string{"a/b.test.ts", "b.spec.tsx", "x/__tests__/y.js", "__tests__/z.mjs", `w\v.test.js`}
	no := []string{"a/b.ts", "test/helpers.ts", "contest.js", "spec.ts", "latest.js"}
	for _, p := range yes {
		if !IsTestPath(p) {
			t.Errorf("IsTestPath(%q) = false", p)
		}
	}
	for _, p := range no {
		if IsTestPath(p) {
			t.Errorf("IsTestPath(%q) = true", p)
		}
	}
}

func TestJSTestTarget(t *testing.T) {
	cases := map[string]string{
		"UserService":                 "UserService",
		"formatName":                  "formatName",
		"Calculator#product works":    "product",
		"Calculator.sum adds numbers": "sum",
		"run()":                       "run",
		"run() returns a value":       "run",
		"works":                       "",
		"sums two numbers":            "",
		"handles %d":                  "",
		"":                            "",
		"UserService::greet":          "greet",
	}
	for in, want := range cases {
		if got := jsTestTarget(in); got != want {
			t.Errorf("jsTestTarget(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestJSBindings(t *testing.T) {
	cases := []struct {
		raw  string
		want []jsBinding
	}{
		{"React", []jsBinding{{"React", "React"}}},
		{"{ a, b as c, type D, }", []jsBinding{{"a", "a"}, {"b", "c"}, {"D", "D"}}},
		{"{ default as Foo }", []jsBinding{{"Foo", "Foo"}}},
		{"{ add, multiply: mul, ...rest }", []jsBinding{{"add", "add"}, {"multiply", "mul"}}},
		{"{ a = 1, b: { nested }, /* c */ d // e\n }", []jsBinding{{"a", "a"}, {"d", "d"}}},
		{`{ "quoted-name" as q }`, nil},
		{"{}", nil},
		{"not an ident", nil},
	}
	for _, c := range cases {
		if got := jsBindings(c.raw); !reflect.DeepEqual(got, c.want) {
			t.Errorf("jsBindings(%q) = %v, want %v", c.raw, got, c.want)
		}
	}
}

func TestJSDefKindPriority(t *testing.T) {
	for _, lang := range []model.Lang{model.LangTypeScript, model.LangJavaScript} {
		f := jsParse(t, lang, `
const f = () => 1, g = 2;
export const h = function () {}, k = new Map();
let u;
var v = async () => {};
class A { x = () => 1; y = 2; z; constructor() {} m() {} }
function outer() { const local = () => 0; const notDef = 1; }
`)
		want := map[string]model.NodeKind{
			"f": model.KindFunction, "g": model.KindConstant, "h": model.KindFunction,
			"k": model.KindConstant, "u": model.KindVariable, "v": model.KindFunction,
			"A": model.KindClass, "A.x": model.KindMethod, "A.y": model.KindField, "A.z": model.KindField,
			"A.constructor": model.KindConstructor, "A.m": model.KindMethod,
			"outer": model.KindFunction, "outer.local": model.KindFunction,
		}
		if got := jsDefKinds(f); !reflect.DeepEqual(got, want) {
			t.Errorf("%v: defs = %v\nwant %v", lang, got, want)
		}
	}
}

func TestJSImportsAndQualifiers(t *testing.T) {
	f := jsParse(t, model.LangTypeScript, `
import * as ns from "./ns";
import { format as fmt, type Level } from "./fmt";
const cfg = require("./cfg");
const { a: b } = require("./ab");
function use(l: Level) {
  ns.helper();
  ns.sub.deep();
  fmt("x");
  b();
  cfg.get();
  other.call();
}
`)
	kinds := jsDefKinds(f)
	if _, ok := kinds["cfg"]; ok {
		t.Errorf("require alias must not be a definition: %v", kinds)
	}
	type ref struct{ name, qual, recv string }
	var got []ref
	for _, r := range f.Refs {
		if r.Kind == model.RefCall {
			got = append(got, ref{r.Name, r.Qualifier, r.Receiver})
		}
	}
	want := []ref{
		{"helper", "ns", ""}, {"deep", "ns.sub", ""}, {"format", "", ""}, {"a", "", ""},
		{"get", "cfg", ""}, {"call", "", "other"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("call refs = %v\nwant %v", got, want)
	}
	var imps []string
	for _, im := range f.Imports {
		imps = append(imps, im.Spec+"|"+im.Alias+"|"+strings.Join(im.Names, ","))
	}
	wantImps := []string{"./ns|ns|", "./fmt||format,Level", "./cfg|cfg|", "./ab||a"}
	if !reflect.DeepEqual(imps, wantImps) {
		t.Errorf("imports = %v\nwant %v", imps, wantImps)
	}
}

func TestJSOverloadsAndParamProps(t *testing.T) {
	f := jsParse(t, model.LangTypeScript, `
export function pick(a: string): string;
export function pick(a: number): number;
export function pick(a: any): any { return helper(a); }
class P {
  constructor(public id: string, private readonly secret?: number, plain = 1) { init(); }
  get v(): number { return 1; }
  set v(x: number) {}
  run(a: string): void;
  run(a: any) {}
}
`)
	var picks, runs int
	for i := range f.Defs {
		switch strings.Join(f.QNameParts(int32(i)), ".") {
		case "pick":
			picks++
			if f.Defs[i].StartLine != 4 {
				t.Errorf("kept overload at line %d, want the implementation", f.Defs[i].StartLine)
			}
		case "P.run":
			runs++
		}
	}
	if picks != 1 || runs != 1 {
		t.Errorf("pick=%d run=%d, want 1 each:\n%s", picks, runs, DumpFacts(f))
	}
	kinds := jsDefKinds(f)
	for _, q := range []string{"P.id", "P.secret"} {
		if kinds[q] != model.KindField {
			t.Errorf("%s = %v, want field:\n%s", q, kinds[q], DumpFacts(f))
		}
	}
	if _, ok := kinds["P.plain"]; ok {
		t.Error("plain constructor parameter must not be a field")
	}
	// The call inside the kept implementation stays enclosed by it.
	for _, r := range f.Refs {
		if r.Name == "helper" && strings.Join(f.QNameParts(r.Enclosing), ".") != "pick" {
			t.Errorf("helper enclosed by %v", f.QNameParts(r.Enclosing))
		}
	}
}

func TestJSRemoveDefsReindexes(t *testing.T) {
	f := &model.Facts{Lang: model.LangTypeScript}
	a := f.AddDef(model.Def{Kind: model.KindClass, Name: "A", Parent: model.NoParent})
	b := f.AddDef(model.Def{Kind: model.KindMethod, Name: "b", Parent: a})
	c := f.AddDef(model.Def{Kind: model.KindFunction, Name: "c", Parent: b})
	d := f.AddDef(model.Def{Kind: model.KindClass, Name: "D", Parent: model.NoParent})
	f.Refs = []model.Ref{{Kind: model.RefCall, Name: "x", Enclosing: b}, {Kind: model.RefCall, Name: "y", Enclosing: c}}
	f.Inherits = []model.Inherit{{Child: d, Kind: model.EdgeExtends, Name: "A"}, {Child: a, Kind: model.EdgeExtends, Name: "Z"}}
	f.Tests = []model.TestMark{{Def: b}, {Def: c}}
	jsRemoveDefs(f, []bool{true, true, false, false})
	if err := f.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(f.Defs) != 2 || f.Defs[0].Name != "c" || f.Defs[0].Parent != model.NoParent || f.Defs[1].Name != "D" {
		t.Fatalf("defs = %+v", f.Defs)
	}
	if f.Refs[0].Enclosing != model.NoParent || f.Refs[1].Enclosing != 0 {
		t.Errorf("refs = %+v", f.Refs)
	}
	if len(f.Inherits) != 1 || f.Inherits[0].Child != 1 {
		t.Errorf("inherits = %+v", f.Inherits)
	}
	if len(f.Tests) != 1 || f.Tests[0].Def != 0 {
		t.Errorf("tests = %+v", f.Tests)
	}
}

// jsSamples returns the golden sources of the JS-family languages.
func jsSamples(t testing.TB) map[string][]byte {
	out := map[string][]byte{}
	for _, dir := range []string{"typescript", "tsx", "javascript"} {
		ents, err := os.ReadDir(filepath.Join(goldenRoot, dir))
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range ents {
			if strings.HasSuffix(e.Name(), ".golden") {
				continue
			}
			b, err := os.ReadFile(filepath.Join(goldenRoot, dir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			out[filepath.Join(dir, e.Name())] = b
		}
	}
	return out
}

var jsFamilyLangs = []model.Lang{model.LangTypeScript, model.LangTSX, model.LangJavaScript}

// TestJSGarbageInput feeds truncated, shuffled and random inputs to every
// JS-family language: extraction must never panic and facts stay valid.
func TestJSGarbageInput(t *testing.T) {
	e := NewEngine()
	defer e.Close()
	rng := rand.New(rand.NewSource(1))
	var inputs [][]byte
	for _, src := range jsSamples(t) {
		for _, cut := range []int{1, 7, len(src) / 3, len(src) / 2, len(src) - 1} {
			if cut > 0 && cut < len(src) {
				inputs = append(inputs, src[:cut], src[cut:])
			}
		}
		shuffled := append([]byte(nil), src...)
		rng.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		inputs = append(inputs, shuffled)
	}
	for i := 0; i < 50; i++ {
		b := make([]byte, rng.Intn(400))
		rng.Read(b)
		inputs = append(inputs, b)
	}
	inputs = append(inputs,
		[]byte("import { from"), []byte("const { a: = require("), []byte("class extends {"),
		[]byte("describe(\"\", () => { it(\"\", ) })"), []byte("export * as from"),
		[]byte("<A.B.C><//>"), []byte("constructor(public)"), []byte("\x00\xff\xfe"))
	for _, l := range jsFamilyLangs {
		for _, in := range inputs {
			res, err := e.ParseLang(context.Background(), l, in)
			if err != nil {
				t.Fatalf("%v: %v", l, err)
			}
			if err := res.Facts.Validate(); err != nil {
				t.Fatalf("%v: invalid facts for %q: %v", l, in, err)
			}
		}
	}
}

func FuzzParseJSFamily(f *testing.F) {
	for _, src := range jsSamples(f) {
		f.Add(src)
	}
	f.Add([]byte("import a, { b as c } from 'x'; c(); a.d();"))
	e := NewEngine()
	defer e.Close()
	f.Fuzz(func(t *testing.T, src []byte) {
		for _, l := range jsFamilyLangs {
			res, err := e.ParseLang(context.Background(), l, src)
			if err != nil {
				t.Fatal(err)
			}
			if err := res.Facts.Validate(); err != nil {
				t.Fatalf("%v: %v", l, err)
			}
		}
	})
}
