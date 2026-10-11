package testutil

import (
	"bytes"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/pragmataW/graphmaster/internal/model"
)

func synthFiles(r *SynthRepo) map[string][]byte {
	m := map[string][]byte{}
	for _, f := range r.Files() {
		m[f.Path] = f.Content
	}
	return m
}

func TestSynthDeterministic(t *testing.T) {
	langs := model.AllLangs()
	a := synthFiles(Synth(42, 60, 80, langs))
	b := synthFiles(Synth(42, 60, 80, langs))
	if len(a) != len(b) {
		t.Fatalf("file counts differ: %d vs %d", len(a), len(b))
	}
	for p, c := range a {
		if !bytes.Equal(c, b[p]) {
			t.Fatalf("%s differs between runs with the same seed", p)
		}
	}

	c := synthFiles(Synth(43, 60, 80, langs))
	same := true
	for p, content := range a {
		if !bytes.Equal(content, c[p]) {
			same = false
			break
		}
	}
	if same {
		t.Error("different seeds produced identical repos")
	}
}

func TestSynthShape(t *testing.T) {
	langs := model.AllLangs()
	const files, loc = 110, 60
	r := Synth(7, files, loc, langs)

	if got := r.SourceCount(); got != files {
		t.Errorf("SourceCount = %d, want %d", got, files)
	}
	perLang := map[model.Lang]int{}
	totalLOC := 0
	for _, f := range r.Sources() {
		perLang[model.LangFromPath(f.Path)]++
		n := bytes.Count(f.Content, []byte("\n"))
		if n < loc {
			t.Errorf("%s has %d lines, want >= %d", f.Path, n, loc)
		}
		totalLOC += n
	}
	for _, l := range langs {
		if perLang[l] != files/len(langs) {
			t.Errorf("%s: %d files, want %d", l, perLang[l], files/len(langs))
		}
	}
	if totalLOC > files*loc*13/10 {
		t.Errorf("total LOC %d overshoots target %d by >30%%", totalLOC, files*loc)
	}
}

// TestSynthCrossFileCalls checks that every language references symbols
// defined in another file.
func TestSynthCrossFileCalls(t *testing.T) {
	r := Synth(1, 200, 40, model.AllLangs())
	cross := map[model.Lang]bool{}
	callRe := regexp.MustCompile(`[fFmM](\d+)_\d+\(|C(\d{4})(?:\.|::)[mM]\d+\(`)
	for _, f := range r.Sources() {
		own := ownIndex(f.Path)
		for _, m := range callRe.FindAllStringSubmatch(string(f.Content), -1) {
			idx := m[1] + m[2]
			if strings.TrimLeft(idx, "0") != strings.TrimLeft(own, "0") {
				cross[model.LangFromPath(f.Path)] = true
			}
		}
	}
	for _, l := range model.AllLangs() {
		if !cross[l] {
			t.Errorf("%s: no cross-file calls generated", l)
		}
	}
}

// ownIndex extracts the per-language file index from a synthetic path.
func ownIndex(p string) string {
	base := strings.TrimSuffix(path.Base(p), path.Ext(p))
	return strings.TrimLeft(base, "mC")
}

func TestSynthGoCompiles(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain not found")
	}
	dir := t.TempDir()
	if err := Synth(3, 90, 50, []model.Lang{model.LangGo}).WriteDir(dir); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(goBin, "vet", "./...")
	cmd.Dir = filepath.Join(dir, "go")
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=", "GOPROXY=off", "GOTOOLCHAIN=local")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("synthetic Go does not compile: %v\n%s", err, out)
	}
}

// TestSynthOtherToolchains syntax-checks languages whose toolchains happen
// to be installed; missing toolchains are skipped.
func TestSynthOtherToolchains(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	dir := t.TempDir()
	langs := []model.Lang{model.LangC, model.LangCPP, model.LangJavaScript, model.LangPython}
	r := Synth(5, 80, 40, langs)
	if err := r.WriteDir(dir); err != nil {
		t.Fatal(err)
	}
	check := func(name string, ext string, argv func(file string) []string) {
		bin, err := exec.LookPath(name)
		if err != nil {
			t.Logf("%s not found; skipping %s check", name, ext)
			return
		}
		for _, f := range r.Sources() {
			if path.Ext(f.Path) != ext {
				continue
			}
			full := filepath.Join(dir, filepath.FromSlash(f.Path))
			cmd := exec.Command(bin, argv(full)...)
			cmd.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("%s rejects %s: %v\n%s", name, f.Path, err, out)
			}
		}
	}
	check("cc", ".c", func(f string) []string { return []string{"-fsyntax-only", "-Wall", "-Werror", f} })
	check("c++", ".cpp", func(f string) []string { return []string{"-std=c++17", "-fsyntax-only", f} })
	check("node", ".js", func(f string) []string { return []string{"--check", f} })
	check("python3", ".py", func(f string) []string {
		return []string{"-c", "import ast,sys; ast.parse(open(sys.argv[1]).read())", f}
	})
}

func TestMaterializeSynth(t *testing.T) {
	root := MaterializeSynth(t, 9, 30, 20, []model.Lang{model.LangGo, model.LangPython})
	tracked := gitList(t, root)
	r := Synth(9, 30, 20, []model.Lang{model.LangGo, model.LangPython})
	if len(tracked) != len(r.Files()) {
		t.Errorf("tracked %d files, want %d", len(tracked), len(r.Files()))
	}
	if st := Git(t, root, "status", "--porcelain"); st != "" {
		t.Errorf("synthetic repo not clean:\n%s", st)
	}
}
