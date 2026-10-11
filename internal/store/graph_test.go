package store

import (
	"context"
	"database/sql"
	"testing"

	"github.com/pragmataW/graphmaster/internal/model"
)

type twoFiles struct {
	fileA, fileB   int64
	foo, bar       int64 // nodes: Foo in A, Bar in B
	fooName        int64
	barName        int64
	edgeBA, edgeAB int64
}

// seedTwoFiles builds: A{Foo}, B{Bar}; Bar calls Foo (edge in B), Foo calls
// Bar (edge in A); B imports A.
func seedTwoFiles(t *testing.T, s *Store) twoFiles {
	t.Helper()
	w := newTestWriter(t, s, WriterOptions{})
	var g twoFiles
	g.fileA = mustFile(t, w, "a/a.go")
	g.foo = mustNode(t, w, g.fileA, "Foo")
	_ = w.FileDone()
	g.fileB = mustFile(t, w, "b/b.go")
	g.bar = mustNode(t, w, g.fileB, "Bar")
	g.fooName, _ = w.Intern("Foo")
	g.barName, _ = w.Intern("Bar")
	var err error
	g.edgeBA, err = w.InsertEdge(&Edge{FileID: g.fileB, Src: g.bar, Dst: g.foo, Kind: model.EdgeCalls, NameID: g.fooName, Line: 3, Col: 2, Conf: 90})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.InsertImport(&Import{FileID: g.fileB, Spec: "example.com/a", Names: []string{"Foo", "Baz"}, TargetFile: g.fileA, Line: 1}); err != nil {
		t.Fatal(err)
	}
	_ = w.FileDone()
	g.edgeAB, err = w.InsertEdge(&Edge{FileID: g.fileA, Src: g.foo, Dst: g.bar, Kind: model.EdgeCalls, Name: "Bar", Line: 5, Conf: 100})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.InsertImport(&Import{FileID: g.fileA, Spec: "example.com/b", Line: 1}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return g
}

func TestDeleteFileGraph(t *testing.T) {
	s := openTestStore(t)
	g := seedTwoFiles(t, s)
	ctx := context.Background()
	if n := countRows(t, s, `SELECT count(*) FROM pending_names`); n != 0 {
		t.Fatalf("pending before delete = %d", n)
	}

	w := newTestWriter(t, s, WriterOptions{})
	if err := w.DeleteFileGraph(g.fileA); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	if n := countRows(t, s, `SELECT count(*) FROM nodes WHERE file_id=?`, g.fileA); n != 0 {
		t.Errorf("A nodes left: %d", n)
	}
	if n := countRows(t, s, `SELECT count(*) FROM edges WHERE file_id=?`, g.fileA); n != 0 {
		t.Errorf("A out-edges left: %d", n)
	}
	if n := countRows(t, s, `SELECT count(*) FROM imports WHERE file_id=?`, g.fileA); n != 0 {
		t.Errorf("A imports left: %d", n)
	}
	if n := countRows(t, s, `SELECT count(*) FROM files WHERE id=?`, g.fileA); n != 1 {
		t.Errorf("files row must survive DeleteFileGraph")
	}
	// Inbound edge from B survives with dst NULL and conf 0.
	var dst sql.NullInt64
	var conf int
	if err := s.DB().QueryRow(`SELECT dst, conf FROM edges WHERE id=?`, g.edgeBA).Scan(&dst, &conf); err != nil {
		t.Fatal(err)
	}
	if dst.Valid || conf != 0 {
		t.Errorf("inbound edge dst=%v conf=%d, want NULL/0", dst, conf)
	}
	// B's own node and import are untouched.
	if n := countRows(t, s, `SELECT count(*) FROM nodes WHERE id=?`, g.bar); n != 1 {
		t.Errorf("B node deleted")
	}
	if n := countRows(t, s, `SELECT count(*) FROM imports WHERE file_id=? AND target_file=?`, g.fileB, g.fileA); n != 1 {
		t.Errorf("B import to A should keep target_file on DeleteFileGraph")
	}
	// Foo is pending (inbound edge name + old def name); Bar is not.
	pend, err := s.PendingNames(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !equalIDs(pend, []int64{g.fooName}) {
		t.Errorf("pending = %v, want [%d]", pend, g.fooName)
	}
	// FTS rows for Foo are gone, Bar remains.
	if ids, _ := s.SearchSymbols(ctx, "Foo", 10); len(ids) != 0 {
		t.Errorf("FTS still finds Foo: %v", ids)
	}
	if ids, _ := s.SearchSubstring(ctx, "Foo", 10); len(ids) != 0 {
		t.Errorf("trigram still finds Foo: %v", ids)
	}
	if ids, _ := s.SearchSymbols(ctx, "Bar", 10); !equalIDs(ids, []int64{g.bar}) {
		t.Errorf("FTS Bar = %v", ids)
	}
}

func TestDeleteFile(t *testing.T) {
	s := openTestStore(t)
	g := seedTwoFiles(t, s)
	w := newTestWriter(t, s, WriterOptions{})
	if err := w.DeleteFile(g.fileA); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, s, `SELECT count(*) FROM files WHERE id=?`, g.fileA); n != 0 {
		t.Errorf("files row left")
	}
	var tf sql.NullInt64
	if err := s.DB().QueryRow(`SELECT target_file FROM imports WHERE file_id=?`, g.fileB).Scan(&tf); err != nil {
		t.Fatal(err)
	}
	if tf.Valid {
		t.Errorf("import target_file = %v, want NULL", tf)
	}
}

func TestReinsertAfterDelete(t *testing.T) {
	s := openTestStore(t)
	g := seedTwoFiles(t, s)
	w := newTestWriter(t, s, WriterOptions{})
	if err := w.DeleteFileGraph(g.fileA); err != nil {
		t.Fatal(err)
	}
	newFoo := mustNode(t, w, g.fileA, "Foo")
	if err := w.EnqueuePending(g.fooName, g.barName); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	ids, err := s.SearchSymbols(context.Background(), "Foo", 10)
	if err != nil || !equalIDs(ids, []int64{newFoo}) {
		t.Fatalf("FTS after reinsert = %v %v want [%d]", ids, err, newFoo)
	}
	pend, _ := s.PendingNames(context.Background(), 0)
	if !equalIDs(pend, []int64{g.fooName, g.barName}) {
		t.Fatalf("pending = %v", pend)
	}
	w2 := newTestWriter(t, s, WriterOptions{})
	if err := w2.ClearPending(g.fooName); err != nil {
		t.Fatal(err)
	}
	_ = w2.Close()
	pend, _ = s.PendingNames(context.Background(), 0)
	if !equalIDs(pend, []int64{g.barName}) {
		t.Fatalf("pending after clear = %v", pend)
	}
}

func TestNodeAndPackageRoundTrip(t *testing.T) {
	s := openTestStore(t)
	w := newTestWriter(t, s, WriterOptions{})
	pkg, err := w.UpsertPackage(&Package{Lang: model.LangGo, Name: "example.com/a", Dir: "a", Kind: 1})
	if err != nil {
		t.Fatal(err)
	}
	pkg2, _ := w.UpsertPackage(&Package{Lang: model.LangGo, Name: "example.com/a", Dir: "a2"})
	if pkg != pkg2 {
		t.Fatalf("package upsert ids differ %d %d", pkg, pkg2)
	}
	fid, _ := w.UpsertFile(&File{Path: "a/a.go", Lang: model.LangGo, PkgID: pkg})
	parent := mustNode(t, w, fid, "Server")
	sk := SymbolKey("a/a.go", "a.Server.Start", model.KindMethod)
	child, err := w.InsertNode(&Node{FileID: fid, ParentID: parent, Kind: model.KindMethod, Name: "Start", QName: "a.Server.Start",
		StartLine: 3, StartCol: 1, EndLine: 9, StartByte: 10, EndByte: 90, Sig: "func (s *Server) Start() error", Flags: model.FlagExported, SKey: sk})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	n, err := s.NodeByID(context.Background(), child)
	if err != nil {
		t.Fatal(err)
	}
	if n.ParentID != parent || n.Name != "Start" || n.Kind != model.KindMethod || n.QName != "a.Server.Start" ||
		n.EndLine != 9 || n.EndByte != 90 || n.Flags != model.FlagExported || string(n.SKey) != string(sk) || n.Sig == "" {
		t.Fatalf("NodeByID = %+v", n)
	}
	if len(sk) != 8 {
		t.Fatalf("SymbolKey len = %d", len(sk))
	}
	if string(SymbolKey("a/a.go", "a.Server.Start", model.KindMethod)) != string(sk) {
		t.Fatal("SymbolKey not deterministic")
	}
	var dir string
	_ = s.DB().QueryRow(`SELECT dir FROM packages WHERE id=?`, pkg).Scan(&dir)
	if dir != "a2" {
		t.Fatalf("package dir = %q", dir)
	}
}

func TestDeleteNode(t *testing.T) {
	s := openTestStore(t)
	g := seedTwoFiles(t, s)
	w := newTestWriter(t, s, WriterOptions{})
	if err := w.DeleteNode(g.fileA, g.foo); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, s, `SELECT count(*) FROM nodes WHERE id=?`, g.foo); n != 0 {
		t.Errorf("node left: %d", n)
	}
	if n := countRows(t, s, `SELECT count(*) FROM sym_tri WHERE rowid=?`, g.foo); n != 0 {
		t.Errorf("trigram rows left: %d", n)
	}
	if n := countRows(t, s, `SELECT count(*) FROM edges WHERE id=? AND dst IS NULL AND conf=0`, g.edgeBA); n != 1 {
		t.Error("inbound edge from B not unlinked")
	}
	if n := countRows(t, s, `SELECT count(*) FROM pending_names WHERE name_id=?`, g.fooName); n != 1 {
		t.Error("name of the inbound edge not queued")
	}
	// The file's own edges are left to the caller.
	if n := countRows(t, s, `SELECT count(*) FROM edges WHERE id=?`, g.edgeAB); n != 1 {
		t.Error("own edge of A removed")
	}
	if n := countRows(t, s, `SELECT count(*) FROM pending_names`); n != 1 {
		t.Errorf("pending = %d, want only the inbound name", n)
	}
}
