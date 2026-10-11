package steer

import (
	"slices"
	"testing"
)

// A directory change before a search moves the search scope; callers check
// Paths against the repository root, so they must see the effective target.
func TestAnalyzeShellDirectoryChanges(t *testing.T) {
	cases := []struct {
		cmd     string
		paths   []string // paths of the last search
		steer   bool
		unknown bool
	}{
		{"rg Foo", []string{"."}, true, false},
		{"cd src && grep -rn Foo .", []string{"src"}, true, false},
		{"cd /abs/repo && rg Foo", []string{"/abs/repo"}, true, false},
		{"cd /abs/repo && rg Foo lib", []string{"/abs/repo/lib"}, true, false},
		{"cd .. && rg Foo", []string{".."}, true, false},
		{"cd sub; rg Foo /etc", []string{"/etc"}, true, false},
		{"cd a && cd b && rg Foo x", []string{"a/b/x"}, true, false},
		{"cd -P /tmp/x && rg Foo", []string{"/tmp/x"}, true, false},
		{"pushd /other && rg Foo", []string{"/other"}, true, false},
		{"git -C sub grep Foo", []string{"sub"}, true, false},
		{"git -C sub grep Foo -- lib", []string{"sub/lib"}, true, false},
		{"git -C sub grep Foo; rg Bar", []string{"."}, true, false},
		// Targets that cannot be resolved statically: never steer.
		{"cd && rg Foo", []string{"."}, false, true},
		{"cd ~ && rg Foo", []string{"."}, false, true},
		{"cd ~/proj && rg Foo", []string{"."}, false, true},
		{"cd - && rg Foo", []string{"."}, false, true},
		{"cd $DIR && rg Foo", []string{"."}, false, true},
		{"popd && rg Foo", []string{"."}, false, true},
		{"git -C $D grep Foo", []string{"."}, false, true},
	}
	for _, c := range cases {
		got, err := AnalyzeShell(c.cmd)
		if err != nil {
			t.Errorf("AnalyzeShell(%q): %v", c.cmd, err)
			continue
		}
		if len(got.Searches) == 0 {
			t.Errorf("AnalyzeShell(%q): no searches", c.cmd)
			continue
		}
		last := got.Searches[len(got.Searches)-1]
		if !c.unknown && !slices.Equal(last.Paths, c.paths) {
			t.Errorf("AnalyzeShell(%q) paths = %q, want %q", c.cmd, last.Paths, c.paths)
		}
		if got.UnknownDir != c.unknown {
			t.Errorf("AnalyzeShell(%q).UnknownDir = %v, want %v", c.cmd, got.UnknownDir, c.unknown)
		}
		if s := got.Steerable(); s != c.steer {
			t.Errorf("AnalyzeShell(%q).Steerable() = %v, want %v", c.cmd, s, c.steer)
		}
	}
}
