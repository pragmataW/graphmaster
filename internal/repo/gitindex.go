package repo

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
)

// IndexEntry is one entry of the git index. Stat holds the cached stat git
// recorded; Size and Ino are truncated to 32 bits as in the on-disk format
// and are zero when the entry came from the git ls-files fallback.
type IndexEntry struct {
	Path         string
	Blob         BlobID
	Stat         FileStat
	Stage        uint8
	SkipWorktree bool
	IntentToAdd  bool
}

// IsGitlink reports whether the entry is a submodule commit pointer.
func (e *IndexEntry) IsGitlink() bool { return e.Stat.Mode == ModeGitlink }

// StatMatches reports whether a worktree lstat agrees with the entry's
// cached stat, comparing the 32-bit fields the way git does. An entry
// without cached stat never matches.
func (e *IndexEntry) StatMatches(st FileStat) bool {
	c := e.Stat
	if c.MtimeNS == 0 || c.Mode != st.Mode || uint32(c.Size) != uint32(st.Size) {
		return false
	}
	if c.Ino != 0 && uint32(c.Ino) != uint32(st.Ino) {
		return false
	}
	const sec = int64(1e9)
	if c.MtimeNS/sec != st.MtimeNS/sec {
		return false
	}
	// Git built without nanosecond support records nsec as 0.
	return c.MtimeNS%sec == 0 || c.MtimeNS%sec == st.MtimeNS%sec
}

// StatSig is the change-detection signature of a single file such as
// .git/index. Its String form is what the store persists.
type StatSig struct {
	MtimeNS int64
	Size    int64
	Ino     uint64
}

// IsZero reports whether the signature describes a missing file.
func (s StatSig) IsZero() bool { return s == StatSig{} }

// String encodes the signature as "mtime_ns:size:ino".
func (s StatSig) String() string {
	return fmt.Sprintf("%d:%d:%d", s.MtimeNS, s.Size, s.Ino)
}

// ParseStatSig decodes the String form.
func ParseStatSig(v string) (StatSig, error) {
	parts := strings.Split(v, ":")
	if len(parts) != 3 {
		return StatSig{}, fmt.Errorf("stat signature %q: want 3 fields", v)
	}
	m, err1 := strconv.ParseInt(parts[0], 10, 64)
	s, err2 := strconv.ParseInt(parts[1], 10, 64)
	i, err3 := strconv.ParseUint(parts[2], 10, 64)
	if err := errors.Join(err1, err2, err3); err != nil {
		return StatSig{}, fmt.Errorf("stat signature %q: %w", v, err)
	}
	return StatSig{MtimeNS: m, Size: s, Ino: i}, nil
}

// StatSigOf stats path; a missing file yields the zero signature.
func StatSigOf(path string) (StatSig, error) {
	fi, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return StatSig{}, nil
	}
	if err != nil {
		return StatSig{}, err
	}
	st := StatFromInfo(fi)
	return StatSig{MtimeNS: st.MtimeNS, Size: st.Size, Ino: st.Ino}, nil
}

// Index is a decoded snapshot of the git index.
type Index struct {
	Version uint32
	// Entries are in index order: sorted by path, then stage.
	Entries []IndexEntry
	// Sig is the stat of the index file taken before it was read.
	Sig StatSig
	// FromGit is set when the entries came from `git ls-files` because the
	// index uses a format this decoder does not handle (split or sparse
	// index); entries then carry no cached stat.
	FromGit bool
}

// ReadIndex loads the worktree's index. It decodes the file directly and
// falls back to `git ls-files -z --stage` when decoding fails. A missing
// index (fresh repository) yields an empty Index.
func ReadIndex(ctx context.Context, r *Root) (*Index, error) {
	path := r.IndexPath()
	sig, err := StatSigOf(path)
	if err != nil {
		return nil, err
	}
	if sig.IsZero() {
		return &Index{}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &Index{}, nil
		}
		return nil, err
	}
	idx, decErr := DecodeIndex(data)
	if decErr != nil {
		idx, err = lsFilesIndex(ctx, r)
		if err != nil {
			return nil, fmt.Errorf("decode %s: %w; git ls-files fallback: %w", path, decErr, err)
		}
	}
	idx.Sig = sig
	return idx, nil
}

// Clean reports whether e can be trusted to describe the worktree file
// with stat st without reading it: the cached stat matches and the entry
// is not racy relative to the index file's own mtime.
func (ix *Index) Clean(e *IndexEntry, st FileStat) bool {
	return e.StatMatches(st) && !IsRacy(e.Stat.MtimeNS, timeFromNS(ix.Sig.MtimeNS))
}

// Files returns the entries describing checked-out files: one entry per
// path (the lowest stage), excluding gitlinks, sparse directory entries and
// skip-worktree paths, which have no file in the worktree.
func (ix *Index) Files() []IndexEntry {
	out := make([]IndexEntry, 0, len(ix.Entries))
	for i := range ix.Entries {
		e := &ix.Entries[i]
		if i > 0 && ix.Entries[i-1].Path == e.Path {
			continue // higher conflict stage of the same path
		}
		if e.IsGitlink() || e.Stat.Mode == ModeDir || e.SkipWorktree {
			continue
		}
		out = append(out, *e)
	}
	return out
}
