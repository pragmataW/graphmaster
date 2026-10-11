package index

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pragmataW/graphmaster/internal/config"
	"github.com/pragmataW/graphmaster/internal/model"
	"github.com/pragmataW/graphmaster/internal/store"
	"github.com/pragmataW/graphmaster/internal/testutil"
)

func TestFreshenFixtureFullBuild(t *testing.T) {
	dir := testutil.Materialize(t)
	ix := openIx(t, dir, Options{Now: futureClock})
	res := freshen(t, ix)
	if !res.FullBuild || res.Added == 0 || res.Parsed == 0 {
		t.Fatalf("first run: %+v", res)
	}
	ctx := context.Background()
	if v, _, _ := ix.Store().Meta(ctx, store.MetaState); v != store.StateReady {
		t.Fatalf("state = %q", v)
	}
	if v, ok, _ := ix.Store().Meta(ctx, store.MetaBulk); ok {
		t.Fatalf("bulk flag left behind: %q", v)
	}

	// File statuses from testdata/fixture-repo/EXPECTED.md.
	want := map[string]model.FileStatus{
		testutil.GeneratedPath:     model.StatusGenerated,
		testutil.BigFilePath:       model.StatusTooBig,
		"go/internal/util/slug.go": model.StatusOK,
		"go/cmd/server/main.go":    model.StatusOK,
	}
	for p, st := range want {
		if f := fileRow(t, ix, p); f.Status != st {
			t.Errorf("%s: status %v, want %v", p, f.Status, st)
		}
	}
	if f := fileRow(t, ix, "go/internal/util/slug.go"); f.Tracked {
		t.Errorf("untracked slug.go stored as tracked")
	}
	if f := fileRow(t, ix, "go/cmd/server/main.go"); !f.Tracked || f.PkgID == 0 || f.LOC == 0 || len(f.Blob) != 20 {
		t.Errorf("main.go row: %+v", f)
	}
	for _, p := range append(testutil.VendorPaths(), testutil.MinifiedPath, "build/output.js", "notes/scratch.md") {
		if _, err := ix.Store().FileByPath(ctx, p); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s must not be indexed (err=%v)", p, err)
		}
	}
	d := dump(t, ix)
	for _, s := range []string{"example.com/fixture/internal/store.Memory.Get", "go:example.com/fixture/internal/service"} {
		if !strings.Contains(d, s) {
			t.Errorf("dump lacks %q", s)
		}
	}
	if strings.Contains(d, "!dangling") {
		t.Errorf("dangling references in dump")
	}

	// Second run: nothing to do (clock says files are old enough).
	res = freshen(t, ix)
	if !res.Clean() || res.Parsed != 0 {
		t.Fatalf("second run not clean: %+v", res)
	}
	if res.Banner() != "fresh" {
		t.Errorf("banner = %q", res.Banner())
	}
	requireSameDump(t, "after no-op", dump(t, ix), d)
}

func TestTouchDoesNotReparse(t *testing.T) {
	dir := testutil.Materialize(t)
	ix := openIx(t, dir, Options{Now: futureClock})
	freshen(t, ix)
	before := dump(t, ix)

	now := time.Now()
	if err := os.Chtimes(filepath.Join(dir, "go/internal/util/format.go"), now, now); err != nil {
		t.Fatal(err)
	}
	res := freshen(t, ix)
	if res.StatOnly != 1 || res.Parsed != 0 || res.CacheHits != 0 || res.Changed() != 0 {
		t.Fatalf("touch: %+v", res)
	}
	requireSameDump(t, "touch", dump(t, ix), before)
}

func TestEditReparsesOnlyThatFile(t *testing.T) {
	dir := testutil.Materialize(t)
	ix := openIx(t, dir, Options{Now: futureClock})
	freshen(t, ix)

	p := "go/internal/util/format.go"
	b, err := os.ReadFile(filepath.Join(dir, p))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, p, string(b)+"\n// Shout upper-cases s.\nfunc Shout(s string) string { return FormatID(s) }\n")
	res := freshen(t, ix)
	if res.Modified != 1 || res.Parsed != 1 || res.Added+res.Deleted != 0 {
		t.Fatalf("edit: %+v", res)
	}
	if !strings.HasPrefix(res.Banner(), "fresh (+1 files, ") {
		t.Errorf("banner = %q", res.Banner())
	}
	if !strings.Contains(dump(t, ix), "util.Shout#function") {
		t.Fatal("new function not indexed")
	}
	requireSameDump(t, "edit", dump(t, ix), scratchDump(t, dir, Options{}))
}

func TestDirtyHintForcesContentCheck(t *testing.T) {
	dir := testutil.Materialize(t)
	ix := openIx(t, dir, Options{Now: futureClock})
	freshen(t, ix)
	ctx := context.Background()
	abs := filepath.Join(ix.Root().WorkTree, "go", "internal", "util", "slug.go")
	if err := ix.Store().AddDirtyHints(ctx, []string{"go/internal/util/format.go", abs}, time.Now().UnixNano()); err != nil {
		t.Fatal(err)
	}
	res := freshen(t, ix)
	if res.Parsed != 0 || res.Changed() != 0 {
		t.Fatalf("unchanged hinted file was reprocessed: %+v", res)
	}
	var n int
	if err := ix.Store().DB().QueryRow(`SELECT count(*) FROM dirty_hints`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("dirty hints not cleared: %d %v", n, err)
	}
}

func TestLockTimeoutReportsStale(t *testing.T) {
	dir := testutil.Materialize(t)
	ix := openIx(t, dir, Options{Now: futureClock})
	freshen(t, ix)

	held := store.NewLock(ix.Location().Dir)
	ok, err := held.TryLock(context.Background(), time.Second)
	if err != nil || !ok {
		t.Fatalf("hold lock: %v %v", ok, err)
	}
	defer held.Unlock()

	writeFile(t, dir, "go/internal/util/new.go", "package util\n\nfunc Fresh() {}\n")
	start := time.Now()
	res, err := ix.Freshen(context.Background(), 150*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Stale || res.Pending != 1 || res.Building {
		t.Fatalf("lock timeout: %+v", res)
	}
	if res.Banner() != "⚠ stale: 1 files pending" {
		t.Errorf("banner = %q", res.Banner())
	}
	if el := time.Since(start); el > 5*time.Second || res.Waited < 100*time.Millisecond {
		t.Errorf("waited %v (result %v)", el, res.Waited)
	}

	// A first build in progress elsewhere is reported as building.
	ctx := context.Background()
	_ = ix.Store().SetMeta(ctx, store.MetaState, store.StateBuilding)
	_ = ix.Store().SetMeta(ctx, store.MetaProgress, "37")
	res, err = ix.Freshen(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Building || res.Progress != 37 || res.Banner() != "⚠ building 37% (partial graph)" {
		t.Fatalf("building: %+v %q", res, res.Banner())
	}

	held.Unlock()
	res = freshen(t, ix)
	if res.Added != 1 {
		t.Fatalf("after unlock: %+v", res)
	}
}

func TestNonGitDirectory(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "main.go", "package main\n\nfunc main() { helper() }\n\nfunc helper() {}\n")
	if _, err := Open(context.Background(), dir, Options{}); !errors.Is(err, ErrNotGitRepo) {
		t.Fatalf("Open(non-git) err = %v", err)
	}
	ix := openIx(t, dir, Options{AllowNoGit: true})
	res := freshen(t, ix)
	if res.Added != 1 {
		t.Fatalf("forced non-git index: %+v", res)
	}
	if f := fileRow(t, ix, "main.go"); f.Tracked || f.Status != model.StatusOK {
		t.Fatalf("main.go: %+v", f)
	}
	// Later opens find the forced root without the flag.
	ix2, err := Open(context.Background(), dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	ix2.Close()
}

func TestParsePanicIsContained(t *testing.T) {
	dir := testutil.Materialize(t)
	ix := openIx(t, dir, Options{Workers: 2})
	bad := "go/internal/util/format.go"
	ix.parseHook = func(path string) {
		if path == bad {
			panic("boom")
		}
	}
	res := freshen(t, ix)
	if res.ParseErrors != 1 {
		t.Fatalf("parse errors = %d, want 1 (%+v)", res.ParseErrors, res)
	}
	if f := fileRow(t, ix, bad); f.Status != model.StatusParseErr {
		t.Fatalf("%s status %v", bad, f.Status)
	}
	if f := fileRow(t, ix, "go/internal/util/slug.go"); f.Status != model.StatusOK {
		t.Fatalf("other files must still parse: %v", f.Status)
	}
}

func TestExtractorVersionBumpReparsesOneLanguage(t *testing.T) {
	dir := testutil.Materialize(t)
	ix := openIx(t, dir, Options{Now: futureClock})
	freshen(t, ix)
	before := dump(t, ix)
	var goFiles int
	_ = ix.Store().DB().QueryRow(`SELECT count(*) FROM files WHERE lang = ?`, int(model.LangGo)).Scan(&goFiles)

	if err := ix.Store().SetMeta(context.Background(), store.MetaExtractorVerPf+"go", "999"); err != nil {
		t.Fatal(err)
	}
	res := freshen(t, ix)
	if res.Modified != goFiles || res.Parsed != 0 || res.Rebuilt != "" {
		t.Fatalf("version bump: %+v (go files %d)", res, goFiles)
	}
	requireSameDump(t, "xver bump", dump(t, ix), before)
}

func TestSchemaMismatchRebuildKeepsCache(t *testing.T) {
	dir := testutil.Materialize(t)
	ix := openIx(t, dir, Options{})
	first := freshen(t, ix)
	want := dump(t, ix)
	if _, err := ix.Store().DB().Exec(`PRAGMA user_version = 999`); err != nil {
		t.Fatal(err)
	}
	ix.Close()

	ix2 := openIx(t, dir, Options{})
	res := freshen(t, ix2)
	if res.Rebuilt == "" || !res.FullBuild {
		t.Fatalf("expected rebuild: %+v", res)
	}
	if res.Parsed != 0 || res.CacheHits != first.Parsed {
		t.Fatalf("rebuild should hit cache.db for every file: %+v (first parsed %d)", res, first.Parsed)
	}
	requireSameDump(t, "schema rebuild", dump(t, ix2), want)
}

func TestConfigChangeRebuilds(t *testing.T) {
	dir := testutil.Materialize(t)
	ix := openIx(t, dir, Options{})
	freshen(t, ix)
	ix.Close()

	cfg := config.Default()
	cfg.MaxFileSize = 200 // most fixture files become too_big
	ix2 := openIx(t, dir, Options{Config: cfg})
	res := freshen(t, ix2)
	if res.Rebuilt == "" {
		t.Fatalf("config change did not rebuild: %+v", res)
	}
	requireSameDump(t, "config change", dump(t, ix2), scratchDump(t, dir, Options{Config: cfg}))
}

func TestManifestChangeRederives(t *testing.T) {
	dir := testutil.Materialize(t)
	ix := openIx(t, dir, Options{})
	freshen(t, ix)
	writeFile(t, dir, "go/go.mod", "module example.com/renamed\n\ngo 1.22\n")
	res := freshen(t, ix)
	// Only the files whose module identity or imports consulted go.mod
	// are reprocessed (from cache), not the whole graph.
	if res.Rebuilt != "" || res.Modified == 0 || res.Parsed != 0 {
		t.Fatalf("go.mod change: %+v", res)
	}
	for _, f := range []string{"python/shop/models.py", "ts/src/models/user.ts"} {
		if fileRow(t, ix, f) == nil {
			t.Fatalf("%s missing", f)
		}
	}
	d := dump(t, ix)
	if !strings.Contains(d, "example.com/renamed/internal/store.Memory") {
		t.Fatal("qnames not re-derived from the new module path")
	}
	requireSameDump(t, "go.mod change", d, scratchDump(t, dir, Options{}))
}

func TestSingleflight(t *testing.T) {
	dir := testutil.Materialize(t)
	ix := openIx(t, dir, Options{})
	release := make(chan struct{})
	var once sync.Once
	ix.parseHook = func(string) { once.Do(func() { <-release }) } // hold the first run
	type out struct {
		r   *Result
		err error
	}
	ch := make(chan out, 4)
	for range 4 {
		go func() {
			r, err := ix.Freshen(context.Background(), 30*time.Second)
			ch <- out{r, err}
		}()
	}
	time.Sleep(100 * time.Millisecond) // let every caller join the run
	close(release)
	for range 4 {
		o := <-ch
		if o.err != nil {
			t.Fatal(o.err)
		}
		if !o.r.FullBuild {
			t.Fatalf("caller did not share the build: %+v", o.r)
		}
	}
	if got := ix.opts.Resolver.(*fakeResolver).calls.Load(); got != 1 {
		t.Fatalf("resolver ran %d times, want 1 shared run", got)
	}
}

func TestStatusFormat(t *testing.T) {
	dir := testutil.Materialize(t)
	ix := openIx(t, dir, Options{})
	freshen(t, ix)
	s, err := ix.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.State != store.StateReady || s.Files[model.StatusOK] == 0 || s.Langs[model.LangGo] == 0 || s.DBBytes == 0 || s.LastScan.IsZero() {
		t.Fatalf("status: %+v", s)
	}
	out := s.Format()
	for _, want := range []string{"state: ready", "mode: balanced", "languages: ", "go ", "too_big 1", "db size: "} {
		if !strings.Contains(out, want) {
			t.Errorf("status output lacks %q:\n%s", want, out)
		}
	}
}
