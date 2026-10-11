package resolve

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/pragmataW/graphmaster/internal/model"
	"github.com/pragmataW/graphmaster/internal/store"
)

// manualRepo writes hand-made nodes and edges, for languages whose parse
// support is owned by other units. Positions are computed from src.
type manualRepo struct {
	t   *testing.T
	st  *store.Store
	w   *store.Writer
	src map[string]string
	fid map[string]int64
	ids map[string]int64 // path::chain -> node id
}

func newManualRepo(t *testing.T, src map[string]string) *manualRepo {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir(), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	w, err := st.NewWriter(context.Background(), store.WriterOptions{})
	if err != nil {
		t.Fatal(err)
	}
	m := &manualRepo{t: t, st: st, w: w, src: src, fid: map[string]int64{}, ids: map[string]int64{}}
	for _, p := range slices.Sorted(func(yield func(string) bool) {
		for p := range src {
			if !yield(p) {
				return
			}
		}
	}) {
		id, err := w.UpsertFile(&store.File{Path: p, Lang: model.LangFromPath(p), Size: int64(len(src[p])), Status: model.StatusOK})
		if err != nil {
			t.Fatal(err)
		}
		m.fid[p] = id
	}
	return m
}

// def adds a definition; chain is "Outer.Name" (parent must exist).
func (m *manualRepo) def(path string, kind model.NodeKind, chain string) {
	m.t.Helper()
	name := chain[strings.LastIndexByte(chain, '.')+1:]
	n := &store.Node{FileID: m.fid[path], Kind: kind, Name: name, QName: chain, StartLine: 1, EndLine: 1}
	if i := strings.LastIndexByte(chain, '.'); i >= 0 {
		n.ParentID = m.ids[path+"::"+chain[:i]]
	}
	id, err := m.w.InsertNode(n)
	if err != nil {
		m.t.Fatal(err)
	}
	m.ids[path+"::"+chain] = id
}

// ref adds an unresolved edge from src to the occurrence of name after
// marker on the line containing marker.
func (m *manualRepo) ref(path, src string, kind model.EdgeKind, marker, name, qual string) {
	m.t.Helper()
	line, col := position(m.src[path], marker, name)
	if line == 0 {
		m.t.Fatalf("marker %q / %q not found", marker, name)
	}
	if _, err := m.w.InsertEdge(&store.Edge{FileID: m.fid[path], Src: m.ids[path+"::"+src], Kind: kind,
		Name: name, Qual: qual, Line: line, Col: col}); err != nil {
		m.t.Fatal(err)
	}
}

func (m *manualRepo) imp(path, spec string, names ...string) {
	m.t.Helper()
	if err := m.w.InsertImport(&store.Import{FileID: m.fid[path], Spec: spec, Names: names, Line: 1}); err != nil {
		m.t.Fatal(err)
	}
}

func position(src, marker, name string) (line, col uint32) {
	for i, l := range strings.Split(src, "\n") {
		if j := strings.Index(l, marker); j >= 0 {
			if k := strings.Index(l[j:], name); k >= 0 {
				return uint32(i + 1), uint32(j + k)
			}
		}
	}
	return 0, 0
}

// resolve closes the writer and runs ResolveAll.
func (m *manualRepo) resolve() map[string][]string {
	m.t.Helper()
	if err := m.w.Close(); err != nil {
		m.t.Fatal(err)
	}
	if _, err := ResolveAll(context.Background(), m.st, model.NewMemRepo(m.src), Options{}); err != nil {
		m.t.Fatal(err)
	}
	keys := map[int64]string{}
	for k, id := range m.ids {
		keys[id] = k
	}
	rows, err := m.st.DB().Query(`SELECT e.src, coalesce(e.dst, 0), m.name, e.line, e.conf FROM edges e
		JOIN names m ON m.id = e.name_id`)
	if err != nil {
		m.t.Fatal(err)
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var src, dst int64
		var name string
		var line, conf int
		if err := rows.Scan(&src, &dst, &name, &line, &conf); err != nil {
			m.t.Fatal(err)
		}
		k := keys[src] + " " + name + "@" + strconv.Itoa(line)
		out[k] = append(out[k], keys[dst]+" "+strconv.Itoa(conf))
	}
	return out
}

const pyModels = `class Base:
    def greet(self):
        pass

class Child(Base):
    def run(self):
        self.helper()
        super().greet()
        self.greet()
        local()
        def local():
            pass
        other.thing()

    def helper(self):
        pass

def top():
    helper2()
    describe(1)

def helper2():
    pass
`

const pyUtil = `def describe(x):
    return x
`

// withGeneric runs the test with lang resolved by Generic.
func withGeneric(t *testing.T, lang model.Lang) {
	prev, had := registry.m[lang]
	unregister(lang)
	t.Cleanup(func() {
		unregister(lang)
		if had {
			Register(lang, prev)
		}
	})
}

// stubSpec is a minimal LanguageSpec: module = file path without extension
// as a dotted name, imports resolve to files.
type stubSpec struct{ model.LanguageSpec }

func (stubSpec) ModuleOf(p string, _ *model.Facts, _ model.RepoContext) string {
	return strings.ReplaceAll(strings.TrimSuffix(p, ".py"), "/", ".")
}

func (stubSpec) ResolveImport(spec, _ string, rc model.RepoContext) model.ImportTarget {
	p := strings.ReplaceAll(spec, ".", "/") + ".py"
	if rc.FileExists(p) {
		return model.ImportTarget{Kind: model.TargetFile, Path: p, Module: spec}
	}
	return model.ImportTarget{Kind: model.TargetExternal, Module: spec}
}

func withStubSpec(t *testing.T, lang model.Lang) {
	prev := lookupSpec
	lookupSpec = func(l model.Lang) (model.LanguageSpec, bool) {
		if l == lang {
			return stubSpec{}, true
		}
		return prev(l)
	}
	t.Cleanup(func() { lookupSpec = prev })
}

func TestGenericResolver(t *testing.T) {
	withGeneric(t, model.LangPython)
	withStubSpec(t, model.LangPython)
	const a, u = "py/models.py", "py/util.py"
	m := newManualRepo(t, map[string]string{a: pyModels, u: pyUtil})
	m.def(a, model.KindClass, "Base")
	m.def(a, model.KindMethod, "Base.greet")
	m.def(a, model.KindClass, "Child")
	m.def(a, model.KindMethod, "Child.run")
	m.def(a, model.KindFunction, "Child.run.local")
	m.def(a, model.KindMethod, "Child.helper")
	m.def(a, model.KindFunction, "top")
	m.def(a, model.KindFunction, "helper2")
	m.def(u, model.KindFunction, "describe")
	m.def(u, model.KindMethod, "describe2") // unrelated
	m.imp(a, "py.util", "describe")

	m.ref(a, "Child", model.EdgeExtends, "class Child(", "Base", "")
	m.ref(a, "Child.run", model.EdgeCalls, "self.helper", "helper", "")
	m.ref(a, "Child.run", model.EdgeCalls, "super().greet", "greet", "")
	m.ref(a, "Child.run", model.EdgeCalls, "self.greet", "greet", "")
	m.ref(a, "Child.run", model.EdgeCalls, "local()", "local", "")
	m.ref(a, "Child.run", model.EdgeCalls, "other.thing", "thing", "")
	m.ref(a, "top", model.EdgeCalls, "helper2()", "helper2", "")
	m.ref(a, "top", model.EdgeCalls, "describe(1)", "describe", "")

	got := m.resolve()
	want := map[string][]string{
		"py/models.py::Child Base@5":       {"py/models.py::Base 90"},
		"py/models.py::Child.run helper@7": {"py/models.py::Child.helper 80"},
		"py/models.py::Child.run greet@8":  {"py/models.py::Base.greet 80"}, // super()
		"py/models.py::Child.run greet@9":  {"py/models.py::Base.greet 80"}, // via ancestor walk
		"py/models.py::Child.run local@10": {"py/models.py::Child.run.local 90"},
		"py/models.py::Child.run thing@13": {" 0"},
		"py/models.py::top helper2@19":     {"py/models.py::helper2 90"},
		"py/models.py::top describe@20":    {"py/util.py::describe 100"}, // named import
	}
	for k, w := range want {
		if g := got[k]; !slices.Equal(g, w) {
			t.Errorf("%s: got %v, want %v", k, g, w)
		}
	}
	if len(got) != len(want) {
		t.Errorf("got %d references, want %d: %v", len(got), len(want), got)
	}
}

func TestTextReceiverShapes(t *testing.T) {
	cases := []struct {
		line string
		name string
		want RecvKind
	}{
		{"foo()", "foo", RecvNone},
		{"x.foo()", "foo", RecvUntyped},
		{"a.b.foo()", "foo", RecvUntyped},
		{"$obj->foo()", "foo", RecvUntyped},
		{"x?.foo()", "foo", RecvUntyped},
		{"f().foo()", "foo", RecvUntyped},
		{"[...xs].foo", "foo", RecvUntyped},
	}
	for _, c := range cases {
		s := newSession(context.Background(), nil, nil, Options{
			Source: func(string) ([]byte, error) { return []byte(c.line), nil },
		})
		site := &Site{File: &File{Path: "x"}, Name: c.name, Line: 1, Col: uint32(strings.LastIndex(c.line, c.name))}
		if got := s.TextReceiver(site); got.Kind != c.want {
			t.Errorf("%q: got %v, want %v", c.line, got.Kind, c.want)
		}
		s.close()
	}
}

func TestLastSegmentAndSplitQual(t *testing.T) {
	segs := map[string]string{
		"./utils/format": "format", "shop.models": "models", "com.example.Strings": "Strings",
		`App\Model\Product`: "Product", "<stdio.h>": "stdio", "std::vector": "vector",
	}
	for in, want := range segs {
		if got := lastSegment(in); got != want {
			t.Errorf("lastSegment(%q) = %q, want %q", in, got, want)
		}
	}
	root, rest := splitQual(`ns::Cls::Inner`)
	if root != "ns" || !slices.Equal(rest, []string{"Cls", "Inner"}) {
		t.Errorf("splitQual = %q %q", root, rest)
	}
	if got := lineAt([]byte("a\r\nb\nc"), 1); string(got) != "a" {
		t.Errorf("lineAt = %q", got)
	}
	if got := lineAt([]byte("a\nb"), 3); got != nil {
		t.Errorf("lineAt past end = %q", got)
	}
}
