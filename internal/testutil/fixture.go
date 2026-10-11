package testutil

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
)

// Well-known paths inside a materialized fixture (repo-relative, slash form).
const (
	// BigFilePath is a >1 MiB source file created by Materialize and
	// committed in the second commit (so it is tracked but oversized).
	BigFilePath = "python/shop/catalog_big.py"
	// GeneratedPath carries a "Code generated ... DO NOT EDIT." header.
	GeneratedPath = "go/internal/gen/status_string.go"
	// MinifiedPath is a minified JavaScript bundle. The default *.min.*
	// config exclude drops it before the content filters run.
	MinifiedPath = "js/public/widgets.min.js"
)

// skipNames are local artifacts that may appear in the fixture directory
// (e.g. after running its Python or editor tooling) and must never be copied.
var skipNames = map[string]bool{
	"__pycache__":  true,
	".DS_Store":    true,
	"node_modules": true,
	"target":       true,
}

// FixtureDir returns the absolute path of testdata/fixture-repo.
func FixtureDir() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		panic("testutil: cannot locate source file")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "testdata", "fixture-repo")
}

// FixturePaths returns the sorted repo-relative paths of the committed
// fixture files, i.e. what Materialize puts in the first commit.
func FixturePaths() ([]string, error) {
	root := FixtureDir()
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if skipNames[d.Name()] {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() {
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	slices.Sort(out)
	return out, err
}

// Materialize builds a git repository from the fixture in t.TempDir() and
// returns its root. The resulting layout is:
//
//   - commit 1: every file of testdata/fixture-repo
//   - commit 2: VendorPaths() and BigFilePath
//   - untracked (not ignored): UntrackedPaths()
//   - ignored: IgnoredPaths()
//
// Commits use a fixed identity and date, so HEAD is identical across runs.
func Materialize(t testing.TB) string {
	t.Helper()
	root := t.TempDir()
	if err := materialize(root); err != nil {
		t.Fatalf("materialize fixture: %v", err)
	}
	return root
}

// MaterializeTo builds the same repository as Materialize in root, which
// must be empty or missing (benchmarks and manual end-to-end runs).
func MaterializeTo(root string) error { return materialize(root) }

func materialize(root string) error {
	paths, err := FixturePaths()
	if err != nil {
		return err
	}
	src := FixtureDir()
	for _, rel := range paths {
		data, err := os.ReadFile(filepath.Join(src, filepath.FromSlash(rel)))
		if err != nil {
			return err
		}
		if err := writeRel(root, rel, data); err != nil {
			return err
		}
	}
	if err := initRepo(root); err != nil {
		return err
	}
	if err := commitAll(root, "fixture: initial import"); err != nil {
		return err
	}

	// Second commit: vendored dependencies and the oversized table.
	for rel, content := range vendorFiles {
		if err := writeRel(root, rel, []byte(content)); err != nil {
			return err
		}
	}
	if err := writeRel(root, BigFilePath, bigFileContent()); err != nil {
		return err
	}
	if err := commitAll(root, "fixture: vendor deps and large catalog"); err != nil {
		return err
	}

	// Working-tree-only files.
	for _, set := range []map[string]string{untrackedFiles, ignoredFiles} {
		for rel, content := range set {
			if err := writeRel(root, rel, []byte(content)); err != nil {
				return err
			}
		}
	}
	return nil
}

// writeRel writes data to root/rel, creating parent directories.
func writeRel(root, rel string, data []byte) error {
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(full, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", rel, err)
	}
	return nil
}

// VendorPaths returns the sorted vendored files tracked by Materialize.
func VendorPaths() []string { return keys(vendorFiles) }

// UntrackedPaths returns the sorted untracked, non-ignored files.
func UntrackedPaths() []string { return keys(untrackedFiles) }

// IgnoredPaths returns the sorted files ignored via .gitignore rules.
func IgnoredPaths() []string { return keys(ignoredFiles) }

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
