package resolve

import (
	"fmt"
	"maps"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/pragmataW/graphmaster/internal/model"
)

// implRows renders the derived Go implements edges of r, sorted:
// "type  <src> -> <dst> <conf>" for type-level rows and
// "method <src> -> <dst> <conf>" for dispatch (method-level) rows.
func (r *testRepo) implRows() []string {
	r.t.Helper()
	rows, err := r.st.DB().Query(`SELECT e.src, coalesce(e.dst, 0), coalesce(e.qual, ''), e.conf FROM edges e
		WHERE e.kind = ? AND e.name_id IS NULL`, int(model.EdgeImplements))
	if err != nil {
		r.t.Fatal(err)
	}
	type raw struct {
		src, dst int64
		qual     string
		conf     int
	}
	var raws []raw
	for rows.Next() {
		var x raw
		if err := rows.Scan(&x.src, &x.dst, &x.qual, &x.conf); err != nil {
			r.t.Fatal(err)
		}
		raws = append(raws, x)
	}
	rows.Close()
	var out []string
	for _, x := range raws {
		kind := "type"
		if x.qual == DispatchQual {
			kind = "method"
		} else if x.qual != "" {
			kind = "qual(" + x.qual + ")"
		}
		dst := "<nil>"
		if x.dst != 0 {
			dst = r.nodeKey(x.dst)
		}
		out = append(out, fmt.Sprintf("%s %s -> %s %d", kind, r.nodeKey(x.src), dst, x.conf))
	}
	slices.Sort(out)
	return out
}

// requireImpl checks that every want row is present and every unwanted
// row (prefix match on "kind src -> dst") is absent.
func requireImpl(t *testing.T, got []string, want []string, unwanted []string) {
	t.Helper()
	for _, w := range want {
		if !slices.Contains(got, w) {
			t.Errorf("missing %q\ngot:\n  %s", w, strings.Join(got, "\n  "))
		}
	}
	for _, u := range unwanted {
		for _, g := range got {
			if strings.HasPrefix(g, u+" ") {
				t.Errorf("unexpected %q", g)
			}
		}
	}
}

const goImplMod = "module example.com/m\n\ngo 1.22\n"

func TestGoImplReceivers(t *testing.T) {
	files := map[string]string{
		"go.mod": goImplMod,
		"shape/shape.go": `package shape

type Shape interface {
	Area() float64
	Name() string
}

type Sq struct{}

func (Sq) Area() float64 { return 1 }
func (s Sq) Name() string { return "sq" }

type Circ struct{ r float64 }

func (c *Circ) Area() float64 { return c.r }
func (c *Circ) Name() string  { return "c" }

type Half struct{}

func (Half) Area() float64 { return 0 }

type Wrong struct{}

func (Wrong) Area() float64      { return 0 }
func (Wrong) Name(x int) string  { return "" }

type Mixed struct{}

func (Mixed) Area() float64  { return 0 }
func (*Mixed) Name() string  { return "" }
`,
		// Methods declared in another file of the package belong to the
		// same method set.
		"shape/more.go": `package shape

type Tri struct{}

func (t *Tri) Name() string { return "t" }
`,
		"shape/tri_area.go": `package shape

func (t *Tri) Area() float64 { return 3 }
`,
	}
	r := fullBuild(t, files, nil)
	const s = "shape/shape.go::"
	requireImpl(t, r.implRows(), []string{
		"type " + s + "Sq -> " + s + "Shape 70",
		"type " + s + "Circ -> " + s + "Shape 70",
		"type " + s + "Mixed -> " + s + "Shape 70",
		"type shape/more.go::Tri -> " + s + "Shape 70",
		"method " + s + "Sq.Area -> " + s + "Shape.Area 70",
		"method " + s + "Circ.Name -> " + s + "Shape.Name 70",
		"method shape/tri_area.go::Tri.Area -> " + s + "Shape.Area 70",
	}, []string{
		"type " + s + "Half -> " + s + "Shape",
		"type " + s + "Wrong -> " + s + "Shape",
		"method " + s + "Half.Area -> " + s + "Shape.Area",
		"method " + s + "Wrong.Area -> " + s + "Shape.Area",
	})
}

func TestGoImplEmbedding(t *testing.T) {
	files := map[string]string{
		"go.mod": goImplMod,
		"io2/io.go": `package io2

type Reader interface{ Read(p []byte) (int, error) }

type Closer interface{ Close() error }

type ReadCloser interface {
	Reader
	Closer
}

type base struct{}

func (*base) Read(p []byte) (n int, err error) { return 0, nil }

// File gets Read from the embedded *base.
type File struct {
	*base
	name string
}

func (File) Close() error { return nil }

// Wrapped embeds an interface: its method set includes Reader's.
type Wrapped struct {
	Reader
}

func (w Wrapped) Close() error { return nil }

// Deep promotes through two levels.
type Deep struct{ File }

// Shadow's own Read (wrong signature) shadows the promoted one.
type Shadow struct{ File }

func (Shadow) Read() int { return 0 }
`,
	}
	r := fullBuild(t, files, nil)
	const s = "io2/io.go::"
	requireImpl(t, r.implRows(), []string{
		"type " + s + "base -> " + s + "Reader 70",
		"type " + s + "File -> " + s + "Reader 70",
		"type " + s + "File -> " + s + "ReadCloser 70",
		"type " + s + "File -> " + s + "Closer 70",
		"type " + s + "Wrapped -> " + s + "ReadCloser 70",
		"type " + s + "Deep -> " + s + "ReadCloser 70",
		"type " + s + "Shadow -> " + s + "Closer 70",
		"method " + s + "base.Read -> " + s + "Reader.Read 70",
		"method " + s + "File.Close -> " + s + "Closer.Close 70",
	}, []string{
		"type " + s + "base -> " + s + "ReadCloser",
		"type " + s + "Shadow -> " + s + "Reader",
		"type " + s + "Shadow -> " + s + "ReadCloser",
		// Interfaces are not concrete implementers.
		"type " + s + "ReadCloser -> " + s + "Reader",
		// The embedded interface's own method is no dispatch target of itself.
		"method " + s + "Reader.Read -> " + s + "Reader.Read",
	})
}

func TestGoImplCrossPackageAndAssertions(t *testing.T) {
	files := map[string]string{
		"go.mod": goImplMod,
		"api/api.go": `package api

import "io"

type Runner interface {
	Run(ctx string) error
	Stop()
}

// Logger embeds an external interface: its method set is only partly
// known, so only an assertion links implementers.
type Logger interface {
	io.Writer
	Log(msg string)
}

type Stringer interface{ String() string }

type Empty interface{}
`,
		"impl/impl.go": `package impl

import "example.com/m/api"

type Job struct{}

func (j *Job) Run(ctx string) error { return nil }
func (j *Job) Stop()                {}
func (j *Job) String() string       { return "job" }

type L struct{}

func (L) Write(p []byte) (int, error) { return 0, nil }
func (L) Log(msg string)              {}

type Named struct{}

func (Named) String() string { return "n" }

type Options struct{}

var _ api.Runner = (*Job)(nil)
var _ api.Logger = L{}
var _ api.Stringer = Named{}
var _ api.Stringer = &Named{o: Options{}}
`,
	}
	r := fullBuild(t, files, nil)
	const a, i = "api/api.go::", "impl/impl.go::"
	requireImpl(t, r.implRows(), []string{
		"type " + i + "Job -> " + a + "Runner 85",
		"method " + i + "Job.Run -> " + a + "Runner.Run 85",
		"type " + i + "L -> " + a + "Logger 85",
		"method " + i + "L.Log -> " + a + "Logger.Log 85",
		"type " + i + "Named -> " + a + "Stringer 85",
	}, []string{
		// Single very common method: only explicit assertions link it.
		"type " + i + "Job -> " + a + "Stringer",
		"type " + i + "Job -> " + a + "Empty",
		// Other types mentioned by an assertion are not asserted.
		"type " + i + "Options -> " + a + "Stringer",
		"type " + i + "L -> " + a + "Empty",
	})
}

func TestGoImplGenericInterface(t *testing.T) {
	files := map[string]string{
		"go.mod": goImplMod,
		"g/g.go": `package g

type Getter[T any] interface{ Get() T }

type IntBox struct{ v int }

func (b IntBox) Get() int { return b.v }

type Box[T any] struct{ v T }

func (b *Box[T]) Get() T { return b.v }

type Pair struct{}

func (Pair) Get() (int, int) { return 0, 0 }
`,
	}
	r := fullBuild(t, files, nil)
	const g = "g/g.go::"
	requireImpl(t, r.implRows(), []string{
		"type " + g + "IntBox -> " + g + "Getter 70",
		"type " + g + "Box -> " + g + "Getter 70",
	}, []string{
		"type " + g + "Pair -> " + g + "Getter",
	})
}

func TestGoShape(t *testing.T) {
	cases := []struct {
		sig  string
		elem bool
		want string // params|results, "!" = unknown
	}{
		{"func (kl *Kubelet) SyncPod(ctx context.Context, updateType kubetypes.SyncPodType, pod, mirrorPod *v1.Pod, podStatus *kubecontainer.PodStatus) (isTerminal bool, err error)", false,
			"Context,SyncPodType,*Pod,*Pod,*PodStatus|bool,error"},
		{"SyncPod(ctx context.Context, updateType kubetypes.SyncPodType, pod *v1.Pod, mirrorPod *v1.Pod, podStatus *kubecontainer.PodStatus) (bool, error)", true,
			"Context,SyncPodType,*Pod,*Pod,*PodStatus|bool,error"},
		{"func (m *Memory[T]) Get(id string) (T, bool)", false, "string|?,bool"},
		{"Read(p []byte) (n int, err error)", true, "[]byte|int,error"},
		{"func (x X) F(a ...string) error", false, "...string|error"},
		{"func (x X) F(m map[string]int, f func(int) error, c <-chan struct{})", false, "map,func,chan|"},
		{"func (x X) F(int, string)", false, "int,string|"},
		{"func (x X) F(a interface{}, b any)", false, "any,any|"},
		{"func (x X) F(a, b int) *pkg.T[int]", false, "int,int|*T"},
		{"func (x X) F(\n\ta int,\n\tb string,\n) error", false, "int,string|error"},
		{"func (x X) F(a int, b string, c float64, d…", false, "!"},
		{"func (x X) F(a int /* why */) error", false, "!"},
	}
	for _, c := range cases {
		var tp map[string]bool
		if strings.Contains(c.sig, "[T]") {
			tp = map[string]bool{"T": true}
		}
		sh := goMethodShape(c.sig, c.elem, tp)
		got := "!"
		if sh.ok {
			got = strings.Join(sh.params, ",") + "|" + strings.Join(sh.results, ",")
		}
		if got != c.want {
			t.Errorf("goMethodShape(%q) = %q, want %q", c.sig, got, c.want)
		}
	}
}

// TestGoImplIncrementalEquivalence edits interfaces, method sets, embedded
// types and assertions one step at a time; after every step the
// incrementally maintained graph must equal a full build.
func TestGoImplIncrementalEquivalence(t *testing.T) {
	base := map[string]string{
		"go.mod": goImplMod,
		"a/iface.go": `package a

type Store interface {
	Get(id string) (int, bool)
	Put(id string, v int)
}
`,
		"a/mem.go": `package a

type Mem struct{ m map[string]int }

func (m *Mem) Get(id string) (int, bool) { v, ok := m.m[id]; return v, ok }
`,
		"a/put.go": `package a

func (m *Mem) Put(id string, v int) { m.m[id] = v }
`,
		"b/wrap.go": `package b

import "example.com/m/a"

type Wrap struct{ *a.Mem }

type Other struct{}

func (Other) Get(id string) (int, bool) { return 0, false }
`,
	}
	inc := newTestRepo(t, base)
	inc.index()
	inc.resolveAll(Options{})
	if got := inc.implRows(); len(got) == 0 {
		t.Fatal("no implements rows after the initial build")
	}

	steps := []struct {
		name  string
		files map[string]string // new contents ("" = delete)
	}{
		{"remove Put", map[string]string{"a/put.go": "package a\n"}},
		{"restore Put", map[string]string{"a/put.go": base["a/put.go"]}},
		{"Other gains Put", map[string]string{"b/wrap.go": base["b/wrap.go"] + "\nfunc (Other) Put(id string, v int) {}\n"}},
		{"interface gains Len", map[string]string{"a/iface.go": strings.Replace(base["a/iface.go"], "Put(id string, v int)", "Put(id string, v int)\n\tLen() int", 1)}},
		{"Mem gains Len", map[string]string{"a/mem.go": base["a/mem.go"] + "\nfunc (m *Mem) Len() int { return len(m.m) }\n"}},
		{"Put signature changes", map[string]string{"a/put.go": "package a\n\nfunc (m *Mem) Put(id string, v int64) {}\n"}},
		{"assert Wrap", map[string]string{"b/assert.go": "package b\n\nimport \"example.com/m/a\"\n\nvar _ a.Store = Wrap{}\n"}},
		{"Wrap stops embedding", map[string]string{"b/wrap.go": strings.Replace(base["b/wrap.go"], "struct{ *a.Mem }", "struct{ m *a.Mem }", 1)}},
		{"delete interface", map[string]string{"a/iface.go": ""}},
		{"interface returns", map[string]string{"a/iface.go": base["a/iface.go"]}},
		{"Put signature restored", map[string]string{"a/put.go": base["a/put.go"]}},
		{"body-only edit", map[string]string{"a/mem.go": strings.Replace(base["a/mem.go"], "return v, ok", "return v + 0, ok", 1)}},
		{"move type", map[string]string{"a/mem.go": "package a\n", "a/mem2.go": "package a\n\n// moved\n\n" + strings.TrimPrefix(base["a/mem.go"], "package a\n")}},
	}
	for _, st := range steps {
		var write, del []string
		for p, src := range st.files {
			if src == "" {
				del = append(del, p)
				continue
			}
			inc.files[p] = src
			write = append(write, p)
		}
		slices.Sort(write)
		if len(write) > 0 {
			inc.index(write...)
		}
		if len(del) > 0 {
			inc.remove(del...)
		}
		inc.resolvePending(Options{BatchNames: 3})
		full := fullBuild(t, maps.Clone(inc.files), nil)
		if got, want := inc.dump(), full.dump(); got != want {
			t.Fatalf("step %q: incremental != full:\n%s", st.name, diffLines(want, got))
		}
		if got, want := strings.Join(inc.implRows(), "\n"), strings.Join(full.implRows(), "\n"); got != want {
			t.Fatalf("step %q: implements rows differ:\nincremental:\n%s\nfull:\n%s", st.name, got, want)
		}
	}
	// The final state still links Wrap (asserted) and Mem.
	requireImpl(t, inc.implRows(), []string{
		"type a/mem2.go::Mem -> a/iface.go::Store 70",
	}, nil)
}

func TestGoAssertion(t *testing.T) {
	cases := []struct{ sig, iface, typ string }{
		{"var _ api.Runner = (*Job)(nil)", "api.Runner", "Job"},
		{"var _ framework.FilterPlugin = &foo{}", "framework.FilterPlugin", "foo"},
		{"var _ Data = testData{a: 1}", "Data", "testData"},
		{"var _ Getter[int] = new(impl.Box[int])", "Getter", "impl.Box"},
		{"var _ http.Handler = HandlerFunc(nil)", "http.Handler", "HandlerFunc"},
		{"var _ = fmt.Sprintf", "", ""},
		{"var x I = T{}", "", ""},
		{"var _ I = (*T)(ni…", "", ""},
	}
	for _, c := range cases {
		i, ty, ok := goAssertion(c.sig)
		if ok != (c.iface != "") || i != c.iface || ty != c.typ {
			t.Errorf("goAssertion(%q) = %q, %q, %v; want %q, %q", c.sig, i, ty, ok, c.iface, c.typ)
		}
	}
}

// TestGoImplRandomEquivalence rewrites random interfaces, method sets,
// embeddings and assertions of a small package pair; after every step the
// incrementally maintained graph must equal a full build.
func TestGoImplRandomEquivalence(t *testing.T) {
	rng := rand.New(rand.NewPCG(11, 17))
	methods := []string{"A() int", "B(x string) error", "C(p *T0, n ...int) (bool, error)"}
	iface := func(i int) string {
		var b strings.Builder
		fmt.Fprintf(&b, "package a\n\ntype I%d interface {\n", i)
		for _, m := range methods {
			if rng.IntN(2) == 0 {
				b.WriteString("\t" + m + "\n")
			}
		}
		if i > 0 && rng.IntN(3) == 0 {
			fmt.Fprintf(&b, "\tI%d\n", rng.IntN(i))
		}
		b.WriteString("}\n")
		return b.String()
	}
	typ := func(i int) (string, string) {
		pkg := "a"
		if i >= 2 {
			pkg = "b"
		}
		var b strings.Builder
		fmt.Fprintf(&b, "package %s\n\n", pkg)
		if pkg == "b" {
			b.WriteString("import \"example.com/m/a\"\n\n")
		}
		fmt.Fprintf(&b, "type T%d struct {\n", i)
		switch rng.IntN(4) {
		case 0:
			if i > 0 {
				embed := fmt.Sprintf("*T%d", rng.IntN(i))
				if pkg == "b" && !strings.HasSuffix(embed, "2") {
					embed = strings.Replace(embed, "*T", "*a.T", 1)
				}
				b.WriteString("\t" + embed + "\n")
			}
		case 1:
			if pkg == "b" {
				fmt.Fprintf(&b, "\ta.I%d\n", rng.IntN(3))
			} else {
				fmt.Fprintf(&b, "\tI%d\n", rng.IntN(3))
			}
		}
		b.WriteString("}\n")
		recv := fmt.Sprintf("(t *T%d)", i)
		if rng.IntN(2) == 0 {
			recv = fmt.Sprintf("(t T%d)", i)
		}
		for _, m := range methods {
			if rng.IntN(2) == 0 {
				m = strings.Replace(m, "*T0", "*a.T0", 1)
				if pkg == "a" {
					m = strings.Replace(m, "*a.T0", "*T0", 1)
				}
				body := " { panic(0) }"
				fmt.Fprintf(&b, "\nfunc %s %s%s\n", recv, m, body)
			}
		}
		if rng.IntN(3) == 0 {
			assert := fmt.Sprintf("I%d", rng.IntN(3))
			if pkg == "b" {
				assert = "a." + assert
			}
			fmt.Fprintf(&b, "\nvar _ %s = (*T%d)(nil)\n", assert, i)
		}
		return fmt.Sprintf("%s/t%d.go", pkg, i), b.String()
	}
	files := map[string]string{"go.mod": goImplMod}
	for i := range 3 {
		files[fmt.Sprintf("a/i%d.go", i)] = iface(i)
	}
	for i := range 4 {
		p, src := typ(i)
		files[p] = src
	}
	inc := newTestRepo(t, files)
	inc.index()
	inc.resolveAll(Options{})
	seen := 0
	for step := range 40 {
		var write []string
		for range 1 + rng.IntN(2) {
			var p, src string
			if rng.IntN(2) == 0 {
				i := rng.IntN(3)
				p, src = fmt.Sprintf("a/i%d.go", i), iface(i)
			} else {
				p, src = typ(rng.IntN(4))
			}
			inc.files[p] = src
			if !slices.Contains(write, p) {
				write = append(write, p)
			}
		}
		slices.Sort(write)
		inc.index(write...)
		inc.resolvePending(Options{BatchNames: 5})
		full := fullBuild(t, maps.Clone(inc.files), nil)
		if got, want := inc.dump(), full.dump(); got != want {
			t.Fatalf("step %d (%v): incremental != full:\n%s", step, write, diffLines(want, got))
		}
		seen = max(seen, len(inc.implRows()))
	}
	if seen < 4 {
		t.Fatalf("the random edits produced at most %d implements rows", seen)
	}
	t.Logf("up to %d implements rows", seen)
}
