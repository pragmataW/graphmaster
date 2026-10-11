package testutil

import (
	"bytes"
	"cmp"
	"fmt"
	"math/rand/v2"
	"runtime"
	"slices"
	"testing"

	"golang.org/x/sync/errgroup"

	"github.com/pragmataW/graphmaster/internal/model"
)

// synthPkgSize is the number of source files per synthetic package/module.
const synthPkgSize = 20

// SynthFile is one generated file.
type SynthFile struct {
	Path    string // repo-relative, slash-separated
	Content []byte
}

// SynthRepo describes a deterministic synthetic repository. Content is
// generated on demand: file contents depend only on (seed, parameters,
// language, file index), never on generation order.
type SynthRepo struct {
	seed  int64
	files int
	loc   int
	langs []model.Lang
}

// Synth describes a synthetic repository of `files` source files spread
// round-robin over langs, each about locPerFile lines long (never shorter;
// longer only when locPerFile is below the size of one function plus
// header). Every function calls a function in another file of its package,
// one in a lower-numbered package (or its own package for package 0) and one
// in its own file, using each language's real import mechanism.
//
// Manifests and package-init files (go.mod, __init__.py, mod.rs, C headers,
// ...) are emitted in addition to the source files. Synth panics on invalid
// arguments, since it is a test helper.
func Synth(seed int64, files, locPerFile int, langs []model.Lang) *SynthRepo {
	if files <= 0 || locPerFile <= 0 || len(langs) == 0 {
		panic(fmt.Sprintf("testutil.Synth: invalid arguments files=%d loc=%d langs=%v", files, locPerFile, langs))
	}
	seen := map[model.Lang]bool{}
	for _, l := range langs {
		if synthLangs[l] == nil {
			panic(fmt.Sprintf("testutil.Synth: unsupported language %v", l))
		}
		if seen[l] {
			panic(fmt.Sprintf("testutil.Synth: duplicate language %v", l))
		}
		seen[l] = true
	}
	return &SynthRepo{seed: seed, files: files, loc: locPerFile, langs: slices.Clone(langs)}
}

// SourceCount returns the number of generated source files.
func (r *SynthRepo) SourceCount() int { return r.files }

// langCount returns how many source files language index li receives.
func (r *SynthRepo) langCount(li int) int {
	n := r.files / len(r.langs)
	if li < r.files%len(r.langs) {
		n++
	}
	return n
}

// fnsPerFile is the uniform number of functions per file for lang l.
func (r *SynthRepo) fnsPerFile(l synthLang) int {
	return max(1, (r.loc-l.baseHeader())/l.fnLines())
}

// pkgSizes returns the number of files in each package of a language with n
// files.
func pkgSizes(n int) []int {
	var sizes []int
	for start := 0; start < n; start += synthPkgSize {
		sizes = append(sizes, min(synthPkgSize, n-start))
	}
	return sizes
}

// Sources generates every source file, sorted by path.
func (r *SynthRepo) Sources() []SynthFile {
	out := make([]SynthFile, 0, r.files)
	for li, l := range r.langs {
		for j := range r.langCount(li) {
			out = append(out, r.source(l, li, j))
		}
	}
	slices.SortFunc(out, func(a, b SynthFile) int { return cmp.Compare(a.Path, b.Path) })
	return out
}

// Files returns sources plus support files (manifests, headers, package
// init files), sorted by path.
func (r *SynthRepo) Files() []SynthFile {
	out := r.Sources()
	for li, l := range r.langs {
		sl := synthLangs[l]
		out = append(out, sl.support(pkgSizes(r.langCount(li)), r.fnsPerFile(sl))...)
	}
	slices.SortFunc(out, func(a, b SynthFile) int { return cmp.Compare(a.Path, b.Path) })
	return out
}

// source renders file j of language l.
func (r *SynthRepo) source(l model.Lang, li, j int) SynthFile {
	sl := synthLangs[l]
	p := r.plan(l, sl, li, j)
	var b bytes.Buffer
	sl.render(&b, p)
	for lines := bytes.Count(b.Bytes(), []byte("\n")); lines < r.loc; lines++ {
		fmt.Fprintf(&b, "%s padding line %d\n", sl.comment(), lines+1)
	}
	return SynthFile{Path: sl.sourcePath(p.pkg, j), Content: b.Bytes()}
}

// synthRef points at function fn of file `file` (per-language index) in
// package pkg.
type synthRef struct{ pkg, file, fn int }

type synthFn struct {
	idx   int
	calls []synthRef
}

// synthPlan is the language-neutral content of one source file.
type synthPlan struct {
	pkg, file int
	fns       []synthFn
}

// local reports whether ref targets the planned file itself.
func (p *synthPlan) local(ref synthRef) bool { return ref.file == p.file }

// deps returns the distinct files (fn = -1) referenced from other files,
// sorted by (pkg, file).
func (p *synthPlan) deps() []synthRef {
	seen := map[synthRef]bool{}
	var out []synthRef
	for _, f := range p.fns {
		for _, c := range f.calls {
			key := synthRef{pkg: c.pkg, file: c.file, fn: -1}
			if !p.local(c) && !seen[key] {
				seen[key] = true
				out = append(out, key)
			}
		}
	}
	slices.SortFunc(out, func(a, b synthRef) int {
		if a.pkg != b.pkg {
			return a.pkg - b.pkg
		}
		return a.file - b.file
	})
	return out
}

// depPkgs returns the distinct packages other than p.pkg referenced by p.
func (p *synthPlan) depPkgs() []int {
	var out []int
	for _, d := range p.deps() {
		if d.pkg != p.pkg && !slices.Contains(out, d.pkg) {
			out = append(out, d.pkg)
		}
	}
	return out
}

func (r *SynthRepo) plan(l model.Lang, sl synthLang, li, j int) *synthPlan {
	n := r.langCount(li)
	nf := r.fnsPerFile(sl)
	rng := rand.New(rand.NewPCG(uint64(r.seed), uint64(l)<<32|uint64(j)))
	pkg := j / synthPkgSize
	pkgFiles := func(q int) (int, int) { // [start, end)
		return q * synthPkgSize, min((q+1)*synthPkgSize, n)
	}
	pick := func(q int) int {
		s, e := pkgFiles(q)
		return s + rng.IntN(e-s)
	}

	p := &synthPlan{pkg: pkg, file: j}
	for k := range nf {
		var calls []synthRef
		// 1. Another file in the same package, when there is one.
		s, e := pkgFiles(pkg)
		if e-s > 1 {
			t := s + rng.IntN(e-s-1)
			if t >= j {
				t++
			}
			calls = append(calls, synthRef{pkg: pkg, file: t, fn: rng.IntN(nf)})
		} else {
			calls = append(calls, synthRef{pkg: pkg, file: j, fn: rng.IntN(nf)})
		}
		// 2. A lower package keeps the import graph acyclic.
		if pkg > 0 {
			q := rng.IntN(pkg)
			calls = append(calls, synthRef{pkg: q, file: pick(q), fn: rng.IntN(nf)})
		} else {
			calls = append(calls, synthRef{pkg: pkg, file: pick(pkg), fn: rng.IntN(nf)})
		}
		// 3. A sibling in the same file.
		calls = append(calls, synthRef{pkg: pkg, file: j, fn: (k + 1) % nf})
		p.fns = append(p.fns, synthFn{idx: k, calls: calls})
	}
	return p
}

// WriteDir writes all files under dir, in parallel.
func (r *SynthRepo) WriteDir(dir string) error {
	var g errgroup.Group
	g.SetLimit(runtime.GOMAXPROCS(0) * 2)
	for _, f := range r.Files() {
		g.Go(func() error { return writeRel(dir, f.Path, f.Content) })
	}
	return g.Wait()
}

// MaterializeSynth writes Synth(seed, files, locPerFile, langs) into
// t.TempDir(), commits everything to a fresh git repository and returns its
// root.
func MaterializeSynth(t testing.TB, seed int64, files, locPerFile int, langs []model.Lang) string {
	t.Helper()
	root := t.TempDir()
	if err := Synth(seed, files, locPerFile, langs).WriteDir(root); err != nil {
		t.Fatalf("write synthetic repo: %v", err)
	}
	if err := initRepo(root); err != nil {
		t.Fatal(err)
	}
	if err := commitAll(root, "synthetic repository"); err != nil {
		t.Fatal(err)
	}
	return root
}
