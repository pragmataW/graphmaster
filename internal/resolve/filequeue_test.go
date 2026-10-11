package resolve

import (
	"context"
	"strconv"
	"testing"

	"github.com/pragmataW/graphmaster/internal/model"
)

// queueFile adds path's file to the per-file resolution queue.
func (r *testRepo) queueFile(path string) int64 {
	r.t.Helper()
	f, err := r.st.FileByPath(context.Background(), path)
	if err != nil {
		r.t.Fatal(err)
	}
	if err := r.st.SetMeta(context.Background(), PendingFileKey(f.ID), "1"); err != nil {
		r.t.Fatal(err)
	}
	return f.ID
}

func (r *testRepo) queuedFiles() int {
	r.t.Helper()
	var n int
	if err := r.st.DB().QueryRow(`SELECT count(*) FROM meta WHERE key >= ? AND key < ?`,
		PendingFilePrefix, PendingFilePrefix+"\xff").Scan(&n); err != nil {
		r.t.Fatal(err)
	}
	return n
}

func TestPendingFileKey(t *testing.T) {
	if got := PendingFileKey(42); got != PendingFilePrefix+"42" {
		t.Fatalf("PendingFileKey(42) = %q", got)
	}
	id, ok := pendingFileID(PendingFileKey(7))
	if !ok || id != 7 {
		t.Fatalf("pendingFileID = %d, %v", id, ok)
	}
	for _, bad := range []string{"x", PendingFilePrefix, PendingFilePrefix + "-1", PendingFilePrefix + "a"} {
		if _, ok := pendingFileID(bad); ok {
			t.Errorf("pendingFileID(%q) accepted", bad)
		}
	}
}

// TestFileQueueResolvesOnlyThatFile: a queued file's own references are
// re-resolved without touching references elsewhere that share their names.
func TestFileQueueResolvesOnlyThatFile(t *testing.T) {
	files := helperRepo(1)
	files["e/e.go"] = "package e\n\nfunc Other() {\n\tHelper()\n}\n"
	r := fullBuild(t, files, nil)
	want := r.dump()

	// Forget every resolution of c/c.go and e/e.go.
	if _, err := r.st.DB().Exec(`UPDATE edges SET dst = NULL, conf = 0 WHERE kind <> ?`, int(model.EdgeContains)); err != nil {
		t.Fatal(err)
	}
	r.queueFile("c/c.go")
	stats := r.resolvePending(Options{})
	if stats.Files != 1 || stats.Names != 0 {
		t.Fatalf("stats = %+v, want 1 file and no names", stats)
	}
	if es := edgesFrom(r.edges(), "c/c.go::Use", "Helper"); len(es) != 1 || es[0].Dst == "" {
		t.Fatalf("c/c.go not resolved: %v", es)
	}
	if es := edgesFrom(r.edges(), "e/e.go::Other", "Helper"); len(es) != 1 || es[0].Dst != "" {
		t.Fatalf("e/e.go must stay untouched: %v", es)
	}
	if n := r.queuedFiles(); n != 0 {
		t.Fatalf("%d files still queued", n)
	}
	r.queueFile("e/e.go")
	r.resolvePending(Options{BatchFiles: 1})
	if got := r.dump(); got != want {
		t.Fatalf("file queue != full build:\n%s", diffLines(want, got))
	}
}

func TestFileQueueDropsVanishedFilesAndFullRunClears(t *testing.T) {
	r := fullBuild(t, helperRepo(1), nil)
	ctx := context.Background()
	if err := r.st.SetMeta(ctx, PendingFileKey(99999), "1"); err != nil {
		t.Fatal(err)
	}
	if stats := r.resolvePending(Options{}); stats.Files != 0 || stats.Refs != 0 {
		t.Fatalf("vanished file resolved something: %+v", stats)
	}
	if n := r.queuedFiles(); n != 0 {
		t.Fatalf("vanished file key kept: %d", n)
	}
	for i := range 3 {
		if err := r.st.SetMeta(ctx, PendingFileKey(int64(i+1)), strconv.Itoa(i)); err != nil {
			t.Fatal(err)
		}
	}
	r.resolveAll(Options{})
	if n := r.queuedFiles(); n != 0 {
		t.Fatalf("ResolveAll left %d queued files", n)
	}
}

func TestIncrementalHints(t *testing.T) {
	res := &Resolver{}
	if got := res.CommonNameLimit(); got != DefaultCommonNameLimit {
		t.Fatalf("CommonNameLimit = %d", got)
	}
	res.Options.CommonNameLimit = 7
	if got := res.CommonNameLimit(); got != 7 {
		t.Fatalf("CommonNameLimit = %d", got)
	}
	for l, want := range map[model.Lang]bool{
		model.LangGo: false, model.LangJava: false, model.LangPHP: false, model.LangCSharp: false,
		model.LangC: false, model.LangCPP: false, model.LangRust: false,
		model.LangPython: true, model.LangTypeScript: true, model.LangTSX: true, model.LangJavaScript: true,
		model.LangUnknown: true,
	} {
		if got := res.ReadsForeignSource(l); got != want {
			t.Errorf("ReadsForeignSource(%v) = %v, want %v", l, got, want)
		}
	}
}

// TestDependentsQueue: a name queued for dependents only re-resolves the
// receiver-shaped references of the files mentioning it, not the
// references of the name itself.
func TestDependentsQueue(t *testing.T) {
	r := fullBuild(t, receiverRepo("T"), nil)
	// Make's result type changes in place (same node), as the indexer
	// records a body-only edit.
	if _, err := r.st.DB().Exec(`UPDATE nodes SET sig = replace(sig, '*T', '*U')
		WHERE name_id = (SELECT id FROM names WHERE name = 'Make')`); err != nil {
		t.Fatal(err)
	}
	r.files["a/make.go"] = receiverRepo("U")["a/make.go"]
	var makeID int64
	if err := r.st.DB().QueryRow(`SELECT id FROM names WHERE name = 'Make'`).Scan(&makeID); err != nil {
		t.Fatal(err)
	}
	if err := r.st.SetMeta(context.Background(), PendingDependentsKey(makeID), "1"); err != nil {
		t.Fatal(err)
	}
	stats := r.resolvePending(Options{})
	if stats.Dependents == 0 || stats.Refs != stats.Dependents {
		t.Fatalf("stats = %+v, want dependents only", stats)
	}
	if es := edgesFrom(r.edges(), "main/main.go::main", "Do"); len(es) != 1 || es[0].Dst != "a/u.go::U.Do" {
		t.Fatalf("dependent not re-resolved: %v", es)
	}
	var n int
	if err := r.st.DB().QueryRow(`SELECT count(*) FROM meta WHERE key >= ? AND key < ?`,
		PendingDependentsPrefix, PendingDependentsPrefix+"\xff").Scan(&n); err != nil || n != 0 {
		t.Fatalf("dependents queue left %d keys (%v)", n, err)
	}
}

// TestRemovedQueue: a name that only lost definitions re-resolves its
// unresolved and low-confidence references (and dependents), not the
// references already resolved with high confidence to a surviving
// definition.
func TestRemovedQueue(t *testing.T) {
	files := map[string]string{
		"go.mod": goMod,
		"a/h.go": "package a\n\nfunc Helper() {}\n",
		"b/h.go": "package b\n\nfunc Helper() {}\n",
		"c/c.go": "package c\n\nimport \"example.com/m/a\"\n\nfunc Use() {\n\ta.Helper()\n}\n",
		"d/d.go": "package d\n\nfunc Guess() {\n\tHelper()\n}\n",
		"e/e.go": "package e\n\nfunc Other() {}\n",
	}
	r := fullBuild(t, files, nil)
	if es := edgesFrom(r.edges(), "d/d.go::Guess", "Helper"); len(es) != 2 {
		t.Fatalf("want a T5 fan-out, got %v", es)
	}
	r.remove("b/h.go")
	var id int64
	if err := r.st.DB().QueryRow(`SELECT id FROM names WHERE name = 'Helper'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := r.st.DB().Exec(`DELETE FROM pending_names WHERE name_id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if err := r.st.SetMeta(context.Background(), PendingRemovedKey(id), "1"); err != nil {
		t.Fatal(err)
	}
	stats := r.resolvePending(Options{})
	if stats.Refs != 1 {
		t.Fatalf("stats = %+v, want only the fan-out re-resolved", stats)
	}
	if got, want := r.dump(), fullBuild(t, r.files, nil).dump(); got != want {
		t.Fatalf("removed queue != full build:\n%s", diffLines(want, got))
	}
}
