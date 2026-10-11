package resolve

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/pragmataW/graphmaster/internal/model"
	"github.com/pragmataW/graphmaster/internal/store"
)

const goMod = "module example.com/m\n\ngo 1.25\n"

// helperRepo has n packages defining Helper and a package c calling it
// unqualified, so only T4/T5 can resolve the call.
func helperRepo(n int) map[string]string {
	files := map[string]string{
		"go.mod":   goMod,
		"c/c.go":   "package c\n\nfunc Use() {\n\tHelper()\n}\n",
		"d/doc.go": "package d\n",
	}
	for i := range n {
		pkg := string(rune('p' + i))
		files[pkg+"/h.go"] = "package " + pkg + "\n\nfunc Helper() {}\n"
	}
	return files
}

func helperEdges(r *testRepo) []resolvedEdge {
	return edgesFrom(r.edges(), "c/c.go::Use", "Helper")
}

func TestAmbiguousFanOut(t *testing.T) {
	r := fullBuild(t, helperRepo(3), nil)
	es := helperEdges(r)
	if len(es) != 3 {
		t.Fatalf("want 3 T5 edges, got %v", es)
	}
	for _, e := range es {
		if e.Conf != 10 || e.Dst == "" {
			t.Errorf("T5 edge %v: want conf 10 (30/3)", e)
		}
	}
}

func TestAmbiguousReplacementAfterCandidateDeletion(t *testing.T) {
	r := fullBuild(t, helperRepo(2), nil)
	if es := helperEdges(r); len(es) != 2 || es[0].Conf != 15 {
		t.Fatalf("want 2 edges conf 15, got %v", es)
	}

	// Deleting one candidate nulls its edge; the sibling T5 edge remains
	// until the resolver replaces the whole group.
	r.remove("q/h.go")
	es := helperEdges(r)
	if len(es) != 2 {
		t.Fatalf("before resolve: want the nulled and the stale sibling edge, got %v", es)
	}
	r.resolvePending(Options{})
	es = helperEdges(r)
	if len(es) != 1 || es[0].Dst != "p/h.go::Helper" || es[0].Conf != int(ConfUnique) {
		t.Fatalf("after resolve: want one T4 edge to p/h.go::Helper, got %v", es)
	}
	if got, want := r.dump(), fullBuild(t, r.files, nil).dump(); got != want {
		t.Fatalf("incremental != full:\n%s", diffLines(want, got))
	}

	// Re-adding the candidate fans out again.
	r.files["q/h.go"] = "package q\n\nfunc Helper() {}\n"
	r.index("q/h.go")
	r.resolvePending(Options{})
	if es := helperEdges(r); len(es) != 2 {
		t.Fatalf("after re-add: want 2 edges, got %v", es)
	}
	if got, want := r.dump(), fullBuild(t, r.files, nil).dump(); got != want {
		t.Fatalf("incremental != full after re-add:\n%s", diffLines(want, got))
	}
}

func TestTooManyCandidatesStayUnresolved(t *testing.T) {
	r := fullBuild(t, helperRepo(MaxAmbiguous+1), nil)
	es := helperEdges(r)
	if len(es) != 1 || es[0].Dst != "" || es[0].Conf != 0 {
		t.Fatalf("want one unresolved edge, got %v", es)
	}
}

func TestCommonNameSkipsGlobalTiers(t *testing.T) {
	files := helperRepo(1)
	files["c/c.go"] = "package c\n\nfunc Use() {\n\tHelper()\n\tHelper()\n}\n" // 2 refs > limit 1
	r := newTestRepo(t, files)
	r.index()
	r.resolveAll(Options{CommonNameLimit: 1})
	if es := helperEdges(r); len(es) != 2 || es[0].Dst != "" || es[1].Dst != "" {
		t.Fatalf("unique global must be skipped for a common name, got %v", es)
	}
	// Lexical resolution is not affected by the cutoff.
	files["c/h.go"] = "package c\n\nfunc Helper() {}\n"
	r = newTestRepo(t, files)
	r.index()
	r.resolveAll(Options{CommonNameLimit: 1})
	for _, e := range helperEdges(r) {
		if e.Dst != "c/h.go::Helper" || e.Conf != int(ConfLexical) {
			t.Fatalf("want T2 edges despite cutoff, got %v", e)
		}
	}
}

// receiverRepo: main types x through Make's result; Make lives in a file
// that defines neither T nor U nor Do.
func receiverRepo(result string) map[string]string {
	return map[string]string{
		"go.mod":       goMod,
		"a/make.go":    "package a\n\nfunc Make() *" + result + " { return nil }\n",
		"a/t.go":       "package a\n\ntype T struct{}\n\nfunc (T) Do() {}\n",
		"a/u.go":       "package a\n\ntype U struct{}\n\nfunc (*U) Do() {}\n",
		"main/main.go": "package main\n\nimport \"example.com/m/a\"\n\nfunc main() {\n\tx := a.Make()\n\tx.Do()\n}\n",
	}
}

func TestDependentsFollowResultTypeChange(t *testing.T) {
	for _, noDeps := range []bool{false, true} {
		r := fullBuild(t, receiverRepo("T"), nil)
		do := func() []resolvedEdge { return edgesFrom(r.edges(), "main/main.go::main", "Do") }
		if es := do(); len(es) != 1 || es[0].Dst != "a/t.go::T.Do" || es[0].Conf != int(ConfReceiver) {
			t.Fatalf("initial: %v", es)
		}
		r.files["a/make.go"] = receiverRepo("U")["a/make.go"]
		r.index("a/make.go")
		stats := r.resolvePending(Options{NoDependents: noDeps})
		es := do()
		if noDeps {
			// Documents why dependents exist: Do is not a pending name.
			if len(es) != 1 || es[0].Dst != "a/t.go::T.Do" {
				t.Fatalf("NoDependents: expected the stale edge, got %v", es)
			}
			continue
		}
		if stats.Dependents == 0 {
			t.Errorf("no dependents re-resolved: %+v", stats)
		}
		if len(es) != 1 || es[0].Dst != "a/u.go::U.Do" || es[0].Conf != int(ConfReceiver) {
			t.Fatalf("after result change: %v", es)
		}
		if got, want := r.dump(), fullBuild(t, r.files, nil).dump(); got != want {
			t.Fatalf("incremental != full:\n%s", diffLines(want, got))
		}
	}
}

func TestDependentsBudget(t *testing.T) {
	r := fullBuild(t, receiverRepo("T"), nil)
	r.files["a/make.go"] = receiverRepo("U")["a/make.go"]
	r.index("a/make.go")
	stats := r.resolvePending(Options{MaxDependentFiles: 1})
	if stats.DependentsSkipped == 0 || stats.Dependents != 0 {
		t.Fatalf("budget not applied: %+v", stats)
	}
}

func TestResolvePendingDrainsQueueAndIsIdempotent(t *testing.T) {
	r := newTestRepo(t, helperRepo(1))
	r.index()
	stats := r.resolvePending(Options{BatchNames: 1})
	if stats.Names == 0 || stats.Batches < 2 || stats.ByTier[Tier4Unique] != 1 {
		t.Fatalf("stats = %+v", stats)
	}
	var n int
	if err := r.st.DB().QueryRow(`SELECT count(*) FROM pending_names`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("pending left: %d %v", n, err)
	}
	before := r.dump()
	if stats := r.resolvePending(Options{}); stats.Names != 0 || stats.Edges != 0 {
		t.Fatalf("empty queue did work: %+v", stats)
	}
	r.resolveAll(Options{})
	if after := r.dump(); after != before {
		t.Fatalf("ResolveAll changed a resolved graph:\n%s", diffLines(before, after))
	}
}

func TestCancelledRunKeepsQueue(t *testing.T) {
	r := newTestRepo(t, helperRepo(1))
	r.index()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := ResolvePending(ctx, r.st, r.rc(), Options{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	var n int
	if err := r.st.DB().QueryRow(`SELECT count(*) FROM pending_names`).Scan(&n); err != nil || n == 0 {
		t.Fatalf("queue lost after cancellation: %d %v", n, err)
	}
	r.resolvePending(Options{})
	if es := helperEdges(r); len(es) != 1 || es[0].Dst == "" {
		t.Fatalf("resume failed: %v", es)
	}
}

func TestBulkBuildMatchesIncremental(t *testing.T) {
	files, status := goFixture(t)
	want := fullBuild(t, files, status).dump()

	r := newTestRepo(t, files)
	for p, s := range status {
		r.status[p] = s
	}
	ctx := context.Background()
	w, err := r.st.NewWriter(ctx, store.WriterOptions{Bulk: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range r.codePaths() {
		r.writeFile(w, p)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	// No FinishBulk: the resolver must finish an interrupted bulk build.
	res := &Resolver{}
	if err := res.ResolveAll(ctx, r.st, r.rc()); err != nil {
		t.Fatal(err)
	}
	if res.Last.Refs == 0 {
		t.Fatalf("adapter stats empty: %+v", res.Last)
	}
	if got := r.dump(); got != want {
		t.Fatalf("bulk != incremental build:\n%s", diffLines(want, got))
	}
	if _, ok, _ := r.st.Meta(ctx, store.MetaBulk); ok {
		t.Fatal("bulk flag still set")
	}
}

func TestStaleSourceDisablesReceiverTyping(t *testing.T) {
	files := receiverRepo("T")
	r := newTestRepo(t, files)
	r.index()
	// The file on disk changed after indexing: its length no longer
	// matches files.size, so receiver typing must not read it.
	stale := func(p string) ([]byte, error) {
		src := r.files[p]
		if p == "main/main.go" {
			src += "\n// edited\n"
		}
		return []byte(src), nil
	}
	if _, err := ResolveAll(context.Background(), r.st, r.rc(), Options{Source: stale}); err != nil {
		t.Fatal(err)
	}
	es := edgesFrom(r.edges(), "main/main.go::main", "Do")
	if len(es) != 2 || es[0].Conf != 15 {
		t.Fatalf("want untyped T5 fallback without source, got %v", es)
	}
}

func TestRegistry(t *testing.T) {
	if _, ok := For(model.LangGo).(goResolver); !ok {
		t.Fatalf("Go resolver not registered: %T", For(model.LangGo))
	}
	const lang = model.LangPHP
	prev, had := registry.m[lang]
	t.Cleanup(func() {
		unregister(lang)
		if had {
			Register(lang, prev)
		}
	})
	unregister(lang)
	if _, ok := For(lang).(Generic); !ok {
		t.Fatalf("unregistered language must fall back to Generic, got %T", For(lang))
	}
	Register(lang, Generic{})
	mustPanic(t, "duplicate", func() { Register(lang, Generic{}) })
	mustPanic(t, "invalid", func() { Register(model.LangUnknown, Generic{}) })
	mustPanic(t, "nil", func() { Register(model.LangC, nil) })
}

func mustPanic(t *testing.T, name string, f func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Errorf("%s: no panic", name)
		}
	}()
	f()
}

func TestTierString(t *testing.T) {
	got := []string{}
	for tr := TierNone; tr < tierCount; tr++ {
		got = append(got, tr.String())
	}
	if s := strings.Join(got, ","); s != "none,T1,T2,T3,T4,T5" {
		t.Fatal(s)
	}
}
