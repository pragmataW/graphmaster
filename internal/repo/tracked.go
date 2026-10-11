package repo

import (
	"strings"
	"sync"
)

// TrackedSet answers "is this path in the index?" for the walker, honoring
// core.ignorecase the way git's index_name_is_other does.
type TrackedSet struct {
	modes      map[string]uint32
	folded     map[string]struct{} // lowercased paths, only with ignoreCase
	gitlinks   map[string]struct{} // submodule paths (lowercased with ignoreCase)
	ignoreCase bool

	dirsOnce sync.Once
	dirs     map[string]struct{} // directories containing tracked entries
}

// NewTrackedSet indexes entries (any stage) by path.
func NewTrackedSet(entries []IndexEntry, ignoreCase bool) *TrackedSet {
	t := &TrackedSet{
		modes:      make(map[string]uint32, len(entries)),
		gitlinks:   map[string]struct{}{},
		ignoreCase: ignoreCase,
	}
	for i := range entries {
		e := &entries[i]
		t.modes[e.Path] = e.Stat.Mode
		if e.IsGitlink() {
			t.gitlinks[t.key(e.Path)] = struct{}{}
		}
	}
	if ignoreCase {
		t.folded = make(map[string]struct{}, len(entries))
		for p := range t.modes {
			t.folded[strings.ToLower(p)] = struct{}{}
		}
	}
	return t
}

// Len is the number of distinct tracked paths.
func (t *TrackedSet) Len() int {
	if t == nil {
		return 0
	}
	return len(t.modes)
}

// Contains reports whether path is tracked.
func (t *TrackedSet) Contains(path string) bool {
	if t == nil {
		return false
	}
	if _, ok := t.modes[path]; ok {
		return true
	}
	if t.ignoreCase {
		_, ok := t.folded[strings.ToLower(path)]
		return ok
	}
	return false
}

// IsGitlink reports whether path is a tracked submodule.
func (t *TrackedSet) IsGitlink(path string) bool {
	if t == nil {
		return false
	}
	_, ok := t.gitlinks[t.key(path)]
	return ok
}

func (t *TrackedSet) key(path string) string {
	if t.ignoreCase {
		return strings.ToLower(path)
	}
	return path
}

// HasTrackedUnder reports whether some tracked path lies below dir. It is
// only needed for rare cases (nested repositories), so the directory set
// is built lazily.
func (t *TrackedSet) HasTrackedUnder(dir string) bool {
	if t == nil {
		return false
	}
	t.dirsOnce.Do(func() {
		t.dirs = map[string]struct{}{}
		for p := range t.modes {
			for i := strings.LastIndexByte(p, '/'); i > 0; i = strings.LastIndexByte(p[:i], '/') {
				d := t.key(p[:i])
				if _, seen := t.dirs[d]; seen {
					break
				}
				t.dirs[d] = struct{}{}
			}
		}
	})
	_, ok := t.dirs[t.key(dir)]
	return ok
}
