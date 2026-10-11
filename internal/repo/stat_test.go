package repo

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGitMode(t *testing.T) {
	cases := []struct {
		in   fs.FileMode
		want uint32
	}{
		{0o644, ModeRegular},
		{0o755, ModeExecutable},
		{0o700, ModeExecutable},
		{fs.ModeSymlink | 0o777, ModeSymlink},
		{fs.ModeDir | 0o755, ModeDir},
		{fs.ModeNamedPipe, 0},
	}
	for _, c := range cases {
		if got := GitMode(c.in); got != c.want {
			t.Errorf("GitMode(%v) = %o, want %o", c.in, got, c.want)
		}
	}
}

func TestLstat(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.go")
	if err := os.WriteFile(p, []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	if st.Size != 10 || st.Mode != ModeRegular || st.MtimeNS == 0 {
		t.Fatalf("unexpected stat %+v", st)
	}
	if err := os.Symlink("a.go", filepath.Join(dir, "l")); err != nil {
		t.Fatal(err)
	}
	ls, err := Lstat(filepath.Join(dir, "l"))
	if err != nil {
		t.Fatal(err)
	}
	if ls.Mode != ModeSymlink {
		t.Fatalf("symlink mode = %o", ls.Mode)
	}
}

func TestIsCleanRacy(t *testing.T) {
	scan := time.Unix(1_700_000_000, 0)
	old := FileStat{MtimeNS: scan.Add(-10 * time.Second).UnixNano(), Size: 5, Ino: 7, Mode: ModeRegular}
	if !IsClean(old, old, scan) {
		t.Fatal("old, equal stat must be clean")
	}
	changed := old
	changed.Size = 6
	if IsClean(old, changed, scan) {
		t.Fatal("size change must be dirty")
	}
	// Within the racy window: equal stat is not enough.
	racy := old
	racy.MtimeNS = scan.Add(-time.Second).UnixNano()
	if IsClean(racy, racy, scan) {
		t.Fatal("mtime within 2s of scan start must be racy")
	}
	edge := old
	edge.MtimeNS = scan.Add(-RacyWindow).UnixNano()
	if IsClean(edge, edge, scan) {
		t.Fatal("mtime exactly at scanStart-2s must be racy")
	}
	edge.MtimeNS--
	if !IsClean(edge, edge, scan) {
		t.Fatal("mtime just before scanStart-2s must be clean")
	}
	future := old
	future.MtimeNS = scan.Add(time.Hour).UnixNano()
	if IsClean(future, future, scan) {
		t.Fatal("future mtime must be racy")
	}
}
