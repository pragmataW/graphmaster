package index

import (
	"bufio"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pragmataW/graphmaster/internal/model"
	"github.com/pragmataW/graphmaster/internal/testutil"
)

// oracleNames is a small pool so that random edits keep creating and
// removing unique, ambiguous and unresolved names.
var oracleNames = []string{"FormatID", "NewMemory", "Helper", "Alpha", "Beta", "Validate", "apply_discount"}

// ignoreToggles are .gitignore lines the oracle switches on and off.
var ignoreToggles = []string{"go/internal/util/slug.go", "*_test.go", "python/", "gen_*.go", "go/internal/extra/"}

type oracle struct {
	t      *testing.T
	dir    string
	rng    *rand.Rand
	branch string
	step   int
}

// TestEquivalenceOracle applies a deterministic random sequence of edits,
// creates, deletes, renames, branch switches, .gitignore and staging
// changes to the fixture repository. After every step the incrementally
// maintained graph must equal a from-scratch build of the same tree.
func TestEquivalenceOracle(t *testing.T) {
	seed := uint64(1)
	if v, err := strconv.ParseUint(os.Getenv("GRAPHMASTER_ORACLE_SEED"), 10, 64); err == nil {
		seed = v
	}
	steps := 40
	if testing.Short() {
		steps = 10
	}
	runOracle(t, seed, steps, func() Resolver { return &fakeResolver{} })
}

// runOracle runs the equivalence oracle for steps steps (overridden by
// GRAPHMASTER_ORACLE_STEPS), resolving with resolvers from newResolver.
func runOracle(t *testing.T, seed uint64, steps int, newResolver func() Resolver) {
	if v, err := strconv.Atoi(os.Getenv("GRAPHMASTER_ORACLE_STEPS")); err == nil {
		steps = v
	}
	dir := testutil.Materialize(t)
	o := &oracle{t: t, dir: dir, rng: rand.New(rand.NewPCG(seed, 0x9e3779b9)), branch: "main"}
	o.makeAltBranch()

	scratch := func() string { return scratchDump(t, dir, Options{Resolver: newResolver()}) }
	ix := openIx(t, dir, Options{Workers: 3, BatchFiles: 7, Resolver: newResolver()})
	freshen(t, ix)
	requireSameDump(t, "initial", dump(t, ix), scratch())

	ops := []struct {
		name   string
		weight int
		fn     func() string
	}{
		{"edit", 6, o.edit},
		{"create", 4, o.create},
		{"delete", 3, o.delete},
		{"rename", 3, o.rename},
		{"branch", 2, o.switchBranch},
		{"gitignore", 2, o.toggleIgnore},
		{"stage", 2, o.stage},
		{"touch", 1, o.touch},
		{"multi", 2, o.multi},
	}
	total := 0
	for _, op := range ops {
		total += op.weight
	}
	for o.step = 1; o.step <= steps; o.step++ {
		n := o.rng.IntN(total)
		var desc string
		for _, op := range ops {
			if n -= op.weight; n < 0 {
				desc = op.name + ": " + op.fn()
				break
			}
		}
		res := freshen(t, ix)
		t.Logf("step %d %s -> %+v", o.step, desc, *res)
		requireSameDump(t, fmt.Sprintf("step %d (%s)", o.step, desc), dump(t, ix), scratch())
	}
}

func (o *oracle) git(args ...string) string { return testutil.Git(o.t, o.dir, args...) }

// makeAltBranch creates branch "alt" diverging from main.
func (o *oracle) makeAltBranch() {
	o.git("checkout", "-q", "-b", "alt")
	for range 3 {
		o.edit()
	}
	o.create()
	o.delete()
	o.git("add", "-A")
	o.git("commit", "-q", "--no-verify", "-m", "alt")
	o.git("checkout", "-q", "main")
}

// codeFiles lists the code files of the working tree (any status).
func (o *oracle) codeFiles() []string {
	var out []string
	_ = filepath.WalkDir(o.dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // entries vanishing mid-walk are skipped
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == DirName) {
			return filepath.SkipDir
		}
		if d.Type().IsRegular() {
			rel, _ := filepath.Rel(o.dir, p)
			rel = filepath.ToSlash(rel)
			if model.IsCodePath(rel) && !strings.Contains(rel, "vendor/") {
				out = append(out, rel)
			}
		}
		return nil
	})
	slices.Sort(out)
	return out
}

func (o *oracle) pick(list []string) string { return list[o.rng.IntN(len(list))] }

func (o *oracle) goFiles() []string {
	var out []string
	for _, p := range o.codeFiles() {
		if strings.HasSuffix(p, ".go") {
			out = append(out, p)
		}
	}
	return out
}

func (o *oracle) name() string { return o.pick(oracleNames) }

func (o *oracle) goFunc() string {
	return fmt.Sprintf("\n// %s is oracle step %d.\nfunc %s%d() int { %s(); return %d }\n",
		o.name(), o.step, o.name(), o.rng.IntN(3), o.name(), o.step)
}

func (o *oracle) edit() string {
	files := o.codeFiles()
	p := o.pick(files)
	if o.rng.IntN(3) > 0 {
		if gf := o.goFiles(); len(gf) > 0 {
			p = o.pick(gf)
		}
	}
	b, err := os.ReadFile(filepath.Join(o.dir, filepath.FromSlash(p)))
	if err != nil {
		o.t.Fatal(err)
	}
	var s string
	switch {
	case strings.HasSuffix(p, ".go") && o.rng.IntN(5) == 0:
		s = string(b) + "\nfunc broken( {\n" // syntax error -> parse_err
	case strings.HasSuffix(p, ".go") && o.rng.IntN(3) == 0:
		// Drop the last function-ish block to remove definitions.
		lines := strings.Split(string(b), "\n")
		if len(lines) > 6 {
			lines = lines[:len(lines)-o.rng.IntN(len(lines)/2)-1]
		}
		s = strings.Join(lines, "\n") + "\n"
	case strings.HasSuffix(p, ".go"):
		s = string(b) + o.goFunc()
	default:
		s = string(b) + "\n// oracle " + strconv.Itoa(o.step) + "\n"
	}
	writeFile(o.t, o.dir, p, s)
	return p
}

func (o *oracle) goPackage(dir string) string {
	for _, p := range o.goFiles() {
		if path.Dir(p) != dir {
			continue
		}
		f, err := os.Open(filepath.Join(o.dir, filepath.FromSlash(p)))
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if rest, ok := strings.CutPrefix(sc.Text(), "package "); ok {
				f.Close()
				return strings.TrimSpace(rest)
			}
		}
		f.Close()
	}
	return path.Base(dir)
}

func (o *oracle) create() string {
	var dir string
	switch o.rng.IntN(4) {
	case 0:
		dir = "go/internal/extra"
	case 1:
		dir = "python/shop"
	default:
		dir = path.Dir(o.pick(o.goFiles()))
	}
	var p, content string
	if strings.HasPrefix(dir, "python") {
		p = fmt.Sprintf("%s/gen_%d.py", dir, o.step)
		content = fmt.Sprintf("def %s():\n    return %s()\n", o.name(), o.name())
	} else {
		p = fmt.Sprintf("%s/gen_%d.go", dir, o.step)
		content = "package " + o.goPackage(dir) + "\n" + o.goFunc() + o.goFunc()
	}
	writeFile(o.t, o.dir, p, content)
	return p
}

func (o *oracle) delete() string {
	p := o.pick(o.codeFiles())
	if err := os.Remove(filepath.Join(o.dir, filepath.FromSlash(p))); err != nil {
		o.t.Fatal(err)
	}
	return p
}

func (o *oracle) rename() string {
	p := o.pick(o.codeFiles())
	ext := path.Ext(p)
	dir := path.Dir(p)
	if o.rng.IntN(3) == 0 {
		dir = path.Dir(o.pick(o.codeFiles())) // move across directories
	}
	np := fmt.Sprintf("%s/renamed_%d%s", dir, o.step, ext)
	if err := os.Rename(filepath.Join(o.dir, filepath.FromSlash(p)), filepath.Join(o.dir, filepath.FromSlash(np))); err != nil {
		o.t.Fatal(err)
	}
	return p + " -> " + np
}

// switchBranch commits the working tree on the current branch and checks
// out the other one.
func (o *oracle) switchBranch() string {
	o.git("add", "-A")
	o.git("commit", "-q", "--no-verify", "--allow-empty", "-m", "step "+strconv.Itoa(o.step))
	next := "alt"
	if o.branch == "alt" {
		next = "main"
	}
	o.git("checkout", "-q", next)
	o.branch = next
	return next
}

func (o *oracle) toggleIgnore() string {
	gi := filepath.Join(o.dir, ".gitignore")
	b, err := os.ReadFile(gi)
	if err != nil {
		o.t.Fatal(err)
	}
	line := o.pick(ignoreToggles)
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if i := slices.Index(lines, line); i >= 0 {
		lines = slices.Delete(lines, i, i+1)
		line = "-" + line
	} else {
		lines = append(lines, line)
		line = "+" + line
	}
	if err := os.WriteFile(gi, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		o.t.Fatal(err)
	}
	return line
}

// stage adds an untracked file to the index or removes a tracked one from it.
func (o *oracle) stage() string {
	untracked := strings.Fields(o.git("ls-files", "-o", "--exclude-standard"))
	var code []string
	for _, p := range untracked {
		if model.IsCodePath(p) {
			code = append(code, p)
		}
	}
	if len(code) > 0 && o.rng.IntN(2) == 0 {
		p := o.pick(code)
		o.git("add", "--", p)
		return "add " + p
	}
	p := o.pick(o.goFiles())
	o.git("rm", "-q", "--cached", "--ignore-unmatch", "--", p)
	return "rm --cached " + p
}

func (o *oracle) touch() string {
	p := o.pick(o.codeFiles())
	now := time.Now()
	if err := os.Chtimes(filepath.Join(o.dir, filepath.FromSlash(p)), now, now); err != nil {
		o.t.Fatal(err)
	}
	return p
}

// multi applies several operations before one Freshen.
func (o *oracle) multi() string {
	parts := []string{o.edit(), o.create(), o.rename(), o.delete()}
	return strings.Join(parts, ", ")
}
