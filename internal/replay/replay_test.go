package replay

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pragmataW/graphmaster/internal/hookrun"
)

func TestLoadEveryFixture(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join(HooksDir(), "*", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no fixtures under testdata/hooks")
	}
	for _, p := range paths {
		rel, _ := filepath.Rel(HooksDir(), p)
		rel = filepath.ToSlash(rel)
		base := filepath.Base(p)
		if !strings.HasPrefix(base, "real-") && !strings.HasPrefix(base, "synthetic-") {
			t.Errorf("%s: fixture names start with real- (captured) or synthetic- (hand-written)", rel)
		}
		rec, err := Load(rel)
		if err != nil {
			t.Errorf("%v", err)
			continue
		}
		if dir := strings.Split(rel, "/")[0]; rec.Agent != dir {
			t.Errorf("%s: agent %q is filed under %s/", rel, rec.Agent, dir)
		}
	}
}

func TestLoadRejectsIncompleteFixtures(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"noagent.json": `{"event":"stop","stdin":{}}`,
		"nostdin.json": `{"agent":"claude","event":"stop"}`,
		"broken.json":  `{`,
	} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		rel, _ := filepath.Rel(HooksDir(), p)
		if _, err := Load(rel); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestExpand(t *testing.T) {
	got, err := Expand("{{REPO}}/a {{SESSION_ID}} {{REPO}}", map[string]string{"REPO": "/r", "SESSION_ID": "s1"})
	if err != nil || got != "/r/a s1 /r" {
		t.Fatalf("Expand = %q, %v", got, err)
	}
	if _, err := Expand("{{REPO}} {{TYPO}} {{OTHER}}", map[string]string{"REPO": "/r"}); err == nil ||
		!strings.Contains(err.Error(), "TYPO, OTHER") {
		t.Fatalf("missing placeholders: err = %v", err)
	}
	if got, err := Expand("{{lower}} {not}", nil); err != nil || got != "{{lower}} {not}" {
		t.Fatalf("non-placeholders must pass through: %q, %v", got, err)
	}
}

func TestExpandJSONEscapes(t *testing.T) {
	got, err := ExpandJSON([]byte(`{"cwd":"{{REPO}}"}`), map[string]string{"REPO": `C:\work\"x"`})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"cwd":"C:\\work\\\"x\""}`; string(got) != want {
		t.Fatalf("ExpandJSON = %s, want %s", got, want)
	}
}

func TestStdinAndEnviron(t *testing.T) {
	rec := &hookrun.Recording{
		Agent: "claude", Event: "pre-tool-use",
		Env:   map[string]string{"Z": "{{REDACTED}}", "CLAUDE_PROJECT_DIR": "{{REPO}}"},
		Stdin: []byte(`{"cwd":"{{REPO}}"}`),
	}
	vars := map[string]string{"REPO": "/r", "REDACTED": "x"}
	in, err := Stdin(rec, vars)
	if err != nil || string(in) != `{"cwd":"/r"}` {
		t.Fatalf("Stdin = %s, %v", in, err)
	}
	env, err := Environ(rec, vars)
	if err != nil || !reflect.DeepEqual(env, []string{"CLAUDE_PROJECT_DIR=/r", "Z=x"}) {
		t.Fatalf("Environ = %v, %v", env, err)
	}
	text := &hookrun.Recording{Agent: "claude", Event: "stop", StdinText: "{oops {{REPO}}"}
	if in, err := Stdin(text, vars); err != nil || string(in) != "{oops /r" {
		t.Fatalf("Stdin(text) = %q, %v", in, err)
	}
}

func TestPlaceholders(t *testing.T) {
	got := Placeholders("{{B}} {{A}} {{B}} {{a}}")
	if !reflect.DeepEqual(got, []string{"A", "B"}) {
		t.Fatalf("Placeholders = %v", got)
	}
}

func TestNormalize(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, "repo")
	s := hookrun.NewScrubber(home, repo)
	in := "index: 3 files · fresh (+12 files, 1.234s) at " + repo + "/a.go, cfg " + home + "/.config; " +
		"fresh (+1 file, 850ms); building 42%; fresh (+2 files, 1m3.5s); 8 redirects"
	want := "index: 3 files · fresh (+12 files, {{DURATION}}) at {{REPO}}/a.go, cfg {{HOME}}/.config; " +
		"fresh (+1 files, {{DURATION}}); building {{PERCENT}}%; fresh (+2 files, {{DURATION}}); 8 redirects"
	if got := Normalize(in, s); got != want {
		t.Fatalf("Normalize =\n%s\nwant\n%s", got, want)
	}
}

func TestFields(t *testing.T) {
	got := Fields([]byte(`{"b":{"reason":"one\ntwo","n":3},"a":"x","l":["p","q"]}`))
	want := []string{"a: x", "b.n: 3", "b.reason: |\n    one\n    two", "l[0]: p", "l[1]: q"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Fields =\n%q\nwant\n%q", got, want)
	}
	if Fields([]byte(`[1]`)) != nil || Fields([]byte(``)) != nil {
		t.Fatal("non-objects have no fields")
	}
}
