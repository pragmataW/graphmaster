package resolve

import (
	"context"
	"database/sql"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/pragmataW/graphmaster/internal/model"
	"github.com/pragmataW/graphmaster/internal/parse"
	"github.com/pragmataW/graphmaster/internal/store"
	"github.com/pragmataW/graphmaster/internal/testutil"
)

// testRepo is an in-memory source tree indexed into a real graph.db the way
// the indexer does it: per file, delete the old graph, insert nodes,
// contains edges, unresolved reference and inheritance edges and imports,
// and queue the file's definition and reference names.
type testRepo struct {
	t      *testing.T
	st     *store.Store
	files  map[string]string
	status map[string]model.FileStatus // non-OK statuses
	eng    *parse.Engine
}

func newTestRepo(t *testing.T, files map[string]string) *testRepo {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir(), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	eng := parse.NewEngine()
	t.Cleanup(func() {
		eng.Close()
		_ = st.Close()
	})
	return &testRepo{t: t, st: st, files: maps.Clone(files), status: map[string]model.FileStatus{}, eng: eng}
}

func (r *testRepo) rc() *model.MemRepo { return model.NewMemRepo(r.files) }

// codePaths returns the indexable source paths in order.
func (r *testRepo) codePaths() []string {
	var out []string
	for p := range r.files {
		if model.LangFromPath(p) != model.LangUnknown {
			out = append(out, p)
		}
	}
	slices.Sort(out)
	return out
}

// index writes the given paths (all code files when none are given).
func (r *testRepo) index(paths ...string) {
	r.t.Helper()
	if len(paths) == 0 {
		paths = r.codePaths()
	}
	ctx := context.Background()
	w, err := r.st.NewWriter(ctx, store.WriterOptions{MaxFiles: 3})
	if err != nil {
		r.t.Fatal(err)
	}
	for _, p := range paths {
		r.writeFile(w, p)
	}
	if err := w.Close(); err != nil {
		r.t.Fatal(err)
	}
}

// remove deletes files from the repo and the graph.
func (r *testRepo) remove(paths ...string) {
	r.t.Helper()
	w, err := r.st.NewWriter(context.Background(), store.WriterOptions{})
	if err != nil {
		r.t.Fatal(err)
	}
	for _, p := range paths {
		delete(r.files, p)
		f, err := r.st.FileByPath(context.Background(), p)
		if err != nil {
			r.t.Fatalf("remove %s: %v", p, err)
		}
		if err := w.DeleteFile(f.ID); err != nil {
			r.t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		r.t.Fatal(err)
	}
}

func (r *testRepo) writeFile(w *store.Writer, p string) {
	r.t.Helper()
	src := r.files[p]
	lang := model.LangFromPath(p)
	status := r.status[p]
	f := &store.File{Path: p, Lang: lang, Size: int64(len(src)), Tracked: true, Status: status}
	fid, err := w.UpsertFile(f)
	if err != nil {
		r.t.Fatal(err)
	}
	if err := w.DeleteFileGraph(fid); err != nil {
		r.t.Fatal(err)
	}
	if !status.Indexed() {
		if err := w.FileDone(); err != nil {
			r.t.Fatal(err)
		}
		return
	}
	res, err := r.eng.ParseLang(context.Background(), lang, []byte(src))
	if err != nil {
		r.t.Fatalf("parse %s: %v", p, err)
	}
	facts := res.Facts
	spec, _ := parse.Lookup(lang)
	module := spec.ModuleOf(p, facts, r.rc())

	var names []int64
	ids := make([]int64, len(facts.Defs))
	for i, d := range facts.Defs {
		qname := module + "." + strings.Join(facts.QNameParts(int32(i)), ".")
		n := &store.Node{
			FileID: fid, Kind: d.Kind, Name: d.Name, QName: qname,
			StartLine: d.StartLine, StartCol: d.StartCol, EndLine: d.EndLine,
			StartByte: d.StartByte, EndByte: d.EndByte, Sig: d.Sig, Flags: d.Flags,
			SKey: store.SymbolKey(p, qname, d.Kind),
		}
		if d.Parent != model.NoParent {
			n.ParentID = ids[d.Parent]
		}
		if ids[i], err = w.InsertNode(n); err != nil {
			r.t.Fatal(err)
		}
		names = append(names, n.NameID)
		if n.ParentID != 0 {
			r.edge(w, &store.Edge{FileID: fid, Src: n.ParentID, Dst: ids[i], Kind: model.EdgeContains, Conf: 100, Line: d.StartLine})
		}
		if d.Flags.Has(model.FlagDeclaration) {
			r.edge(w, &store.Edge{FileID: fid, Src: ids[i], Kind: model.EdgeDeclares, Name: d.Name, Line: d.StartLine, Col: d.StartCol})
		}
	}
	for _, ref := range facts.Refs {
		if ref.Enclosing == model.NoParent {
			continue
		}
		e := &store.Edge{FileID: fid, Src: ids[ref.Enclosing], Kind: ref.Kind.EdgeKind(),
			Name: ref.Name, Qual: ref.Qualifier, Line: ref.Line, Col: ref.Col}
		r.edge(w, e)
		names = append(names, e.NameID)
	}
	for _, in := range facts.Inherits {
		e := &store.Edge{FileID: fid, Src: ids[in.Child], Kind: in.Kind,
			Name: in.Name, Qual: in.Qualifier, Line: in.Line, Col: in.Col}
		r.edge(w, e)
		names = append(names, e.NameID)
	}
	for _, im := range facts.Imports {
		if err := w.InsertImport(&store.Import{FileID: fid, Spec: im.Spec, Alias: im.Alias, Names: im.Names, Line: im.Line}); err != nil {
			r.t.Fatal(err)
		}
	}
	if err := w.EnqueuePending(names...); err != nil {
		r.t.Fatal(err)
	}
	if err := w.FileDone(); err != nil {
		r.t.Fatal(err)
	}
}

func (r *testRepo) edge(w *store.Writer, e *store.Edge) {
	r.t.Helper()
	if _, err := w.InsertEdge(e); err != nil {
		r.t.Fatal(err)
	}
}

func (r *testRepo) resolvePending(opts Options) Stats {
	r.t.Helper()
	stats, err := ResolvePending(context.Background(), r.st, r.rc(), opts)
	if err != nil {
		r.t.Fatalf("ResolvePending: %v", err)
	}
	return stats
}

func (r *testRepo) resolveAll(opts Options) Stats {
	r.t.Helper()
	stats, err := ResolveAll(context.Background(), r.st, r.rc(), opts)
	if err != nil {
		r.t.Fatalf("ResolveAll: %v", err)
	}
	return stats
}

func (r *testRepo) dump() string { return testutil.CanonicalDump(r.st.DB()) }

// fullBuild indexes files into a fresh store and resolves everything.
func fullBuild(t *testing.T, files map[string]string, status map[string]model.FileStatus) *testRepo {
	t.Helper()
	r := newTestRepo(t, files)
	maps.Copy(r.status, status)
	r.index()
	r.resolveAll(Options{})
	return r
}

// resolvedEdge is an edge row with its endpoints rendered as path::chain.
type resolvedEdge struct {
	Kind model.EdgeKind
	Src  string
	Dst  string // "" when unresolved
	Name string
	Conf int
	Line uint32
}

// edges returns every non-contains edge with src/dst rendered as
// "path::Chain" where Chain is the container chain joined by "." (module
// identity stripped from the qname).
func (r *testRepo) edges() []resolvedEdge {
	r.t.Helper()
	rows, err := r.st.DB().Query(`SELECT e.kind, e.src, coalesce(e.dst, 0), coalesce(m.name, ''), e.conf, e.line
		FROM edges e LEFT JOIN names m ON m.id = e.name_id WHERE e.kind <> ?`, int(model.EdgeContains))
	if err != nil {
		r.t.Fatal(err)
	}
	type raw struct {
		kind     int
		src, dst int64
		name     string
		conf     int
		line     uint32
	}
	var raws []raw
	for rows.Next() {
		var x raw
		if err := rows.Scan(&x.kind, &x.src, &x.dst, &x.name, &x.conf, &x.line); err != nil {
			r.t.Fatal(err)
		}
		raws = append(raws, x)
	}
	rows.Close()
	out := make([]resolvedEdge, 0, len(raws))
	for _, x := range raws {
		e := resolvedEdge{Kind: model.EdgeKind(x.kind), Src: r.nodeKey(x.src), Name: x.name, Conf: x.conf, Line: x.line}
		if x.dst != 0 {
			e.Dst = r.nodeKey(x.dst)
		}
		out = append(out, e)
	}
	return out
}

// nodeKey renders a node as "path::Chain".
func (r *testRepo) nodeKey(id int64) string {
	r.t.Helper()
	var path, qname string
	var parent sql.NullInt64
	err := r.st.DB().QueryRow(`SELECT f.path, n.qname, n.parent_id FROM nodes n JOIN files f ON f.id = n.file_id
		WHERE n.id = ?`, id).Scan(&path, &qname, &parent)
	if err != nil {
		r.t.Fatalf("node %d: %v", id, err)
	}
	// qname = module + "." + chain; Go modules contain dots, so strip the
	// module computed the same way the writer did.
	spec, _ := parse.Lookup(model.LangFromPath(path))
	mod := spec.ModuleOf(path, nil, r.rc())
	chain := strings.TrimPrefix(qname, mod+".")
	if chain == qname {
		chain = strings.TrimPrefix(qname, mod+"_test.")
	}
	return path + "::" + chain
}

// edgesFrom returns the edges whose src is key and whose name is name.
func edgesFrom(es []resolvedEdge, src, name string) []resolvedEdge {
	var out []resolvedEdge
	for _, e := range es {
		if e.Src == src && e.Name == name {
			out = append(out, e)
		}
	}
	return out
}
