package install

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// readOptional returns the file's bytes, or exists=false when it is absent.
func readOptional(path string) (b []byte, exists bool, err error) {
	b, err = os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return b, true, nil
}

// resolveTarget follows a symlinked config file (dotfile managers) so the
// atomic rename replaces the real file instead of the link.
func resolveTarget(path string) string {
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&fs.ModeSymlink != 0 {
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			return resolved
		}
	}
	return path
}

// writeFileAtomic writes b via a temp file and rename. An existing file
// keeps its permission bits; otherwise perm is used.
func writeFileAtomic(path string, b []byte, perm fs.FileMode) error {
	path = resolveTarget(path)
	if fi, err := os.Stat(path); err == nil {
		perm = fi.Mode().Perm()
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".gm-tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }() // no-op after a successful rename
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// mkdirAllRecord creates dir and returns the directories it had to create,
// outermost first.
func mkdirAllRecord(dir string) ([]string, error) {
	var missing []string
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(d); err == nil {
			break
		} else if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		missing = append([]string{d}, missing...)
		if filepath.Dir(d) == d {
			break
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return missing, nil
}

// removeEmptyDirs removes dirs innermost first, skipping non-empty ones.
func removeEmptyDirs(dirs []string) {
	for i := len(dirs) - 1; i >= 0; i-- {
		_ = os.Remove(dirs[i]) // fails harmlessly when not empty
	}
}
