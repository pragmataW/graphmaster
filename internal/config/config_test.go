package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pragmataW/graphmaster/internal/hookapi"
	"github.com/pragmataW/graphmaster/internal/model"
)

// env is a fake environment for LookupEnv.
type env map[string]string

func (e env) lookup(k string) (string, bool) {
	v, ok := e[k]
	return v, ok
}

// setup writes optional user/project config files into temp dirs and
// returns Options wired to them.
func setup(t *testing.T, user, project string, e env) Options {
	t.Helper()
	home := t.TempDir()
	root := t.TempDir()
	if e == nil {
		e = env{}
	}
	if _, ok := e["HOME"]; !ok {
		e["HOME"] = home
	}
	if user != "" {
		p := filepath.Join(home, ".config", "graphmaster", "config.toml")
		if xdg, ok := e["XDG_CONFIG_HOME"]; ok {
			p = filepath.Join(xdg, "graphmaster", "config.toml")
		}
		writeFile(t, p, user)
	}
	if project != "" {
		writeFile(t, ProjectConfigPath(root), project)
	}
	return Options{Root: root, LookupEnv: e.lookup}
}

func writeFile(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDefaults(t *testing.T) {
	c := Default()
	if c.Mode != ModeBalanced {
		t.Errorf("Mode = %q", c.Mode)
	}
	if c.MaxFileSize != 1<<20 {
		t.Errorf("MaxFileSize = %d", c.MaxFileSize)
	}
	wantEx := []string{"vendor/", "node_modules/", "dist/", "build/", "target/", "*.min.*"}
	if !reflect.DeepEqual(c.Excludes, wantEx) {
		t.Errorf("Excludes = %v", c.Excludes)
	}
	if c.SoftBudget != 8 {
		t.Errorf("SoftBudget = %d", c.SoftBudget)
	}
	if len(c.EnabledLangs()) != len(model.AllLangs()) {
		t.Errorf("EnabledLangs = %v", c.EnabledLangs())
	}
	if c.Timeouts.MCP.D() != 20*time.Second || c.Timeouts.PreToolUse.D() != 1500*time.Millisecond ||
		c.Timeouts.SessionStart.D() != 590*time.Second {
		t.Errorf("Timeouts = %+v", c.Timeouts)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("defaults invalid: %v", err)
	}
	// Default must return independent copies.
	c.Excludes[0] = "x"
	if Default().Excludes[0] != "vendor/" {
		t.Fatal("Default shares slices")
	}
}

func TestLoadNoFilesGivesDefaults(t *testing.T) {
	c, err := LoadWith(setup(t, "", "", nil))
	if err != nil {
		t.Fatal(err)
	}
	d := Default()
	if c.Mode != d.Mode || c.MaxFileSize != d.MaxFileSize || c.SoftBudget != d.SoftBudget ||
		!reflect.DeepEqual(c.Excludes, d.Excludes) || c.Timeouts != d.Timeouts {
		t.Fatalf("got %+v, want defaults", c)
	}
	if len(c.Sources) != 0 {
		t.Fatalf("Sources = %v", c.Sources)
	}
}

func TestPrecedence(t *testing.T) {
	cases := []struct {
		name          string
		user, project string
		env           env
		wantMode      Mode
		wantBudget    int
		wantSize      int64
		wantExcludes  []string
		wantExtra     []string
		wantLangs     []model.Lang
		wantPreTool   time.Duration
		wantSessStart time.Duration
	}{
		{
			name:     "user only",
			user:     "mode = \"strict\"\nsoft_budget = 3\n",
			wantMode: ModeStrict, wantBudget: 3,
		},
		{
			name:     "project overrides user",
			user:     "mode = \"strict\"\nsoft_budget = 3\n",
			project:  "mode = \"off\"\n",
			wantMode: ModeOff, wantBudget: 3, // budget falls through from user
		},
		{
			name:     "env overrides project and user",
			user:     "mode = \"strict\"\n",
			project:  "mode = \"off\"\nsoft_budget = 4\n",
			env:      env{"GRAPHMASTER_MODE": "balanced", "GRAPHMASTER_SOFT_BUDGET": "12"},
			wantMode: ModeBalanced, wantBudget: 12,
		},
		{
			name:     "env only",
			env:      env{"GRAPHMASTER_MODE": "STRICT"},
			wantMode: ModeStrict, wantBudget: 8,
		},
		{
			name:     "max file size units and ints",
			user:     "max_file_size = \"2MiB\"\n",
			project:  "max_file_size = 4096\n",
			wantMode: ModeBalanced, wantBudget: 8, wantSize: 4096,
		},
		{
			name:     "max file size env",
			project:  "max_file_size = 4096\n",
			env:      env{"GRAPHMASTER_MAX_FILE_SIZE": "512KiB"},
			wantMode: ModeBalanced, wantBudget: 8, wantSize: 512 << 10,
		},
		{
			name:     "excludes replace, extra_excludes accumulate",
			user:     "excludes = [\"third_party/\"]\nextra_excludes = [\"gen/\"]\n",
			project:  "extra_excludes = [\"fixtures/\"]\n",
			env:      env{"GRAPHMASTER_EXTRA_EXCLUDES": "tmp/, out/"},
			wantMode: ModeBalanced, wantBudget: 8,
			wantExcludes: []string{"third_party/"},
			wantExtra:    []string{"gen/", "fixtures/", "tmp/", "out/"},
		},
		{
			name:     "env excludes replace",
			project:  "excludes = [\"a/\"]\n",
			env:      env{"GRAPHMASTER_EXCLUDES": "b/,c/"},
			wantMode: ModeBalanced, wantBudget: 8,
			wantExcludes: []string{"b/", "c/"},
		},
		{
			name:     "languages project then env",
			user:     "languages = [\"go\", \"python\"]\n",
			project:  "languages = [\"rust\"]\n",
			wantMode: ModeBalanced, wantBudget: 8,
			wantLangs: []model.Lang{model.LangRust},
		},
		{
			name:     "languages env",
			project:  "languages = [\"rust\"]\n",
			env:      env{"GRAPHMASTER_LANGUAGES": "go,ts"},
			wantMode: ModeBalanced, wantBudget: 8,
			wantLangs: []model.Lang{model.LangGo, model.LangTypeScript},
		},
		{
			name:     "timeouts merge per key",
			user:     "[timeouts]\npre_tool_use = \"2s\"\nsession_start = \"60s\"\n",
			project:  "[timeouts]\npre_tool_use = \"750ms\"\n",
			wantMode: ModeBalanced, wantBudget: 8,
			wantPreTool:   750 * time.Millisecond,
			wantSessStart: 60 * time.Second,
		},
		{
			name:     "timeout env",
			project:  "[timeouts]\npre_tool_use = \"750ms\"\n",
			env:      env{"GRAPHMASTER_TIMEOUT_PRE_TOOL_USE": "3s"},
			wantMode: ModeBalanced, wantBudget: 8,
			wantPreTool:   3 * time.Second,
			wantSessStart: 590 * time.Second,
		},
		{
			name:     "XDG_CONFIG_HOME respected",
			user:     "mode = \"off\"\n",
			env:      env{"XDG_CONFIG_HOME": "XDG"}, // replaced with temp dir below
			wantMode: ModeOff, wantBudget: 8,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.env != nil && c.env["XDG_CONFIG_HOME"] == "XDG" {
				c.env["XDG_CONFIG_HOME"] = t.TempDir()
			}
			cfg, err := LoadWith(setup(t, c.user, c.project, c.env))
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Mode != c.wantMode {
				t.Errorf("Mode = %q, want %q", cfg.Mode, c.wantMode)
			}
			if cfg.SoftBudget != c.wantBudget {
				t.Errorf("SoftBudget = %d, want %d", cfg.SoftBudget, c.wantBudget)
			}
			if c.wantSize != 0 && int64(cfg.MaxFileSize) != c.wantSize {
				t.Errorf("MaxFileSize = %d, want %d", cfg.MaxFileSize, c.wantSize)
			}
			if c.wantExcludes != nil && !reflect.DeepEqual(cfg.Excludes, c.wantExcludes) {
				t.Errorf("Excludes = %v, want %v", cfg.Excludes, c.wantExcludes)
			}
			if c.wantExtra != nil && !reflect.DeepEqual(cfg.ExtraExcludes, c.wantExtra) {
				t.Errorf("ExtraExcludes = %v, want %v", cfg.ExtraExcludes, c.wantExtra)
			}
			if c.wantLangs != nil && !reflect.DeepEqual(cfg.EnabledLangs(), c.wantLangs) {
				t.Errorf("EnabledLangs = %v, want %v", cfg.EnabledLangs(), c.wantLangs)
			}
			if c.wantPreTool != 0 && cfg.Timeouts.PreToolUse.D() != c.wantPreTool {
				t.Errorf("PreToolUse = %v, want %v", cfg.Timeouts.PreToolUse.D(), c.wantPreTool)
			}
			if c.wantSessStart != 0 && cfg.Timeouts.SessionStart.D() != c.wantSessStart {
				t.Errorf("SessionStart = %v, want %v", cfg.Timeouts.SessionStart.D(), c.wantSessStart)
			}
		})
	}
}

func TestSourcesRecorded(t *testing.T) {
	o := setup(t, "mode = \"strict\"\n", "mode = \"off\"\n", env{"GRAPHMASTER_MODE": "balanced"})
	cfg, err := LoadWith(o)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Sources) != 3 || !strings.HasSuffix(cfg.Sources[0], "config.toml") ||
		cfg.Sources[1] != ProjectConfigPath(o.Root) || cfg.Sources[2] != "env" {
		t.Fatalf("Sources = %v", cfg.Sources)
	}
}

func TestLoadErrors(t *testing.T) {
	cases := []struct {
		name, user, project string
		env                 env
		wantInErr           string
	}{
		{name: "bad toml", project: "mode = ", wantInErr: ProjectConfigPath("")},
		{name: "bad mode in file", user: "mode = \"loud\"\n", wantInErr: "loud"},
		{name: "bad mode env", env: env{"GRAPHMASTER_MODE": "loud"}, wantInErr: "GRAPHMASTER_MODE"},
		{name: "bad budget env", env: env{"GRAPHMASTER_SOFT_BUDGET": "many"}, wantInErr: "GRAPHMASTER_SOFT_BUDGET"},
		{name: "negative budget", project: "soft_budget = -1\n", wantInErr: "soft_budget"},
		{name: "bad size", project: "max_file_size = \"huge\"\n", wantInErr: "huge"},
		{name: "zero size", project: "max_file_size = 0\n", wantInErr: "max_file_size"},
		{name: "bad lang", project: "languages = [\"cobol\"]\n", wantInErr: "cobol"},
		{name: "bad duration", project: "[timeouts]\nmcp = \"soon\"\n", wantInErr: "soon"},
		{name: "negative duration env", env: env{"GRAPHMASTER_TIMEOUT_MCP": "-1s"}, wantInErr: "mcp"},
		{name: "wrong type", project: "mode = 3\n", wantInErr: "config.toml"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := LoadWith(setup(t, c.user, c.project, c.env))
			if err == nil {
				t.Fatal("expected error")
			}
			want := c.wantInErr
			if want == ProjectConfigPath("") {
				want = filepath.Join(".graphmaster", "config.toml")
			}
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("error %q does not mention %q", err, want)
			}
		})
	}
}

func TestUnknownKeysWarn(t *testing.T) {
	cfg, err := LoadWith(setup(t, "", "mode = \"off\"\nfuture_knob = true\n[timeouts]\nwarp = \"1s\"\n", nil))
	if err != nil {
		t.Fatalf("unknown keys must not fail loading: %v", err)
	}
	if cfg.Mode != ModeOff {
		t.Fatalf("known keys must still apply, Mode = %q", cfg.Mode)
	}
	joined := strings.Join(cfg.Warnings, "\n")
	if !strings.Contains(joined, "future_knob") || !strings.Contains(joined, "warp") {
		t.Fatalf("Warnings = %v", cfg.Warnings)
	}
}

func TestUserConfigPath(t *testing.T) {
	p, err := UserConfigPath(env{"HOME": "/h"}.lookup)
	if err != nil || p != filepath.Join("/h", ".config", "graphmaster", "config.toml") {
		t.Fatalf("got %q, %v", p, err)
	}
	p, err = UserConfigPath(env{"HOME": "/h", "XDG_CONFIG_HOME": "/x"}.lookup)
	if err != nil || p != filepath.Join("/x", "graphmaster", "config.toml") {
		t.Fatalf("got %q, %v", p, err)
	}
	// Relative XDG_CONFIG_HOME is invalid per spec and ignored.
	p, err = UserConfigPath(env{"HOME": "/h", "XDG_CONFIG_HOME": "rel"}.lookup)
	if err != nil || p != filepath.Join("/h", ".config", "graphmaster", "config.toml") {
		t.Fatalf("got %q, %v", p, err)
	}
}

func TestProjectLayerSkippedWithoutRoot(t *testing.T) {
	o := setup(t, "", "mode = \"off\"\n", nil)
	o.Root = ""
	cfg, err := LoadWith(o)
	if err != nil || cfg.Mode != ModeBalanced {
		t.Fatalf("got %v, %v", cfg.Mode, err)
	}
}

func TestExplicitUserPath(t *testing.T) {
	o := setup(t, "", "", nil)
	p := filepath.Join(t.TempDir(), "custom.toml")
	writeFile(t, p, "soft_budget = 2\n")
	o.UserPath = p
	cfg, err := LoadWith(o)
	if err != nil || cfg.SoftBudget != 2 {
		t.Fatalf("got %d, %v", cfg.SoftBudget, err)
	}
}

func TestAllExcludesAndLangEnabled(t *testing.T) {
	c := Default()
	c.Excludes = []string{"a/"}
	c.ExtraExcludes = []string{"b/", "a/"}
	if got := c.AllExcludes(); !reflect.DeepEqual(got, []string{"a/", "b/"}) {
		t.Fatalf("AllExcludes = %v", got)
	}
	c.Languages = []string{"go"}
	if !c.LangEnabled(model.LangGo) || c.LangEnabled(model.LangRust) {
		t.Fatal("LangEnabled wrong")
	}
	c.Languages = nil
	if !c.LangEnabled(model.LangRust) {
		t.Fatal("empty language list means all")
	}
}

func TestTimeoutForEvent(t *testing.T) {
	c := Default()
	cases := map[hookapi.Event]time.Duration{
		hookapi.EventSessionStart:  590 * time.Second,
		hookapi.EventPreToolUse:    1500 * time.Millisecond,
		hookapi.EventPostToolUse:   1500 * time.Millisecond,
		hookapi.EventSubagentStart: 1500 * time.Millisecond,
		hookapi.EventPreCompact:    1500 * time.Millisecond,
		hookapi.EventStop:          0,
	}
	for ev, want := range cases {
		if got := c.Timeouts.ForEvent(ev); got != want {
			t.Errorf("ForEvent(%s) = %v, want %v", ev, got, want)
		}
	}
}

func TestParseMode(t *testing.T) {
	for in, want := range map[string]Mode{"strict": ModeStrict, " Balanced": ModeBalanced, "OFF": ModeOff} { //nolint:gocritic // surrounding whitespace is part of the test
		if got, err := ParseMode(in); err != nil || got != want {
			t.Errorf("ParseMode(%q) = %q, %v", in, got, err)
		}
	}
	if _, err := ParseMode(""); err == nil {
		t.Fatal("empty mode must fail")
	}
}

func TestByteSize(t *testing.T) {
	cases := map[string]int64{
		"1024": 1024, "1KiB": 1 << 10, "1kib": 1 << 10, "2MiB": 2 << 20, "1GiB": 1 << 30,
		"1KB": 1000, "3MB": 3_000_000, "10B": 10, " 5 MiB ": 5 << 20, //nolint:gocritic // surrounding whitespace is part of the test
	}
	for in, want := range cases {
		var b ByteSize
		if err := b.UnmarshalText([]byte(in)); err != nil || int64(b) != want {
			t.Errorf("ByteSize(%q) = %d, %v; want %d", in, b, err, want)
		}
	}
	for _, bad := range []string{"", "MiB", "1.5MiB", "-1", "1TiBx"} {
		var b ByteSize
		if err := b.UnmarshalText([]byte(bad)); err == nil {
			t.Errorf("ByteSize(%q) should fail", bad)
		}
	}
	if s, _ := ByteSize(1 << 20).MarshalText(); string(s) != "1MiB" {
		t.Fatalf("MarshalText = %s", s)
	}
	if s, _ := ByteSize(1500).MarshalText(); string(s) != "1500" {
		t.Fatalf("MarshalText = %s", s)
	}
}

func TestDurationText(t *testing.T) {
	var d Duration
	if err := d.UnmarshalText([]byte("1.5s")); err != nil || d.D() != 1500*time.Millisecond {
		t.Fatalf("got %v, %v", d, err)
	}
	if b, _ := d.MarshalText(); string(b) != "1.5s" {
		t.Fatalf("MarshalText = %s", b)
	}
	if err := d.UnmarshalText([]byte("x")); err == nil {
		t.Fatal("expected error")
	}
}
