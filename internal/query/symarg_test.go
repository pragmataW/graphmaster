package query

import (
	"context"
	"regexp"
	"strings"
	"testing"
)

func TestResolveSymbolForms(t *testing.T) {
	e := fixtureEngine(t)
	q := e.querier()
	ctx := context.Background()
	const want = "example.com/fixture/internal/store.Memory.Put"

	one := func(arg string) *sym {
		t.Helper()
		ss, err := q.resolveSymbol(ctx, arg)
		if err != nil {
			t.Fatalf("%q: %v", arg, err)
		}
		if len(ss) != 1 {
			t.Fatalf("%q: %d matches, want 1", arg, len(ss))
		}
		return ss[0]
	}
	s := one(want)
	if s.QName != want {
		t.Fatalf("qname: got %s", s.QName)
	}
	for _, arg := range []string{
		"Memory.Put",                    // qname suffix
		"store.Memory.Put",              // longer suffix
		"go/internal/store/store.go:33", // path:line inside the body
		"./go/internal/store/store.go:31",
		s.skeyHex(), // skey
	} {
		if got := one(arg); got.ID != s.ID {
			t.Errorf("%q resolved to %s", arg, got.QName)
		}
	}
	// path:line on a line between definitions picks the innermost one.
	if got := one("go/internal/store/store.go:13"); got.QName != "example.com/fixture/internal/store.Memory.mu" {
		t.Errorf("innermost: got %s", got.QName)
	}
	// A bare unique name.
	if got := one("NewAudited"); got.QName != "example.com/fixture/internal/store.NewAudited" {
		t.Errorf("bare: got %s", got.QName)
	}
	// C: the header prototype yields to the definition.
	if got := one("vec_add"); got.Path != "c/src/vec.c" {
		t.Errorf("declaration preferred over definition: %s", got.Path)
	}
	// Suffixes respect segment boundaries.
	if ss, _ := q.resolveSymbol(ctx, "emory.Put"); len(ss) != 0 {
		t.Errorf("partial segment matched %d symbols", len(ss))
	}
}

func TestAmbiguousListIsNumberedAndResolvable(t *testing.T) {
	e := fixtureEngine(t)
	out := runTool(t, e, "callers", `{"symbol":"Put"}`)
	if !strings.Contains(out, `ambiguous symbol "Put": 3 candidates`) {
		t.Fatalf("no ambiguity header:\n%s", out)
	}
	re := regexp.MustCompile(`(?m)^ ?(\d+)\. .*  skey ([0-9a-f]{16})$`)
	m := re.FindAllStringSubmatch(out, -1)
	if len(m) != 3 {
		t.Fatalf("want 3 numbered candidates:\n%s", out)
	}
	for i, c := range m {
		if c[1] != string(rune('1'+i)) {
			t.Errorf("candidate %d numbered %s", i+1, c[1])
		}
		// Each candidate's skey is an unambiguous argument.
		res := runTool(t, e, "callers", `{"symbol":"`+c[2]+`"}`)
		if strings.Contains(res, "ambiguous") || strings.Contains(res, "no symbol") {
			t.Errorf("skey %s did not resolve:\n%s", c[2], res)
		}
	}
}

func TestAmbiguousListIsCapped(t *testing.T) {
	ss := make([]*sym, 30)
	for i := range ss {
		ss[i] = &sym{QName: "q", Path: "p", SKey: make([]byte, 8)}
	}
	o := newOutput(DefaultMaxTokens)
	o.header("b")
	writeAmbiguous(o, "x", ss)
	if n := strings.Count(o.String(), "skey 0000000000000000"); n != maxCandidates {
		t.Errorf("%d candidates listed, want %d", n, maxCandidates)
	}
	if !strings.Contains(o.String(), "… +10 more") {
		t.Fatalf("cap footer missing:\n%s", o.String())
	}
}

func TestSplitPathLineAndSegments(t *testing.T) {
	for _, c := range []struct {
		in   string
		path string
		line int
		ok   bool
	}{
		{"a/b.go:12", "a/b.go", 12, true},
		{"./a.go:1", "a.go", 1, true},
		{"geo::Shape::area", "", 0, false},
		{"a.go:0", "", 0, false},
		{"a.go:", "", 0, false},
		{":5", "", 0, false},
	} {
		p, l, ok := splitPathLine(c.in)
		if p != c.path || l != c.line || ok != c.ok {
			t.Errorf("splitPathLine(%q) = %q,%d,%v", c.in, p, l, ok)
		}
	}
	for in, want := range map[string]string{
		"a.b.C":         "C",
		"geo::Shape::m": "m",
		`App\Model\P`:   "P",
		"js/a.js.f":     "f",
		"plain":         "plain",
		"trailing.":     "trailing.",
	} {
		if got := lastSegment(in); got != want {
			t.Errorf("lastSegment(%q) = %q, want %q", in, got, want)
		}
	}
	if !hasQNameSuffix("a.Memory.Put", "Memory.Put") || hasQNameSuffix("a.XMemory.Put", "Memory.Put") {
		t.Error("hasQNameSuffix boundary")
	}
}
