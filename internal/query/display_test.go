package query

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pragmataW/graphmaster/internal/model"
	"github.com/pragmataW/graphmaster/internal/testutil"
)

func TestDisplayQName(t *testing.T) {
	for _, c := range []struct {
		lang        model.Lang
		qname, path string
		want        string
	}{
		// Go: the import path collapses to its last segment.
		{model.LangGo, "github.com/acme/app/internal/steer.ClassifyPattern", "internal/steer/classify.go", "steer.ClassifyPattern"},
		{model.LangGo, "github.com/acme/app/internal/steer.Index.Freshen", "internal/steer/engine.go", "steer.Index.Freshen"},
		{model.LangGo, "github.com/acme/app.Version", "version.go", "app.Version"},
		{model.LangGo, "fixture.Main", "main.go", "fixture.Main"},
		// TS/JS: the module is the file path, already printed next to it.
		{model.LangTypeScript, "ts/src/models/user.ts.User", "ts/src/models/user.ts", "User"},
		{model.LangTypeScript, "ts/src/models/user.ts.Admin.role", "ts/src/models/user.ts", "Admin.role"},
		{model.LangTSX, "ts/src/components/UserCard.tsx.UserCard", "ts/src/components/UserCard.tsx", "UserCard"},
		{model.LangJavaScript, "js/src/format.mjs.formatTotal", "js/src/format.mjs", "formatTotal"},
		{model.LangTypeScript, "ts/src/models/user.ts", "ts/src/models/user.ts", "ts/src/models/user.ts"},
		// Namespaced / dotted-module languages keep their qualified names.
		{model.LangJava, "com.example.zoo.Animal", "java/src/main/java/com/example/zoo/Animal.java", "com.example.zoo.Animal"},
		{model.LangPython, "shop.models.Item", "python/shop/models.py", "shop.models.Item"},
		{model.LangCSharp, "Fixture.App.Program.Main", "csharp/src/App/Program.cs", "Fixture.App.Program.Main"},
		{model.LangPHP, `App\format_money`, "php/src/helpers.php", `App\format_money`},
		{model.LangRust, "fixture_shapes::geometry::square_of", "rust/src/geometry.rs", "fixture_shapes::geometry::square_of"},
		{model.LangCPP, "geo::Canvas", "cpp/include/geo/canvas.hpp", "geo::Canvas"},
		{model.LangC, "clamp_int", "c/src/util.c", "clamp_int"},
	} {
		s := &sym{Lang: c.lang, QName: c.qname, Path: c.path}
		if got := s.displayQName(); got != c.want {
			t.Errorf("displayQName(%s, %q) = %q, want %q", c.lang, c.qname, got, c.want)
		}
	}
}

// TestDisplayFormResolves: every symbol's short display form is accepted
// back as a symbol argument, and so is its full stored qname.
func TestDisplayFormResolves(t *testing.T) {
	e := fixtureEngine(t)
	q := e.querier()
	ctx := context.Background()
	all, err := q.symsWhere(ctx, `1`)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) < 100 {
		t.Fatalf("only %d symbols in the fixture", len(all))
	}
	contains := func(ss []*sym, id int64) bool {
		for _, s := range ss {
			if s.ID == id {
				return true
			}
		}
		return false
	}
	for _, s := range all {
		if s.Flags.Has(model.FlagDeclaration) {
			continue // prototypes yield to their definitions
		}
		for _, arg := range []string{s.displayQName(), s.QName} {
			ss, err := q.resolveSymbol(ctx, arg)
			if err != nil {
				t.Fatalf("%q: %v", arg, err)
			}
			if !contains(ss, s.ID) {
				t.Errorf("%q does not resolve to %s %s:%d", arg, s.QName, s.Path, s.Start)
			}
		}
	}
	// Short forms of unique symbols resolve to exactly that symbol.
	for arg, want := range map[string]string{
		"service.OrderService.PlaceOrder": "example.com/fixture/internal/service.OrderService.PlaceOrder",
		"store.NewMemory":                 "example.com/fixture/internal/store.NewMemory",
		"server.main":                     "example.com/fixture/cmd/server.main",
		"Admin.role":                      "ts/src/models/user.ts.Admin.role",
	} {
		ss, err := q.resolveSymbol(ctx, arg)
		if err != nil || len(ss) != 1 || ss[0].QName != want {
			t.Errorf("%q: got %d matches (err %v), want only %s", arg, len(ss), err, want)
		}
	}
}

func TestRowsUseDisplayQName(t *testing.T) {
	e := fixtureEngine(t)
	out := runTool(t, e, "find_symbol", `{"query":"PlaceOrder"}`)
	mustContain(t, out, "method service.OrderService.PlaceOrder  go/internal/service/order.go:24-32")
	if strings.Contains(out, "example.com/fixture/") {
		t.Errorf("rows still carry the module path:\n%s", out)
	}
	out = runTool(t, e, "impact", `{"symbol":"Memory.Put"}`)
	if strings.Contains(out, "example.com/fixture/") {
		t.Errorf("impact rows still carry the module path:\n%s", out)
	}
}

// writeTree writes files (path -> content) below dir.
func writeTree(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, src := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestRepoMapTopSymbols: top symbols skip tests and test files, prefer
// referenced definitions, and are not repeated on directory rows whose
// subdirectories are listed.
func TestRepoMapTopSymbols(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		"go.mod": "module example.com/demo\n\ngo 1.22\n",
		"cmd/app/main.go": `package main

import "example.com/demo/lib"

func main() { lib.Used(); lib.Used2(); Run() }

// Run is exported but called once.
func Run() {}
`,
		"cmd/app/main_test.go": `package main

import "testing"

func Helper() {}

func TestAlpha(t *testing.T) { Helper(); Helper() }
func TestBeta(t *testing.T)  { Helper() }
`,
		"lib/lib.go": `package lib

func Used()  { Used2() }
func Used2() {}
func Zeta()  {}
`,
		"lib/lib_test.go": `package lib

import "testing"

func TestUsed(t *testing.T) { Used(); Used2(); Zeta() }
`,
	})
	testutil.Git(t, dir, "init", "-q")
	e := New(openIndexed(t, dir), Options{Freshener: fixedFreshener{}})

	d1 := lines(runTool(t, e, "repo_map", `{"depth":1}`))
	row := func(ls []string, label string) string {
		t.Helper()
		for _, l := range ls {
			if strings.HasPrefix(strings.TrimLeft(l, " "), label+"  ") {
				return l
			}
		}
		t.Fatalf("no %s row in:\n%s", label, strings.Join(ls, "\n"))
		return ""
	}
	for _, l := range d1 {
		if strings.Contains(l, "Test") || strings.Contains(l, "Helper") {
			t.Errorf("test symbol in repo map: %q", l)
		}
	}
	if r := row(d1, "./"); strings.Contains(r, "·") {
		t.Errorf("root row repeats its children's symbols: %q", r)
	}
	if r := row(d1, "lib/"); !strings.HasSuffix(r, "· Used2, Used") {
		t.Errorf("lib row should list referenced symbols by fan-in: %q", r)
	}
	if r := row(d1, "cmd/"); !strings.HasSuffix(r, "· Run") {
		t.Errorf("collapsed cmd row should summarize its subtree: %q", r)
	}

	d2 := lines(runTool(t, e, "repo_map", `{"depth":2}`))
	if r := row(d2, "cmd/"); strings.Contains(r, "·") {
		t.Errorf("expanded cmd row repeats app's symbols: %q", r)
	}
	if r := row(d2, "app/"); !strings.HasSuffix(r, "· Run") {
		t.Errorf("app row: %q", r)
	}
}
