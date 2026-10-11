package testutil_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/pragmataW/graphmaster/internal/model"
	"github.com/pragmataW/graphmaster/internal/parse"
	"github.com/pragmataW/graphmaster/internal/store"
	"github.com/pragmataW/graphmaster/internal/testutil"
)

// These tests wire parse -> store -> CanonicalDump end to end on the real
// graph.db schema, so a schema change in internal/store that the dump does
// not understand (or a facts shape the store cannot hold) fails here.

type parsedFile struct {
	path  string
	facts model.Facts
}

// parseFixtureGo parses every Go source file of the fixture repository.
func parseFixtureGo(t *testing.T) []parsedFile {
	t.Helper()
	paths, err := testutil.FixturePaths()
	if err != nil {
		t.Fatal(err)
	}
	eng := parse.NewEngine()
	defer eng.Close()
	var out []parsedFile
	for _, p := range paths {
		if model.LangFromPath(p) != model.LangGo {
			continue
		}
		src, err := os.ReadFile(filepath.Join(testutil.FixtureDir(), filepath.FromSlash(p)))
		if err != nil {
			t.Fatal(err)
		}
		res, err := eng.ParseLang(context.Background(), model.LangGo, src)
		if err != nil {
			t.Fatalf("parse %s: %v", p, err)
		}
		out = append(out, parsedFile{path: p, facts: *res.Facts})
	}
	if len(out) < 3 {
		t.Fatalf("expected several Go fixture files, got %d", len(out))
	}
	return out
}

// writeFile stores one file's facts the way the indexer will: files row,
// nodes (parents first), contains edges and unresolved reference edges.
func writeFile(t *testing.T, w *store.Writer, pf parsedFile) {
	t.Helper()
	f := &store.File{Path: pf.path, Lang: pf.facts.Lang, Tracked: true, Status: model.StatusOK}
	fid, err := w.UpsertFile(f)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.DeleteFileGraph(fid); err != nil {
		t.Fatal(err)
	}
	ids := make([]int64, len(pf.facts.Defs))
	for i, d := range pf.facts.Defs {
		qname := strings.Join(append(pf.facts.QNameParts(int32(i)), d.Name), ".")
		n := &store.Node{
			FileID: fid, Kind: d.Kind, Name: d.Name, QName: qname,
			StartLine: d.StartLine, StartCol: d.StartCol, EndLine: d.EndLine,
			StartByte: d.StartByte, EndByte: d.EndByte, Sig: d.Sig, Flags: d.Flags,
			SKey: store.SymbolKey(pf.path, qname, d.Kind),
		}
		if d.Parent != model.NoParent {
			n.ParentID = ids[d.Parent]
		}
		if ids[i], err = w.InsertNode(n); err != nil {
			t.Fatal(err)
		}
		if n.ParentID != 0 {
			if _, err := w.InsertEdge(&store.Edge{FileID: fid, Src: n.ParentID, Dst: ids[i],
				Kind: model.EdgeContains, Conf: 100, Line: d.StartLine}); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, r := range pf.facts.Refs {
		if r.Enclosing == model.NoParent {
			continue
		}
		if _, err := w.InsertEdge(&store.Edge{FileID: fid, Src: ids[r.Enclosing], Kind: r.Kind.EdgeKind(),
			Name: r.Name, Qual: r.Qualifier, Line: r.Line, Col: r.Col}); err != nil {
			t.Fatal(err)
		}
	}
	for _, im := range pf.facts.Imports {
		if err := w.InsertImport(&store.Import{FileID: fid, Spec: im.Spec, Alias: im.Alias,
			Names: im.Names, Line: im.Line}); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.FileDone(); err != nil {
		t.Fatal(err)
	}
}

func buildStore(t *testing.T, files []parsedFile, bulk bool) *store.Store {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, t.TempDir(), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	w, err := s.NewWriter(ctx, store.WriterOptions{Bulk: bulk, MaxFiles: 2})
	if err != nil {
		t.Fatal(err)
	}
	for _, pf := range files {
		writeFile(t, w, pf)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if bulk {
		if err := s.FinishBulk(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetMeta(ctx, store.MetaState, store.StateReady); err != nil {
		t.Fatal(err)
	}
	return s
}

func clearPending(t *testing.T, s *store.Store) {
	t.Helper()
	if _, err := s.DB().Exec(`DELETE FROM pending_names`); err != nil {
		t.Fatal(err)
	}
}

func TestCanonicalDumpOnRealStore(t *testing.T) {
	files := parseFixtureGo(t)

	a := buildStore(t, files, true)
	rev := slices.Clone(files)
	slices.Reverse(rev)
	b := buildStore(t, rev, false)

	da, db := testutil.CanonicalDump(a.DB()), testutil.CanonicalDump(b.DB())
	if da != db {
		t.Fatalf("bulk build and reverse-order incremental build differ:\n--- bulk\n%s\n--- incremental\n%s", da, db)
	}
	for _, want := range []string{"== files (", "== nodes (", "== edges (", "schema_ver=" + strconv.Itoa(store.SchemaVersion), "state=ready",
		"go/internal/store/store.go", "contains "} {
		if !strings.Contains(da, want) {
			t.Errorf("dump lacks %q", want)
		}
	}
	if strings.Contains(da, "!dangling") {
		t.Errorf("dump has dangling references:\n%s", da)
	}

	// Re-indexing one file in place (delete graph + reinsert) must give the
	// same graph once the resolution queue is drained.
	w, err := b.NewWriter(context.Background(), store.WriterOptions{})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, w, files[0])
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	clearPending(t, a)
	clearPending(t, b)
	if da, db := testutil.CanonicalDump(a.DB()), testutil.CanonicalDump(b.DB()); da != db {
		t.Fatalf("re-indexed file changed the dump:\n--- before\n%s\n--- after\n%s", da, db)
	}

	// FTS was populated on both paths.
	for _, s := range []*store.Store{a, b} {
		ids, err := s.SearchSymbols(context.Background(), "store", 5)
		if err != nil || len(ids) == 0 {
			t.Fatalf("SearchSymbols: %v %v", ids, err)
		}
	}
}
