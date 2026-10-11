package query

import (
	"bytes"
	"context"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/pragmataW/graphmaster/internal/repo"
	"github.com/pragmataW/graphmaster/internal/store"
)

// stagedDiffTimeout bounds the `git diff --cached` probe.
const stagedDiffTimeout = 2 * time.Second

// changedFile is an indexed file whose content differs from git.
type changedFile struct {
	ID   int64
	Path string
	Why  string // "modified", "untracked" or "staged"
}

// changedFiles compares each indexed file's content key (git blob id of
// the working-tree content) with the git index entry: a different blob is
// an unstaged modification, a missing entry an untracked file. Paths
// staged but not committed (`git diff --cached` against HEAD) are added
// when git is available.
func (q *querier) changedFiles(ctx context.Context, root *repo.Root) ([]changedFile, error) {
	idx, err := repo.ReadIndex(ctx, root)
	if err != nil {
		return nil, err
	}
	inIndex := make(map[string]repo.BlobID, len(idx.Entries))
	for _, e := range idx.Files() {
		inIndex[e.Path] = e.Blob
	}
	staged := stagedPaths(ctx, root)
	var out []changedFile
	err = q.st.ForEachFile(ctx, func(f *store.File) error {
		blob, tracked := inIndex[f.Path]
		switch {
		case !tracked:
			out = append(out, changedFile{ID: f.ID, Path: f.Path, Why: "untracked"})
		case !bytes.Equal(blob[:], f.Blob):
			out = append(out, changedFile{ID: f.ID, Path: f.Path, Why: "modified"})
		case staged[f.Path]:
			out = append(out, changedFile{ID: f.ID, Path: f.Path, Why: "staged"})
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, err
}

// stagedPaths lists paths whose index entry differs from HEAD. It is best
// effort: without git, or in a repository without commits, it returns nil.
func stagedPaths(ctx context.Context, root *repo.Root) map[string]bool {
	ctx, cancel := context.WithTimeout(ctx, stagedDiffTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "diff", "--cached", "--name-only", "--no-renames", "-z", "HEAD")
	cmd.Dir = root.WorkTree
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	m := map[string]bool{}
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			m[p] = true
		}
	}
	return m
}
