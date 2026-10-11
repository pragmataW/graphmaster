package index

import (
	"slices"
	"strings"
	"testing"

	"github.com/pragmataW/graphmaster/internal/resolve"
)

func TestQueuePrefixesMatchResolver(t *testing.T) {
	for _, p := range [][2]string{
		{metaPendingFile, resolve.PendingFilePrefix},
		{metaPendingDependents, resolve.PendingDependentsPrefix},
		{metaPendingRemoved, resolve.PendingRemovedPrefix},
	} {
		if p[0] != p[1] {
			t.Errorf("index queues %q, resolver drains %q", p[0], p[1])
		}
	}
	var _ IncrementalHints = (*resolve.Resolver)(nil)
	var _ IncrementalHints = (*fakeResolver)(nil)
}

// inPlaceRepo indexes files (a non-git directory) with the real resolver.
type inPlaceRepo struct {
	t    *testing.T
	dir  string
	ix   *Indexer
	res  *resolve.Resolver
	opts resolve.Options
}

func newInPlaceRepo(t *testing.T, files map[string]string, opts resolve.Options) *inPlaceRepo {
	t.Helper()
	dir := t.TempDir()
	for p, c := range files {
		writeFile(t, dir, p, c)
	}
	r := &inPlaceRepo{t: t, dir: dir, res: &resolve.Resolver{Options: opts}, opts: opts}
	r.ix = openIx(t, dir, Options{AllowNoGit: true, Resolver: r.res, Now: futureClock})
	freshen(t, r.ix)
	r.requireFull("initial")
	return r
}

// requireFull compares the graph with a from-scratch build.
func (r *inPlaceRepo) requireFull(label string) {
	r.t.Helper()
	want := scratchDump(r.t, r.dir, Options{AllowNoGit: true, Resolver: &resolve.Resolver{Options: r.opts}})
	requireSameDump(r.t, label, dump(r.t, r.ix), want)
}

func (r *inPlaceRepo) nodeID(path, name string) int64 {
	r.t.Helper()
	var id int64
	if err := r.ix.Store().DB().QueryRow(`SELECT n.id FROM nodes n JOIN names m ON m.id = n.name_id
		JOIN files f ON f.id = n.file_id WHERE f.path = ? AND m.name = ?`, path, name).Scan(&id); err != nil {
		r.t.Fatalf("node %s %s: %v", path, name, err)
	}
	return id
}

// edgeTargets returns the files the edges named name from file path point
// into ("" for an unresolved edge), sorted.
func (r *inPlaceRepo) edgeTargets(path, name string) []string {
	r.t.Helper()
	rows, err := r.ix.Store().DB().Query(`SELECT coalesce(tf.path, '') FROM edges e
		JOIN names m ON m.id = e.name_id JOIN files f ON f.id = e.file_id
		LEFT JOIN nodes d ON d.id = e.dst LEFT JOIN nodes p ON p.id = d.parent_id
		LEFT JOIN files tf ON tf.id = d.file_id
		WHERE f.path = ? AND m.name = ? ORDER BY 1`, path, name)
	if err != nil {
		r.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			r.t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

// TestBodyEditRewritesInPlace: a body-only edit keeps the file's node ids
// and resolves only the file's own references.
func TestBodyEditRewritesInPlace(t *testing.T) {
	r := newInPlaceRepo(t, map[string]string{
		"go.mod": "module example.com/m\n\ngo 1.25\n",
		"a/a.go": "package a\n\nfunc Helper() int {\n\treturn 1\n}\n\nfunc Other() int { return Helper() }\n",
		"b/b.go": "package b\n\nimport \"example.com/m/a\"\n\nfunc Use() int { return a.Helper() + a.Other() }\n",
		"c/c.go": "package c\n\nimport \"example.com/m/a\"\n\nfunc Use2() int { return a.Helper() }\n",
	}, resolve.Options{})
	// Node ids may be reused by SQLite, so the resolver stats tell an
	// in-place rewrite from a replacement.
	helper, other := r.nodeID("a/a.go", "Helper"), r.nodeID("a/a.go", "Other")
	writeFile(t, r.dir, "a/a.go", "package a\n\n// Helper is documented now.\nfunc Helper() int {\n\tx := 2\n\treturn x\n}\n\nfunc Other() int { return Helper() + 1 }\n")
	res := freshen(t, r.ix)
	if res.Modified != 1 {
		t.Fatalf("result %+v", *res)
	}
	if r.nodeID("a/a.go", "Helper") != helper || r.nodeID("a/a.go", "Other") != other {
		t.Fatal("node ids changed by a body edit")
	}
	if st := r.res.Last; st.Names != 0 || st.Files != 1 {
		t.Fatalf("resolver %+v: want only the edited file re-resolved", st)
	}
	r.requireFull("body edit")

	// A signature change replaces that definition: its name is queued,
	// the other definition stays in place.
	writeFile(t, r.dir, "a/a.go", "package a\n\nfunc Helper(n int) int {\n\treturn n\n}\n\nfunc Other() int { return Helper(1) }\n")
	freshen(t, r.ix)
	if st := r.res.Last; st.Files != 1 || st.Names != 1 {
		t.Fatalf("resolver %+v: want the file and Helper queued", st)
	}
	if r.nodeID("a/a.go", "Other") != other {
		t.Fatal("node id of an unchanged definition changed by a signature change")
	}
	r.requireFull("signature change")
}

// TestBodyEditForeignSource: TypeScript and Python resolvers type
// receivers from other files' definition text (initializers, attribute
// assignments in method bodies), so a body-only edit there re-resolves the
// dependent receiver-typed references elsewhere.
func TestBodyEditForeignSource(t *testing.T) {
	const tsTypes = "export class Foo {\n  bar(): number { return 1; }\n}\n\nexport class Baz {\n  bar(): number { return 2; }\n}\n\n"
	const pyTypes = "class Foo:\n    def bar(self):\n        return 1\n\n\nclass Baz:\n    def bar(self):\n        return 2\n\n\n"
	cases := []struct {
		name, def, use, before, after, useSrc string
	}{
		{
			name: "typescript", def: "a.ts", use: "b.ts",
			// The signature of a body-less definition is its first line.
			before: tsTypes + "export const svc =\n  new Foo();\n",
			after:  tsTypes + "export const svc =\n  new Baz();\n",
			useSrc: "import { svc } from './a';\n\nexport function use(): number {\n  return svc.bar();\n}\n",
		},
		{
			name: "python", def: "a.py", use: "b.py",
			before: pyTypes + "class Holder:\n    def __init__(self):\n        self.x = Foo()\n",
			after:  pyTypes + "class Holder:\n    def __init__(self):\n        self.x = Baz()\n",
			useSrc: "from a import Holder\n\n\ndef use():\n    h = Holder()\n    return h.x.bar()\n",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newInPlaceRepo(t, map[string]string{c.def: c.before, c.use: c.useSrc}, resolve.Options{})
			if got := r.edgeQNames(c.use, "bar"); !slices.Equal(got, []string{"Foo.bar"}) {
				t.Fatalf("before: receiver not typed through %s: %v", c.def, got)
			}
			writeFile(t, r.dir, c.def, c.after)
			freshen(t, r.ix)
			if st := r.res.Last; st.Files != 1 || st.Names != 0 {
				t.Fatalf("resolver %+v: body edit not rewritten in place", st)
			}
			if got := r.edgeQNames(c.use, "bar"); !slices.Equal(got, []string{"Baz.bar"}) {
				t.Fatalf("after: dependent not re-resolved: %v", got)
			}
			r.requireFull("body edit")
		})
	}
}

// edgeQNames returns the qualified names (module stripped) of the targets
// of the edges named name from file path, sorted.
func (r *inPlaceRepo) edgeQNames(path, name string) []string {
	r.t.Helper()
	rows, err := r.ix.Store().DB().Query(`SELECT coalesce(d.qname, '') FROM edges e
		JOIN names m ON m.id = e.name_id JOIN files f ON f.id = e.file_id
		LEFT JOIN nodes d ON d.id = e.dst WHERE f.path = ? AND m.name = ? ORDER BY 1`, path, name)
	if err != nil {
		r.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var q string
		if err := rows.Scan(&q); err != nil {
			r.t.Fatal(err)
		}
		parts := strings.Split(q, ".")
		out = append(out, strings.Join(parts[max(0, len(parts)-2):], "."))
	}
	return out
}

// TestBodyEditCommonNameFlip: references added by a body edit can push a
// name over the common-name limit, which changes how every reference of
// the name resolves elsewhere.
func TestBodyEditCommonNameFlip(t *testing.T) {
	r := newInPlaceRepo(t, map[string]string{
		"go.mod": "module example.com/m\n\ngo 1.25\n",
		"a/h.go": "package a\n\nfunc Helper() {}\n",
		"b/h.go": "package b\n\nfunc Helper() {}\n",
		"c/c.go": "package c\n\nfunc Use() {\n\tHelper()\n}\n",
		"d/d.go": "package d\n\nfunc Other() {\n}\n",
	}, resolve.Options{CommonNameLimit: 2})
	writeFile(t, r.dir, "d/d.go", "package d\n\nfunc Other() {\n\tHelper()\n\tHelper()\n}\n")
	freshen(t, r.ix)
	if st := r.res.Last; st.Files != 1 || st.Names != 1 {
		t.Fatalf("resolver %+v: want d/d.go in place and Helper queued", st)
	}
	r.requireFull("over the limit")
	if got := r.edgeTargets("c/c.go", "Helper"); !slices.Equal(got, []string{""}) {
		t.Fatalf("common name still guessed: %v", got)
	}
	writeFile(t, r.dir, "d/d.go", "package d\n\nfunc Other() {\n}\n")
	freshen(t, r.ix)
	r.requireFull("back under the limit")
	if got := r.edgeTargets("c/c.go", "Helper"); !slices.Equal(got, []string{"a/h.go", "b/h.go"}) {
		t.Fatalf("want the T5 fan-out back, got %v", got)
	}
}
