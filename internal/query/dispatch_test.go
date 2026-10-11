package query

import (
	"strings"
	"testing"

	"github.com/pragmataW/graphmaster/internal/model"
	"github.com/pragmataW/graphmaster/internal/testutil"
)

// dispatchEngine indexes a small repository where calls go through Go
// interfaces, Java interfaces and base classes, and TypeScript interfaces.
func dispatchEngine(t *testing.T) *Engine {
	t.Helper()
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		"go.mod": "module example.com/disp\n\ngo 1.22\n",
		"runner/runner.go": `package runner

type syncer interface {
	Sync(name string) error
	Stop()
}

type Worker struct{ s syncer }

func (w *Worker) Loop() error {
	w.s.Stop()
	return w.s.Sync("x")
}

type Real struct{}

func (r *Real) Sync(name string) error { return nil }
func (r *Real) Stop()                  {}
`,
		"runner/fake.go": `package runner

type Fake struct{}

func (f *Fake) Sync(name string) error { return nil }
func (f *Fake) Stop()                  {}
`,
		"runner/runner_test.go": `package runner

import "testing"

func TestLoop(t *testing.T) {
	w := &Worker{s: &Real{}}
	_ = w.Loop()
}
`,
		"java/src/main/java/com/ex/Shape.java": `package com.ex;

public interface Shape {
    double area();
}
`,
		"java/src/main/java/com/ex/Square.java": `package com.ex;

public class Square implements Shape {
    public double area() { return 1; }
}
`,
		"java/src/main/java/com/ex/Base.java": `package com.ex;

public abstract class Base {
    public void run() {}
}
`,
		"java/src/main/java/com/ex/Child.java": `package com.ex;

public class Child extends Base {
    public void run() {}
}
`,
		"java/src/main/java/com/ex/Report.java": `package com.ex;

public class Report {
    double total(Shape s) { return s.area(); }
    void go(Base b) { b.run(); }
}
`,
		"ts/shape.ts": `export interface Area {
  area(): number;
}

export class Box implements Area {
  area(): number { return 1; }
}

export function total(a: Area): number {
  return a.area();
}
`,
	})
	testutil.Git(t, dir, "init", "-q")
	return New(openIndexed(t, dir), Options{Freshener: fixedFreshener{}})
}

func TestCallersThroughDispatch(t *testing.T) {
	e := dispatchEngine(t)
	cases := []struct {
		symbol string
		want   []string
	}{
		{"runner.Real.Sync", []string{"runner.Worker.Loop", "~via runner.syncer.Sync"}},
		{"runner.Fake.Stop", []string{"runner.Worker.Loop", "~via runner.syncer.Stop"}},
		{"com.ex.Square.area", []string{"com.ex.Report.total", "~via com.ex.Shape.area"}},
		{"com.ex.Child.run", []string{"com.ex.Report.go", "~via com.ex.Base.run"}},
		{"Box.area", []string{"total", "~via Area.area"}},
	}
	for _, c := range cases {
		out := runTool(t, e, "callers", `{"symbol":"`+c.symbol+`"}`)
		mustContain(t, out, c.want...)
	}
	// Depth 2 continues above the dispatched caller.
	out := runTool(t, e, "callers", `{"symbol":"runner.Real.Sync","depth":2}`)
	mustContain(t, out, "runner.TestLoop")
	// The interface method's own callers are not marked.
	out = runTool(t, e, "callers", `{"symbol":"runner.syncer.Sync"}`)
	if strings.Contains(out, "~via") {
		t.Errorf("callers of the interface method are direct:\n%s", out)
	}
}

func TestImpactThroughDispatch(t *testing.T) {
	e := dispatchEngine(t)
	out := runTool(t, e, "impact", `{"symbol":"runner.Real.Sync"}`)
	mustContain(t, out, "~method runner.Worker.Loop")
	// Neither the interface method nor sibling implementations are affected.
	for _, bad := range []string{"runner.syncer.Sync", "runner.Fake.Sync"} {
		if strings.Contains(out, bad) {
			t.Errorf("impact lists %s:\n%s", bad, out)
		}
	}
	out = runTool(t, e, "impact", `{"symbol":"com.ex.Child.run"}`)
	mustContain(t, out, "~method com.ex.Report.go")
	out = runTool(t, e, "tests_for", `{"symbol":"runner.Fake.Sync"}`)
	mustContain(t, out, "runner.TestLoop")
}

func TestCalleesListImplementations(t *testing.T) {
	e := dispatchEngine(t)
	out := runTool(t, e, "callees", `{"symbol":"runner.Worker.Loop"}`)
	mustContain(t, out, "~implemented by runner.Fake.Sync, runner.Real.Sync",
		"~implemented by runner.Fake.Stop, runner.Real.Stop")
	out = runTool(t, e, "callees", `{"symbol":"com.ex.Report.go"}`)
	mustContain(t, out, "~implemented by com.ex.Child.run")
	out = runTool(t, e, "callees", `{"symbol":"ts/shape.ts:9"}`)
	mustContain(t, out, "~implemented by Box.area")
}

func TestImplLineCap(t *testing.T) {
	var impls []*sym
	for _, n := range []string{"a", "b", "c", "d", "e", "f", "g"} {
		impls = append(impls, &sym{QName: "p." + n})
	}
	got := implLine("  ", impls)
	if want := "    ~implemented by p.a, p.b, p.c, p.d, p.e (+2 more)"; got != want {
		t.Errorf("implLine = %q, want %q", got, want)
	}
}

func TestPkgLabel(t *testing.T) {
	cases := []struct {
		dir, name string
		isGo      bool
		want      string
	}{
		{"internal/adapters", "github.com/pragmataW/graphmaster/internal/adapters", true, ""},
		{"cmd/server", "example.com/fixture/cmd/server", true, ""},
		{"api/v2", "example.com/m/api/v2", true, ""},
		{"internal/x", "example.com/m/internal/x_test", true, "x_test"},
		{"js/test", "js/test/calc.test.js", false, ""},
		{"python/shop", "shop", false, ""},
		{"python/src", "shop", false, "shop"},
		{"java/src/main/java/com/ex", "com.ex", false, ""},
	}
	for _, c := range cases {
		lang := model.LangPython
		if c.isGo {
			lang = model.LangGo
		}
		if got := pkgLabel(c.dir, c.name, lang); got != c.want {
			t.Errorf("pkgLabel(%q, %q) = %q, want %q", c.dir, c.name, got, c.want)
		}
	}
}
