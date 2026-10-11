package steer

import (
	"testing"

	"github.com/pragmataW/graphmaster/internal/model"
)

func TestExtSetHas(t *testing.T) {
	all := DefaultExtSet()
	goOnly := NewExtSet([]model.Lang{model.LangGo})
	cases := []struct {
		set  ExtSet
		ext  string
		want bool
	}{
		{all, ".go", true},
		{all, ".GO", true},
		{all, ".py", true},
		{all, ".tsx", true},
		{all, ".C", true},
		{all, ".md", false},
		{all, "", false},
		{goOnly, ".go", true},
		{goOnly, ".py", false},
		{nil, ".rs", true}, // nil set means every supported language
		{nil, ".txt", false},
	}
	for _, c := range cases {
		if got := c.set.Has(c.ext); got != c.want {
			t.Errorf("Has(%q) = %v, want %v", c.ext, got, c.want)
		}
	}
}

func TestIsCodePath(t *testing.T) {
	goOnly := NewExtSet([]model.Lang{model.LangGo})
	cases := []struct {
		path string
		set  ExtSet
		want bool
	}{
		{"main.go", nil, true},
		{"internal/steer/paths.go", nil, true},
		{`C:\src\app.cs`, nil, true},
		{"README.md", nil, false},
		{"docs/guide.txt", nil, false},
		{"Makefile", nil, false},
		{".gitignore", nil, false},
		{"src/", nil, false},
		{"lib/x.py", goOnly, false},
		{"lib/x.go", goOnly, true},
		{"component.TSX", nil, true},
		{"", nil, false},
	}
	for _, c := range cases {
		if got := IsCodePath(c.path, c.set); got != c.want {
			t.Errorf("IsCodePath(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

func TestClassifyTarget(t *testing.T) {
	cases := []struct {
		path string
		want TargetKind
	}{
		{"main.go", TargetCode},
		{"./pkg/a.rs", TargetCode},
		{"README.md", TargetNonCode},
		{"docs/notes.txt", TargetNonCode},
		{"package.json", TargetNonCode},
		{".", TargetTree},
		{"..", TargetTree},
		{"", TargetTree},
		{"src", TargetTree},
		{"src/", TargetTree},
		{"./internal/steer/", TargetTree},
		{"*.md", TargetNonCode},
		{"docs/*.{md,txt}", TargetNonCode},
		{"*.{md,go}", TargetCode},
		{"*.go", TargetCode},
		{"src/**", TargetTree},
		{"*", TargetTree},
		{"*.*", TargetTree},
		{"-", TargetNonCode}, // stdin
		{"/dev/null", TargetNonCode},
		{"Makefile", TargetTree}, // extensionless: may be a directory
		{".github", TargetTree},
		{"config.yaml", TargetNonCode},
		{"src/**/*.ts", TargetCode},
	}
	for _, c := range cases {
		if got := ClassifyTarget(c.path, nil); got != c.want {
			t.Errorf("ClassifyTarget(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

func TestAllNonCode(t *testing.T) {
	cases := []struct {
		paths []string
		want  bool
	}{
		{nil, false},
		{[]string{}, false},
		{[]string{"README.md"}, true},
		{[]string{"README.md", "CHANGELOG.md"}, true},
		{[]string{"README.md", "main.go"}, false},
		{[]string{"README.md", "."}, false},
		{[]string{"*.json"}, true},
		{[]string{"src"}, false},
	}
	for _, c := range cases {
		if got := AllNonCode(c.paths, nil); got != c.want {
			t.Errorf("AllNonCode(%q) = %v, want %v", c.paths, got, c.want)
		}
	}
}

func TestOutsideRoot(t *testing.T) {
	root := "/repo"
	cases := []struct {
		cwd, target string
		want        bool
	}{
		{"/repo", "main.go", false},
		{"/repo", ".", false},
		{"/repo/sub", "..", false},
		{"/repo/sub", "../..", true},
		{"/repo", "/etc/hosts", true},
		{"/repo", "/repo/x/y.go", false},
		{"/repo", "../other/x.go", true},
		{"/tmp", "x.go", true},
		{"/repo", "", false},
		{"/repox", "a.go", true},
	}
	for _, c := range cases {
		if got := OutsideRoot(root, c.cwd, c.target); got != c.want {
			t.Errorf("OutsideRoot(%q,%q,%q) = %v, want %v", root, c.cwd, c.target, got, c.want)
		}
	}
	if !AllOutsideRoot(root, "/repo", []string{"/etc", "/usr/include"}) {
		t.Error("AllOutsideRoot: want true for all-external targets")
	}
	if AllOutsideRoot(root, "/repo", []string{"/etc", "src"}) {
		t.Error("AllOutsideRoot: want false when one target is inside")
	}
	if AllOutsideRoot(root, "/repo", nil) {
		t.Error("AllOutsideRoot(nil) = true, want false (implicit cwd)")
	}
}

func TestExpandBraces(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"a", []string{"a"}},
		{"*.{ts,tsx}", []string{"*.ts", "*.tsx"}},
		{"{a,b}{c,d}", []string{"ac", "ad", "bc", "bd"}},
		{"{a,{b,c}}", []string{"a", "b", "c"}},
		{"{a}", []string{"{a}"}},
		{"x{", []string{"x{"}},
	}
	for _, c := range cases {
		got := expandBraces(c.in)
		if len(got) != len(c.want) {
			t.Errorf("expandBraces(%q) = %q, want %q", c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("expandBraces(%q) = %q, want %q", c.in, got, c.want)
				break
			}
		}
	}
}
