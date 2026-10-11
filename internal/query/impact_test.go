package query

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pragmataW/graphmaster/internal/model"
	"github.com/pragmataW/graphmaster/internal/testutil"
)

// TestImpactChanged compares content keys with git: an unstaged edit, a
// staged edit and untracked files are the starting set.
func TestImpactChanged(t *testing.T) {
	dir := testutil.Materialize(t)
	ix := openIndexed(t, dir)
	e := New(ix, Options{})

	edit := func(rel, old, new string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), old) {
			t.Fatalf("%s lacks %q", rel, old)
		}
		if err := os.WriteFile(p, []byte(strings.Replace(string(b), old, new, 1)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Unstaged modification.
	edit("go/internal/util/format.go", "return strings.ToUpper(strings.TrimSpace(id))", "return strings.ToUpper(id)")
	// Staged modification (index == worktree, but differs from HEAD).
	edit("go/internal/store/item.go", "return i.Price * i.Quantity", "return i.Quantity * i.Price")
	testutil.Git(t, dir, "add", "go/internal/store/item.go")

	out := runTool(t, e, "impact", `{"changed":true}`)
	mustContain(t, out,
		"go/internal/store/item.go (staged)",
		"go/internal/util/format.go (modified)",
		"go/internal/util/slug.go (untracked)",
		// Callers of the edited files are affected...
		"method service.OrderService.PlaceOrder",
		"function server.main",
		// ...including tests, which are counted.
		"test service.TestPlaceOrder",
	)
	if strings.Contains(out, "affected tests: 0") {
		t.Errorf("tests not counted:\n%s", out)
	}
	// Unchanged files are not seeds.
	if strings.Contains(out, "go/internal/service/order.go (") {
		t.Errorf("unchanged file listed as changed:\n%s", out)
	}
}

func TestImpactChangedClean(t *testing.T) {
	dir := testutil.MaterializeSynth(t, 3, 20, 20, []model.Lang{model.LangGo})
	e := New(openIndexed(t, dir), Options{})
	out := runTool(t, e, "impact", `{"changed":true}`)
	mustContain(t, out, "no changed files")
}
