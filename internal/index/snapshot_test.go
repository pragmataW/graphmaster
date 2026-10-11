package index

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/pragmataW/graphmaster/internal/model"
	"github.com/pragmataW/graphmaster/internal/store"
	"github.com/pragmataW/graphmaster/internal/testutil"
)

func TestSnapshotRoundTrip(t *testing.T) {
	files := map[string]*store.File{
		"a/b.go": {ID: 7, Path: "a/b.go", Lang: model.LangGo, Size: 120, MtimeNS: 1_700_000_000_123, Ino: 99,
			Mode: 0o100644, Blob: []byte("01234567890123456789"), Tracked: true, Status: model.StatusOK, LOC: 12, PkgID: 3},
		"c.py": {ID: 8, Path: "c.py", Lang: model.LangPython, Status: model.StatusTooBig},
	}
	dir := t.TempDir()
	if err := writeSnapshot(dir, "gen-1", files); err != nil {
		t.Fatal(err)
	}
	got, ok := readSnapshot(dir, "gen-1")
	if !ok || !reflect.DeepEqual(got, files) {
		t.Fatalf("round trip: ok=%v\n got %+v\nwant %+v", ok, got, files)
	}
	if _, ok := readSnapshot(dir, "gen-2"); ok {
		t.Fatal("snapshot with another label must be ignored")
	}
	data, _ := os.ReadFile(filepath.Join(dir, snapshotName))
	if err := os.WriteFile(filepath.Join(dir, snapshotName), data[:len(data)-3], 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := readSnapshot(dir, "gen-1"); ok {
		t.Fatal("truncated snapshot must be rejected")
	}
}

// TestColdOpenUsesSnapshot checks that a fresh process trusts files.snap
// only while it matches files_gen.
func TestColdOpenUsesSnapshot(t *testing.T) {
	dir := testutil.Materialize(t)
	ix := openIx(t, dir, Options{Now: futureClock})
	freshen(t, ix)
	freshen(t, ix) // loads the files table and writes the snapshot
	snap := filepath.Join(ix.Location().Dir, snapshotName)
	if _, err := os.Stat(snap); err != nil {
		t.Fatalf("snapshot not written: %v", err)
	}
	ix.Close()

	writeFile(t, dir, "go/internal/util/extra.go", "package util\n\nfunc Extra() {}\n")
	ix2 := openIx(t, dir, Options{Now: futureClock})
	if res := freshen(t, ix2); res.Added != 1 {
		t.Fatalf("cold run with snapshot: %+v", res)
	}
	ix2.Close()
	ix3 := openIx(t, dir, Options{Now: futureClock})
	if res := freshen(t, ix3); res.Changed() != 0 {
		t.Fatalf("stale snapshot was trusted: %+v", res)
	}
	requireSameDump(t, "snapshot", dump(t, ix3), scratchDump(t, dir, Options{}))
}
