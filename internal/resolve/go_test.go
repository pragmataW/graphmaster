package resolve

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
)

const inferSrc = `package a

import "fmt"

type Node struct {
	Next  *Node
	Items []Item
	ByID  map[string]*Item
	inner Inner
	Inner
}

type Inner struct{}

func (Inner) Hello() {}

func (Inner) Embedded() {}

type Item struct{}

func (*Item) Use() {}

func New() (*Node, error) { return nil, nil }

var Default = &Node{}

func (n *Node) Walk() {
	n.Next.Walk()
	for _, it := range n.Items {
		it.Use()
	}
	if v, ok := n.ByID["x"]; ok {
		v.Use()
	}
	n.inner.Hello()
	n.Embedded()
	n.Inner.Hello()
	x, err := New()
	_ = err
	x.Walk()
	Default.Walk()
	y := new(Item)
	y.Use()
	var z Item
	z.Use()
	w := (&Item{})
	w.Use()
	var s fmt.Stringer
	s.String()
	f := func(q *Node) { q.Walk() }
	_ = f
	{
		n := Item{}
		n.Use()
	}
	n.Walk()
}

func Gen[T any](t T) {
	t.Use()
}
`

func TestGoReceiverInference(t *testing.T) {
	r := fullBuild(t, map[string]string{"go.mod": goMod, "a/a.go": inferSrc}, nil)
	var got []string
	for _, e := range r.edges() {
		if e.Src == "a/a.go::Node.Walk" || e.Src == "a/a.go::Gen" {
			got = append(got, fmt.Sprintf("%s@%d -> %s %d", e.Name, e.Line, e.Dst, e.Conf))
		}
	}
	slices.Sort(got)
	want := []string{
		"ByID@32 -> a/a.go::Node.ByID 80",
		"Embedded@36 -> a/a.go::Inner.Embedded 80", // promoted through embedding
		"Hello@35 -> a/a.go::Inner.Hello 80",       // named field of type Inner
		"Hello@37 -> a/a.go::Inner.Hello 80",       // embedded field selector
		"Inner@37 ->  0",                           // embedded fields have no def
		"Item@42 -> a/a.go::Item 90",
		"Item@44 -> a/a.go::Item 90",
		"Item@46 -> a/a.go::Item 90",
		"Item@53 -> a/a.go::Item 90",
		"Items@29 -> a/a.go::Node.Items 80",
		"New@38 -> a/a.go::New 90",
		"Next@28 -> a/a.go::Node.Next 80",
		"Node@27 -> a/a.go::Node 90",
		"Node@50 -> a/a.go::Node 90",
		"String@49 ->  0",               // fmt.Stringer: external receiver
		"Stringer@48 ->  0",             // fmt.Stringer
		"Use@30 -> a/a.go::Item.Use 80", // range value of []Item
		"Use@33 -> a/a.go::Item.Use 80", // comma-ok map index
		"Use@43 -> a/a.go::Item.Use 80", // new(Item)
		"Use@45 -> a/a.go::Item.Use 80", // var z Item
		"Use@47 -> a/a.go::Item.Use 80", // (&Item{})
		"Use@54 -> a/a.go::Item.Use 80", // block-local n shadows the receiver
		"Use@60 -> a/a.go::Item.Use 50", // type parameter: untyped member, unique
		"Walk@28 -> a/a.go::Node.Walk 80",
		"Walk@40 -> a/a.go::Node.Walk 80", // first result of New()
		"Walk@41 -> a/a.go::Node.Walk 80", // package-level var
		"Walk@50 -> a/a.go::Node.Walk 80", // func literal parameter
		"Walk@56 -> a/a.go::Node.Walk 80", // receiver again after the block
		"inner@35 -> a/a.go::Node.inner 80",
	}
	if !slices.Equal(got, want) {
		t.Errorf("edges:\n%s", diffLines(strings.Join(want, "\n"), strings.Join(got, "\n")))
	}
}

func TestGoSigParsing(t *testing.T) {
	s := newSession(context.Background(), nil, nil, Options{})
	defer s.close()
	results := []struct {
		sig  string
		want []string
	}{
		{"func NewMemory() *Memory", []string{"*Memory"}},
		{"func (m *Memory) Get(id string) (Item, bool)", []string{"Item", "bool"}},
		{"func F() (a, b int, err error)", []string{"int", "int", "error"}},
		{"func G[T any](x T) []T", []string{"[]T"}},
		{"func H()", nil},
		{"Get(id string) (Item, bool)", []string{"Item", "bool"}},
		{"func Trunc(a, b, c…", nil},
	}
	for _, c := range results {
		if got := goResults(s, c.sig); !slices.Equal(got, c.want) {
			t.Errorf("goResults(%q) = %q, want %q", c.sig, got, c.want)
		}
	}
	if got := goFieldType(s, "log, Log *stdlog.Logger", "Log"); got != "*stdlog.Logger" {
		t.Errorf("goFieldType = %q", got)
	}
	vars := []struct{ sig, name, want string }{
		{"var DefaultServer *Server", "DefaultServer", "*Server"},
		{"var a, B = 1, 2", "B", ""},
		{"var D = &Server{}", "D", "*Server"},
		{"var E = pkg.T{}", "E", "pkg.T"},
		{"const Single int = iota", "Single", "int"},
	}
	for _, c := range vars {
		if got := goVarType(s, c.sig, c.name); got != c.want {
			t.Errorf("goVarType(%q, %q) = %q, want %q", c.sig, c.name, got, c.want)
		}
	}
}

func TestGoTypeText(t *testing.T) {
	named := []struct{ in, q, n string }{
		{"*store.Item", "store", "Item"},
		{"Item", "", "Item"},
		{"**(List[T])", "", "List"},
		{"pkg.Map[K, V]", "pkg", "Map"},
	}
	for _, c := range named {
		q, n, ok := goNamed(c.in)
		if !ok || q != c.q || n != c.n {
			t.Errorf("goNamed(%q) = %q %q %v", c.in, q, n, ok)
		}
	}
	for _, in := range []string{"[]Item", "map[string]Item", "func()", "chan int", "struct{}", "a.b.c", "interface{ M() }"} {
		if _, _, ok := goNamed(in); ok {
			t.Errorf("goNamed(%q) should not be named", in)
		}
	}
	elems := map[string]string{"[]*Item": "*Item", "[4]Item": "Item", "map[string][]Item": "[]Item", "*[]X": "X"}
	for in, want := range elems {
		if got, ok := goElem(in); !ok || got != want {
			t.Errorf("goElem(%q) = %q %v", in, got, ok)
		}
	}
	if _, ok := goElem("string"); ok {
		t.Error("goElem(string) should fail")
	}
	if k, v, ok := goRange("map[ID]*Item"); !ok || k != "ID" || v != "*Item" {
		t.Errorf("goRange map = %q %q", k, v)
	}
	if e, ok := goChanElem("<-chan Event"); !ok || e != "Event" {
		t.Errorf("goChanElem = %q", e)
	}
	recv := map[string]string{
		"func (m *Memory) Get(id string) (Item, bool)": "Memory",
		"func (List[T]) Len() int":                     "List",
		"func (s *pkg.Bad) X()":                        "Bad",
		"func Plain()":                                 "",
	}
	for sig, want := range recv {
		if got := goReceiverTypeName(sig); got != want {
			t.Errorf("goReceiverTypeName(%q) = %q, want %q", sig, got, want)
		}
	}
	pkgs := map[string]string{
		"example.com/fixture/internal/store": "store",
		"github.com/x/go-yaml":               "yaml",
		"github.com/x/client-go":             "client",
		"gopkg.in/yaml.v3":                   "yaml",
		"example.com/mod/v2":                 "mod",
		"github.com/a/b-c":                   "b_c",
	}
	for in, want := range pkgs {
		if got := goPackageName(in); got != want {
			t.Errorf("goPackageName(%q) = %q, want %q", in, got, want)
		}
	}
	bind := goResolver{}.ImportBindings
	if got := bind(Import{Spec: "x/y", Alias: "_"}); got != nil {
		t.Errorf("blank import binds %v", got)
	}
	if got := bind(Import{Spec: "x/y", Alias: "z"}); !slices.Equal(got, []string{"z"}) {
		t.Errorf("alias binds %v", got)
	}
}
