package repo

import (
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBlobSHA1KnownValues(t *testing.T) {
	cases := map[string]string{
		"":              "e69de29bb2d1d6434b8b29ae775ad8c2e48c5391",
		"hello world\n": "3b18e512dba79e4c8300dd08aeb37f8e728b8dad",
	}
	for in, want := range cases {
		got := BlobSHA1([]byte(in))
		if hex.EncodeToString(got[:]) != want {
			t.Errorf("BlobSHA1(%q) = %x, want %s", in, got, want)
		}
	}
}

func TestHashPathMatchesGit(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	reg := filepath.Join(dir, "f.txt")
	big := strings.Repeat("graphmaster\n", 100_000)
	if err := os.WriteFile(reg, []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "l")
	if err := os.Symlink("some/target", link); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{reg, link} {
		sum, st, err := HashPath(p)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode == 0 {
			t.Fatalf("%s: missing stat", p)
		}
		want := gitHashObject(t, p)
		if hex.EncodeToString(sum[:]) != want {
			t.Errorf("%s: HashPath = %x, git = %s", p, sum, want)
		}
	}
}

// gitHashObject returns git's blob id for path; symlinks hash their target.
func gitHashObject(t *testing.T, path string) string {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	var cmd *exec.Cmd
	if fi.Mode()&os.ModeSymlink != 0 {
		target, _ := os.Readlink(path)
		cmd = exec.Command("git", "hash-object", "--stdin")
		cmd.Stdin = strings.NewReader(target)
	} else {
		cmd = exec.Command("git", "hash-object", "--no-filters", path)
	}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git hash-object: %v", err)
	}
	return strings.TrimSpace(string(out))
}
