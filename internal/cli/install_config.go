package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/pragmataW/graphmaster/internal/config"
	"github.com/pragmataW/graphmaster/internal/install"
)

// userConfigPath is ~/.config/graphmaster/config.toml (XDG aware); it lives
// next to install-state.json.
func userConfigPath(env install.Env) (string, error) {
	dir, err := env.StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.toml"), nil
}

// ensureUserConfig writes the default user config when none exists. It
// reports whether the file was (or, in a dry run, would be) created; an
// existing file is never touched.
func ensureUserConfig(env install.Env, dryRun bool) (path string, created bool, err error) {
	path, err = userConfigPath(env)
	if err != nil {
		return "", false, err
	}
	if _, err := os.Lstat(path); err == nil {
		return path, false, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return path, false, err
	}
	if dryRun {
		return path, true, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return path, false, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, fs.ErrExist) {
		return path, false, nil // created concurrently; keep theirs
	}
	if err != nil {
		return path, false, err
	}
	if _, err := f.WriteString(defaultUserConfig()); err != nil {
		f.Close()
		return path, false, err
	}
	return path, true, f.Close()
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// defaultUserConfig renders the user config install creates: the mode set
// explicitly and every other option commented out at its default value.
func defaultUserConfig() string {
	d := config.Default()
	t := d.Timeouts
	var b strings.Builder
	b.WriteString(`# graphmaster user configuration (created by "graphmaster install").
# <repo>/.graphmaster/config.toml and GRAPHMASTER_* environment variables
# override these values. Uncomment a line to change it.

# How searches over code are steered to the code graph:
#   "balanced"  redirect the first symbol-like grep/glob, allow it when repeated
#   "strict"    always redirect code searches to the graph
#   "off"       never steer; the MCP tools stay available
mode = "balanced"

# Files larger than this are not parsed.
`)
	fmt.Fprintf(&b, "# max_file_size = %s\n\n", tomlString(byteSizeText(int64(d.MaxFileSize))))
	b.WriteString("# Paths never indexed (gitignore syntax); setting this replaces the defaults.\n")
	fmt.Fprintf(&b, "# excludes = %s\n", tomlArray(d.Excludes))
	b.WriteString("# Extra paths to skip on top of excludes.\n# extra_excludes = []\n\n")
	b.WriteString("# Languages to index (default: all).\n")
	fmt.Fprintf(&b, "# languages = %s\n\n", tomlArray(d.Languages))
	b.WriteString("# Balanced mode stops redirecting after this many denials per agent session.\n")
	fmt.Fprintf(&b, "# soft_budget = %d\n\n", d.SoftBudget)
	b.WriteString("# How long each caller waits for another process updating the index before\n" +
		"# answering from the current graph with a staleness banner.\n# [timeouts]\n")
	for _, kv := range []struct {
		key string
		val config.Duration
	}{
		{"mcp", t.MCP}, {"pre_tool_use", t.PreToolUse}, {"post_tool_use", t.PostToolUse},
		{"session_start", t.SessionStart}, {"subagent_start", t.SubagentStart}, {"pre_compact", t.PreCompact},
	} {
		fmt.Fprintf(&b, "# %s = %s\n", kv.key, tomlString(kv.val.D().String()))
	}
	return b.String()
}

// byteSizeText renders n with the largest exact binary unit.
func byteSizeText(n int64) string {
	for _, u := range []struct {
		suffix string
		size   int64
	}{{"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10}} {
		if n >= u.size && n%u.size == 0 {
			return strconv.FormatInt(n/u.size, 10) + u.suffix
		}
	}
	return strconv.FormatInt(n, 10)
}

// tomlString renders s as a TOML basic string; strconv.Quote output is valid
// TOML for the plain ASCII defaults written here.
func tomlString(s string) string { return strconv.Quote(s) }

func tomlArray(items []string) string {
	q := make([]string, len(items))
	for i, s := range items {
		q[i] = tomlString(s)
	}
	return "[" + strings.Join(q, ", ") + "]"
}
