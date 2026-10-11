package repo

import (
	"errors"
	"math/rand/v2"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestTrimTrailingSpaces(t *testing.T) {
	cases := map[string]string{
		"foo":    "foo",
		"foo   ": "foo",
		`foo\ `:  `foo\ `,
		`foo\  `: `foo\ `,
		"a b  ":  "a b",
		`trail\`: `trail\`,
		"   ":    "",
		"x\t":    "x\t",
	}
	for in, want := range cases {
		if got := trimTrailingSpaces(in); got != want {
			t.Errorf("trimTrailingSpaces(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseIgnore(t *testing.T) {
	content := "\xef\xbb\xbf# comment\n\n*.o\r\n!keep.o\nbuild/\n/root.txt\n\\#lit\nsub/dir   \n"
	ps := parseIgnore([]byte(content), "base")
	var got []string
	for _, p := range ps {
		got = append(got, p.pattern)
	}
	want := []string{"*.o", "keep.o", "build", "root.txt", `\#lit`, "sub/dir"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("patterns = %q, want %q", got, want)
	}
	if ps[1].flags&patNegative == 0 || ps[2].flags&patMustBeDir == 0 || ps[2].flags&patNoDir == 0 {
		t.Fatal("flags not parsed")
	}
	if ps[3].flags&patNoDir != 0 || ps[5].flags&patNoDir != 0 {
		t.Fatal("patterns with a slash must match the pathname")
	}
	if ps[0].flags&patEndsWith == 0 || ps[0].base != "base" {
		t.Fatal("ENDSWITH/base not set")
	}
}

func TestWildmatchBasics(t *testing.T) {
	cases := []struct {
		pat, text string
		flags     int
		want      bool
	}{
		{"foo", "foo", 0, true},
		{"foo", "bar", 0, false},
		{"", "", 0, true},
		{"???", "abc", 0, true},
		{"*", "foo/bar", 0, true},
		{"*", "foo/bar", wmPathname, false},
		{"*/bar", "foo/bar", wmPathname, true},
		{"**/foo", "a/b/foo", wmPathname, true},
		{"**/foo", "foo", wmPathname, true},
		{"foo/**", "foo/a/b", wmPathname, true},
		{"foo/**", "foo", wmPathname, false},
		{"a/**/b", "a/b", wmPathname, true},
		{"a/**/b", "a/x/y/b", wmPathname, true},
		{"**/*a", "c/b/a", wmPathname, true},
		{"[a-c]x", "bx", 0, true},
		{"[!a-c]x", "dx", 0, true},
		{"[^a-c]x", "ax", 0, false},
		{"[[:digit:]]", "5", 0, true},
		{"[[:upper:]]", "a", wmCasefold, true},
		{`\*`, "*", 0, true},
		{`\*`, "a", 0, false},
		{"*.C", "foo.c", wmCasefold, true},
		{"*.C", "foo.c", 0, false},
		{"[]]", "]", 0, true},
		{"[a-", "a", 0, false},
		{"a[/]b", "a/b", wmPathname, false},
		{"foo*", "foobar", wmPathname, true},
		{"*foo*", "a/foo/b", wmPathname, false},
		{"**", "a/b/c", wmPathname, true},
	}
	for _, c := range cases {
		if got := wildmatch(c.pat, c.text, c.flags); got != c.want {
			t.Errorf("wildmatch(%q, %q, %d) = %v, want %v", c.pat, c.text, c.flags, got, c.want)
		}
	}
}

// ignoreCorpus is the set of files used by the check-ignore differential.
var ignoreCorpus = []string{
	"foo.c", "FOO.C", "foo.h", "foobar", "dir/foo.c", "dir/sub/foo.c",
	"dir/sub/deep/x.txt", "a/b/c.txt", "a/x/b/c.txt", "a/x/y/b/c.txt",
	"abc/def", "b/a", "c/b/a", "test[1].txt", "[ab]", "x y", "trail ",
	"#hash", "!bang", "q?", "doc/frotz/a", "frotz/doc/a", "z.min.js",
	"deep/a/b/c/d/e.go", "Upper/lower.go", "9lives", "sub/dir/file",
}

var ignorePatterns = []string{
	"foo.c", "*.c", "*.C", "/foo.c", "dir/", "dir/*", "dir/**", "**/foo.c",
	"**/sub", "a/**/c.txt", "a/*/b", "a/**/b/", "**/*a", "c/*a", "[a-c]*",
	"[!a]*", "[^a]*", "*.[ch]", `test\[1\].txt`, `\#hash`, `\!bang`, `q\?`,
	"q?", `x\ y`, `trail\ `, "trail ", "doc/frotz/", "frotz/", "*.min.*",
	"deep/**/e.go", "**", "*", "foo*", "foo**", "**foo.c", "dir/**/",
	"[[:upper:]]*", "[[:digit:]]*", "*/sub/*", "/a/b", "a/b", "sub/dir",
	"*/", "**/b/**", "[a-", "[[:bogus:]]", `foo\`, "UPPER/", "a/**/**/c.txt",
}

// TestIgnoreMatchesCheckIgnore compares Ignore.IsIgnored with
// `git check-ignore --no-index` for each pattern, case-sensitive and not.
func TestIgnoreMatchesCheckIgnore(t *testing.T) {
	dir := newRepo(t)
	files := map[string]string{}
	for _, f := range ignoreCorpus {
		files[f] = "x"
	}
	writeFiles(t, dir, files)
	r, err := FindRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, icase := range []string{"false", "true"} {
		git(t, dir, "config", "core.ignorecase", icase)
		for _, pat := range ignorePatterns {
			writeFiles(t, dir, map[string]string{".gitignore": pat + "\n"})
			want := checkIgnore(t, dir, ignoreCorpus)
			ig, err := NewIgnore(r, LoadGitConfig(r), nil)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, f := range ignoreCorpus {
				if ig.IsIgnored(f, false) {
					got = append(got, f)
				}
			}
			sort.Strings(got)
			if d := diffSets(want, got); d != "" {
				t.Errorf("ignorecase=%s pattern %q:\n%s", icase, pat, d)
			}
		}
	}
}

func TestIgnorePrecedence(t *testing.T) {
	dir := newRepo(t)
	writeFiles(t, dir, map[string]string{
		".gitignore":           "*.log\n!keep.log\nbuild/\n",
		"sub/.gitignore":       "!*.log\nlocal.txt\n",
		"sub/inner/.gitignore": "*.txt\n!inner.txt\n",
		".git/info/exclude":    "secret\n*.tmp\n",
		"global-ignore":        "*.bak\n!info.tmp\n",
	})
	git(t, dir, "config", "core.excludesFile", filepath.Join(dir, "global-ignore"))
	r, _ := FindRoot(dir)
	ig, err := NewIgnore(r, LoadGitConfig(r), []string{"vendor/", "*.min.*"})
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]bool{
		"a.log":               true,
		"keep.log":            false,
		"sub/a.log":           false, // deeper negation wins
		"sub/local.txt":       true,
		"sub/inner/x.txt":     true,
		"sub/inner/inner.txt": false,
		"sub/inner/local.txt": true, // parent .gitignore still applies
		"secret":              true,
		"x.tmp":               true,
		"info.tmp":            true, // info/exclude beats core.excludesFile
		"x.bak":               true,
		"build/out.go":        true,
		"vendor/lib.go":       true,
		"app.min.js":          true,
		"src/vendor/x.go":     true,
		"src/ok.go":           false,
	}
	for p, want := range cases {
		if got := ig.IsIgnored(p, false); got != want {
			t.Errorf("IsIgnored(%q) = %v, want %v", p, got, want)
		}
	}
	if !ig.ExcludedByConfig("third/vendor/a.go") || ig.ExcludedByConfig("sub/a.log") {
		t.Fatal("ExcludedByConfig wrong")
	}
}

func checkIgnore(t *testing.T, dir string, paths []string) []string {
	t.Helper()
	cmd := exec.Command("git", "check-ignore", "--no-index", "--stdin", "-z")
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(strings.Join(paths, "\x00") + "\x00")
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != 1 { // 1 = nothing ignored
			t.Fatalf("git check-ignore: %v", err)
		}
	}
	return splitZ(string(out))
}

// TestIgnoreRandomPatternsMatchCheckIgnore cross-checks randomly generated
// patterns (fixed seed) against git on a small tree of a/b/c components.
func TestIgnoreRandomPatternsMatchCheckIgnore(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns git per pattern")
	}
	dir := newRepo(t)
	git(t, dir, "config", "core.ignorecase", "false")
	var corpus []string
	names := []string{"a", "b", "ab", "c.txt"}
	for _, x := range names {
		for _, y := range names {
			corpus = append(corpus, "d"+x+"/"+y, "e/"+x+"/f"+y+"/"+y)
		}
	}
	files := map[string]string{}
	for _, f := range corpus {
		files[f] = "x"
	}
	writeFiles(t, dir, files)
	r, _ := FindRoot(dir)

	tokens := []string{"a", "b", "c", "d", "e", "f", "/", "*", "**", "?", "[a-c]", "[!b]", ".txt", "**/", "/**"}
	rng := rand.New(rand.NewPCG(1, 2))
	for range 150 {
		var sb strings.Builder
		if rng.IntN(4) == 0 {
			sb.WriteString("!")
		}
		for range 1 + rng.IntN(5) {
			sb.WriteString(tokens[rng.IntN(len(tokens))])
		}
		pat := sb.String()
		// A negation needs something to negate; prepend a broad rule.
		content := pat + "\n"
		if strings.HasPrefix(pat, "!") {
			content = "*\n!*/\n" + content
		}
		writeFiles(t, dir, map[string]string{".gitignore": content})
		want := checkIgnore(t, dir, corpus)
		ig, err := NewIgnore(r, LoadGitConfig(r), nil)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, f := range corpus {
			if ig.IsIgnored(f, false) {
				got = append(got, f)
			}
		}
		sort.Strings(got)
		if d := diffSets(want, got); d != "" {
			t.Errorf("pattern file %q:\n%s", content, d)
		}
	}
}
