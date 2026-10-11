package parse

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pragmataW/graphmaster/internal/model"
)

var update = flag.Bool("update", false, "rewrite golden files in testdata/golden")

const goldenRoot = "../../testdata/golden"

// TestGolden parses every source file under testdata/golden/<lang>/ and
// compares DumpFacts output with <file>.golden. Language units only add
// files there; this harness picks them up automatically.
func TestGolden(t *testing.T) {
	langDirs, err := os.ReadDir(goldenRoot)
	if err != nil {
		t.Fatal(err)
	}
	eng := NewEngine()
	defer eng.Close()
	for _, ld := range langDirs {
		if !ld.IsDir() {
			continue
		}
		lang, err := model.ParseLang(ld.Name())
		if err != nil {
			t.Errorf("golden dir %q: %v", ld.Name(), err)
			continue
		}
		t.Run(ld.Name(), func(t *testing.T) {
			spec, ok := Lookup(lang)
			if !ok {
				t.Skipf("language %v not registered", lang)
			}
			dir := filepath.Join(goldenRoot, ld.Name())
			files, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			n := 0
			for _, fe := range files {
				name := fe.Name()
				if fe.IsDir() || strings.HasSuffix(name, ".golden") {
					continue
				}
				if s, ok := ForPath(name); !ok || s.Lang() != lang {
					continue
				}
				n++
				t.Run(name, func(t *testing.T) {
					checkGolden(t, eng, spec, filepath.Join(dir, name))
				})
			}
			if n == 0 {
				t.Fatalf("no %v sources in %s", lang, dir)
			}
		})
	}
}

func checkGolden(t *testing.T, eng *Engine, spec model.LanguageSpec, srcPath string) {
	t.Helper()
	src, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	res, err := eng.Parse(context.Background(), spec, src)
	if err != nil {
		t.Fatal(err)
	}
	if res.SyntaxErrors {
		t.Errorf("%s: unexpected syntax errors", srcPath)
	}
	if err := res.Facts.Validate(); err != nil {
		t.Errorf("facts invalid: %v", err)
	}
	got := DumpFacts(res.Facts)
	goldenPath := srcPath + ".golden"
	if *update {
		if err := os.WriteFile(goldenPath, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("%v (run with -update to create)", err)
	}
	if got != string(want) {
		t.Errorf("facts mismatch for %s (run with -update after review)\n%s", srcPath, lineDiff(string(want), got))
	}
}

// lineDiff is a minimal line-oriented diff for test output.
func lineDiff(want, got string) string {
	wl, gl := strings.Split(want, "\n"), strings.Split(got, "\n")
	var b strings.Builder
	n := max(len(wl), len(gl))
	shown := 0
	for i := 0; i < n && shown < 40; i++ {
		var w, g string
		if i < len(wl) {
			w = wl[i]
		}
		if i < len(gl) {
			g = gl[i]
		}
		if w != g {
			b.WriteString("- " + w + "\n+ " + g + "\n")
			shown++
		}
	}
	return b.String()
}
