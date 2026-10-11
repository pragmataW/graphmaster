package steer

import (
	"testing"

	"github.com/pragmataW/graphmaster/internal/model"
)

func TestClassifyGlob(t *testing.T) {
	cases := []struct {
		glob string
		kind GlobKind
		stem string // expected Stem for literals / Hint for explore
	}{
		// Literal file names: allowed.
		{"**/user_service.go", GlobLiteral, "user_service"},
		{"user_service.go", GlobLiteral, "user_service"},
		{"src/**/UserService.java", GlobLiteral, "UserService"},
		{"**/Makefile", GlobLiteral, "Makefile"},
		{"internal/steer/paths.go", GlobLiteral, "paths"},
		{"**/{user,order}_service.go", GlobLiteral, ""},
		{"**/README.md", GlobLiteral, "README"},
		{"src", GlobLiteral, "src"},
		{"./main.go", GlobLiteral, "main"},
		{"**/.eslintrc", GlobLiteral, ".eslintrc"},
		// Broad code exploration.
		{"**/*.ts", GlobExplore, ""},
		{"src/**", GlobExplore, ""},
		{"**/*", GlobExplore, ""},
		{"*", GlobExplore, ""},
		{"**", GlobExplore, ""},
		{"*.go", GlobExplore, ""},
		{"**/*_test.go", GlobExplore, "_test"},
		{"internal/**/*.go", GlobExplore, ""},
		{"**/*.{ts,tsx}", GlobExplore, ""},
		{"**/*.{md,go}", GlobExplore, ""},
		{"**/*Service*.java", GlobExplore, "Service"},
		{"**/User*.ts", GlobExplore, "User"},
		{"src/*/index.?s", GlobExplore, "index"},
		{"**/*.*", GlobExplore, ""},
		{"**/[A-Z]*.go", GlobExplore, ""},
		{"lib/", GlobExplore, ""},
		{"", GlobExplore, ""},
		// Non-code only: allowed.
		{"**/*.md", GlobNonCode, ""},
		{"docs/**/*.txt", GlobNonCode, ""},
		{"**/*.{json,yaml,yml}", GlobNonCode, ""},
		{"*.lock", GlobNonCode, ""},
		{"**/package.json", GlobLiteral, "package"},
		{"!**/*.go", GlobNonCode, ""},
		{"**/*.css", GlobNonCode, ""},
		{"**/*.MD", GlobNonCode, ""},
		{"**/*.PY", GlobExplore, ""},
	}
	for _, c := range cases {
		got := ClassifyGlob(c.glob, nil)
		if got.Kind != c.kind {
			t.Errorf("ClassifyGlob(%q).Kind = %v, want %v", c.glob, got.Kind, c.kind)
			continue
		}
		var stem string
		if got.Kind == GlobLiteral {
			stem = got.Stem
		} else {
			stem = got.Hint
		}
		if stem != c.stem {
			t.Errorf("ClassifyGlob(%q) stem/hint = %q, want %q", c.glob, stem, c.stem)
		}
	}
}

func TestClassifyGlobRespectsEnabledLangs(t *testing.T) {
	goOnly := NewExtSet([]model.Lang{model.LangGo})
	if k := ClassifyGlob("**/*.py", goOnly).Kind; k != GlobNonCode {
		t.Errorf("*.py with only Go enabled = %v, want noncode", k)
	}
	if k := ClassifyGlob("**/*.go", goOnly).Kind; k != GlobExplore {
		t.Errorf("*.go with Go enabled = %v, want explore", k)
	}
}

func TestClassifyGlobLiteralName(t *testing.T) {
	g := ClassifyGlob("**/user_service.go", nil)
	if g.Name != "user_service.go" {
		t.Errorf("Name = %q", g.Name)
	}
	if !g.Allow() {
		t.Error("literal glob should be allowed")
	}
	if ClassifyGlob("**/*.go", nil).Allow() {
		t.Error("explore glob should not be allowed")
	}
	if !ClassifyGlob("**/*.md", nil).Allow() {
		t.Error("noncode glob should be allowed")
	}
}

func FuzzClassifyGlob(f *testing.F) {
	for _, s := range []string{"**/*.go", "{a,{b,c}}", "src/**", "[", "{", "*.{x", "!a", "a/b/c.d"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		_ = ClassifyGlob(s, nil)
		_ = ClassifyTarget(s, nil)
	})
}
