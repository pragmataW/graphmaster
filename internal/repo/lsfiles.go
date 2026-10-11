package repo

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"os/exec"
	"strconv"
)

// lsFilesIndex builds an Index from `git ls-files -z --stage`, used when the
// index file cannot be decoded directly (split index, sparse index). The
// entries have no cached stat, so every file is re-hashed by callers. The -t
// tag carries the skip-worktree bit (tag "S"); sparse directories are
// expanded by git.
func lsFilesIndex(ctx context.Context, r *Root) (*Index, error) {
	cmd := exec.CommandContext(ctx, "git", "--no-optional-locks", "ls-files", "-z", "--stage", "-t")
	cmd.Dir = r.WorkTree
	cmd.Env = append(cmd.Environ(), "GIT_DIR="+r.GitDir, "GIT_WORK_TREE="+r.WorkTree)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-files: %w: %s", err, bytes.TrimSpace(stderr.Bytes()))
	}
	entries, err := parseLsFilesStage(out)
	if err != nil {
		return nil, err
	}
	return &Index{Entries: entries, FromGit: true}, nil
}

// parseLsFilesStage parses records of the form
// "[<tag> SP] <mode> SP <sha> SP <stage> TAB <path> NUL".
func parseLsFilesStage(out []byte) ([]IndexEntry, error) {
	var entries []IndexEntry
	for len(out) > 0 {
		rec, rest, ok := bytes.Cut(out, []byte{0})
		if !ok {
			return nil, fmt.Errorf("ls-files: unterminated record %q", rec)
		}
		out = rest
		meta, path, ok := bytes.Cut(rec, []byte{'\t'})
		f := bytes.Fields(meta)
		var tag []byte
		if len(f) == 4 {
			tag, f = f[0], f[1:]
		}
		if !ok || len(f) != 3 || len(f[1]) != 2*len(BlobID{}) {
			return nil, fmt.Errorf("ls-files: malformed record %q", rec)
		}
		mode, err := strconv.ParseUint(string(f[0]), 8, 32)
		if err != nil {
			return nil, fmt.Errorf("ls-files: mode in %q: %w", rec, err)
		}
		stage, err := strconv.ParseUint(string(f[2]), 10, 2)
		if err != nil {
			return nil, fmt.Errorf("ls-files: stage in %q: %w", rec, err)
		}
		e := IndexEntry{
			Path:         string(path),
			Stage:        uint8(stage),
			Stat:         FileStat{Mode: uint32(mode)},
			SkipWorktree: bytes.EqualFold(tag, []byte("S")),
		}
		if _, err := hex.Decode(e.Blob[:], f[1]); err != nil {
			return nil, fmt.Errorf("ls-files: sha in %q: %w", rec, err)
		}
		entries = append(entries, e)
	}
	return entries, nil
}
