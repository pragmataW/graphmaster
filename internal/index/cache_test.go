package index

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pragmataW/graphmaster/internal/model"
	"github.com/pragmataW/graphmaster/internal/testutil"
)

// TestBranchSwitchCacheHits switches between two branches that differ in
// half of the files: after both sides have been seen once, every switch
// must be served from cache.db (>= 95% hits).
func TestBranchSwitchCacheHits(t *testing.T) {
	dir := testutil.MaterializeSynth(t, 7, 120, 30, []model.Lang{model.LangGo})
	ix := openIx(t, dir, Options{})
	freshen(t, ix)

	testutil.Git(t, dir, "checkout", "-q", "-b", "feature")
	changed := 0
	for i, p := range strings.Fields(testutil.Git(t, dir, "ls-files", "*.go")) {
		if i%2 == 1 {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, p))
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, dir, p, string(b)+fmt.Sprintf("\nfunc Feature%d() {}\n", i))
		changed++
	}
	testutil.Git(t, dir, "commit", "-q", "-am", "feature")
	res := freshen(t, ix)
	if res.Modified != changed || res.Parsed != changed {
		t.Fatalf("first switch: %+v (changed %d)", res, changed)
	}

	for _, branch := range []string{"main", "feature", "main"} {
		testutil.Git(t, dir, "checkout", "-q", branch)
		res := freshen(t, ix)
		work := res.Parsed + res.CacheHits
		if res.Modified != changed || work == 0 {
			t.Fatalf("switch to %s: %+v", branch, res)
		}
		if ratio := float64(res.CacheHits) / float64(work); ratio < 0.95 {
			t.Fatalf("switch to %s: cache hit ratio %.2f < 0.95 (%+v)", branch, ratio, res)
		}
	}
	requireSameDump(t, "after switches", dump(t, ix), scratchDump(t, dir, Options{}))
}
