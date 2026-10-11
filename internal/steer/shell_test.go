package steer

import (
	"reflect"
	"strings"
	"testing"
)

func TestAnalyzeShellSteering(t *testing.T) {
	cases := []struct {
		cmd   string
		ro    bool // AllReadOnly
		steer bool // Steerable()
	}{
		// grep family.
		{"grep -r Foo .", true, true},
		{"grep -rn Foo src/", true, true},
		{"grep -R Foo", true, true},
		{"grep Foo main.go", true, false},
		{"grep -r Foo main.go", true, false},
		{"grep Foo *.go", true, false},
		{`grep -rn "func Foo" --include=*.go .`, true, true},
		{`grep -r --include '*.go' Foo .`, true, true},
		{"grep --recursive Foo .", true, true},
		{"grep -d recurse Foo .", true, true},
		{"grep --directories=recurse Foo .", true, true},
		{"egrep -r 'Foo|Bar' .", true, true},
		{"fgrep -r Foo .", true, true},
		{"rgrep Foo .", true, true},
		{"ggrep -r Foo .", true, true},
		{"/usr/bin/grep -r Foo .", true, true},
		{`\grep -r Foo .`, true, true},
		{"grep -r -e Foo -e Bar .", true, true},
		{"grep -rf patterns.txt .", true, true},
		{`grep -r "$VAR" .`, true, true},
		{"grep -rl Foo . 2>/dev/null", true, true},
		{"grep -A3 -B 2 -r Foo .", true, true},
		{"grep -5 -r Foo .", true, true},
		{"grep -r Foo -- -dir", true, true},
		// Pipes and stdin.
		{"ps aux | grep foo", true, false},
		{"ps aux | grep -v grep | grep foo", true, false},
		{"cat file.go | grep Foo", true, false},
		{"cat x | grep -r Foo", true, true},
		{"echo foo | grep foo", true, false},
		{"grep -r Foo . | head -20", true, true},
		{"grep -r Foo . | wc -l", true, true},
		{"grep -r Foo . | sort | uniq -c", true, true},
		{"grep -r Foo . | cut -d: -f1 | sort -u", true, true},
		{"grep -r Foo . | awk -F: '{print $1}'", true, true},
		{"grep -r Foo . | xargs -I{} echo {}", true, true},
		{"grep Foo < main.go", true, false},
		{`grep Foo <<< "$x"`, true, false},
		{"grep Foo - < main.go", true, false},
		{"git log --oneline | grep fix", true, false},
		{"history | grep docker", false, false},
		// Redirects and side effects.
		{"grep -r Foo . > out.txt", false, false},
		{"grep -r Foo . >> out.txt", false, false},
		{"grep -r Foo . 2>/dev/null", true, true},
		{"grep -r Foo . 2>&1", true, true},
		{"grep -r Foo . &> /dev/null", true, true},
		{"grep -r Foo . >/dev/null 2>&1", true, true},
		{"grep -r Foo . >&2", true, true},
		{"grep -r Foo . | tee out.txt", false, false},
		{"grep -r Foo . && rm -rf x", false, false},
		{"grep -r Foo . ; git checkout main", false, false},
		{"grep -r Foo . <> rw.txt", false, false},
		// Lists, subshells, control flow.
		{"grep -r Foo . ; echo done", true, true},
		{"grep -r Foo . || true", true, true},
		{"cd src && grep -rn Foo .", true, true},
		{"(grep -r Foo .)", true, true},
		{"{ grep -r Foo .; }", true, true},
		{"grep -r Foo . &", true, true},
		{"! grep -r Foo .", true, true},
		{"if grep -rq Foo .; then echo yes; fi", true, true},
		{"for f in *.go; do grep Foo $f; done", true, false},
		{"f() { grep -r Foo .; }; f", false, false},
		{"X=1; grep -r Foo .", true, true},
		{"export X=1; grep -r Foo .", true, true},
		{"[[ -d src ]] && grep -r Foo src", true, true},
		{"coproc grep -r Foo .", false, false},
		// Substitutions.
		{`grep -r "$(echo Foo)" .`, true, true},
		{"grep -r Foo $(rm -rf x)", false, false},
		{"echo $(grep -r Foo .)", true, true},
		{"cat <(grep -r Foo .)", true, true},
		{"grep -r Foo $(git ls-files)", true, true},
		{"echo `grep -r Foo .`", true, true},
		// xargs and wrappers.
		{"find . -name '*.go' | xargs grep Foo", true, true},
		{"git ls-files | xargs grep Foo", true, true},
		{"git ls-files -z | xargs -0 grep -l Foo", true, true},
		{"rg -l Foo | xargs sed -i 's/Foo/Bar/g'", false, false},
		{"rg -l Foo | xargs sed -n '1p'", true, true},
		{"rg -l Foo | xargs rm", false, false},
		{"xargs grep Foo < files.txt", true, true},
		{"env LC_ALL=C grep -r Foo .", true, true},
		{"env -i grep -r Foo .", true, true},
		{"env -u HOME grep -r Foo .", true, true},
		{"time grep -r Foo .", true, true},
		{"time -p grep -r Foo .", true, true},
		{"command grep -r Foo .", true, true},
		{"command -v grep", true, false},
		{"nice -n 10 rg Foo", true, true},
		{"nice rg Foo", true, true},
		{"nice -5 rg Foo", true, true},
		{"LC_ALL=C grep -r Foo .", true, true},
		{"sudo grep -r Foo .", false, false},
		{"env", true, false},
		{"xargs", true, false},
		{"$GREP -r Foo .", false, false},
		{"builtin echo hi", true, false},
		// find.
		{"find . -name '*.go' -exec grep -l Foo {} \\;", true, true},
		{"find . -name '*.go' -exec grep -l Foo {} +", true, true},
		{"find . -name '*.tmp' -delete", false, false},
		{"find . -name '*.go' -exec rm {} \\;", false, false},
		{"find . -type f -fprint out.txt", false, false},
		{"find . -maxdepth 1 -name '*.go'", true, false},
		{"find src -name user_service.go", true, true},
		{"find . -iname '*user*'", true, true},
		{"find . -regex '.*\\.go'", true, true},
		{"find", true, true},
		{"find -L . -name '*.go'", true, true},
		{"find . \\( -name '*.go' -o -name '*.rs' \\) -print", true, true},
		{"find . -name '*.go' -execdir wc -l {} \\;", true, true},
		{"find . -name '*.go' -ok rm {} \\;", false, false},
		{"find . -name x -exec", true, true},
		// fd.
		{"fd Foo", true, true},
		{"fd -e go Foo src", true, true},
		{"fd -d 1 Foo", true, false},
		{"fd -e go -x rm", false, false},
		{"fd -e go -x wc -l", true, true},
		{"fd -e go --exec-batch rm {}", false, false},
		{"fdfind Foo", true, true},
		{"fd -g '*.go'", true, true},
		// rg.
		{"rg Foo", true, true},
		{"rg -n Foo src", true, true},
		{"rg Foo main.go", true, false},
		{"rg -t go Foo", true, true},
		{"rg --type=go Foo", true, true},
		{"rg -g '*.go' Foo", true, true},
		{"rg -F 'a.b' .", true, true},
		{"rg -e Foo -e Bar", true, true},
		{"rg --files", true, true},
		{"rg --files | rg user", true, true},
		{"ps | rg foo", true, false},
		{"rg --max-depth 1 Foo", true, false},
		{"rg Foo --replace Bar", true, true},
		{"rg Foo -r Bar .", true, true},
		{"rg --type-list", true, false},
		{"rg Foo < input.txt", true, false},
		{"cat x | rg Foo src", true, true},
		{"rg -uuu Foo", true, true},
		{"rg -C 3 Foo", true, true},
		// ag / ack.
		{"ag Foo", true, true},
		{`ag -G '\.go$' Foo`, true, true},
		{"ag -g user", true, true},
		{"ag --go Foo", true, true},
		{"ag Foo main.go", true, false},
		{"echo x | ag Foo", true, false},
		{"ack Foo", true, true},
		{"ack --type=go Foo", true, true},
		{"ack -f", true, true},
		{"ack Foo file.go", true, false},
		// git grep.
		{"git grep Foo", true, true},
		{"git grep -n Foo -- '*.go'", true, true},
		{"git --no-pager grep Foo", true, true},
		{"git -C sub grep Foo", true, true},
		{"git grep -e Foo HEAD~1 -- src", true, true},
		{"git grep Foo main.go", true, false},
		{"git status", true, false},
		{"git commit -m x", false, false},
		{"git -c core.pager=cat grep -i foo", true, true},
		// ls / tree / locate.
		{"ls", true, false},
		{"ls -la src", true, false},
		{"ls -R", true, true},
		{"ls -laR src", true, true},
		{"ls --recursive", true, true},
		{"ls -R | grep foo", true, true},
		{"tree", true, true},
		{"tree -L 1", true, false},
		{"tree -L 2 src", true, true},
		{"tree -P '*.go'", true, true},
		{"tree -o out.txt", false, false},
		{"locate user_service", true, true},
		// Shell re-parse.
		{"bash -c 'grep -r Foo .'", true, true},
		{`sh -c "rg Foo"`, true, true},
		{"zsh -c 'grep -r Foo . > out'", false, false},
		{"bash -lc 'rg Foo'", true, true},
		{"bash -c 'rg Foo' arg0 arg1", true, true},
		{"bash -o pipefail -c 'rg Foo | head'", true, true},
		{"bash script.sh", false, false},
		{"bash", false, false},
		{`bash -c "bash -c 'grep -r Foo .'"`, true, true},
		{"sh -c 'ps | grep x'", true, false},
		// Other read-only / side-effect commands.
		{"sed -i 's/a/b/' x.go", false, false},
		{"sed -n '1,10p' main.go", true, false},
		{"sed 's/a/b/' x", false, false},
		{"sed -ni 's/a/b/p' x", false, false},
		{"sed -n -i.bak 's/a/b/p' x", false, false},
		{"sed -n --in-place 's/a/b/p' x", false, false},
		{"sed -n '/foo/w out.txt' x", false, false},
		{"awk '{print $1}' file", true, false},
		{`awk 'BEGIN{system("rm x")}'`, false, false},
		{"sort -o out.txt file", false, false},
		{"sort --output=out.txt file", false, false},
		{"uniq a b", false, false},
		{"uniq -c a", true, false},
		{"cat main.go", true, false},
		{"head -n 50 main.go", true, false},
		{"tail -f log.txt", true, false},
		{"echo hi", true, false},
		{"wc -l *.go", true, false},
		{"rm -rf /", false, false},
		{"mkdir x", false, false},
		{"go test ./...", false, false},
		{"make", false, false},
		{"", true, false},
		{"# just a comment", true, false},
		{"echo 'grep -r Foo .'", true, false},
		{"cat <<EOF\ngrep -r Foo .\nEOF", true, false},
		{"pwd; cd ..; ls", true, false},
		{"true && false", true, false},
	}
	for _, c := range cases {
		got, err := AnalyzeShell(c.cmd)
		if err != nil {
			t.Errorf("AnalyzeShell(%q): unexpected error %v", c.cmd, err)
			continue
		}
		if got.AllReadOnly != c.ro {
			t.Errorf("AnalyzeShell(%q).AllReadOnly = %v, want %v (searches %+v)", c.cmd, got.AllReadOnly, c.ro, got.Searches)
		}
		if s := got.Steerable(); s != c.steer {
			t.Errorf("AnalyzeShell(%q).Steerable() = %v, want %v (searches %+v)", c.cmd, s, c.steer, got.Searches)
		}
	}
}

func TestAnalyzeShellParseErrors(t *testing.T) {
	for _, cmd := range []string{
		"grep 'unterminated",
		`grep "unterminated`,
		"if then",
		"(((",
		"grep -r Foo . |",
		`bash -c 'echo "unterminated'`,
		"for do done",
	} {
		if _, err := AnalyzeShell(cmd); err == nil {
			t.Errorf("AnalyzeShell(%q): want parse error", cmd)
		}
	}
}

func TestAnalyzeShellSearchDetails(t *testing.T) {
	type sc = SearchCall
	cases := []struct {
		cmd  string
		want []SearchCall
	}{
		{"grep -rn Foo src/", []sc{{Tool: "grep", Patterns: []string{"Foo"}, Paths: []string{"src/"}, Recursive: true}}},
		{"grep -R Foo", []sc{{Tool: "grep", Patterns: []string{"Foo"}, Paths: []string{"."}, Recursive: true}}},
		{"grep Foo main.go util.go", []sc{{Tool: "grep", Patterns: []string{"Foo"}, Paths: []string{"main.go", "util.go"}}}},
		{"ps | grep foo", []sc{{Tool: "grep", Patterns: []string{"foo"}, ReadsStdin: true}}},
		{`grep -rn "func Foo" --include=*.go --include '*.rs' .`, []sc{{Tool: "grep", Patterns: []string{"func Foo"}, Paths: []string{"."}, Globs: []string{"*.go", "*.rs"}, Recursive: true}}},
		{"grep -r -e Foo -e Bar src lib", []sc{{Tool: "grep", Patterns: []string{"Foo", "Bar"}, Paths: []string{"src", "lib"}, Recursive: true}}},
		{"grep -rneFoo .", []sc{{Tool: "grep", Patterns: []string{"Foo"}, Paths: []string{"."}, Recursive: true}}},
		{"grep -rF 'a.b' .", []sc{{Tool: "grep", Patterns: []string{"a.b"}, Paths: []string{"."}, Recursive: true, Fixed: true}}},
		{"fgrep -r Foo .", []sc{{Tool: "fgrep", Patterns: []string{"Foo"}, Paths: []string{"."}, Recursive: true, Fixed: true}}},
		{"grep -r --regexp=Foo .", []sc{{Tool: "grep", Patterns: []string{"Foo"}, Paths: []string{"."}, Recursive: true}}},
		{`grep -r 'it'\''s' .`, []sc{{Tool: "grep", Patterns: []string{"it's"}, Paths: []string{"."}, Recursive: true}}},
		{`grep -r Foo\.bar .`, []sc{{Tool: "grep", Patterns: []string{"Foo.bar"}, Paths: []string{"."}, Recursive: true}}},
		{`grep -r "a\"b" .`, []sc{{Tool: "grep", Patterns: []string{`a"b`}, Paths: []string{"."}, Recursive: true}}},
		{`grep -r "Foo\.bar" .`, []sc{{Tool: "grep", Patterns: []string{`Foo\.bar`}, Paths: []string{"."}, Recursive: true}}},
		{`grep -r $'a\tb' .`, []sc{{Tool: "grep", Patterns: []string{"a\tb"}, Paths: []string{"."}, Recursive: true}}},
		{"rg -t go -g '*.rs' Foo", []sc{{Tool: "rg", Patterns: []string{"Foo"}, Paths: []string{"."}, Globs: []string{"*.go", "*.rs"}, Recursive: true}}},
		{"rg --type=py Foo lib", []sc{{Tool: "rg", Patterns: []string{"Foo"}, Paths: []string{"lib"}, Globs: []string{"*.py", "*.pyi"}, Recursive: true}}},
		{"rg -e Foo -e Bar", []sc{{Tool: "rg", Patterns: []string{"Foo", "Bar"}, Paths: []string{"."}, Recursive: true}}},
		{"rg --files src", []sc{{Tool: "rg", Paths: []string{"src"}, Recursive: true, NameSearch: true}}},
		{"rg Foo -r Bar .", []sc{{Tool: "rg", Patterns: []string{"Foo"}, Paths: []string{"."}, Recursive: true}}},
		{"rg -F a.b", []sc{{Tool: "rg", Patterns: []string{"a.b"}, Paths: []string{"."}, Recursive: true, Fixed: true}}},
		{"rg -g '!*.md' Foo", []sc{{Tool: "rg", Patterns: []string{"Foo"}, Paths: []string{"."}, Recursive: true}}},
		{"ag --go Foo", []sc{{Tool: "ag", Patterns: []string{"Foo"}, Paths: []string{"."}, Globs: []string{"*.go"}, Recursive: true}}},
		{"ag -g user src", []sc{{Tool: "ag", Patterns: []string{"user"}, Paths: []string{"src"}, Recursive: true, NameSearch: true}}},
		{"ag -Q a.b", []sc{{Tool: "ag", Patterns: []string{"a.b"}, Paths: []string{"."}, Recursive: true, Fixed: true}}},
		{"ack --type=go Foo", []sc{{Tool: "ack", Patterns: []string{"Foo"}, Paths: []string{"."}, Globs: []string{"*.go"}, Recursive: true}}},
		{"ack -f", []sc{{Tool: "ack", Paths: []string{"."}, Recursive: true, NameSearch: true}}},
		{"git grep -n Foo -- '*.go'", []sc{{Tool: "git grep", Patterns: []string{"Foo"}, Paths: []string{"*.go"}, Recursive: true}}},
		{"git grep -e Foo HEAD~1 -- src", []sc{{Tool: "git grep", Patterns: []string{"Foo"}, Paths: []string{"src"}, Recursive: true}}},
		{"git grep Foo", []sc{{Tool: "git grep", Patterns: []string{"Foo"}, Paths: []string{"."}, Recursive: true}}},
		{"git -C sub --no-pager grep -F a.b", []sc{{Tool: "git grep", Patterns: []string{"a.b"}, Paths: []string{"sub"}, Recursive: true, Fixed: true}}},
		{"find src -name '*.go'", []sc{{Tool: "find", Paths: []string{"src"}, Globs: []string{"*.go"}, Recursive: true, NameSearch: true}}},
		{"find . -iname '*user*' -o -path '*/api/*'", []sc{{Tool: "find", Paths: []string{"."}, Globs: []string{"*user*", "*/api/*"}, Recursive: true, NameSearch: true}}},
		{"find . -regex '.*_test\\.go'", []sc{{Tool: "find", Patterns: []string{`.*_test\.go`}, Paths: []string{"."}, Recursive: true, NameSearch: true}}},
		{"find a b -type f", []sc{{Tool: "find", Paths: []string{"a", "b"}, Recursive: true, NameSearch: true}}},
		{"find . -name '*.go' -exec grep -l Foo {} +", []sc{
			{Tool: "find", Paths: []string{"."}, Globs: []string{"*.go"}, Recursive: true, NameSearch: true},
			{Tool: "grep", Patterns: []string{"Foo"}, Recursive: true},
		}},
		{"fd -e go Foo src", []sc{{Tool: "fd", Patterns: []string{"Foo"}, Paths: []string{"src"}, Globs: []string{"*.go"}, Recursive: true, NameSearch: true}}},
		{"fd -g '*.go'", []sc{{Tool: "fd", Paths: []string{"."}, Globs: []string{"*.go"}, Recursive: true, NameSearch: true}}},
		{"ls -R src", []sc{{Tool: "ls", Paths: []string{"src"}, Recursive: true, NameSearch: true}}},
		{"tree -P '*.go' src", []sc{{Tool: "tree", Paths: []string{"src"}, Globs: []string{"*.go"}, Recursive: true, NameSearch: true}}},
		{"locate user_service", []sc{{Tool: "locate", Patterns: []string{"user_service"}, Recursive: true, NameSearch: true}}},
		{"find . -name '*.go' | xargs grep Foo", []sc{
			{Tool: "find", Paths: []string{"."}, Globs: []string{"*.go"}, Recursive: true, NameSearch: true},
			{Tool: "grep", Patterns: []string{"Foo"}, Recursive: true},
		}},
		{"bash -c 'rg Foo src'", []sc{{Tool: "rg", Patterns: []string{"Foo"}, Paths: []string{"src"}, Recursive: true}}},
		{"env FOO=1 time nice -n 5 command rg Foo", []sc{{Tool: "rg", Patterns: []string{"Foo"}, Paths: []string{"."}, Recursive: true}}},
		{"echo hi", nil},
	}
	for _, c := range cases {
		got, err := AnalyzeShell(c.cmd)
		if err != nil {
			t.Errorf("AnalyzeShell(%q): %v", c.cmd, err)
			continue
		}
		if !reflect.DeepEqual(got.Searches, c.want) {
			t.Errorf("AnalyzeShell(%q).Searches =\n  %+v\nwant\n  %+v", c.cmd, got.Searches, c.want)
		}
	}
}

func TestAnalyzeShellDepthLimit(t *testing.T) {
	cmd := "grep -r Foo ."
	for i := 0; i < maxShellDepth+2; i++ {
		cmd = "bash -c " + shellQuote(cmd)
	}
	got, err := AnalyzeShell(cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.AllReadOnly {
		t.Error("over-deep nesting must not be treated as read-only")
	}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func FuzzAnalyzeShell(f *testing.F) {
	for _, s := range []string{
		"grep -r Foo .", "ps | grep x", "bash -c 'rg Foo'", "find . -name '*.go' -exec grep -l Foo {} +",
		"rg -t go Foo", "git grep -e x -- src", "xargs -I{} grep {}", "sed -n 1p x > y", "$(", "a <<EOF\nx\nEOF",
		"fd -x", "env -u", "nice -n", "grep -e", "find . -exec", "git -C", "tree -L",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		a, err := AnalyzeShell(s)
		if err == nil {
			_ = a.Steerable()
		}
	})
}
