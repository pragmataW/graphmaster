package testutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/pragmataW/graphmaster/internal/model"
)

// gitList runs a NUL-separated git ls-files variant and returns sorted paths.
func gitList(t *testing.T, root string, args ...string) []string {
	t.Helper()
	out := Git(t, root, append([]string{"ls-files", "-z"}, args...)...)
	var paths []string
	for p := range strings.SplitSeq(out, "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	slices.Sort(paths)
	return paths
}

func sorted(in []string) []string {
	out := slices.Clone(in)
	slices.Sort(out)
	return out
}

func diffSets(t *testing.T, what string, got, want []string) {
	t.Helper()
	gotSet := map[string]bool{}
	for _, p := range got {
		gotSet[p] = true
	}
	wantSet := map[string]bool{}
	for _, p := range want {
		wantSet[p] = true
	}
	for _, p := range want {
		if !gotSet[p] {
			t.Errorf("%s: missing %s", what, p)
		}
	}
	for _, p := range got {
		if !wantSet[p] {
			t.Errorf("%s: unexpected %s", what, p)
		}
	}
}

func TestMaterializeGitSets(t *testing.T) {
	root := Materialize(t)

	committed, err := FixturePaths()
	if err != nil {
		t.Fatal(err)
	}
	wantTracked := append(append(slices.Clone(committed), VendorPaths()...), BigFilePath)
	diffSets(t, "tracked", gitList(t, root), sorted(wantTracked))
	diffSets(t, "untracked", gitList(t, root, "-o", "--exclude-standard"), sorted(UntrackedPaths()))
	diffSets(t, "ignored", gitList(t, root, "-o", "-i", "--exclude-standard"), sorted(IgnoredPaths()))

	if st := Git(t, root, "status", "--porcelain", "--untracked-files=no"); st != "" {
		t.Errorf("tracked files modified after materialize:\n%s", st)
	}
}

func TestMaterializeSpecialFiles(t *testing.T) {
	root := Materialize(t)

	fi, err := os.Stat(filepath.Join(root, filepath.FromSlash(BigFilePath)))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() <= 1<<20 {
		t.Errorf("big file is %d bytes, want > 1MiB", fi.Size())
	}

	gen, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(GeneratedPath)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(gen), "Code generated") || !strings.Contains(string(gen), "DO NOT EDIT") {
		t.Errorf("%s lacks the generated-code marker", GeneratedPath)
	}

	minified, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(MinifiedPath)))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Count(string(minified), "\n") + 1
	if avg := len(minified) / lines; avg <= 300 {
		t.Errorf("%s average line length %d, want > 300", MinifiedPath, avg)
	}
}

func TestFixtureCoversAllLanguages(t *testing.T) {
	paths, err := FixturePaths()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[model.Lang]int{}
	for _, p := range paths {
		seen[model.LangFromPath(p)]++
	}
	for _, l := range model.AllLangs() {
		if seen[l] == 0 {
			t.Errorf("fixture has no %s file", l)
		}
	}
	for _, p := range []string{"go/go.mod", "ts/tsconfig.json", "php/composer.json", "rust/Cargo.toml",
		"csharp/Fixture.csproj", "python/shop/__init__.py", "README.md", "EXPECTED.md", ".gitignore"} {
		if !slices.Contains(paths, p) {
			t.Errorf("fixture lacks %s", p)
		}
	}
}

func TestMaterializeDeterministicCommit(t *testing.T) {
	a := Git(t, Materialize(t), "rev-parse", "HEAD")
	b := Git(t, Materialize(t), "rev-parse", "HEAD")
	if a != b {
		t.Errorf("HEAD differs between materializations: %s vs %s", a, b)
	}
}

// TestMaterializeGoBuilds proves the Go part of the fixture (including the
// vendored dependency) is real, compilable code.
func TestMaterializeGoBuilds(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain not found")
	}
	root := Materialize(t)
	cmd := exec.Command(goBin, "vet", "-mod=vendor", "./...")
	cmd.Dir = filepath.Join(root, "go")
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=", "GOPROXY=off", "GOTOOLCHAIN=local")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go vet in fixture failed: %v\n%s", err, out)
	}
}
