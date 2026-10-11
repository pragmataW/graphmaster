package repo

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	gitconfig "github.com/go-git/go-git/v5/plumbing/format/config"
)

// GitConfig holds the few git settings discovery depends on.
type GitConfig struct {
	// ExcludesFile is the resolved core.excludesFile path (or git's XDG
	// default); it may not exist.
	ExcludesFile string
	// IgnoreCase mirrors core.ignorecase: ignore patterns and tracked-path
	// lookups compare case-insensitively.
	IgnoreCase bool
}

// LoadGitConfig reads the global (XDG, then ~/.gitconfig) and repository
// config in git's precedence order; later files win. System config and
// [include] directives are not consulted. Unreadable or malformed files are
// skipped, as discovery must never fail because of config.
func LoadGitConfig(r *Root) GitConfig {
	home, _ := os.UserHomeDir()
	xdg := os.Getenv("XDG_CONFIG_HOME")
	if xdg == "" && home != "" {
		xdg = filepath.Join(home, ".config")
	}

	var files []string
	if xdg != "" {
		files = append(files, filepath.Join(xdg, "git", "config"))
	}
	if home != "" {
		files = append(files, filepath.Join(home, ".gitconfig"))
	}
	files = append(files, r.ConfigPath())

	var excludes, ignoreCase string
	for _, f := range files {
		core := readCoreSection(f)
		if core == nil {
			continue
		}
		if core.Has("excludesfile") {
			excludes = core.Get("excludesfile")
		}
		if core.Has("ignorecase") {
			ignoreCase = core.Get("ignorecase")
		}
	}

	cfg := GitConfig{IgnoreCase: parseGitBool(ignoreCase)}
	switch {
	case excludes != "":
		cfg.ExcludesFile = expandPath(excludes, home, r.WorkTree)
	case xdg != "":
		cfg.ExcludesFile = filepath.Join(xdg, "git", "ignore")
	}
	return cfg
}

func readCoreSection(path string) gitconfig.Options {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	c := gitconfig.New()
	if err := gitconfig.NewDecoder(f).Decode(c); err != nil {
		return nil
	}
	if !c.HasSection("core") {
		return nil
	}
	return c.Section("core").Options
}

// parseGitBool follows git's boolean syntax. A key present without a value
// decodes to "" in go-git and is not distinguishable from absent here, so
// only explicit values count.
func parseGitBool(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "yes", "on", "1":
		return true
	}
	return false
}

// expandPath resolves "~/" against home and relative paths against base.
func expandPath(p, home, base string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home == "" {
			return ""
		}
		p = filepath.Join(home, p[1:])
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(base, p)
	}
	return filepath.Clean(p)
}

// readOptionalFile returns nil content for a missing file.
func readOptionalFile(path string) ([]byte, error) {
	if path == "" {
		return nil, nil
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) || isNotDir(err) {
		return nil, nil
	}
	return b, err
}
