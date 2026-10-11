package resolve

import (
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pragmataW/graphmaster/internal/model"
	"github.com/pragmataW/graphmaster/internal/testutil"
)

// goFixture returns the Go part of the materialized fixture (tracked and
// untracked files, without vendor/) and the statuses the indexer assigns.
func goFixture(t *testing.T) (map[string]string, map[string]model.FileStatus) {
	t.Helper()
	root := testutil.Materialize(t)
	files := map[string]string{}
	err := filepath.WalkDir(filepath.Join(root, "go"), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel == "go/vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		files[rel] = string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := files["go/internal/util/slug.go"]; !ok {
		t.Fatal("untracked slug.go missing from materialized fixture")
	}
	return files, map[string]model.FileStatus{testutil.GeneratedPath: model.StatusGenerated}
}

func TestGoFixtureExpectedEdges(t *testing.T) {
	files, status := goFixture(t)
	r := fullBuild(t, files, status)
	exps := loadExpectations(t, "go")
	if len(exps) < 20 {
		t.Fatalf("only %d Go expectations loaded", len(exps))
	}
	checkExpectations(t, r, exps)
}

// TestGoFixtureConfidence pins exact tiers for representative references.
func TestGoFixtureConfidence(t *testing.T) {
	files, status := goFixture(t)
	r := fullBuild(t, files, status)
	es := r.edges()

	const (
		main    = "go/cmd/server/main.go::main"
		place   = "go/internal/service/order.go::OrderService.PlaceOrder"
		putA    = "go/internal/store/store.go::Audited.Put"
		getM    = "go/internal/store/store.go::Memory.Get"
		testPO  = "go/internal/service/order_test.go::TestPlaceOrder"
		storeGo = "go/internal/store/store.go::"
	)
	cases := []struct {
		src, name, dst string
		conf           int
	}{
		{main, "NewAudited", storeGo + "NewAudited", 100},
		{main, "PlaceOrder", place, 80},
		{main, "Put", storeGo + "Audited.Put", 80},
		{main, "Item", "go/internal/store/item.go::Item", 100},
		{main, "String", "", 0}, // gen.Shipped.String(): generated package has no nodes
		{main, "Fatal", "", 0},  // log.Fatal: external
		{place, "validate", "go/internal/service/order.go::validate", 90},
		{place, "Get", storeGo + "Store.Get", 80},
		{place, "store", "go/internal/service/order.go::OrderService.store", 80},
		{place, "Quantity", "go/internal/store/item.go::Item.Quantity", 80},
		{place, "Price", "go/internal/store/item.go::Item.Price", 80},
		{putA, "Put", storeGo + "Memory.Put", 80},
		{putA, "Memory", "", 0}, // embedded field access: no field def
		{putA, "Writes", storeGo + "Audited.Writes", 80},
		{getM, "items", storeGo + "Memory.items", 80},
		{getM, "RLock", "", 0}, // sync.RWMutex method
		{storeGo + "Memory.items", "Item", "go/internal/store/item.go::Item", 90},
		{testPO, "NewOrderService", "go/internal/service/order.go::NewOrderService", 90},
		{testPO, "Put", storeGo + "Memory.Put", 80},
		{testPO, "PlaceOrder", place, 80},
		{testPO, "Fatalf", "", 0},
		{"go/internal/util/format.go::Mirror", "Reverse", "", 0},
		{"go/internal/util/slug.go::Slug", "FormatID", "go/internal/util/format.go::FormatID", 90},
	}
	for _, c := range cases {
		got := edgesFrom(es, c.src, c.name)
		if len(got) == 0 {
			t.Errorf("%s -> %s: no edge", c.src, c.name)
		}
		for _, e := range got { // one per reference site
			if e.Dst != c.dst || e.Conf != c.conf {
				t.Errorf("%s -> %s (line %d): got %q conf %d, want %q conf %d", c.src, c.name, e.Line, e.Dst, e.Conf, c.dst, c.conf)
			}
		}
	}

	var embeds []resolvedEdge
	for _, e := range es {
		if e.Kind == model.EdgeEmbeds {
			embeds = append(embeds, e)
		}
	}
	if len(embeds) != 1 || embeds[0].Dst != storeGo+"Memory" || embeds[0].Conf != 90 {
		t.Errorf("embeds = %v", embeds)
	}
}

// TestGoFixtureIncrementalEquivalence applies edits one by one, resolving
// only pending names, and compares every step with a from-scratch build.
func TestGoFixtureIncrementalEquivalence(t *testing.T) {
	files, status := goFixture(t)
	const (
		storePath = "go/internal/store/store.go"
		orderPath = "go/internal/service/order.go"
		slugPath  = "go/internal/util/slug.go"
		mainPath  = "go/cmd/server/main.go"
	)

	// Start without store.go, then add it.
	partial := maps.Clone(files)
	delete(partial, storePath)
	inc := newTestRepo(t, partial)
	maps.Copy(inc.status, status)
	inc.index()
	inc.resolveAll(Options{})

	steps := []struct {
		name  string
		apply func(r *testRepo) []string // returns paths to (re)index
		del   []string
	}{
		{name: "add store.go", apply: func(r *testRepo) []string {
			r.files[storePath] = files[storePath]
			return []string{storePath}
		}},
		{name: "NewAudited returns *Memory", apply: func(r *testRepo) []string {
			r.files[storePath] = strings.Replace(r.files[storePath],
				"func NewAudited() *Audited {\n\treturn &Audited{Memory: NewMemory()}",
				"func NewAudited() *Memory {\n\treturn NewMemory()", 1)
			return []string{storePath}
		}},
		{name: "rename validate", apply: func(r *testRepo) []string {
			r.files[orderPath] = strings.ReplaceAll(r.files[orderPath], "validate", "check")
			return []string{orderPath}
		}},
		{name: "second FormatID in service", apply: func(r *testRepo) []string {
			r.files["go/internal/service/fmt.go"] = "package service\n\nfunc FormatID(id string) string { return id }\n"
			return []string{"go/internal/service/fmt.go"}
		}},
		{name: "delete slug.go", del: []string{slugPath}},
		{name: "delete store.go", del: []string{storePath}},
		{name: "main without imports", apply: func(r *testRepo) []string {
			r.files[mainPath] = "package main\n\nfunc main() {\n\tNewAudited().Put(nil)\n}\n"
			return []string{mainPath}
		}},
	}
	for _, step := range steps {
		if step.apply != nil {
			inc.index(step.apply(inc)...)
		}
		if len(step.del) > 0 {
			inc.remove(step.del...)
		}
		inc.resolvePending(Options{BatchNames: 7})
		full := fullBuild(t, inc.files, status)
		if got, want := inc.dump(), full.dump(); got != want {
			t.Fatalf("step %q: incremental != full:\n%s", step.name, diffLines(want, got))
		}
	}
}

// diffLines renders lines only in a (-) or only in b (+).
func diffLines(a, b string) string {
	in := func(s string) map[string]bool {
		m := map[string]bool{}
		for _, l := range strings.Split(s, "\n") {
			m[l] = true
		}
		return m
	}
	ma, mb := in(a), in(b)
	var out strings.Builder
	for _, l := range strings.Split(a, "\n") {
		if !mb[l] {
			out.WriteString("- " + l + "\n")
		}
	}
	for _, l := range strings.Split(b, "\n") {
		if !ma[l] {
			out.WriteString("+ " + l + "\n")
		}
	}
	return out.String()
}
