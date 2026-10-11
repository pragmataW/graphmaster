package repo_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/pragmataW/graphmaster/internal/config"
	"github.com/pragmataW/graphmaster/internal/model"
	"github.com/pragmataW/graphmaster/internal/repo"
	"github.com/pragmataW/graphmaster/internal/testutil"
)

// TestFixtureRepoStatuses runs the discovery and classification steps of the
// freshness pipeline (tracked index + untracked walk + config excludes +
// Classify) over the shared fixture repository and checks the file-status
// table in testdata/fixture-repo/EXPECTED.md.
func TestFixtureRepoStatuses(t *testing.T) {
	dir := testutil.Materialize(t)
	ctx := context.Background()
	cfg := config.Default()

	root, err := repo.FindRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	ix, err := repo.ReadIndex(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	gc := repo.LoadGitConfig(root)
	ig, err := repo.NewIgnore(root, gc, cfg.AllExcludes())
	if err != nil {
		t.Fatal(err)
	}
	tracked := repo.NewTrackedSet(ix.Entries, gc.IgnoreCase)
	res, err := repo.Walk(ctx, repo.WalkOptions{Root: root, Tracked: tracked, Ignore: ig})
	if err != nil {
		t.Fatal(err)
	}

	// Candidate set = tracked files not excluded by config + untracked walk.
	candidates := map[string]bool{} // path -> tracked
	for _, e := range ix.Files() {
		if !ig.ExcludedByConfig(e.Path) {
			candidates[e.Path] = true
		}
	}
	for _, u := range res.Untracked {
		candidates[u.Path] = false
	}

	for _, p := range testutil.VendorPaths() {
		if _, ok := candidates[p]; ok {
			t.Errorf("vendor file %s not excluded by default config", p)
		}
	}
	// *.min.* is a default config exclude, so the minified fixture never
	// reaches the content filters during discovery; its content must still
	// be classified as minified should a user drop that exclude.
	if _, ok := candidates[testutil.MinifiedPath]; ok {
		t.Errorf("%s not excluded by the default *.min.* exclude", testutil.MinifiedPath)
	}
	if minSrc, err := os.ReadFile(filepath.Join(dir, testutil.MinifiedPath)); err != nil {
		t.Fatal(err)
	} else if got := repo.Classify(testutil.MinifiedPath, int64(len(minSrc)), minSrc, int64(cfg.MaxFileSize)); got != model.StatusMinified {
		t.Errorf("Classify(%s) = %v, want minified", testutil.MinifiedPath, got)
	}
	for _, p := range testutil.IgnoredPaths() {
		if _, ok := candidates[p]; ok {
			t.Errorf("ignored file %s was discovered", p)
		}
	}
	for _, p := range testutil.UntrackedPaths() {
		if tr, ok := candidates[p]; !ok || tr {
			t.Errorf("untracked file %s: discovered=%v tracked=%v", p, ok, tr)
		}
	}

	want := map[string]model.FileStatus{
		testutil.GeneratedPath:              model.StatusGenerated,
		testutil.BigFilePath:                model.StatusTooBig,
		"go/internal/util/slug.go":          model.StatusOK,
		"python/shop/experimental.py":       model.StatusOK,
		"notes/scratch.md":                  model.StatusUnsupported,
		"go/internal/store/store.go":        model.StatusOK,
		"go/internal/service/order.go":      model.StatusOK,
		"go/cmd/server/main.go":             model.StatusOK,
		"go/internal/store/item.go":         model.StatusOK,
		"go/internal/util/format.go":        model.StatusOK,
		"go/internal/service/order_test.go": model.StatusOK,
	}
	langs := map[model.Lang]int{}
	for p := range candidates {
		abs := filepath.Join(dir, filepath.FromSlash(p))
		fi, err := os.Lstat(abs)
		if err != nil {
			t.Fatal(err)
		}
		var content []byte
		if fi.Size() <= int64(cfg.MaxFileSize) {
			if content, err = os.ReadFile(abs); err != nil {
				t.Fatal(err)
			}
		}
		got := repo.Classify(p, fi.Size(), content, int64(cfg.MaxFileSize))
		if w, ok := want[p]; ok && got != w {
			t.Errorf("Classify(%s) = %v, want %v", p, got, w)
		}
		if got == model.StatusOK {
			langs[repo.DetectLang(p, content)]++
		}
	}
	for p := range want {
		if _, ok := candidates[p]; !ok {
			t.Errorf("expected file %s was not discovered", p)
		}
	}
	// Every language has at least one indexable file.
	for _, l := range model.AllLangs() {
		if langs[l] == 0 {
			t.Errorf("no indexable %v file in the fixture", l)
		}
	}
}
