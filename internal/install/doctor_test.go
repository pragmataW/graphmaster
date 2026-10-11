package install

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const seedDemoSettings = `{
  // user hooks
  "hooks": {
    "PreToolUse": [
      {"matcher": "Bash", "hooks": [{"type": "command", "command": "~/bin/guard.sh"}]}
    ]
  }
}
`

// demoPlan exercises every format with bin embedded the way adapters do.
func demoPlan(home, bin string) Plan {
	hookCmd := "'" + bin + "' hook pre-tool-use --agent demo"
	return Plan{Agent: "demo", Files: []FileChange{
		{Path: filepath.Join(home, ".demo.json"), Format: FormatJSON, Ops: []Op{
			JSONSet{Pointer: Pointer("mcpServers", "graphmaster"), Value: map[string]any{"command": bin, "args": []string{"mcp"}}},
		}},
		{Path: filepath.Join(home, ".demo", "settings.json"), Format: FormatJSON, Ops: []Op{
			JSONAppend{Pointer: Pointer("hooks", "PreToolUse"), Value: map[string]any{
				"matcher": "Grep|Bash",
				"hooks":   []any{map[string]any{"type": "command", "command": hookCmd}},
			}},
		}},
		{Path: filepath.Join(home, ".demo", "config.toml"), Format: FormatTOML, Ops: []Op{
			Block{Body: "[mcp_servers.graphmaster]\ncommand = \"" + bin + "\"\nargs = [\"mcp\"]\n"},
		}},
		{Path: filepath.Join(home, ".demo", "AGENTS.md"), Format: FormatMarkdown, Ops: []Op{
			Block{Body: "Use graphmaster.\n"},
		}},
		{Path: filepath.Join(home, ".demo", "plugins", "graphmaster.js"), Format: FormatGenerated,
			Content: []byte("const BIN = \"" + bin + "\";\n")},
	}}
}

func seedDemo(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	write(t, filepath.Join(home, ".demo", "settings.json"), seedDemoSettings)
	return home
}

func statuses(checks []OpCheck) []CheckStatus {
	out := make([]CheckStatus, len(checks))
	for i, c := range checks {
		out[i] = c.Status
	}
	return out
}

func TestCheckPlanNothingInstalled(t *testing.T) {
	home := seedDemo(t)
	checks := CheckPlan(demoPlan(home, "/opt/bin/graphmaster"), nil)
	if len(checks) != 5 {
		t.Fatalf("got %d checks, want one per op/file (5)", len(checks))
	}
	for _, c := range checks {
		if c.Status != CheckMissing {
			t.Errorf("%s: status %s, want missing", c.Path, c.Status)
		}
		if c.Index != -1 {
			t.Errorf("%s: index %d, want -1", c.Path, c.Index)
		}
	}
}

func TestCheckPlanAfterInstall(t *testing.T) {
	home := seedDemo(t)
	bin := "/opt/my tools/graphmaster"
	eng := NewEngine(EnvWithHome(home))
	if _, err := eng.Install(Options{}, demoPlan(home, bin)); err != nil {
		t.Fatal(err)
	}
	st, err := eng.State()
	if err != nil {
		t.Fatal(err)
	}
	checks := CheckPlan(demoPlan(home, bin), st)
	wantComp := []Component{ComponentMCP, ComponentHooks, ComponentMCP, ComponentInstructions, ComponentHooks}
	for i, c := range checks {
		if c.Status != CheckOK {
			t.Errorf("%s: status %s (%v), want ok", c.Path, c.Status, c.Err)
		}
		if c.Component != wantComp[i] {
			t.Errorf("%s: component %s, want %s", c.Path, c.Component, wantComp[i])
		}
		if c.Component == ComponentInstructions {
			if len(c.Binaries) != 0 {
				t.Errorf("%s: binaries %v, want none", c.Path, c.Binaries)
			}
			continue
		}
		if !slices.Equal(c.Binaries, []string{bin}) {
			t.Errorf("%s: binaries %v, want [%s]", c.Path, c.Binaries, bin)
		}
		if len(c.Found) == 0 {
			t.Errorf("%s: no found text", c.Path)
		}
	}
	// The user's hook stays first; graphmaster's group was appended.
	if checks[1].Index != 1 {
		t.Errorf("hook element index = %d, want 1", checks[1].Index)
	}
}

func TestCheckPlanStaleAfterBinaryMove(t *testing.T) {
	home := seedDemo(t)
	oldBin, newBin := "/old/bin/graphmaster", "/new/bin/graphmaster"
	eng := NewEngine(EnvWithHome(home))
	if _, err := eng.Install(Options{}, demoPlan(home, oldBin)); err != nil {
		t.Fatal(err)
	}
	st, _ := eng.State()

	for _, state := range []*State{st, nil} { // nil: recognized by content alone
		checks := CheckPlan(demoPlan(home, newBin), state)
		for _, c := range checks {
			if c.Component == ComponentInstructions {
				if c.Status != CheckOK {
					t.Errorf("%s: status %s, want ok", c.Path, c.Status)
				}
				continue
			}
			if c.Status != CheckStale {
				t.Errorf("state=%v %s: status %s, want stale", state != nil, c.Path, c.Status)
			}
			if !slices.Equal(c.Binaries, []string{oldBin}) {
				t.Errorf("%s: binaries %v, want [%s]", c.Path, c.Binaries, oldBin)
			}
		}
		if checks[1].Index != 1 {
			t.Errorf("stale hook index = %d, want 1", checks[1].Index)
		}
	}
}

func TestCheckPlanChangedInstructionsAreStale(t *testing.T) {
	home := seedDemo(t)
	p := demoPlan(home, "/b/graphmaster")
	if _, err := NewEngine(EnvWithHome(home)).Install(Options{}, p); err != nil {
		t.Fatal(err)
	}
	p.Files[3].Ops = []Op{Block{Body: "New text.\n"}}
	if c := CheckPlan(p, nil)[3]; c.Status != CheckStale || !strings.Contains(c.Found[0], "Use graphmaster.") {
		t.Fatalf("got %s %q, want stale with the old block", c.Status, c.Found)
	}
}

func TestCheckPlanInvalidFiles(t *testing.T) {
	home := seedDemo(t)
	write(t, filepath.Join(home, ".demo.json"), "{ not json")
	write(t, filepath.Join(home, ".demo", "config.toml"), "a = = 1\n")
	write(t, filepath.Join(home, ".demo", "AGENTS.md"), "<!-- graphmaster:begin v1 -->\nunterminated\n")
	checks := CheckPlan(demoPlan(home, "/b/graphmaster"), nil)
	want := []CheckStatus{CheckInvalid, CheckMissing, CheckInvalid, CheckInvalid, CheckMissing}
	if got := statuses(checks); !slices.Equal(got, want) {
		t.Fatalf("statuses %v, want %v", got, want)
	}
	for _, i := range []int{0, 2, 3} {
		if checks[i].Err == nil {
			t.Errorf("%s: invalid without error", checks[i].Path)
		}
	}
}

func TestCheckPlanUserHookNotMistakenForOurs(t *testing.T) {
	home := seedDemo(t)
	c := CheckPlan(demoPlan(home, "/b/graphmaster"), nil)[1]
	if c.Status != CheckMissing || len(c.Found) != 0 {
		t.Fatalf("got %s %q, want missing with nothing found", c.Status, c.Found)
	}
}

func TestClassify(t *testing.T) {
	for _, tc := range []struct {
		fc   FileChange
		op   Op
		want Component
	}{
		{FileChange{Path: "/h/.claude.json", Format: FormatJSON}, JSONSet{Pointer: "/mcpServers/graphmaster"}, ComponentMCP},
		{FileChange{Path: "/h/opencode.json", Format: FormatJSON}, JSONSet{Pointer: "/mcp/graphmaster"}, ComponentMCP},
		{FileChange{Path: "/h/settings.json", Format: FormatJSON}, JSONAppend{Pointer: "/hooks/PreToolUse"}, ComponentHooks},
		{FileChange{Path: "/h/.cursor/hooks.json", Format: FormatJSON}, JSONSet{Pointer: "/version"}, ComponentHooks},
		{FileChange{Path: "/h/other.json", Format: FormatJSON}, JSONSet{Pointer: "/theme"}, ComponentOther},
		{FileChange{Path: "/h/config.toml", Format: FormatTOML}, Block{Body: "[mcp_servers.graphmaster]\n"}, ComponentMCP},
		{FileChange{Path: "/h/AGENTS.md", Format: FormatMarkdown}, Block{Body: "x"}, ComponentInstructions},
		{FileChange{Path: "/h/plugins/graphmaster.js", Format: FormatGenerated}, nil, ComponentHooks},
		{FileChange{Path: "/h/rules/graphmaster.mdc", Format: FormatGenerated}, nil, ComponentInstructions},
	} {
		if got := Classify(tc.fc, tc.op); got != tc.want {
			t.Errorf("Classify(%s, %#v) = %s, want %s", tc.fc.Path, tc.op, got, tc.want)
		}
	}
}

func TestBinaryRefs(t *testing.T) {
	for _, tc := range []struct {
		text string
		want []string
	}{
		{`{"command":"/opt/homebrew/bin/graphmaster","args":["mcp"]}`, []string{"/opt/homebrew/bin/graphmaster"}},
		{`{"command":"/usr/local/bin/graphmaster hook pre-tool-use --agent claude"}`, []string{"/usr/local/bin/graphmaster"}},
		{`{"command":"'/Users/me/my tools/graphmaster' hook x"}`, []string{"/Users/me/my tools/graphmaster"}},
		{`{"command":"'/a/it'\\''s/graphmaster' hook x"}`, []string{"/a/it's/graphmaster"}},
		{`{"command":["/x/graphmaster","mcp"],"other":"/x/graphmaster"}`, []string{"/x/graphmaster"}},
		{"command = \"/t/graphmaster\"\nargs = [\"mcp\"]\n", []string{"/t/graphmaster"}},
		{`const BIN = "/p/graphmaster"; spawn(BIN, ["hook"])`, []string{"/p/graphmaster"}},
		{`{"command":"graphmaster hook x","path":"/x/graphmasterd","n":"/x/notgraphmaster"}`, nil},
		{`{"command":"~/bin/guard.sh"}`, nil},
	} {
		if got := binaryRefs(tc.text); !slices.Equal(got, tc.want) {
			t.Errorf("binaryRefs(%s) = %q, want %q", tc.text, got, tc.want)
		}
	}
}
