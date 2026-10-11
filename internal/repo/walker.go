package repo

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"time"
)

// WalkOptions configures an untracked-file walk.
type WalkOptions struct {
	Root *Root
	// Tracked is the index content; tracked paths are not reported and
	// gitlinks are not entered. Nil means nothing is tracked.
	Tracked *TrackedSet
	// Ignore holds the exclude rules (required).
	Ignore *Ignore
	// Cache, if set, is consulted and updated in place.
	Cache *DirCache
	// ScanStart is the start time of this scan, used for the racy-git
	// rule when deciding what may be cached. Zero means time.Now().
	ScanStart time.Time
	// Workers bounds concurrent directory reads; <= 0 picks a default.
	Workers int
}

// UntrackedFile is an untracked, not ignored file.
type UntrackedFile struct {
	Path    string
	Symlink bool
}

// WalkResult is the outcome of Walk.
type WalkResult struct {
	// Untracked lists untracked, not ignored files sorted by path; this is
	// the set `git ls-files -o --exclude-standard` prints, minus nested
	// repositories.
	Untracked []UntrackedFile
	// NestedRepos are untracked directories holding their own repository.
	// They are opaque: git lists them as "dir/" and so do we, here.
	NestedRepos []string
	// DirsRead and DirsCached count directories listed from disk versus
	// served from the cache.
	DirsRead, DirsCached int
	// ChangedDirs and RemovedDirs describe how the cache changed, for
	// incremental persistence. Empty without a cache.
	ChangedDirs []DirRow
	RemovedDirs []string
}

// Walk enumerates untracked-but-not-ignored files with git semantics:
// ".git" entries are skipped everywhere, excluded directories are pruned
// (so negations below them cannot re-include anything), tracked submodules
// are not entered, and untracked nested repositories are reported but not
// descended into.
func Walk(ctx context.Context, opts WalkOptions) (*WalkResult, error) {
	if opts.Root == nil || opts.Ignore == nil {
		return nil, errors.New("walk: Root and Ignore are required")
	}
	if opts.ScanStart.IsZero() {
		opts.ScanStart = time.Now()
	}
	workers := opts.Workers
	if workers <= 0 {
		workers = min(4*runtime.GOMAXPROCS(0), 64)
	}
	if opts.Cache != nil {
		// One walk per cache at a time: cached entries are updated in place.
		opts.Cache.walkMu.Lock()
		defer opts.Cache.walkMu.Unlock()
	}
	w := &walker{
		opts:     opts,
		ctx:      ctx,
		sem:      make(chan struct{}, workers),
		next:     map[string]*cachedDir{},
		racyFrom: opts.ScanStart.Add(-RacyWindow).UnixNano(),
	}
	w.dir("", nil)
	w.wg.Wait()
	if err := w.firstErr(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	res := &w.res
	sort.Slice(res.Untracked, func(i, j int) bool { return res.Untracked[i].Path < res.Untracked[j].Path })
	sort.Strings(res.NestedRepos)
	if opts.Cache != nil {
		res.ChangedDirs, res.RemovedDirs = opts.Cache.replace(w.next)
	}
	return res, nil
}

type walker struct {
	opts     WalkOptions
	ctx      context.Context
	sem      chan struct{}
	wg       sync.WaitGroup
	racyFrom int64

	mu   sync.Mutex
	res  WalkResult
	next map[string]*cachedDir
	err  error
}

func (w *walker) fail(err error) {
	w.mu.Lock()
	if w.err == nil {
		w.err = err
	}
	w.mu.Unlock()
}

func (w *walker) firstErr() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err
}

// spawn runs fn on a new goroutine when a worker slot is free, inline
// otherwise, so the walk never blocks waiting for itself.
func (w *walker) spawn(fn func()) {
	select {
	case w.sem <- struct{}{}:
		w.wg.Add(1)
		go func() {
			defer func() { <-w.sem; w.wg.Done() }()
			fn()
		}()
	default:
		fn()
	}
}

// dir processes one non-excluded directory. rel is "" for the root; frame
// is the .gitignore stack of its parent.
func (w *walker) dir(rel string, frame *ignoreFrame) {
	if w.ctx.Err() != nil || w.firstErr() != nil {
		return
	}
	abs := w.opts.Root.Abs(rel)
	cd, err := w.listing(rel, abs)
	if err != nil {
		if rel != "" && (errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission)) {
			return // vanished or unreadable subdirectory: git skips it too
		}
		w.fail(err)
		return
	}
	if rel != "" && hasNestedRepo(abs, cd.names) && !w.opts.Tracked.HasTrackedUnder(rel) {
		w.mu.Lock()
		w.res.NestedRepos = append(w.res.NestedRepos, rel)
		w.mu.Unlock()
		return
	}
	frame = frame.push(cd.patterns)

	var files []UntrackedFile
	for _, e := range cd.names {
		if e.name == ".git" {
			continue
		}
		child := e.name
		if rel != "" {
			child = rel + "/" + e.name
		}
		if e.kind == kindDir {
			if w.opts.Tracked.IsGitlink(child) || w.opts.Ignore.excludedAt(frame, child, true) {
				continue
			}
			w.spawn(func() { w.dir(child, frame) })
			continue
		}
		if w.opts.Tracked.Contains(child) || w.opts.Ignore.excludedAt(frame, child, false) {
			continue
		}
		files = append(files, UntrackedFile{Path: child, Symlink: e.kind == kindSymlink})
	}
	if len(files) > 0 {
		w.mu.Lock()
		w.res.Untracked = append(w.res.Untracked, files...)
		w.mu.Unlock()
	}
}

// listing returns the directory's entries and parsed .gitignore, from the
// cache when the directory's mtime is unchanged.
func (w *walker) listing(rel, abs string) (*cachedDir, error) {
	fi, err := os.Lstat(abs)
	if err != nil {
		return nil, err
	}
	mtime := fi.ModTime().UnixNano()

	var cd *cachedDir
	fromCache := false
	if w.opts.Cache != nil {
		if old := w.opts.Cache.get(rel); old != nil && old.mtimeNS == mtime {
			cd, fromCache = old, true
		}
	}
	if cd == nil {
		names, err := readDirNames(abs)
		if err != nil {
			return nil, err
		}
		cd = &cachedDir{mtimeNS: mtime, names: names}
	}
	if cd, err = w.loadIgnore(abs, cd); err != nil {
		return nil, err
	}

	w.mu.Lock()
	if fromCache {
		w.res.DirsCached++
	} else {
		w.res.DirsRead++
	}
	// A directory modified within the racy window may change again without
	// a visible mtime change; do not trust its listing next time.
	if w.opts.Cache != nil && mtime < w.racyFrom {
		w.next[rel] = cd
	}
	w.mu.Unlock()
	return cd, nil
}

// loadIgnore makes sure cd.patterns reflects the directory's .gitignore.
// Persisted fields of a cached entry are never modified in place: a change
// produces a copy, which is how Walk detects rows to re-persist.
func (w *walker) loadIgnore(abs string, cd *cachedDir) (*cachedDir, error) {
	noIgnore := func() *cachedDir {
		if cd.ignoreSig != "" {
			cp := *cd
			cp.ignoreSig = ""
			cd = &cp
		}
		cd.patterns, cd.parsed = nil, true
		return cd
	}
	if !hasRegularGitignore(cd.names) {
		return noIgnore(), nil
	}
	p := filepath.Join(abs, ".gitignore")
	st, err := Lstat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return noIgnore(), nil
	}
	if err != nil {
		return nil, err
	}
	// A racy .gitignore gets no signature and is re-read on every walk.
	sig := ""
	if !IsRacy(st.MtimeNS, w.opts.ScanStart) {
		sig = StatSig{MtimeNS: st.MtimeNS, Size: st.Size, Ino: st.Ino}.String()
	}
	if cd.parsed && sig != "" && sig == cd.ignoreSig {
		return cd, nil
	}
	b, err := readOptionalFile(p)
	if err != nil {
		return nil, err
	}
	if sig != cd.ignoreSig {
		cp := *cd
		cp.ignoreSig = sig
		cd = &cp
	}
	rel, _ := w.opts.Root.Rel(abs)
	cd.patterns, cd.parsed = parseIgnore(b, rel), true
	return cd, nil
}

// hasRegularGitignore reports whether names holds a regular .gitignore.
// Like git >= 2.32, a symlinked .gitignore is not followed.
func hasRegularGitignore(names []dirName) bool {
	for _, e := range names {
		if e.name == ".gitignore" {
			return e.kind == kindFile
		}
	}
	return false
}

// readDirNames lists a directory without following symlinks. Entries other
// than regular files, directories and symlinks are dropped.
func readDirNames(abs string) ([]dirName, error) {
	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil, err
	}
	names := make([]dirName, 0, len(entries))
	for _, e := range entries {
		var kind byte
		switch t := e.Type(); {
		case t.IsDir():
			kind = kindDir
		case t&fs.ModeSymlink != 0:
			kind = kindSymlink
		case t.IsRegular():
			kind = kindFile
		default:
			continue
		}
		names = append(names, dirName{name: e.Name(), kind: kind})
	}
	return names, nil
}

// hasNestedRepo reports whether a directory listing contains a valid ".git"
// (directory with HEAD, or a gitfile pointing at one).
func hasNestedRepo(abs string, names []dirName) bool {
	for _, e := range names {
		if e.name == ".git" && e.kind != kindSymlink {
			gd, err := resolveDotGit(abs)
			return err == nil && gd != ""
		}
	}
	return false
}
