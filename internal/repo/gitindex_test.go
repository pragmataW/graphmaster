package repo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/format/index"
)

// stageLines renders entries like `git ls-files -s`.
func stageLines(entries []IndexEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, fmt.Sprintf("%06o %x %d\t%s", e.Stat.Mode, e.Blob[:], e.Stage, e.Path))
	}
	return out
}

func gitStageLines(t *testing.T, dir string) []string {
	t.Helper()
	return splitZ(git(t, dir, "ls-files", "-s", "-z"))
}

func readIndexT(t *testing.T, dir string) *Index {
	t.Helper()
	r, err := FindRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	ix, err := ReadIndex(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	return ix
}

func assertIndexMatchesGit(t *testing.T, dir string, ix *Index) {
	t.Helper()
	got := stageLines(ix.Entries)
	want := gitStageLines(t, dir)
	if d := diffSets(want, got); d != "" {
		t.Fatalf("index entries differ from git ls-files -s:\n%s", d)
	}
	if len(got) != len(want) {
		t.Fatalf("entry count %d, git has %d", len(got), len(want))
	}
}

// populateIndexRepo builds a repo exercising modes, nesting, unicode,
// a gitlink and a symlink.
func populateIndexRepo(t *testing.T) string {
	t.Helper()
	sub := newRepo(t)
	writeFiles(t, sub, map[string]string{"s.go": "package s\n"})
	git(t, sub, "add", ".")
	git(t, sub, "commit", "-qm", "sub")

	dir := newRepo(t)
	files := map[string]string{
		"main.go":                   "package main\n",
		"a/b/c/deep.py":             "x = 1\n",
		"a/b/sibling.ts":            "export const a = 1\n",
		"ünïcödé/файл.rs":           "fn main() {}\n",
		"with space/file name.java": "class A {}\n",
		"empty.txt":                 "",
	}
	for i := range 40 {
		files[fmt.Sprintf("many/dir%02d/f%02d.c", i%7, i)] = fmt.Sprintf("int f%d;\n", i)
	}
	writeFiles(t, dir, files)
	if err := os.Chmod(filepath.Join(dir, "main.go"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("main.go", filepath.Join(dir, "link.go")); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "-c", "core.precomposeunicode=true", "add", ".")
	git(t, dir, "submodule", "add", "-q", sub, "vendor/sub")
	git(t, dir, "commit", "-qm", "init")
	return dir
}

func TestReadIndexVersionsMatchGit(t *testing.T) {
	dir := populateIndexRepo(t)
	for _, v := range []string{"2", "3", "4"} {
		t.Run("v"+v, func(t *testing.T) {
			git(t, dir, "update-index", "--index-version", v)
			ix := readIndexT(t, dir)
			if ix.FromGit {
				t.Fatal("expected direct decode, got git fallback")
			}
			// git downgrades v3 to v2 when no entry needs extended flags.
			if (v == "4") != (ix.Version == 4) {
				t.Fatalf("version = %d, requested %s", ix.Version, v)
			}
			assertIndexMatchesGit(t, dir, ix)
		})
	}
}

func TestReadIndexIntentToAddAndSkipWorktree(t *testing.T) {
	dir := populateIndexRepo(t)
	writeFiles(t, dir, map[string]string{"new.go": "package main\n"})
	git(t, dir, "add", "-N", "new.go")
	git(t, dir, "update-index", "--skip-worktree", "a/b/sibling.ts")
	ix := readIndexT(t, dir)
	if ix.Version != 3 {
		t.Fatalf("extended flags need index v3, got v%d", ix.Version)
	}
	assertIndexMatchesGit(t, dir, ix)
	var ita, skip bool
	for _, e := range ix.Entries {
		ita = ita || (e.Path == "new.go" && e.IntentToAdd)
		skip = skip || (e.Path == "a/b/sibling.ts" && e.SkipWorktree)
	}
	if !ita || !skip {
		t.Fatalf("extended flags lost: ita=%v skip=%v", ita, skip)
	}
	for _, f := range ix.Files() {
		if f.Path == "a/b/sibling.ts" || f.Path == "vendor/sub" {
			t.Fatalf("Files() must exclude skip-worktree and gitlinks, got %s", f.Path)
		}
	}
}

func TestReadIndexConflictStages(t *testing.T) {
	dir := newRepo(t)
	writeFiles(t, dir, map[string]string{"c.go": "package c\n// base\n"})
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-qm", "base")
	git(t, dir, "checkout", "-qb", "other")
	writeFiles(t, dir, map[string]string{"c.go": "package c\n// other\n"})
	git(t, dir, "commit", "-qam", "other")
	git(t, dir, "checkout", "-q", "main")
	writeFiles(t, dir, map[string]string{"c.go": "package c\n// main\n"})
	git(t, dir, "commit", "-qam", "main")
	cmd := []string{"merge", "-q", "other"}
	_ = runGitAllowFail(dir, cmd...)

	ix := readIndexT(t, dir)
	assertIndexMatchesGit(t, dir, ix)
	if len(ix.Entries) != 3 {
		t.Fatalf("want 3 conflict stages, got %d", len(ix.Entries))
	}
	if files := ix.Files(); len(files) != 1 || files[0].Stage != 1 {
		t.Fatalf("Files() must dedupe stages: %+v", files)
	}
}

func TestReadIndexSplitIndexFallsBack(t *testing.T) {
	dir := populateIndexRepo(t)
	git(t, dir, "update-index", "--split-index")
	writeFiles(t, dir, map[string]string{"later.go": "package main\n"})
	git(t, dir, "add", "later.go")
	ix := readIndexT(t, dir)
	if !ix.FromGit {
		t.Fatal("split index should use the git ls-files fallback")
	}
	assertIndexMatchesGit(t, dir, ix)
}

func TestReadIndexSparseIndexFallsBack(t *testing.T) {
	dir := populateIndexRepo(t)
	git(t, dir, "sparse-checkout", "set", "--cone", "--sparse-index", "a")
	ix := readIndexT(t, dir)
	if !ix.FromGit {
		t.Fatal("sparse index should use the git ls-files fallback")
	}
	assertIndexMatchesGit(t, dir, ix)
	for _, f := range ix.Files() {
		if strings.HasPrefix(f.Path, "many/") {
			t.Fatalf("out-of-cone path %s should be skip-worktree", f.Path)
		}
	}
}

func TestReadIndexSkipHash(t *testing.T) {
	dir := populateIndexRepo(t)
	git(t, dir, "config", "index.skipHash", "true")
	writeFiles(t, dir, map[string]string{"z.go": "package main\n"})
	git(t, dir, "add", "z.go")
	ix := readIndexT(t, dir)
	if ix.FromGit {
		t.Fatal("skipHash index must decode directly")
	}
	assertIndexMatchesGit(t, dir, ix)
}

func TestReadIndexMissing(t *testing.T) {
	dir := newRepo(t)
	ix := readIndexT(t, dir)
	if len(ix.Entries) != 0 || !ix.Sig.IsZero() {
		t.Fatalf("fresh repo: %+v", ix)
	}
}

func TestReadIndexCorruptFallsBack(t *testing.T) {
	dir := populateIndexRepo(t)
	path := filepath.Join(dir, ".git", "index")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)-1] ^= 0xff
	if _, err := DecodeIndex(data); !errors.Is(err, ErrUnsupportedIndex) {
		t.Fatalf("corrupt checksum: err = %v", err)
	}
	for _, bad := range [][]byte{nil, []byte("DIRX"), data[:30]} {
		if _, err := DecodeIndex(bad); err == nil {
			t.Fatalf("garbage %q decoded", bad)
		}
	}
}

func TestIndexStatAndClean(t *testing.T) {
	dir := newRepo(t)
	writeFiles(t, dir, map[string]string{"old.go": "package a\n", "fresh.go": "package a\n"})
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(dir, "old.go"), past, past); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", ".")
	ix := readIndexT(t, dir)
	byPath := map[string]*IndexEntry{}
	for i := range ix.Entries {
		byPath[ix.Entries[i].Path] = &ix.Entries[i]
	}
	st, err := Lstat(filepath.Join(dir, "old.go"))
	if err != nil {
		t.Fatal(err)
	}
	old := byPath["old.go"]
	if !old.StatMatches(st) {
		t.Fatalf("stat mismatch: index %+v lstat %+v", old.Stat, st)
	}
	if !ix.Clean(old, st) {
		t.Fatal("old file should be clean")
	}
	if old.Blob != BlobSHA1([]byte("package a\n")) {
		t.Fatal("blob id mismatch")
	}
	st2, _ := Lstat(filepath.Join(dir, "fresh.go"))
	if ix.Clean(byPath["fresh.go"], st2) {
		t.Fatal("file modified within the racy window of the index write must not be clean")
	}
	st.Size++
	if old.StatMatches(st) {
		t.Fatal("size change must not match")
	}
}

func TestStatSigRoundTrip(t *testing.T) {
	s := StatSig{MtimeNS: 1_700_000_000_123_456_789, Size: 42, Ino: 99}
	got, err := ParseStatSig(s.String())
	if err != nil || got != s {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	for _, bad := range []string{"", "1:2", "a:b:c", "1:2:-3"} {
		if _, err := ParseStatSig(bad); err == nil {
			t.Fatalf("ParseStatSig(%q) should fail", bad)
		}
	}
	dir := newRepo(t)
	writeFiles(t, dir, map[string]string{"f": "x"})
	git(t, dir, "add", ".")
	r, _ := FindRoot(dir)
	sig, err := StatSigOf(r.IndexPath())
	if err != nil || sig.IsZero() {
		t.Fatalf("StatSigOf: %+v %v", sig, err)
	}
	if ix := readIndexT(t, dir); ix.Sig != sig {
		t.Fatalf("Index.Sig %+v != %+v", ix.Sig, sig)
	}
}

// TestDecodeAgainstGoGit cross-checks the decoder with go-git's reference
// implementation on synthetic indexes, including names longer than the
// 12-bit length field.
func TestDecodeAgainstGoGit(t *testing.T) {
	for _, v := range []uint32{2, 3, 4} {
		gg := synthIndex(t, 300, v)
		gg.Entries[5].Name = "long/" + strings.Repeat("n", 5000) + ".go"
		var buf bytes.Buffer
		if err := index.NewEncoder(&buf).Encode(gg); err != nil {
			t.Fatal(err)
		}
		var ref index.Index
		if err := index.NewDecoder(bytes.NewReader(buf.Bytes())).Decode(&ref); err != nil {
			t.Fatal(err)
		}
		ix, err := DecodeIndex(buf.Bytes())
		if err != nil {
			t.Fatalf("v%d: %v", v, err)
		}
		if len(ix.Entries) != len(ref.Entries) {
			t.Fatalf("v%d: %d entries, go-git %d", v, len(ix.Entries), len(ref.Entries))
		}
		for i, re := range ref.Entries {
			e := ix.Entries[i]
			if e.Path != re.Name || e.Blob != BlobID(re.Hash) || e.Stat.Mode != uint32(re.Mode) ||
				e.Stat.Size != int64(re.Size) || e.Stat.Ino != uint64(re.Inode) ||
				e.Stat.MtimeNS != re.ModifiedAt.UnixNano() || e.Stage != uint8(re.Stage) ||
				e.SkipWorktree != re.SkipWorktree || e.IntentToAdd != re.IntentToAdd {
				t.Fatalf("v%d entry %d differs:\n ours %+v\n ref  %+v", v, i, e, re)
			}
		}
	}
}

func TestDecodeIndexVarint(t *testing.T) {
	// Values from git's varint encoding: 0..127 one byte, 128 -> 0x80 0x00.
	cases := []struct {
		in   []byte
		want uint64
		n    int
	}{
		{[]byte{0x00}, 0, 1},
		{[]byte{0x7f}, 127, 1},
		{[]byte{0x80, 0x00}, 128, 2},
		{[]byte{0x80, 0x7f}, 255, 2},
		{[]byte{0x81, 0x00}, 256, 2},
	}
	for _, c := range cases {
		v, n := decodeIndexVarint(c.in)
		if v != c.want || n != c.n {
			t.Errorf("decode %x = %d,%d want %d,%d", c.in, v, n, c.want, c.n)
		}
	}
	if _, n := decodeIndexVarint([]byte{0x80}); n != 0 {
		t.Error("truncated varint must fail")
	}
}

func synthIndex(t testing.TB, n int, version uint32) *index.Index {
	t.Helper()
	idx := &index.Index{Version: version}
	base := time.Unix(1_700_000_000, 0)
	for i := range n {
		e := idx.Add(fmt.Sprintf("pkg%03d/sub%02d/file_%06d.go", i/1000, (i/10)%100, i))
		e.Hash = plumbing.NewHash(fmt.Sprintf("%040x", i+1))
		e.Mode = filemode.Regular
		e.ModifiedAt = base.Add(time.Duration(i) * time.Millisecond)
		e.Size = uint32(100 + i)
		e.Inode = uint32(1000 + i)
		if version >= 3 && i%97 == 0 {
			e.SkipWorktree = true
		}
	}
	return idx
}

func runGitAllowFail(dir string, args ...string) error {
	cmd := gitCmd(dir, args...)
	return cmd.Run()
}
