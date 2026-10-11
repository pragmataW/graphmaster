package install

import (
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/pragmataW/graphmaster/internal/config"
)

// Env abstracts the process environment so tests can inject a temp HOME.
// The zero value uses the real environment and clock.
type Env struct {
	// LookupEnv defaults to os.LookupEnv.
	LookupEnv func(string) (string, bool)
	// Now defaults to time.Now; it names backup directories.
	Now func() time.Time
}

// EnvWithHome returns an Env whose HOME is home and which ignores every
// other variable (XDG_CONFIG_HOME included). Intended for tests.
func EnvWithHome(home string) Env {
	return Env{LookupEnv: func(k string) (string, bool) {
		if k == "HOME" {
			return home, true
		}
		return "", false
	}}
}

func (e Env) lookup(k string) (string, bool) {
	if e.LookupEnv != nil {
		return e.LookupEnv(k)
	}
	return os.LookupEnv(k)
}

func (e Env) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// Home returns $HOME, falling back to os.UserHomeDir when unset.
func (e Env) Home() (string, error) {
	if h, ok := e.lookup("HOME"); ok && h != "" {
		return h, nil
	}
	if e.LookupEnv != nil {
		return "", errors.New("install: HOME is not set")
	}
	return os.UserHomeDir()
}

// ConfigHome returns $XDG_CONFIG_HOME when absolute, else $HOME/.config.
func (e Env) ConfigHome() (string, error) {
	if x, ok := e.lookup("XDG_CONFIG_HOME"); ok && filepath.IsAbs(x) {
		return x, nil
	}
	home, err := e.Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config"), nil
}

// StateDir is graphmaster's user config directory (the directory holding
// config.toml), where install-state.json and backups/ live.
func (e Env) StateDir() (string, error) {
	// Reuse config's resolution so both agree on XDG handling.
	lookup := func(k string) (string, bool) {
		if k == "HOME" {
			h, err := e.Home()
			return h, err == nil
		}
		return e.lookup(k)
	}
	p, err := config.UserConfigPath(lookup)
	if err != nil {
		return "", err
	}
	return filepath.Dir(p), nil
}

// StatePath returns the location of install-state.json.
func (e Env) StatePath() (string, error) {
	d, err := e.StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, StateFileName), nil
}

// BackupDir returns the root directory for timestamped backups.
func (e Env) BackupDir() (string, error) {
	d, err := e.StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "backups"), nil
}
