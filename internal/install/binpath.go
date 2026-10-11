package install

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// BinaryName is the executable name looked up on PATH.
const BinaryName = "graphmaster"

// BinaryLocator finds a stable absolute path to the graphmaster binary for
// agent configs. Function fields default to the os/exec/filepath versions.
type BinaryLocator struct {
	Executable   func() (string, error)
	LookPath     func(string) (string, error)
	EvalSymlinks func(string) (string, error)
	// TempDirs are roots whose binaries are considered ephemeral
	// (default: os.TempDir()).
	TempDirs []string
}

// BinaryPath is a resolved binary location.
type BinaryPath struct {
	Path   string
	Reason string
}

// ResolveBinary resolves the running graphmaster binary with defaults.
func ResolveBinary() (BinaryPath, error) { return BinaryLocator{}.Resolve() }

// Resolve picks, in order:
//  1. the Homebrew prefix bin symlink when running from a Cellar/Caskroom
//     version directory (survives `brew upgrade`);
//  2. a PATH entry resolving to the running executable (keeps symlinks);
//  3. the running executable itself, unless it is a temporary build
//     (`go run`, go-build dirs, TempDir), in which case an installed
//     graphmaster on PATH is used or an error is returned.
func (l BinaryLocator) Resolve() (BinaryPath, error) {
	l.defaults()
	exe, err := l.Executable()
	if err != nil {
		return BinaryPath{}, fmt.Errorf("locate graphmaster executable: %w", err)
	}
	exe, err = filepath.Abs(exe)
	if err != nil {
		return BinaryPath{}, err
	}
	resolved := l.resolve(exe)

	// Try the path as invoked first so a symlinked prefix keeps its name.
	for _, cand := range []string{exe, resolved} {
		if p, ok := l.homebrewLink(cand); ok {
			return BinaryPath{Path: p, Reason: "Homebrew bin symlink"}, nil
		}
	}
	onPath, pathErr := l.LookPath(BinaryName)
	if pathErr == nil {
		if abs, err := filepath.Abs(onPath); err == nil {
			onPath = abs
		}
		if l.resolve(onPath) == resolved {
			return BinaryPath{Path: onPath, Reason: "PATH entry"}, nil
		}
	}
	if !l.ephemeral(resolved) {
		return BinaryPath{Path: exe, Reason: "current executable"}, nil
	}
	if pathErr == nil && !l.ephemeral(l.resolve(onPath)) {
		return BinaryPath{Path: onPath, Reason: "PATH entry (current executable is a temporary build)"}, nil
	}
	return BinaryPath{}, fmt.Errorf("graphmaster is running from a temporary build (%s); install it first (brew install pragmataW/tap/graphmaster or go install) and re-run", exe)
}

func (l *BinaryLocator) defaults() {
	if l.Executable == nil {
		l.Executable = os.Executable
	}
	if l.LookPath == nil {
		l.LookPath = exec.LookPath
	}
	if l.EvalSymlinks == nil {
		l.EvalSymlinks = filepath.EvalSymlinks
	}
	if l.TempDirs == nil {
		l.TempDirs = []string{os.TempDir()}
	}
}

func (l BinaryLocator) resolve(p string) string {
	if r, err := l.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// homebrewLink maps <prefix>/Cellar/graphmaster/<ver>/bin/graphmaster or
// <prefix>/Caskroom/graphmaster/<ver>/graphmaster to <prefix>/bin/graphmaster
// when that link exists and points into the same keg/cask.
func (l BinaryLocator) homebrewLink(p string) (string, bool) {
	slash := filepath.ToSlash(p)
	for _, room := range []string{"/Cellar/", "/Caskroom/"} {
		i := strings.LastIndex(slash, room+BinaryName+"/")
		if i < 0 {
			continue
		}
		prefix := filepath.FromSlash(slash[:i])
		link := filepath.Join(prefix, "bin", BinaryName)
		keg := l.resolve(filepath.Join(prefix, strings.Trim(room, "/"), BinaryName))
		target := l.resolve(link)
		if rel, err := filepath.Rel(keg, target); err == nil && !strings.HasPrefix(rel, "..") {
			return link, true
		}
	}
	return "", false
}

// ephemeral reports whether p looks like a throwaway build.
func (l BinaryLocator) ephemeral(p string) bool {
	for _, seg := range strings.Split(filepath.ToSlash(p), "/") {
		if strings.HasPrefix(seg, "go-build") {
			return true
		}
	}
	for _, t := range l.TempDirs {
		if t == "" {
			continue
		}
		root := l.resolve(t)
		if rel, err := filepath.Rel(root, p); err == nil && !strings.HasPrefix(rel, "..") {
			return true
		}
	}
	return false
}
