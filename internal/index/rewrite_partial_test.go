package index

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pragmataW/graphmaster/internal/resolve"
)

// TestAddedDefinitionRewritesInPlace: adding or removing definitions keeps
// the node ids of the others, and only the added or removed names are
// queued besides the file itself.
func TestAddedDefinitionRewritesInPlace(t *testing.T) {
	const a = "package a\n\nfunc Helper() int {\n\treturn 1\n}\n\nfunc Other() int { return Helper() }\n"
	r := newInPlaceRepo(t, map[string]string{
		"go.mod": "module example.com/m\n\ngo 1.25\n",
		"a/a.go": a,
		"b/b.go": "package b\n\nimport \"example.com/m/a\"\n\nfunc Use() int { return a.Helper() + a.Other() }\n",
	}, resolve.Options{})
	helper, other := r.nodeID("a/a.go", "Helper"), r.nodeID("a/a.go", "Other")
	keep := func(label string) {
		t.Helper()
		if r.nodeID("a/a.go", "Helper") != helper || r.nodeID("a/a.go", "Other") != other {
			t.Fatalf("%s: node ids of kept definitions changed", label)
		}
		r.requireFull(label)
	}

	// Appended definition: only its name is queued.
	writeFile(t, r.dir, "a/a.go", a+"\nfunc Probe() {}\n")
	freshen(t, r.ix)
	if st := r.res.Last; st.Files != 1 || st.Names != 1 {
		t.Fatalf("append: resolver %+v: want the file and Probe only", st)
	}
	keep("append")

	// Removing it again queues nothing in pending_names.
	writeFile(t, r.dir, "a/a.go", a)
	freshen(t, r.ix)
	if st := r.res.Last; st.Files != 1 || st.Names != 0 {
		t.Fatalf("remove: resolver %+v: want the file only", st)
	}
	keep("remove")

	// A definition inserted between the others.
	writeFile(t, r.dir, "a/a.go", strings.Replace(a, "func Other", "func Middle() int { return Helper() }\n\nfunc Other", 1))
	freshen(t, r.ix)
	if st := r.res.Last; st.Files != 1 || st.Names != 1 {
		t.Fatalf("insert: resolver %+v: want the file and Middle only", st)
	}
	keep("insert")

	// A signature change replaces that definition only.
	writeFile(t, r.dir, "a/a.go", "package a\n\nfunc Helper() int {\n\treturn 1\n}\n\nfunc Middle(n int) int { return n }\n\nfunc Other() int { return Helper() }\n")
	freshen(t, r.ix)
	if st := r.res.Last; st.Files != 1 || st.Names != 1 {
		t.Fatalf("signature: resolver %+v: want the file and Middle only", st)
	}
	keep("signature")
}

// TestAddedDefinitionKeepsSameNameOrder: definitions sharing a name keep
// their ids in source order, as a full build assigns them, so a
// definition inserted before a same-named one replaces the file.
func TestAddedDefinitionKeepsSameNameOrder(t *testing.T) {
	const a = "package a\n\ntype A struct{}\n\ntype B struct{}\n\nfunc (A) Do() int { return 1 }\n"
	r := newInPlaceRepo(t, map[string]string{
		"go.mod": "module example.com/m\n\ngo 1.25\n",
		"a/a.go": a,
		"b/b.go": "package b\n\nimport \"example.com/m/a\"\n\nfunc Use() int {\n\tvar x a.A\n\tvar y a.B\n\treturn x.Do() + y.Do()\n}\n",
	}, resolve.Options{})
	writeFile(t, r.dir, "a/a.go", strings.Replace(a, "func (A) Do", "func (B) Do() int { return 2 }\n\nfunc (A) Do", 1))
	freshen(t, r.ix)
	r.requireFull("same name inserted before")
	writeFile(t, r.dir, "a/a.go", a+"\nfunc (B) Do() int { return 2 }\n")
	freshen(t, r.ix)
	r.requireFull("same name moved after")
}

// TestAddedDefinitionTypeDependents: a kept definition whose signature
// names an added definition types receivers elsewhere through it, so its
// dependents are re-resolved.
func TestAddedDefinitionTypeDependents(t *testing.T) {
	const a = "package a\n\nfunc New() *Foo { return nil }\n"
	r := newInPlaceRepo(t, map[string]string{
		"go.mod": "module example.com/m\n\ngo 1.25\n",
		"a/a.go": a,
		"b/b.go": "package b\n\nimport \"example.com/m/a\"\n\nfunc Use() int {\n\tx := a.New()\n\treturn x.Bar()\n}\n",
		"c/c.go": "package c\n\nfunc Bar() int { return 0 }\n",
	}, resolve.Options{})
	writeFile(t, r.dir, "a/a.go", a+"\ntype Foo struct{}\n\nfunc (*Foo) Bar() int { return 1 }\n")
	freshen(t, r.ix)
	r.requireFull("type added")
	writeFile(t, r.dir, "a/a.go", a)
	freshen(t, r.ix)
	r.requireFull("type removed")
}

// sharedNameRepo writes a Go module of pkgs packages with files files each.
// Every file defines a type with methods named like those of every other
// file (String, Get) and calls helpers shared by the whole repository, the
// shape of real code bases (kubernetes) where many files share common
// names.
func sharedNameRepo(t testing.TB, pkgs, files int) (dir string, paths []string) {
	dir = t.TempDir()
	writeFile(t, dir, "go.mod", "module example.com/m\n\ngo 1.25\n")
	writeFile(t, dir, "common/common.go", "package common\n\nfunc Check(err error) {}\n\nfunc Wrap(s string) string { return s }\n")
	for p := range pkgs {
		for f := range files {
			rel := fmt.Sprintf("p%03d/f%03d.go", p, f)
			var b strings.Builder
			fmt.Fprintf(&b, "package p%03d\n\nimport \"example.com/m/common\"\n\n", p)
			fmt.Fprintf(&b, "type T%d struct{ n int }\n\n", f)
			fmt.Fprintf(&b, "func (t *T%d) String() string { return common.Wrap(\"t\") }\n\n", f)
			fmt.Fprintf(&b, "func (t *T%d) Get() int { common.Check(nil); return t.n }\n\n", f)
			fmt.Fprintf(&b, "func Use%d() string {\n\tx := &T%d{}\n\tx.Get()\n\treturn x.String()\n}\n", f, f)
			writeFile(t, dir, rel, b.String())
			paths = append(paths, rel)
		}
	}
	return dir, paths
}

// TestMultiFileEditWorkIsLinear: appending a definition to several files
// re-resolves about their own references, not every reference of the
// names they share with the rest of the repository (kubernetes: 50 edited
// files took a minute).
func TestMultiFileEditWorkIsLinear(t *testing.T) {
	dir, paths := sharedNameRepo(t, 6, 20)
	res := &resolve.Resolver{}
	ix := openIx(t, dir, Options{AllowNoGit: true, Resolver: res, Now: futureClock})
	freshen(t, ix)
	total := res.Last.Refs

	edited := paths[:10]
	orig := map[string]string{}
	for i, p := range edited {
		b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(p)))
		if err != nil {
			t.Fatal(err)
		}
		orig[p] = string(b)
		writeFile(t, dir, p, string(b)+fmt.Sprintf("\nfunc probe%d() {}\n", i))
	}
	check := func(label string) {
		t.Helper()
		st := res.Last
		// Each file has 8 references; dependents of the probes (none)
		// and of kept names may add a few more.
		if limit := 2 * 8 * len(edited); st.Refs > limit {
			t.Fatalf("%s: resolver re-resolved %d references (full build %d), want <= %d: %+v",
				label, st.Refs, total, limit, st)
		}
		requireSameDump(t, label, dump(t, ix), scratchDump(t, dir, Options{AllowNoGit: true, Resolver: &resolve.Resolver{}}))
	}
	freshen(t, ix)
	check("edit")
	for _, p := range edited {
		writeFile(t, dir, p, orig[p])
	}
	freshen(t, ix)
	check("restore")
}

// BenchmarkMultiFileEdit measures appending a definition to 50 files of a
// repository whose files share common names, and restoring them.
func BenchmarkMultiFileEdit(b *testing.B) {
	dir, paths := sharedNameRepo(b, 40, 50)
	res := &resolve.Resolver{}
	ix := openIx(b, dir, Options{AllowNoGit: true, Resolver: res, Now: futureClock})
	freshen(b, ix)
	edited := make([]string, 0, 50)
	for i := 0; i < len(paths) && len(edited) < 50; i += len(paths) / 50 {
		edited = append(edited, paths[i])
	}
	orig := map[string]string{}
	for _, p := range edited {
		c, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(p)))
		if err != nil {
			b.Fatal(err)
		}
		orig[p] = string(c)
	}
	b.ResetTimer()
	for b.Loop() {
		for i, p := range edited {
			writeFile(b, dir, p, orig[p]+fmt.Sprintf("\nfunc probe%d() {}\n", i))
		}
		freshen(b, ix)
		for _, p := range edited {
			writeFile(b, dir, p, orig[p])
		}
		freshen(b, ix)
	}
	b.ReportMetric(float64(res.Last.Refs), "refs/restore")
}
